package server

// List the models tree from inside the UI.
//
// The reveal button (revealfolder.go) shells out on the SERVER, which only ever
// makes sense when the browser and the models tree are on one box. A headless
// deployment — quartermaster in a container, dashboard open from a laptop — has
// no desktop session to open a file manager in, so that button cannot work
// there no matter what is installed (issue #66: `xdg-open` missing in an LXC is
// the symptom; no display is the cause). This is the answer for that case: a
// read-only listing the browser renders itself, so "where did that download
// land, and what is in there" is answerable without a shell on the host.
//
// Read-only on purpose. Nothing here renames, moves or deletes, and nothing
// serves file CONTENT — it reports names, sizes and mtimes for directories at
// or under the models root, and refuses everything else. Those refusals are the
// security boundary of this file: see resolveUnderRoot.

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// A directory of tens of thousands of entries is a config mistake, not a
// listing to render; cap it so one bad path cannot hand the browser a 50 MB
// JSON document.
const hubFilesMaxEntries = 4000

type hubFileEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Dir      bool   `json:"dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified,omitempty"`
}

type hubFilesDTO struct {
	Root      string         `json:"root"`
	Path      string         `json:"path"`
	Rel       string         `json:"rel"`
	Parent    string         `json:"parent"`
	Entries   []hubFileEntry `json:"entries"`
	Truncated bool           `json:"truncated,omitempty"`
}

// handleAPIHubFiles lists one directory under the models root.
//
// Empty path = the root itself, the same convention as /api/hub/reveal, so the
// UI's "open the models folder" needs no path to say "the obvious one".
func (s *Server) handleAPIHubFiles(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimSpace(s.hubModelsRoot())
	if root == "" {
		shared.SendResponse(w, r, http.StatusNotImplemented, "the models folder is not configured in this build")
		return
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "cannot resolve the models folder: "+err.Error())
		return
	}
	target, err := revealTarget(rootAbs, r.URL.Query().Get("path"))
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	entries, truncated, err := listFolderEntries(target)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "cannot read that folder: "+err.Error())
		return
	}

	out := hubFilesDTO{Root: rootAbs, Path: target, Entries: entries, Truncated: truncated}
	if rel, err := filepath.Rel(rootAbs, target); err == nil && rel != "." {
		out.Rel = filepath.ToSlash(rel)
	}
	// The root has no parent the caller may walk to, and saying so here keeps
	// the "up" affordance out of the UI's hands: it cannot offer a step the
	// server would refuse.
	if target != rootAbs {
		out.Parent = filepath.Dir(target)
	}
	writeJSON(w, out)
}

// listFolderEntries reads one directory into the DTO's entry form.
//
// Split out of the handler so the shape and the ordering can be tested without
// standing up a Server: the interesting behaviour here is all in what a
// symlink, a dangling link and a huge directory turn into.
func listFolderEntries(dir string) ([]hubFileEntry, bool, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, err
	}
	out := []hubFileEntry{}
	truncated := false
	for _, de := range des {
		if len(out) >= hubFilesMaxEntries {
			truncated = true
			break
		}
		e := hubFileEntry{Name: de.Name(), Path: filepath.Join(dir, de.Name()), Dir: de.IsDir()}
		// A symlink reports as neither dir nor file until it is followed; Stat
		// answers what it POINTS at, which is what a listing should show. A
		// dangling one keeps the Lstat answer rather than vanishing.
		if de.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(e.Path); err == nil {
				e.Dir = st.IsDir()
			}
		}
		if fi, err := de.Info(); err == nil {
			if !e.Dir {
				e.Size = fi.Size()
			}
			e.Modified = fi.ModTime().UTC().Format(time.RFC3339)
		}
		out = append(out, e)
	}
	// Folders first, then case-insensitive by name: the tree is repo dirs
	// holding a handful of weights, so burying the dirs under 30 shards is the
	// one ordering that makes it unreadable.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, truncated, nil
}
