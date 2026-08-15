package server

// Tracked backend repos: the "install a backend" flow, pointed at a GitHub repo
// the built-in catalog doesn't know about — a llama.cpp fork with different GPU
// builds, an in-house engine, anything that publishes release assets.
//
// The design constraint that shapes this whole file: the user never writes an
// asset regex. They pick the asset they want out of a real release and the
// server derives the pattern from it (internal/backends/derive.go), so a tracked
// source ends up with exactly the same Component shape the static catalog uses
// and every downstream path — install, activate, ★ default, rollback — is
// unchanged. What the UI shows in place of a pattern is the file name the source
// currently resolves to, which is something a user can actually judge.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/backends"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// customIDPrefix namespaces tracked-source ids away from the built-in catalog.
// A collision would let a tracked repo take over a built-in's install directory
// and registry row, so the prefix is a guard, not decoration (Manager.Catalog
// drops colliding ids as a second line of defence).
const customIDPrefix = "custom-"

// --- wire types ---

type backendSourceVariantDTO struct {
	ID      string   `json:"id,omitempty"`
	Label   string   `json:"label"`
	Asset   string   `json:"asset"`
	Pattern string   `json:"pattern,omitempty"` // derived; read-only, shown only as detail
	Extras  []string `json:"extras,omitempty"`  // companion asset names
}

type backendSourceDTO struct {
	ID              string                    `json:"id,omitempty"`
	Name            string                    `json:"name"`
	Blurb           string                    `json:"blurb,omitempty"`
	Repo            string                    `json:"repo"`
	Kind            string                    `json:"kind"`
	Exe             string                    `json:"exe"`
	Bare            bool                      `json:"bare,omitempty"`
	AllowPrerelease bool                      `json:"allowPrerelease,omitempty"`
	OS              string                    `json:"os,omitempty"`
	Tag             string                    `json:"tag,omitempty"` // release the assets were picked from
	Variants        []backendSourceVariantDTO `json:"variants"`
}

type backendAssetDTO struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Recommended marks assets plausible for this host — the right OS, and an
	// archive or executable rather than a checksum or signature file. The picker
	// shows these first instead of dumping thirty files at the user.
	Recommended bool `json:"recommended"`
}

type backendAssetsResp struct {
	Repo      string              `json:"repo"`
	Tag       string              `json:"tag"`
	HasStable bool                `json:"hasStable"`
	Releases  []backendReleaseDTO `json:"releases"`
	Assets    []backendAssetDTO   `json:"assets"`
}

type backendResolveResp struct {
	Component string `json:"component"`
	Variant   string `json:"variant"`
	Tag       string `json:"tag"`
	Asset     string `json:"asset,omitempty"`
	// Set only when nothing matched: the closest asset by name similarity, so the
	// UI can say "upstream looks renamed, nearest is X" instead of a dead error.
	Error   string `json:"error,omitempty"`
	Closest string `json:"closest,omitempty"`
	Score   int    `json:"score,omitempty"`
}

// --- sidecar <-> component ---

// trackedSources is the Manager.Sources hook: the user's tracked repos in the
// shape internal/backends consumes. Errors are swallowed to nil — a broken
// sidecar must not take the whole backends tab down with it, and the built-in
// catalog still works.
func (s *Server) trackedSources() []backends.Component {
	if s.autogen == nil {
		return nil
	}
	list, err := autogen.LoadSidecarBackendSources(s.autogen.GeneratePath)
	if err != nil {
		return nil
	}
	out := make([]backends.Component, 0, len(list))
	for _, src := range list {
		out = append(out, sourceComponent(src))
	}
	return out
}

// sourceComponent converts a persisted source into the Component the installer
// understands. Patterns are scoped to the GOOS the source was configured on:
// the user picked from the asset list of *this* machine, so claiming the result
// is portable would just produce installs that fail on another OS.
func sourceComponent(src autogen.BackendSource) backends.Component {
	goos := src.OS
	if goos == "" {
		goos = runtime.GOOS
	}
	exe := src.Exe
	if exe == "" {
		exe = "server"
	}
	c := backends.Component{
		ID:              src.ID,
		Name:            src.Name,
		Blurb:           src.Blurb,
		Repo:            src.Repo,
		Kind:            src.Kind,
		Exe:             map[string]string{"default": exe, goos: exe},
		Bare:            src.Bare,
		Custom:          true,
		AllowPrerelease: src.AllowPrerelease,
	}
	for _, v := range src.Variants {
		bv := backends.Variant{
			ID:       v.ID,
			Label:    v.Label,
			Patterns: map[string][]string{goos: {v.Pattern}},
			Exemplar: map[string]string{goos: v.Asset},
		}
		for _, e := range v.Extras {
			bv.Extra = map[string][]string{goos: append(bv.Extra[goos], e.Pattern)}
		}
		c.Variants = append(c.Variants, bv)
	}
	return c
}

// sourceDTO renders a persisted source back for the editor.
func sourceDTO(src autogen.BackendSource) backendSourceDTO {
	d := backendSourceDTO{
		ID: src.ID, Name: src.Name, Blurb: src.Blurb, Repo: src.Repo,
		Kind: src.Kind, Exe: src.Exe, Bare: src.Bare,
		AllowPrerelease: src.AllowPrerelease, OS: src.OS,
		Variants: make([]backendSourceVariantDTO, 0, len(src.Variants)),
	}
	for _, v := range src.Variants {
		vd := backendSourceVariantDTO{ID: v.ID, Label: v.Label, Asset: v.Asset, Pattern: v.Pattern}
		for _, e := range v.Extras {
			vd.Extras = append(vd.Extras, e.Asset)
		}
		d.Variants = append(d.Variants, vd)
	}
	return d
}

// --- handlers ---

// handleAPIBackendSourcesList returns the tracked repos in editable form.
func (s *Server) handleAPIBackendSourcesList(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) || !s.requireAutogen(w, r) {
		return
	}
	list, err := autogen.LoadSidecarBackendSources(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]backendSourceDTO, 0, len(list))
	for _, src := range list {
		out = append(out, sourceDTO(src))
	}
	writeJSON(w, out)
}

// handleAPIBackendSourceAssets powers the asset picker: one release's files,
// for a repo that may not be tracked yet. This is the only GitHub call the
// add-a-repo form makes, and it is what replaces "type a regex".
func (s *Server) handleAPIBackendSourceAssets(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) {
		return
	}
	repo, err := backends.ParseRepo(r.URL.Query().Get("repo"))
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	rels, err := s.backends.ReleasesForRepo(r.Context(), repo, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadGateway, "listing releases failed: "+err.Error())
		return
	}
	if len(rels) == 0 {
		shared.SendResponse(w, r, http.StatusNotFound, "that repository publishes no releases")
		return
	}
	resp := backendAssetsResp{Repo: repo, Releases: make([]backendReleaseDTO, 0, len(rels))}
	for _, rel := range rels {
		if !rel.Prerelease {
			resp.HasStable = true
		}
		resp.Releases = append(resp.Releases, backendReleaseDTO{
			Tag: rel.Tag, Name: rel.Name, PublishedAt: rel.PublishedAt, Prerelease: rel.Prerelease,
		})
	}
	// Default to the newest release that has assets at all — a source-only tag
	// (Real-ESRGAN's newest, for one) would otherwise show an empty picker.
	want := strings.TrimSpace(r.URL.Query().Get("tag"))
	var chosen *backends.Release
	for i := range rels {
		if want != "" {
			if rels[i].Tag == want {
				chosen = &rels[i]
				break
			}
			continue
		}
		if len(rels[i].Assets) > 0 {
			chosen = &rels[i]
			break
		}
	}
	if chosen == nil {
		chosen = &rels[0]
	}
	resp.Tag = chosen.Tag
	for _, a := range chosen.Assets {
		resp.Assets = append(resp.Assets, backendAssetDTO{
			Name: a.Name, Size: a.Size, Recommended: plausibleAsset(a.Name, runtime.GOOS),
		})
	}
	writeJSON(w, resp)
}

// osHints maps a GOOS onto the substrings projects actually use in asset names.
var osHints = map[string][]string{
	"windows": {"win", "windows", "msvc", "mingw"},
	"linux":   {"linux", "ubuntu", "debian", "manylinux"},
	"darwin":  {"darwin", "macos", "osx", "apple"},
}

// plausibleAsset reports whether an asset looks like a runnable build for goos.
// It only reorders the picker — a user can always tick something it skipped —
// so it errs toward including: a name that names no OS at all stays in.
func plausibleAsset(name, goos string) bool {
	l := strings.ToLower(name)
	for _, ext := range []string{".txt", ".sha256", ".sha512", ".md5", ".sig", ".asc", ".json", ".md", ".pdb", ".whl", ".deb", ".rpm"} {
		if strings.HasSuffix(l, ext) {
			return false
		}
	}
	if strings.HasPrefix(l, "source code") {
		return false
	}
	// Named for a different OS => not for us. Named for none => keep it.
	for other, hints := range osHints {
		if other == goos {
			continue
		}
		for _, h := range hints {
			if strings.Contains(l, h) && !hasAnyHint(l, osHints[goos]) {
				return false
			}
		}
	}
	return true
}

func hasAnyHint(l string, hints []string) bool {
	for _, h := range hints {
		if strings.Contains(l, h) {
			return true
		}
	}
	return false
}

// handleAPIBackendSourceSave creates or updates a tracked repo. The client sends
// the assets it picked and the release tag it picked them from; the patterns are
// derived here, against that release's full asset list, so a build that differs
// from a sibling only by version still resolves unambiguously.
func (s *Server) handleAPIBackendSourceSave(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) || !s.requireAutogen(w, r) {
		return
	}
	var body backendSourceDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	repo, err := backends.ParseRepo(body.Repo)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Kind) == "" {
		shared.SendResponse(w, r, http.StatusBadRequest, "pick what kind of backend this is")
		return
	}
	if !body.Bare && strings.TrimSpace(body.Exe) == "" {
		shared.SendResponse(w, r, http.StatusBadRequest, "the executable name is required")
		return
	}
	if len(body.Variants) == 0 {
		shared.SendResponse(w, r, http.StatusBadRequest, "pick at least one asset to download")
		return
	}

	rels, err := s.backends.ReleasesForRepo(r.Context(), repo, false)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadGateway, "listing releases failed: "+err.Error())
		return
	}
	rel, ok := findRelease(rels, body.Tag)
	if !ok {
		shared.SendResponse(w, r, http.StatusBadRequest, "that release no longer exists - re-pick the assets")
		return
	}
	names := rel.AssetNames()

	existing, err := autogen.LoadSidecarBackendSources(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = uniqueSourceID(repo, existing)
	}

	src := autogen.BackendSource{
		ID: id, Repo: repo,
		Name:  firstNonEmpty(strings.TrimSpace(body.Name), repoTail(repo)),
		Blurb: strings.TrimSpace(body.Blurb),
		Kind:  strings.TrimSpace(body.Kind),
		Exe:   strings.TrimSpace(body.Exe),
		Bare:  body.Bare,
		OS:    runtime.GOOS,
		// A repo that has never cut a stable release (nightly-only forks are the
		// whole reason this feature exists) would otherwise resolve "latest" to
		// nothing installable. Decide it from the release history instead of
		// asking the user a question about GitHub semantics.
		AllowPrerelease: body.AllowPrerelease || !anyStable(rels),
	}
	seenVar := map[string]bool{}
	for i, v := range body.Variants {
		asset := strings.TrimSpace(v.Asset)
		if asset == "" {
			continue
		}
		if _, ok := rel.AssetByName(asset); !ok {
			shared.SendResponse(w, r, http.StatusBadRequest,
				fmt.Sprintf("%q is not an asset of %s", asset, rel.Tag))
			return
		}
		label := firstNonEmpty(strings.TrimSpace(v.Label), backends.SuggestLabel(asset))
		vid := firstNonEmpty(strings.TrimSpace(v.ID), slugifyVariant(label), fmt.Sprintf("build%d", i+1))
		for seenVar[vid] {
			vid += "x"
		}
		seenVar[vid] = true
		out := autogen.BackendSourceVariant{
			ID: vid, Label: label, Asset: asset,
			Pattern: backends.DeriveUnique(asset, names),
		}
		for _, ex := range v.Extras {
			ex = strings.TrimSpace(ex)
			if ex == "" || ex == asset {
				continue
			}
			if _, ok := rel.AssetByName(ex); !ok {
				continue // a stale pick from an older release: drop it, don't fail the save
			}
			out.Extras = append(out.Extras, autogen.BackendSourceExtra{
				Asset: ex, Pattern: backends.DeriveUnique(ex, names),
			})
		}
		src.Variants = append(src.Variants, out)
	}
	if len(src.Variants) == 0 {
		shared.SendResponse(w, r, http.StatusBadRequest, "pick at least one asset to download")
		return
	}

	replaced := false
	for i := range existing {
		if strings.EqualFold(existing[i].ID, src.ID) {
			existing[i] = src
			replaced = true
			break
		}
	}
	if !replaced {
		if _, clash := backends.Find(src.ID); clash {
			shared.SendResponse(w, r, http.StatusConflict, "that id is taken by a built-in backend")
			return
		}
		existing = append(existing, src)
	}
	if err := autogen.UpsertSidecarBackendSources(s.autogen.GeneratePath, existing); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, sourceDTO(src))
}

// handleAPIBackendSourceDelete stops tracking a repo. Installed builds block the
// removal rather than being deleted with it: the registry may be pointing at one
// of them right now, and silently uninstalling a running backend's executable is
// not something a "stop tracking" button should do.
func (s *Server) handleAPIBackendSourceDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) || !s.requireAutogen(w, r) {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	id := strings.TrimSpace(body.ID)
	if n := len(s.backends.Installed(id)); n > 0 {
		shared.SendResponse(w, r, http.StatusConflict,
			fmt.Sprintf("%d installed build(s) came from this repo - remove them first", n))
		return
	}
	list, err := autogen.LoadSidecarBackendSources(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	kept := make([]autogen.BackendSource, 0, len(list))
	found := false
	for _, src := range list {
		if strings.EqualFold(src.ID, id) {
			found = true
			continue
		}
		kept = append(kept, src)
	}
	if !found {
		shared.SendResponse(w, r, http.StatusNotFound, "not a tracked repository")
		return
	}
	if err := autogen.UpsertSidecarBackendSources(s.autogen.GeneratePath, kept); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "removed"})
}

// handleAPIBackendResolve reports the file an install would actually download
// right now. This is the preview that stands in for showing a pattern: a user
// can tell at a glance whether "llamacpp-rocm-b1247-Windows-gfx110X.zip" is the
// build they meant, which is not true of the regex behind it.
func (s *Server) handleAPIBackendResolve(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackendMgr(w, r) {
		return
	}
	comp := r.PathValue("component")
	variant := r.URL.Query().Get("variant")
	rel, asset, closest, score, err := s.backends.Resolve(r.Context(), comp, variant, r.URL.Query().Get("version"))
	if err != nil && asset == "" && rel.Tag == "" {
		shared.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	out := backendResolveResp{Component: comp, Variant: variant, Tag: rel.Tag, Asset: asset}
	if err != nil {
		out.Error, out.Closest, out.Score = err.Error(), closest, score
	}
	writeJSON(w, out)
}

// --- helpers ---

func findRelease(rels []backends.Release, tag string) (backends.Release, bool) {
	tag = strings.TrimSpace(tag)
	for _, r := range rels {
		if tag == "" || r.Tag == tag {
			return r, true
		}
	}
	return backends.Release{}, false
}

func anyStable(rels []backends.Release) bool {
	for _, r := range rels {
		if !r.Prerelease {
			return true
		}
	}
	return false
}

func repoTail(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

// slugifyVariant reduces a label to a directory-safe variant id — it becomes
// half of an install directory name.
func slugifyVariant(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// uniqueSourceID mints a namespaced id from the repo, disambiguating against
// built-ins and already-tracked repos.
func uniqueSourceID(repo string, existing []autogen.BackendSource) string {
	base := customIDPrefix + slugifyVariant(strings.ReplaceAll(repo, "/", "-"))
	taken := func(id string) bool {
		if _, ok := backends.Find(id); ok {
			return true
		}
		for _, s := range existing {
			if strings.EqualFold(s.ID, id) {
				return true
			}
		}
		return false
	}
	id := base
	for i := 2; taken(id); i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}
