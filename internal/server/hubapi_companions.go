package server

import (
	"net/http"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/hub"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// Companion files for an image DiT: what else the model needs (VAE, text
// encoder, tokenizer), whether each is already here, and its download size.
//
// The join lives here because neither side may know the other: autogen owns
// "which roles are missing" (the same resolveComponents a regen runs) and the
// curated recipe, hub owns the listing and the disk. The browser shows the
// result next to the quant picker and queues the needed rows alongside the DiT.

// Companion states, one per row.
const (
	companionLocal       = "local"       // on disk at full size, nothing to do
	companionDownloading = "downloading" // a live job already carries it
	companionNeeded      = "needed"      // will be queued with the model
	companionUnavailable = "unavailable" // the recipe names it, the hub no longer lists it
)

type hubCompanion struct {
	Role      string `json:"role"`
	Label     string `json:"label"`
	Source    string `json:"source"`
	Repo      string `json:"repo"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	State     string `json:"state"`
}

type hubCompanionsResp struct {
	// Image is false for anything that is not a split diffusion model; the
	// browser then shows nothing.
	Image      bool           `json:"image"`
	Family     string         `json:"family"`
	Missing    []string       `json:"missing"`
	Companions []hubCompanion `json:"companions"`
	Unsourced  []string       `json:"unsourced"`
	Err        string         `json:"error,omitempty"`
}

// companionSource is the hub every recipe entry points at. companions.json
// lists Hugging Face repos only, whichever hub the DiT itself came from.
const companionSource = "hf"

// handleAPIHubCompanions answers GET /api/hub/companions?source=&repo=&path=.
// A failure to read the DiT's header is a 200 with error set, as for the
// estimate route: the picker must still work when the extra cannot be computed.
func (s *Server) handleAPIHubCompanions(w http.ResponseWriter, r *http.Request) {
	if !s.requireHub(w, r) {
		return
	}
	if s.autogen == nil {
		shared.SendResponse(w, r, http.StatusNotImplemented, "companions need -generate")
		return
	}
	q := r.URL.Query()
	src, ok := s.hub.Source(q.Get("source"))
	if !ok {
		shared.SendResponse(w, r, http.StatusBadRequest, "unknown hub")
		return
	}
	repo := strings.Trim(q.Get("repo"), "/")
	path := strings.TrimSpace(q.Get("path"))
	if repo == "" || path == "" {
		shared.SendResponse(w, r, http.StatusBadRequest, "repo and path are required")
		return
	}
	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, s.hubCompanions(r, src, repo, path, gf.Settings))
}

func (s *Server) hubCompanions(r *http.Request, src hub.Source, repo, path string, set autogen.Settings) hubCompanionsResp {
	out := hubCompanionsResp{Missing: []string{}, Companions: []hubCompanion{}, Unsourced: []string{}}

	det, err := src.Detail(r.Context(), repo)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	var group string
	for _, f := range det.Files {
		if f.Path == path {
			group = f.Group
			break
		}
	}
	if group == "" {
		out.Err = "no such file in this repo"
		return out
	}
	var total int64
	for _, f := range det.Files {
		if f.Group == group {
			total += f.SizeBytes
		}
	}
	meta, err := s.hubModelMeta(r, src, repo, path, group, total)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	// The repo path carries the family name the arms key on ("flux-3", "moss-image")
	// even when the file name alone is just a quant.
	plan, ok := autogen.PlanImageCompanions(set, meta, repo+"/"+path)
	if !ok {
		return out
	}
	out.Image, out.Family = true, plan.Family
	out.Missing = append(out.Missing, plan.Missing...)
	out.Unsourced = append(out.Unsourced, plan.Unsourced...)

	csrc, ok := s.hub.Source(companionSource)
	if !ok {
		for _, f := range plan.Files {
			out.Unsourced = append(out.Unsourced, f.Role)
		}
		return out
	}
	jobs := s.hub.Jobs()
	for _, f := range plan.Files {
		c := hubCompanion{Role: f.Role, Label: f.Label, Source: companionSource, Repo: f.Repo, Path: f.Path}
		c.State, c.SizeBytes = s.companionState(r, csrc, f.Repo, f.Path, jobs)
		out.Companions = append(out.Companions, c)
	}
	return out
}

// companionState places one recipe file: already here, already coming, to be
// queued, or gone from the hub. "local" is checked against the hub's own
// manifest, which matters in one case the plan cannot see: a companion that has
// just finished downloading is on disk before the 30 s encoder-pool cache
// notices it, and offering it again would be a second 9 GB pull.
func (s *Server) companionState(r *http.Request, src hub.Source, repo, path string, jobs []hub.Job) (string, int64) {
	var size int64 = -1
	if det, err := src.Detail(r.Context(), repo); err == nil {
		for _, f := range det.Files {
			if f.Path == path {
				size = f.SizeBytes
				break
			}
		}
	}
	if have, ok := s.hub.LocalFiles(repo)[path]; ok && (size <= 0 || have.Size >= size) {
		return companionLocal, max(size, 0)
	}
	for _, j := range jobs {
		if j.Done() || j.Repo != repo {
			continue
		}
		for _, f := range j.Files {
			if f.Path == path {
				return companionDownloading, max(size, 0)
			}
		}
	}
	if size < 0 {
		return companionUnavailable, 0
	}
	return companionNeeded, size
}
