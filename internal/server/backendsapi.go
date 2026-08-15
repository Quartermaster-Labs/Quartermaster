package server

// Managed backend installs: the dashboard's Backends tab can download an
// inference-server build from its upstream GitHub release, keep several
// versions side by side, switch between them, and remove them. Installs are
// registered into the same autogen backend registry that hand-entered paths
// live in (`Managed` marks the difference), so per-model backend selection,
// the ★ class default, and config generation are unchanged.
//
// The heavy lifting is in internal/backends; this file is wire types, the
// registry write-back, and the HTTP surface. Every route is admin-gated.

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/backends"
	"github.com/quartermaster-labs/quartermaster/internal/peimports"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// --- wire types ---

type backendVariantDTO struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Note      string `json:"note,omitempty"`
	Available bool   `json:"available"` // publishes assets for this OS
}

type backendInstalledDTO struct {
	Version     string    `json:"version"`
	Variant     string    `json:"variant"`
	Exe         string    `json:"exe"`
	InstalledAt time.Time `json:"installedAt"`
	SizeBytes   int64     `json:"sizeBytes"`
	Active      bool      `json:"active"` // the registry currently points here
	// Warning is set when the build is on disk but cannot actually launch —
	// almost always an upstream archive packaged without its GPU runtime. It has
	// to live on the build rather than only on the install job, because the job
	// scrolls away and the broken build stays.
	Warning string `json:"warning,omitempty"`
}

type backendComponentDTO struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Blurb     string                `json:"blurb"`
	Repo      string                `json:"repo"`
	Kind      string                `json:"kind"`             // "" = helper binary, never registered
	Manual    bool                  `json:"manual"`           // engine we can drive but not install
	Setup     string                `json:"setup,omitempty"`  // shown instead of install controls
	Custom    bool                  `json:"custom,omitempty"` // a repo the user tracks, editable/removable
	Supported bool                  `json:"supported"`
	Suggested string                `json:"suggested"` // variant preselected for this host
	Variants  []backendVariantDTO   `json:"variants"`
	Installed []backendInstalledDTO `json:"installed"`
	Active    *backendInstalledDTO  `json:"active,omitempty"`
	// IsDefault reports whether this component's registry row is the one its
	// class actually resolves to, i.e. whether installing it changed what
	// Quartermaster launches. DefaultOwner names the row that wins instead —
	// typically a hand-entered backend the user set up before installing this
	// one, which silently keeps winning. DefaultImplicit says that win came from
	// being first of its class rather than from a deliberate ★; the UI must not
	// call an accident a default, and must not claim nothing is in use.
	IsDefault       bool   `json:"isDefault"`
	DefaultOwner    string `json:"defaultOwner,omitempty"`
	DefaultImplicit bool   `json:"defaultImplicit,omitempty"`
	// Class groups the kinds that compete for one ★ (llama and vllm both serve
	// text), so a card can name the group it wins or loses.
	Class string `json:"class,omitempty"`
}

type backendCatalogResp struct {
	Root       string                `json:"root"`
	OS         string                `json:"os"`
	Components []backendComponentDTO `json:"components"`
	Jobs       []backends.Job        `json:"jobs"`
	GPUs       []string              `json:"gpus"`
}

type backendReleaseDTO struct {
	Tag         string    `json:"tag"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"publishedAt"`
	Prerelease  bool      `json:"prerelease"`
	// Variants lists the variant ids this release actually ships for this OS —
	// upstream occasionally skips a flavour, and the picker must not offer a
	// version that can't be installed.
	Variants []string `json:"variants"`
}

type backendInstallReq struct {
	Component string `json:"component"`
	Variant   string `json:"variant"`
	Version   string `json:"version"` // "" / "latest" => newest stable
}

type backendBuildRef struct {
	Component string `json:"component"`
	Version   string `json:"version"`
	Variant   string `json:"variant"`
}

// --- registry write-back ---

// managedEntry finds the registry row the manager owns for a component.
func managedEntry(list []autogen.BackendEntry, comp string) int {
	for i, e := range list {
		if e.Managed && strings.EqualFold(e.Component, comp) {
			return i
		}
	}
	return -1
}

// registerManagedBackend points the component's registry row at an installed
// build, creating the row on first install. A component with no registry kind
// (yt-dlp) installs to disk and stops there. Regenerates + hot-reloads so the
// new exe is used on each model's next load.
func (s *Server) registerManagedBackend(inst backends.Installed) error {
	comp, ok := s.backends.Find(inst.Component)
	if !ok || comp.Kind == "" {
		return nil
	}
	a := s.autogen
	if a == nil {
		// No -generate: the binary is installed and visible in the manager, but
		// there is no sidecar to record it in.
		return nil
	}
	list, err := autogen.LoadSidecarBackendList(a.GeneratePath)
	if err != nil {
		return err
	}
	row := autogen.BackendEntry{
		ID:        "managed-" + comp.ID,
		Kind:      comp.Kind,
		Name:      comp.Name,
		Path:      inst.Exe,
		Managed:   true,
		Component: comp.ID,
		Version:   inst.Version,
		Variant:   inst.Variant,
	}
	if i := managedEntry(list, comp.ID); i >= 0 {
		row.ID = list[i].ID // keep the id: per-model overrides reference it
		row.Default = list[i].Default
		list[i] = row
	} else {
		// The first backend of a class becomes the ★ auto-pick; if the class is
		// already populated, leave whatever the user chose alone.
		classTaken := false
		for _, e := range list {
			if autogen.KindClass(e.Kind) == autogen.KindClass(comp.Kind) {
				classTaken = true
				break
			}
		}
		row.Default = !classTaken
		list = append(list, row)
	}
	if err := autogen.UpsertSidecarBackendList(a.GeneratePath, list); err != nil {
		return err
	}
	return s.regenReload()
}

// regenReload regenerates the config from the sidecar and hot-reloads, for
// callers outside a request (the install goroutine).
func (s *Server) regenReload() error {
	a := s.autogen
	if a == nil {
		return nil
	}
	if _, err := autogen.EnsureConfig(a.GeneratePath, a.ConfigPath, a.ModelsDir, func(m string) { s.proxylog.Info(m) }); err != nil {
		return err
	}
	if a.Reload != nil {
		a.Reload()
	}
	return nil
}

// classDefault reports whether this component's managed row is the one the
// launcher actually picks for its class, and when it isn't, the label of the row
// that wins instead. Installing a backend does not steal ★ from a row the user
// set up earlier, so without this a managed install can sit on disk, up to date,
// and never actually be launched.
//
// It mirrors resolveBackend's precedence exactly — the ★ row of the class, else
// the FIRST row of the class. That fallback is the part worth being careful
// about: with two backends of one kind and no ★ anywhere, one of them is still
// silently the winner, and reporting "no default is set" on both cards leaves
// the user unable to tell which binary runs. implicit says the win came from
// list order rather than a deliberate ★, which is worth wording differently.
func (s *Server) classDefault(c backends.Component) (isDefault bool, owner string, implicit bool) {
	if c.Kind == "" || s.autogen == nil {
		return false, "", false
	}
	list, err := autogen.LoadSidecarBackendList(s.autogen.GeneratePath)
	if err != nil {
		return false, "", false
	}
	return classDefaultFor(list, autogen.KindClass(c.Kind), managedEntry(list, c.ID))
}

// classDefaultFor is classDefault's decision, split out from the sidecar read so
// it can be tested directly. mine is the index of this component's row, or -1.
func classDefaultFor(list []autogen.BackendEntry, class string, mine int) (isDefault bool, owner string, implicit bool) {
	starred, first := -1, -1
	for i, e := range list {
		if autogen.KindClass(e.Kind) != class {
			continue
		}
		if first < 0 {
			first = i
		}
		if e.Default && starred < 0 {
			starred = i
		}
	}
	win := starred
	if win < 0 {
		win, implicit = first, true
	}
	if win < 0 {
		// No row of this class at all: nothing resolves through the registry and
		// autogen keeps its legacy derived exe.
		return false, "", false
	}
	if win == mine {
		return true, "", implicit
	}
	label := list[win].Name
	if label == "" {
		label = list[win].Path
	}
	return false, label, implicit
}

// setClassDefault moves ★ within one class onto the given row.
func setClassDefault(list []autogen.BackendEntry, id string) []autogen.BackendEntry {
	class := ""
	for _, e := range list {
		if e.ID == id {
			class = autogen.KindClass(e.Kind)
		}
	}
	for i := range list {
		if autogen.KindClass(list[i].Kind) == class {
			list[i].Default = list[i].ID == id
		}
	}
	return list
}

// activeBuild returns the installed build the registry currently points at.
func (s *Server) activeBuild(comp string, installed []backends.Installed) *backends.Installed {
	if s.autogen == nil {
		return nil
	}
	list, err := autogen.LoadSidecarBackendList(s.autogen.GeneratePath)
	if err != nil {
		return nil
	}
	i := managedEntry(list, comp)
	if i < 0 {
		return nil
	}
	for k := range installed {
		if strings.EqualFold(installed[k].Exe, list[i].Path) {
			return &installed[k]
		}
	}
	return nil
}

// preflightCache memoises peimports.Hint per installed build. The catalog is
// polled while the Backends tab is open and the walk opens every DLL beside the
// exe, so recomputing it per request would turn an idle tab into steady disk
// traffic. An install directory is immutable once committed (versioned dirs are
// never written in place — a reinstall replaces the whole tree), and the exe's
// modtime keys the entry so a replaced build is re-checked rather than
// inheriting the old verdict.
var preflightCache sync.Map // exePath+"\x00"+modtime -> string

// buildWarning reports why an installed build cannot run, or "" when it looks
// fine. Cached; see preflightCache.
func buildWarning(exe string) string {
	st, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	key := exe + "\x00" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
	if v, ok := preflightCache.Load(key); ok {
		return v.(string)
	}
	hint := peimports.Hint(exe)
	preflightCache.Store(key, hint)
	return hint
}

// gpuNames lists the host's GPU names from the latest perf sample, deduped.
func (s *Server) gpuNames() []string {
	if s.perf == nil {
		return nil
	}
	_, gpus := s.perf.Current()
	seen := map[string]bool{}
	var out []string
	for _, g := range gpus {
		n := strings.TrimSpace(g.Name)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// --- handlers ---

// requireBackendMgr guards the managed-install endpoints.
func (s *Server) requireBackendMgr(w http.ResponseWriter, r *http.Request) bool {
	if s.backends == nil {
		shared.SendResponse(w, r, http.StatusNotImplemented, "the backend manager is unavailable in this build")
		return false
	}
	return true
}

// handleAPIBackendCatalog returns the installable components with what is
// already on disk. Local-only — it never calls GitHub, so opening the settings
// modal is instant and works offline.
func (s *Server) handleAPIBackendCatalog(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) {
		return
	}
	m := s.backends
	gpus := s.gpuNames()
	resp := backendCatalogResp{Root: m.Root(), OS: runtime.GOOS, Jobs: m.Jobs(), GPUs: gpus}
	// Same reason as the per-component slices below: never hand the UI a null
	// where it expects a list.
	if resp.Jobs == nil {
		resp.Jobs = []backends.Job{}
	}
	if resp.GPUs == nil {
		resp.GPUs = []string{}
	}
	// m.Catalog(), not the package-level one: it appends the user's tracked repos
	// to the built-ins, which is what makes a custom source render as an ordinary
	// card with the ordinary install controls.
	cat := m.Catalog()
	resp.Components = make([]backendComponentDTO, 0, len(cat))
	for _, c := range cat {
		installed := m.Installed(c.ID)
		active := s.activeBuild(c.ID, installed)
		if c.Kind == "" && active == nil && len(installed) > 0 {
			// A helper binary (yt-dlp) has no registry row to point at a build, so
			// there is nothing to activate. Consumers take the newest install —
			// Installed() sorts newest-first, and ytDlpPath takes [0] — so report
			// that one as active rather than showing an inert "use" control.
			active = &installed[0]
		}
		dto := backendComponentDTO{
			ID: c.ID, Name: c.Name, Blurb: c.Blurb, Repo: c.Repo, Kind: c.Kind,
			Manual: c.Manual, Setup: c.Setup, Custom: c.Custom,
			Supported: c.SupportedOn(runtime.GOOS),
			Suggested: c.DefaultVariant(gpus, runtime.GOOS),
			// Non-nil so the field marshals as [] rather than null: the UI treats
			// these as plain arrays, and a null is a render-time crash.
			Variants:  make([]backendVariantDTO, 0, len(c.Variants)),
			Installed: make([]backendInstalledDTO, 0, len(installed)),
		}
		for _, v := range c.Variants {
			dto.Variants = append(dto.Variants, backendVariantDTO{
				ID: v.ID, Label: v.Label, Note: v.Note,
				Available: len(v.Patterns[runtime.GOOS]) > 0,
			})
		}
		for _, in := range installed {
			row := backendInstalledDTO{
				Version: in.Version, Variant: in.Variant, Exe: in.Exe,
				InstalledAt: in.InstalledAt, SizeBytes: in.SizeBytes,
				Active:  active != nil && active.Exe == in.Exe,
				Warning: buildWarning(in.Exe),
			}
			if row.Active {
				a := row
				dto.Active = &a
			}
			dto.Installed = append(dto.Installed, row)
		}
		dto.IsDefault, dto.DefaultOwner, dto.DefaultImplicit = s.classDefault(c)
		dto.Class = autogen.KindClass(c.Kind)
		resp.Components = append(resp.Components, dto)
	}
	writeJSON(w, resp)
}

// handleAPIBackendReleases lists a component's recent upstream releases. Hits
// GitHub (10-minute cache; ?refresh=1 forces a fresh check).
func (s *Server) handleAPIBackendReleases(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) {
		return
	}
	id := r.PathValue("component")
	comp, ok := s.backends.Find(id)
	if !ok {
		shared.SendResponse(w, r, http.StatusNotFound, "unknown component")
		return
	}
	force := r.URL.Query().Get("refresh") == "1"
	rels, err := s.backends.Releases(r.Context(), id, force)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadGateway, "listing releases failed: "+err.Error())
		return
	}
	out := make([]backendReleaseDTO, 0, len(rels))
	for _, rel := range rels {
		names := rel.AssetNames()
		d := backendReleaseDTO{Tag: rel.Tag, Name: rel.Name, PublishedAt: rel.PublishedAt, Prerelease: rel.Prerelease}
		for _, v := range comp.Variants {
			if _, _, err := comp.MatchAssets(v.ID, runtime.GOOS, names); err == nil {
				d.Variants = append(d.Variants, v.ID)
			}
		}
		if len(d.Variants) == 0 {
			continue // nothing installable from this release on this OS
		}
		out = append(out, d)
	}
	writeJSON(w, out)
}

// handleAPIBackendInstall starts a download+install job and returns its id.
func (s *Server) handleAPIBackendInstall(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) {
		return
	}
	var body backendInstallReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	id, err := s.backends.Install(body.Component, body.Variant, body.Version)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]string{"jobId": id})
}

// handleAPIBackendJobs returns the install-job history (the UI polls this for
// progress while a download runs).
func (s *Server) handleAPIBackendJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) {
		return
	}
	jobs := s.backends.Jobs()
	if jobs == nil {
		jobs = []backends.Job{} // the poller iterates this; never send null
	}
	writeJSON(w, jobs)
}

// handleAPIBackendActivate points the component's registry row at an already
// installed version (switch build / roll back an update).
func (s *Server) handleAPIBackendActivate(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) || !s.requireAutogen(w, r) {
		return
	}
	var body backendBuildRef
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var found *backends.Installed
	for _, in := range s.backends.Installed(body.Component) {
		if in.Version == body.Version && in.Variant == body.Variant {
			c := in
			found = &c
			break
		}
	}
	if found == nil {
		shared.SendResponse(w, r, http.StatusNotFound, "that build is not installed")
		return
	}
	if err := s.registerManagedBackend(*found); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "activating failed: "+err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "activated", "exe": found.Exe})
}

// handleAPIBackendDefault makes a managed component the ★ auto-pick for its
// class, taking it from whichever row holds it now. Separate from activate,
// which only chooses between installed builds of this component: a user can
// legitimately keep a hand-built backend as the default and still install and
// track upstream releases alongside it.
func (s *Server) handleAPIBackendDefault(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) || !s.requireAutogen(w, r) {
		return
	}
	var body backendBuildRef
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	list, err := autogen.LoadSidecarBackendList(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	i := managedEntry(list, body.Component)
	if i < 0 {
		shared.SendResponse(w, r, http.StatusNotFound, "that component is not installed")
		return
	}
	list = setClassDefault(list, list[i].ID)
	if err := autogen.UpsertSidecarBackendList(s.autogen.GeneratePath, list); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.regenReload(); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "default"})
}

// handleAPIBackendUninstall deletes one installed build. The active build is
// refused — switch to another version first, so the registry can't be left
// pointing at a deleted exe.
func (s *Server) handleAPIBackendUninstall(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) {
		return
	}
	var body backendBuildRef
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	installed := s.backends.Installed(body.Component)
	if active := s.activeBuild(body.Component, installed); active != nil &&
		active.Version == body.Version && active.Variant == body.Variant {
		shared.SendResponse(w, r, http.StatusConflict, "that build is in use - activate another version first")
		return
	}
	if err := s.backends.Uninstall(body.Component, body.Version, body.Variant); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "removed"})
}
