package server

// A file picker the browser renders itself.
//
// Every Browse button used to open a NATIVE dialog on the server (zenity,
// WinForms). On a headless box there is no desktop to open it on, and with the
// dashboard open from another machine the dialog pops on a screen nobody is
// looking at (issue #93). This is the fallback: the UI lists folders through
// this route and hands the chosen path back to the same form field the native
// dialog would have filled.
//
// Confined to a short list of roots on purpose: the models folders and the
// backends install folder, which is where every path a Browse button asks for
// normally lives. The dashboard has no login of its own (API keys gate
// inference only), so a route that walked the whole disk would be a disk
// listing for whoever can reach the admin listener. Anything outside the roots
// stays a typed path. Containment is revealTarget's, the same check the
// models-folder listing uses.

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// pickRoot is one folder the web picker may browse under.
type pickRoot struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

type pickBrowseDTO struct {
	Roots     []pickRoot     `json:"roots"`
	Root      string         `json:"root"` // id of the root being listed
	Path      string         `json:"path"`
	Rel       string         `json:"rel"`
	Parent    string         `json:"parent"` // "" at the root: no step up is offered
	Entries   []hubFileEntry `json:"entries"`
	Truncated bool           `json:"truncated,omitempty"`
	// Filtered reports that files were hidden by the kind's extension list, so
	// the UI can offer "show all files" only when it would change something.
	Filtered bool `json:"filtered,omitempty"`
}

// pickRoots lists the folders the web picker may browse, in display order:
// the shared models folder, each per-category scan folder, then the backends
// install folder. Folders that are not on disk are left out, and duplicates
// collapse to their first appearance.
func (s *Server) pickRoots() []pickRoot {
	var out []pickRoot
	seen := map[string]bool{}
	add := func(id, label, p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return
		}
		key := config.PathKey(abs)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, pickRoot{ID: id, Label: label, Path: abs})
	}

	if a := s.autogen; a != nil {
		if gf, err := autogen.LoadGenerateFile(a.GeneratePath, a.ModelsDir); err == nil {
			add("models", "Models", gf.Settings.ModelsRoot)
			for _, c := range autogen.CategoryOrder {
				add("models:"+c, "Models ("+c+")", gf.Settings.CategoryRoots[c])
			}
		} else {
			add("models", "Models", s.hubModelsRoot())
		}
	}
	if s.backends != nil {
		add("backends", "Backends", s.backends.Root())
	}
	return out
}

// handleAPIPickBrowse lists one folder under one of the picker roots.
//
// Query: root (a pickRoot id; default: the root holding an absolute `path`,
// else the first root), path (absolute, or relative to the root; a file
// resolves to its folder, so a form field's current value is a valid start),
// kind ("folder" lists folders only; otherwise a pickSpecs key whose Exts
// filter the files), all=1 to drop that filter.
func (s *Server) handleAPIPickBrowse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := strings.TrimSpace(q.Get("kind"))
	foldersOnly := kind == "" || kind == "folder"
	var exts []string
	if !foldersOnly {
		spec, ok := pickSpecs[kind]
		if !ok {
			shared.SendResponse(w, r, http.StatusBadRequest, "unknown file picker kind: "+kind)
			return
		}
		if q.Get("all") != "1" {
			exts = spec.Exts
		}
	}

	roots := s.pickRoots()
	if len(roots) == 0 {
		shared.SendResponse(w, r, http.StatusNotImplemented, "no browsable folders are configured (set a models folder first)")
		return
	}
	root, target, err := pickBrowseTarget(roots, q.Get("root"), q.Get("path"))
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	entries, truncated, err := listFolderEntries(target)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "cannot read that folder: "+err.Error())
		return
	}
	entries, filtered := filterPickEntries(entries, foldersOnly, exts)

	out := pickBrowseDTO{Roots: roots, Root: root.ID, Path: target, Entries: entries, Truncated: truncated, Filtered: filtered}
	if rel, err := filepath.Rel(root.Path, target); err == nil && rel != "." {
		out.Rel = filepath.ToSlash(rel)
	}
	if target != root.Path {
		out.Parent = filepath.Dir(target)
	}
	writeJSON(w, out)
}

// pickBrowseTarget picks the root to list under and resolves the path inside
// it. An explicit root id must exist; without one, an absolute path selects
// the first root that contains it, and a path under no root is refused rather
// than quietly listing some other folder.
func pickBrowseTarget(roots []pickRoot, rootID, path string) (pickRoot, string, error) {
	rootID = strings.TrimSpace(rootID)
	path = strings.TrimSpace(path)
	if rootID != "" {
		for _, rt := range roots {
			if rt.ID == rootID {
				target, err := revealTarget(rt.Path, path)
				return rt, target, err
			}
		}
		return pickRoot{}, "", fmt.Errorf("unknown picker root: %s", rootID)
	}
	if path == "" || !filepath.IsAbs(path) {
		target, err := revealTarget(roots[0].Path, path)
		return roots[0], target, err
	}
	for _, rt := range roots {
		if target, err := revealTarget(rt.Path, path); err == nil {
			return rt, target, nil
		}
	}
	return pickRoot{}, "", fmt.Errorf("refusing to list %q: it is outside the folders the picker can browse", path)
}

// filterPickEntries drops what the picker cannot select: every file when
// picking a folder, and files whose extension is not in exts otherwise (an
// empty exts keeps them all). Folders always stay, since they are how the user
// gets somewhere. filtered reports whether any file was hidden by exts.
func filterPickEntries(in []hubFileEntry, foldersOnly bool, exts []string) (out []hubFileEntry, filtered bool) {
	out = make([]hubFileEntry, 0, len(in))
	for _, e := range in {
		if e.Dir {
			out = append(out, e)
			continue
		}
		if foldersOnly {
			continue
		}
		if len(exts) > 0 && !hasPickExt(e.Name, exts) {
			filtered = true
			continue
		}
		out = append(out, e)
	}
	return out, filtered
}

func hasPickExt(name string, exts []string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, x := range exts {
		if ext == x {
			return true
		}
	}
	return false
}

// nativePickerUsable reports whether opening a NATIVE dialog could reach the
// person who clicked Browse: the browser must be on this machine (loopback,
// judged on RemoteAddr for the same reason canReveal does) and the platform
// must have a dialog to show. When it cannot, the pick handlers answer 501 up
// front, which is the UI's cue to open the web picker, instead of popping a
// dialog on a screen nobody is looking at or failing on a missing zenity.
func nativePickerUsable(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !isLoopbackIP(strings.Trim(host, "[]")) {
		return false
	}
	return nativePickerPresent()
}

// runNativePick is the shared front half of every native pick handler. It
// answers 501 when no dialog can reach the caller (the UI's cue for the web
// picker; also what a dialog that fails to launch maps to) and 204 on cancel,
// returning ok=false once it has written that response.
func runNativePick(w http.ResponseWriter, r *http.Request, pick func() (string, error)) (string, bool) {
	if !nativePickerUsable(r) {
		shared.SendResponse(w, r, http.StatusNotImplemented,
			"no native file dialog can reach this browser (no desktop on the server, or the dashboard is open from another machine)")
		return "", false
	}
	path, err := pick()
	if err != nil {
		shared.SendResponse(w, r, http.StatusNotImplemented, "file picker unavailable: "+err.Error())
		return "", false
	}
	if strings.TrimSpace(path) == "" {
		w.WriteHeader(http.StatusNoContent) // cancelled
		return "", false
	}
	return path, true
}

// runFolderPick resolves the folder for a persisting folder picker: the path
// the web picker sent when there is one (validated, 400 if bad), else the
// native dialog.
func runFolderPick(w http.ResponseWriter, r *http.Request, given string) (string, bool) {
	if strings.TrimSpace(given) == "" {
		return runNativePick(w, r, pickFolder)
	}
	p, err := pickedFolder(given)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return "", false
	}
	return p, true
}

// pickedFolder validates a folder path the web picker (or a typed field) sent
// in place of a native dialog result. It must be absolute and an existing
// folder; it is not confined to the picker roots, because choosing a scan
// folder outside the current ones is the whole point of the category picker.
func pickedFolder(p string) (string, error) {
	p = strings.TrimSpace(p)
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("folder path must be absolute: %q", p)
	}
	st, err := os.Stat(p)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("not a folder on this machine: %q", p)
	}
	return filepath.Clean(p), nil
}
