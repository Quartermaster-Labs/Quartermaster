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
	"runtime"
	"strings"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/backends"
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
}

type backendComponentDTO struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Blurb     string                `json:"blurb"`
	Repo      string                `json:"repo"`
	Kind      string                `json:"kind"`            // "" = helper binary, never registered
	Manual    bool                  `json:"manual"`          // engine we can drive but not install
	Setup     string                `json:"setup,omitempty"` // shown instead of install controls
	Supported bool                  `json:"supported"`
	Suggested string                `json:"suggested"` // variant preselected for this host
	Variants  []backendVariantDTO   `json:"variants"`
	Installed []backendInstalledDTO `json:"installed"`
	Active    *backendInstalledDTO  `json:"active,omitempty"`
	// IsDefault reports whether this component's registry row is the ★ auto-pick
	// for its class, i.e. whether installing it actually changed what
	// Quartermaster launches. DefaultOwner names the row that holds ★ instead —
	// typically a hand-entered backend the user set up before installing this
	// one, which silently keeps winning.
	IsDefault    bool   `json:"isDefault"`
	DefaultOwner string `json:"defaultOwner,omitempty"`
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
	comp, ok := backends.Find(inst.Component)
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

// classDefault reports whether this component's managed row holds ★ for its
// class and, when it doesn't, the label of the row that does. Installing a
// backend does not steal ★ from a row the user set up earlier, so without this
// a managed install can sit on disk, up to date, and never actually be launched.
func (s *Server) classDefault(c backends.Component) (isDefault bool, owner string) {
	if c.Kind == "" || s.autogen == nil {
		return false, ""
	}
	list, err := autogen.LoadSidecarBackendList(s.autogen.GeneratePath)
	if err != nil {
		return false, ""
	}
	class := autogen.KindClass(c.Kind)
	mine := managedEntry(list, c.ID)
	for i, e := range list {
		if !e.Default || autogen.KindClass(e.Kind) != class {
			continue
		}
		if i == mine {
			return true, ""
		}
		label := e.Name
		if label == "" {
			label = e.Path
		}
		return false, label
	}
	// No row of this class is starred: autogen falls back to its built-in
	// default, so a managed install is still not what gets launched.
	return false, ""
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
	resp.Components = make([]backendComponentDTO, 0, len(backends.Catalog()))
	for _, c := range backends.Catalog() {
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
			Manual: c.Manual, Setup: c.Setup,
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
				Active: active != nil && active.Exe == in.Exe,
			}
			if row.Active {
				a := row
				dto.Active = &a
			}
			dto.Installed = append(dto.Installed, row)
		}
		dto.IsDefault, dto.DefaultOwner = s.classDefault(c)
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
	comp, ok := backends.Find(id)
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
		shared.SendResponse(w, r, http.StatusConflict, "that build is in use — activate another version first")
		return
	}
	if err := s.backends.Uninstall(body.Component, body.Version, body.Variant); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "removed"})
}
