package server

// Deleting a model from the Models table: the weight file(s) of ONE quant.
//
// The unit is the FILE, not the catalog id. Every ctx tier, -vision twin and
// backend variant of a model launches the same gguf, so "delete this id" and
// "delete this file" are the same act, and the plan names every id that goes
// with it. Companion files in the same folder (a projector, an encoder, another
// quant) are deliberately left alone and listed as kept: a projector is shared
// by every quant beside it and an encoder by every image model that names it,
// and nothing here can prove the last user of one is gone.
//
// The plan is served on its own (GET) so the confirmation dialog shows exactly
// what the DELETE will remove, computed by the same function.

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

type deleteFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type deletePlan struct {
	Model string       `json:"model"`
	Files []deleteFile `json:"files"`
	Bytes int64        `json:"bytes"`
	// Catalog ids that load this file and vanish with it (the model itself,
	// its ctx tiers, twins and backend variants).
	Removes []string `json:"removes"`
	// Ids among Removes that are loaded right now; the DELETE unloads them.
	Running []string `json:"running,omitempty"`
	// Other catalog ids naming one of the files as a secondary argument (a
	// draft model, typically). They lose it on the regen that follows.
	UsedBy []string `json:"usedBy,omitempty"`
	// Files left in the folder, for the dialog to say what is NOT removed.
	Kept []deleteFile `json:"kept,omitempty"`
}

// planModelDelete resolves what deleting realID would remove. roots are the
// folders model discovery scans; a file outside all of them is refused, so this
// route can only ever remove what the models folder put in the catalog.
func planModelDelete(cfg config.Config, realID string, roots []string, running map[string]bool) (deletePlan, error) {
	mc, ok := cfg.Models[realID]
	if !ok {
		return deletePlan{}, errors.New("model not found")
	}
	weights := modelGguf(mc)
	if weights == "" {
		return deletePlan{}, errors.New("model has no weights file to delete")
	}
	files, err := weightFiles(weights)
	if err != nil {
		return deletePlan{}, err
	}
	plan := deletePlan{Model: realID}
	for _, f := range files {
		if !underAnyRoot(f.Path, roots) {
			return deletePlan{}, fmt.Errorf("%s is outside the models folder; delete it by hand", f.Path)
		}
		plan.Files = append(plan.Files, f)
		plan.Bytes += f.Size
	}

	ids := make([]string, 0, len(cfg.Models))
	for id := range cfg.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m := cfg.Models[id]
		if config.PathEqual(modelGguf(m), weights) {
			plan.Removes = append(plan.Removes, id)
			continue
		}
		if cmdNamesAny(m.Cmd, plan.Files) {
			plan.UsedBy = append(plan.UsedBy, id)
		}
	}
	removes := map[string]bool{}
	for _, id := range plan.Removes {
		removes[id] = true
	}
	for id := range running {
		// A per-request ?ctx= variant runs under its own id; its base names it.
		if base, _, _ := config.SplitCtxRequest(id); removes[base] {
			plan.Running = append(plan.Running, id)
		}
	}
	sort.Strings(plan.Running)
	plan.Kept = keptSiblings(filepath.Dir(filepath.FromSlash(weights)), plan.Files)
	return plan, nil
}

// weightFiles is the file set one model path stands for: every shard of a
// multi-part gguf (the same glob statSizeGB sums), else the file itself. Only
// regular files qualify; a model directory (some audio.cpp packages) or a
// symlink is refused rather than walked.
func weightFiles(path string) ([]deleteFile, error) {
	path = filepath.FromSlash(path)
	paths := []string{path}
	if m := shardSuffix.FindStringSubmatch(filepath.ToSlash(path)); m != nil {
		if matches, err := filepath.Glob(shardSuffix.ReplaceAllString(path, `-*-of-`+m[1]+`.gguf`)); err == nil && len(matches) > 0 {
			paths = matches
		}
	}
	sort.Strings(paths)
	out := make([]deleteFile, 0, len(paths))
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", p, err)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a plain file; delete it by hand", p)
		}
		out = append(out, deleteFile{Path: p, Size: fi.Size()})
	}
	return out, nil
}

func underAnyRoot(path string, roots []string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		r, err := filepath.Abs(strings.TrimSpace(root))
		if err != nil || strings.TrimSpace(root) == "" {
			continue
		}
		rel, err := filepath.Rel(r, abs)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		return true
	}
	return false
}

// cmdNamesAny reports whether a launch command passes one of files as an
// argument value, token-exact (bare or --flag=value).
func cmdNamesAny(cmd string, files []deleteFile) bool {
	for _, a := range config.ParseCmd(cmd).Argv {
		if i := strings.Index(a, "="); i > 0 && strings.HasPrefix(a, "-") {
			a = a[i+1:]
		}
		for _, f := range files {
			if config.PathEqual(a, f.Path) {
				return true
			}
		}
	}
	return false
}

// keptSiblings lists the plain files in dir that are not being deleted, minus
// dotfiles (the hub's own bookkeeping).
func keptSiblings(dir string, del []deleteFile) []deleteFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []deleteFile
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		gone := false
		for _, f := range del {
			if config.PathEqual(p, f.Path) {
				gone = true
				break
			}
		}
		if gone {
			continue
		}
		if fi, err := e.Info(); err == nil {
			out = append(out, deleteFile{Path: p, Size: fi.Size()})
		}
	}
	return out
}

func (s *Server) runningSet() map[string]bool {
	out := map[string]bool{}
	for id := range s.local.RunningModels() {
		out[id] = true
	}
	return out
}

// resolveDeletePlan is the shared front half of both handlers.
func (s *Server) resolveDeletePlan(w http.ResponseWriter, r *http.Request) (deletePlan, bool) {
	realID, _, _, ok := s.resolveModelGguf(w, r)
	if !ok {
		return deletePlan{}, false
	}
	plan, err := planModelDelete(s.config(), realID, s.modelsRoots(), s.runningSet())
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return deletePlan{}, false
	}
	return plan, true
}

// handleAPIModelDeletePlan answers what DELETE /api/models/{model}/files would
// remove, without touching anything.
func (s *Server) handleAPIModelDeletePlan(w http.ResponseWriter, r *http.Request) {
	if plan, ok := s.resolveDeletePlan(w, r); ok {
		writeJSON(w, plan)
	}
}

// handleAPIModelDeleteFiles unloads every process serving the file, removes it
// (all shards), drops the folder if that emptied it, and regenerates + reloads
// so the catalog loses the rows. The unload comes first because Windows refuses
// to delete a file a running server has mapped.
func (s *Server) handleAPIModelDeleteFiles(w http.ResponseWriter, r *http.Request) {
	plan, ok := s.resolveDeletePlan(w, r)
	if !ok {
		return
	}
	if len(plan.Running) > 0 {
		s.local.Unload(apiUnloadTimeout, plan.Running...)
	}
	var freed int64
	var failed []string
	for _, f := range plan.Files {
		if err := os.Remove(f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed = append(failed, f.Path+": "+err.Error())
			continue
		}
		freed += f.Size
		sizeCache.Delete(filepath.ToSlash(f.Path))
		sizeCache.Delete(f.Path)
	}
	if len(plan.Files) > 0 && len(plan.Kept) == 0 {
		// A hub download nests <owner>/<repo>; take the owner folder too once
		// its last repo is gone, but never a models root itself.
		dir := filepath.Dir(plan.Files[0].Path)
		if removeEmptyDir(dir, s.modelsRoots()) {
			removeEmptyDir(filepath.Dir(dir), s.modelsRoots())
		}
	}
	s.proxylog.Infof("deleted model %s: %d file(s), %d bytes freed", plan.Model, len(plan.Files)-len(failed), freed)
	// Regenerate even after a partial failure: whatever did go is gone, and the
	// catalog must stop offering a model with a missing shard.
	if !s.regenAndReload(w, r) {
		return
	}
	if len(failed) > 0 {
		shared.SendResponse(w, r, http.StatusInternalServerError, "could not delete: "+strings.Join(failed, "; "))
		return
	}
	writeJSON(w, map[string]any{"model": plan.Model, "freedBytes": freed})
}

// removeEmptyDir removes dir if the only thing left in it is the hub's
// manifest (a journal means a paused download still owns the folder), and
// reports whether it did. A models root is never removed, empty or not.
func removeEmptyDir(dir string, roots []string) bool {
	for _, r := range roots {
		if config.PathEqual(filepath.Clean(dir), filepath.Clean(strings.TrimSpace(r))) {
			return false
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() != ".quartermaster-hub.json" {
			return false
		}
	}
	_ = os.Remove(filepath.Join(dir, ".quartermaster-hub.json"))
	return os.Remove(dir) == nil
}
