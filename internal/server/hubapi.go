package server

// Model browser: search a model hub from inside quartermaster, read the repo's
// file list, and download a quant into the models folder, where autogen picks
// it up like any hand-copied GGUF.
//
// Every hub call is proxied server-side for the same reason websearch.go is:
// no CORS dance, and a Hugging Face token never lands in browser JS. The heavy
// lifting (adapter, resumable downloader) is internal/hub; this file is wire
// types, the models-root lookup and the HTTP surface. Every route is
// admin-gated.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/hub"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// hubPartialMaxAge is how long an untouched `.part` survives the startup sweep.
// Long enough that a download paused over a weekend is still resumable, short
// enough that a dead one does not sit in the models folder forever.
const hubPartialMaxAge = 14 * 24 * time.Hour

type hubSourceDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type hubSearchResp struct {
	Source string      `json:"source"`
	Query  string      `json:"query"`
	Models []hub.Model `json:"models"`
	// Where the next page starts, and whether asking for one is worth a round
	// trip. The browser loads more as the user scrolls rather than capping at a
	// page size, so these two are the whole pagination contract.
	NextSkip int  `json:"nextSkip"`
	HasMore  bool `json:"hasMore"`
}

type hubCancelReq struct {
	JobID string `json:"jobId"`
}

func (s *Server) requireHub(w http.ResponseWriter, r *http.Request) bool {
	if s.hub == nil {
		shared.SendResponse(w, r, http.StatusNotImplemented, "the model browser is unavailable in this build")
		return false
	}
	return true
}

// hubModelsRoot resolves where downloads land. It reads the settings on every
// call rather than caching: a live config reload can move the models folder,
// and a stale root would silently drop a 40 GB download in the wrong place.
func (s *Server) hubModelsRoot() string {
	a := s.autogen
	if a == nil {
		return ""
	}
	if strings.TrimSpace(a.ModelsDir) != "" {
		return a.ModelsDir
	}
	set, err := autogen.LoadBaseSettings(a.GeneratePath, a.ModelsDir)
	if err != nil {
		return ""
	}
	return set.ModelsRoot
}

// hubToken supplies the credential for gated and private repos. For now it is
// the standard environment variable the huggingface CLI already writes, so a
// user who has logged in elsewhere on the box needs no extra setup; a stored,
// UI-editable token is the next step.
func hubToken() string {
	for _, k := range []string{"HF_TOKEN", "HUGGING_FACE_HUB_TOKEN", "HUGGINGFACE_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// handleAPIHubSources lists the hub adapters this build ships.
func (s *Server) handleAPIHubSources(w http.ResponseWriter, r *http.Request) {
	if !s.requireHub(w, r) {
		return
	}
	out := []hubSourceDTO{}
	for _, src := range s.hub.Sources() {
		out = append(out, hubSourceDTO{ID: src.ID(), Name: src.Name()})
	}
	writeJSON(w, map[string]any{
		"sources":    out,
		"modelsRoot": s.hubModelsRoot(),
		"hasToken":   hubToken() != "",
	})
}

// handleAPIHubSearch proxies one hub search.
func (s *Server) handleAPIHubSearch(w http.ResponseWriter, r *http.Request) {
	if !s.requireHub(w, r) {
		return
	}
	q := r.URL.Query()
	src, ok := s.hub.Source(q.Get("source"))
	if !ok {
		shared.SendResponse(w, r, http.StatusBadRequest, "unknown hub "+q.Get("source"))
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	maxParams, _ := strconv.ParseFloat(q.Get("maxParams"), 64)
	maxAge, _ := strconv.Atoi(q.Get("maxAgeDays"))
	skip, _ := strconv.Atoi(q.Get("skip"))
	page, err := src.Search(r.Context(), hub.Query{
		Text:       q.Get("q"),
		Sort:       q.Get("sort"),
		Kind:       q.Get("kind"),
		Limit:      limit,
		MaxParamsB: maxParams,
		MaxAgeDays: maxAge,
		Skip:       max(0, skip),
	})
	if err != nil {
		s.sendHubError(w, r, err)
		return
	}
	if page.Models == nil {
		page.Models = []hub.Model{} // the UI iterates this; never send null
	}
	writeJSON(w, hubSearchResp{
		Source:   src.ID(),
		Query:    q.Get("q"),
		Models:   page.Models,
		NextSkip: page.NextSkip,
		HasMore:  page.HasMore,
	})
}

// handleAPIHubModel returns one repo page: metadata, README and file list.
// The id is "owner/name", so the route takes a trailing wildcard.
func (s *Server) handleAPIHubModel(w http.ResponseWriter, r *http.Request) {
	if !s.requireHub(w, r) {
		return
	}
	src, ok := s.hub.Source(r.URL.Query().Get("source"))
	if !ok {
		shared.SendResponse(w, r, http.StatusBadRequest, "unknown hub")
		return
	}
	id := strings.Trim(r.PathValue("id"), "/")
	det, err := src.Detail(r.Context(), id)
	if err != nil {
		s.sendHubError(w, r, err)
		return
	}
	if det.Files == nil {
		det.Files = []hub.File{}
	}
	// Mark what is already downloaded, so the picker can say so on the row
	// rather than offering a 20 GB pull the user already has. Done here rather
	// than in the adapter: the disk is a property of this installation, not of
	// the hub. A file shorter than the hub says it should be is a truncated
	// copy, so it stays a download.
	//
	// A name is not an identity, though. Publishers re-upload a quant in place,
	// often at a byte count within rounding of the old one, and on size alone
	// the new revision reads as "already downloaded" — the user's only way out
	// being to rename or delete the file by hand. So where we recorded the id
	// we fetched at and the hub now states a different one, the row is Local
	// AND Stale: it is on disk, and it is not what the repo is serving. Both
	// ids have to be present for that call — an absent one is "no opinion", and
	// claiming an update on a file we know nothing about would send the user
	// after 40 GB they already have.
	local := s.hub.LocalFiles(id)
	for i, f := range det.Files {
		have, ok := local[f.Path]
		if !ok {
			continue
		}
		superseded := have.OID != "" && f.OID != "" && have.OID != f.OID
		if superseded {
			det.Files[i].Local, det.Files[i].Stale = true, true
			continue
		}
		if f.SizeBytes <= 0 || have.Size >= f.SizeBytes {
			det.Files[i].Local = true
		}
	}
	writeJSON(w, det)
}

// handleAPIHubAvatar resolves one publisher's avatar URL.
//
// A miss is 200 with an empty url, not 404: the caller renders a monogram
// either way, and an error status would put a red line in the console for every
// author who never uploaded a picture.
func (s *Server) handleAPIHubAvatar(w http.ResponseWriter, r *http.Request) {
	if !s.requireHub(w, r) {
		return
	}
	src, ok := s.hub.Source(r.URL.Query().Get("source"))
	if !ok {
		shared.SendResponse(w, r, http.StatusBadRequest, "unknown hub")
		return
	}
	av, ok := src.(hub.AvatarSource)
	if !ok {
		writeJSON(w, map[string]string{"url": ""})
		return
	}
	url, err := av.Avatar(r.Context(), strings.TrimSpace(r.URL.Query().Get("author")))
	if err != nil {
		// A bad author name or a hub hiccup is still just a missing picture.
		writeJSON(w, map[string]string{"url": ""})
		return
	}
	writeJSON(w, map[string]string{"url": url})
}

// handleAPIHubDownload admits a download and returns its job id.
func (s *Server) handleAPIHubDownload(w http.ResponseWriter, r *http.Request) {
	if !s.requireHub(w, r) {
		return
	}
	var body hub.StartRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	id, err := s.hub.Start(r.Context(), body)
	if err != nil {
		s.sendHubError(w, r, err)
		return
	}
	// The manager logs the finish ("… downloaded into …"); without this the
	// start of a multi-gigabyte transfer — the part that explains the disk and
	// network load for the next half hour — was never recorded.
	s.proxylog.Infof("hub: downloading %s into %s", body.Repo, s.hubModelsRoot())
	writeJSON(w, map[string]string{"jobId": id})
}

// handleAPIHubJobs returns the download history; the UI polls it for progress.
func (s *Server) handleAPIHubJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireHub(w, r) {
		return
	}
	jobs := s.hub.Jobs()
	if jobs == nil {
		jobs = []hub.Job{}
	}
	writeJSON(w, jobs)
}

// handleAPIHubCancel stops a download and DISCARDS its partial bytes. Pause is
// the non-destructive one — this is the route the UI confirms before calling.
func (s *Server) handleAPIHubCancel(w http.ResponseWriter, r *http.Request) {
	s.hubJobAction(w, r, s.hub.Cancel, "canceling")
}

// handleAPIHubPause stops a running download and keeps every byte on disk.
func (s *Server) handleAPIHubPause(w http.ResponseWriter, r *http.Request) {
	s.hubJobAction(w, r, s.hub.Pause, "pausing")
}

// handleAPIHubResume continues a paused download from its `.part` files.
func (s *Server) handleAPIHubResume(w http.ResponseWriter, r *http.Request) {
	s.hubJobAction(w, r, s.hub.Resume, "resuming")
}

// hubJobAction is the shared body-decode + dispatch for the three job verbs,
// which differ only in which manager method they call.
func (s *Server) hubJobAction(w http.ResponseWriter, r *http.Request, do func(string) error, status string) {
	if !s.requireHub(w, r) {
		return
	}
	var body hubCancelReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := do(body.JobID); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": status})
}

// sendHubError maps a hub failure onto a status the UI can branch on. A gated
// repo is 403 with the "accept the license" wording, not a generic 500 the user
// would read as a broken download.
func (s *Server) sendHubError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *hub.AuthError
	if errors.As(err, &ae) {
		shared.SendResponse(w, r, http.StatusForbidden, ae.Error())
		return
	}
	shared.SendResponse(w, r, http.StatusBadGateway, err.Error())
}
