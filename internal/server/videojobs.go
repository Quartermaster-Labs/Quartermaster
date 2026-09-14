package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// Video generation is the first thing quartermaster serves that OUTLIVES the
// request which started it. sd-server's native job API is asynchronous by
// design: POST /sdcpp/v1/vid_gen validates the parameters, enqueues the render
// and answers in milliseconds with
//
//	{"id":"job_...","kind":"vid_gen","status":"queued","poll_url":"/sdcpp/v1/jobs/job_..."}
//
// while the sampler runs for minutes afterwards. Everything in this file exists
// because that shape breaks two assumptions the rest of the router is built on:
//
//  1. "In flight" means "an HTTP request is open". The instant the 202 lands the
//     model looks idle, so the next request for another model evicts it, TTL
//     eventually unloads it, and the render dies in a process that gets SIGKILLed
//     mid-sample. The fix is a scheduler LEASE (router.LocalRouter.Lease) held
//     for the life of the job, which makes the model count as busy for eviction,
//     for co-resident spawns, and for the idle-grace hold.
//
//  2. "The request names its model". GET /sdcpp/v1/jobs/{id} carries a job id and
//     nothing else, so it is not routable at all without a server-side map from
//     job id to model id. That map is videoJobs.
//
// The watcher goroutine is what closes the loop on both: it polls the upstream
// job until it reaches a terminal state, caches each document it sees, and drops
// the lease when the render is over. Polling ourselves rather than trusting the
// CLIENT to poll is deliberate. A browser tab that closes mid-render would
// otherwise leave the lease held forever (the model pinned, nothing else able to
// load), and the finished video would be lost the moment the process unloaded.
// Our poll doubles as the TTL keepalive, since it is a real request through the
// process and refreshes its lastUse.

const (
	// videoPollInterval is how often the watcher asks the upstream for a job's
	// status. Generous on purpose: the document is tiny but the poll is a real
	// request through the reverse proxy, and a render measured in minutes gains
	// nothing from sub-second resolution.
	videoPollInterval = 2 * time.Second

	// videoJobGrace is how long a FINISHED job stays readable after it reaches a
	// terminal state. The terminal document carries the whole video as base64,
	// and the lease drops the moment it lands, so the model may be evicted
	// immediately afterwards: the cached copy is the only thing that can still
	// answer the client's last poll. Bounded because that copy is held in RAM.
	videoJobGrace = 10 * time.Minute

	// videoJobMaxLife is the backstop on a job that never reaches a terminal
	// state (an upstream that wedges, a process killed out from under us). It
	// caps how long a lease can pin a model when everything else has failed.
	videoJobMaxLife = 2 * time.Hour

	// videoJobDocLimit caps the upstream document we buffer. A completed webm
	// arrives as base64 in the same JSON, so this is sized for a clip, not a
	// status line; anything larger is a bug or an abuse and is refused rather
	// than held in memory.
	videoJobDocLimit = 256 << 20
)

// videoJob is one tracked async render.
type videoJob struct {
	id      string
	modelID string
	// model is the id AS THE CLIENT NAMED IT (an alias, or a synthetic ?ctx=/
	// X-QM-Backend variant's parent). Kept so the internal poll re-dispatches
	// through the same resolution the original POST went through.
	model   string
	release func()
	started time.Time

	mu       sync.Mutex
	status   string // queued | generating | completed | failed | cancelled
	doc      []byte // the last upstream job document, served to pollers
	docCode  int    // the status code that came with it
	doneAt   time.Time
	released bool
}

// terminal reports whether a status string means the render is over. Unknown
// statuses are treated as NON-terminal so a future sd.cpp state cannot silently
// strand a lease-releasing watcher; videoJobMaxLife is the backstop for that.
func terminalVideoStatus(s string) bool {
	switch s {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// videoJobs maps job ids to the model that is rendering them. It is the only
// thing that makes GET /sdcpp/v1/jobs/{id} routable, and it owns each job's
// scheduler lease.
type videoJobs struct {
	mu   sync.Mutex
	jobs map[string]*videoJob
}

func newVideoJobs() *videoJobs { return &videoJobs{jobs: map[string]*videoJob{}} }

func (v *videoJobs) get(id string) (*videoJob, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	j, ok := v.jobs[id]
	return j, ok
}

func (v *videoJobs) put(j *videoJob) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.jobs[j.id] = j
}

func (v *videoJobs) drop(id string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.jobs, id)
}

// observe records an upstream job document and reports whether the job just
// reached a terminal state.
func (j *videoJob) observe(code int, doc []byte) (nowTerminal bool) {
	status := videoStatusOf(doc)
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(doc) > 0 {
		j.doc = doc
		j.docCode = code
	}
	if status == "" || status == j.status {
		return false
	}
	was := terminalVideoStatus(j.status)
	j.status = status
	if terminalVideoStatus(status) && !was {
		j.doneAt = time.Now()
		return true
	}
	return false
}

// snapshot returns the last document seen for this job.
func (j *videoJob) snapshot() (int, []byte, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.docCode, j.doc, j.status
}

// releaseLease drops the scheduler lease once. Safe to call repeatedly: the
// router's release is idempotent too, but the flag keeps the log honest.
func (j *videoJob) releaseLease() {
	j.mu.Lock()
	already := j.released
	j.released = true
	j.mu.Unlock()
	if !already && j.release != nil {
		j.release()
	}
}

// videoStatusOf pulls the status field out of an upstream job document. An
// unparseable body yields "", which observe treats as "no change".
func videoStatusOf(doc []byte) string {
	var v struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(doc, &v); err != nil {
		return ""
	}
	return v.Status
}

// videoJobIDOf pulls the job id out of a vid_gen response.
func videoJobIDOf(doc []byte) string {
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(doc, &v); err != nil {
		return ""
	}
	return v.ID
}

// bufferedResponseWriter collects a handler's whole response instead of
// streaming it, so the caller can inspect the body before the client sees it.
// Used on vid_gen: the lease has to be taken BEFORE the client learns the job
// id, or a client that polls instantly can race the eviction it is meant to
// prevent. The documents involved are one small JSON object, so buffering costs
// nothing.
type bufferedResponseWriter struct {
	header http.Header
	code   int
	body   bytes.Buffer
	over   bool
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: http.Header{}, code: http.StatusOK}
}

func (b *bufferedResponseWriter) Header() http.Header { return b.header }

func (b *bufferedResponseWriter) WriteHeader(code int) { b.code = code }

func (b *bufferedResponseWriter) Write(p []byte) (int, error) {
	if b.body.Len()+len(p) > videoJobDocLimit {
		b.over = true
		return len(p), nil
	}
	return b.body.Write(p)
}

// flushTo replays the buffered response onto the real client writer.
func (b *bufferedResponseWriter) flushTo(w http.ResponseWriter) {
	for k, vs := range b.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(b.code)
	_, _ = w.Write(b.body.Bytes())
}

// handleVidGen proxies POST /sdcpp/v1/vid_gen and turns the job it starts into a
// tracked, leased render. It runs INSIDE the model chain, so auth, listener and
// API-key scoping, filters and metrics have already applied by the time the
// dispatch below resolves the model.
func (s *Server) handleVidGen(w http.ResponseWriter, r *http.Request) {
	buf := newBufferedResponseWriter()
	s.localPeerHandler(buf, r)

	// localPeerHandler resolves and pins the model onto r in place, so the id is
	// readable here even though we never parsed the body ourselves.
	data, _ := shared.ReadContext(r.Context())

	if buf.over {
		s.proxylog.Errorf("vid_gen: upstream response for model %s exceeded %d bytes, not tracking the job", data.ModelID, videoJobDocLimit)
		shared.SendResponse(w, r, http.StatusBadGateway, "upstream job response too large")
		return
	}
	if buf.code >= http.StatusBadRequest || data.ModelID == "" {
		buf.flushTo(w)
		return
	}
	jobID := videoJobIDOf(buf.body.Bytes())
	if jobID == "" {
		// A 2xx with no id is not a job we can poll, cancel or protect. Pass it
		// through rather than inventing an error, but say so: it means the
		// backend's contract has moved.
		s.proxylog.Warnf("vid_gen: model %s returned %d with no job id, the render is UNPROTECTED (eviction and TTL may kill it)", data.ModelID, buf.code)
		buf.flushTo(w)
		return
	}

	release, leased := s.local.Lease(data.ModelID)
	if !leased {
		// A peer-hosted model, or a router that is shutting down. The job is
		// still tracked (so its polls route) but nothing local is holding it.
		s.proxylog.Debugf("vid_gen: no local lease for model %s, tracking job %s unprotected", data.ModelID, jobID)
	}
	job := &videoJob{
		id:      jobID,
		modelID: data.ModelID,
		model:   data.Model,
		release: release,
		started: time.Now(),
		status:  "queued",
		doc:     append([]byte(nil), buf.body.Bytes()...),
		docCode: buf.code,
	}
	job.observe(buf.code, job.doc)
	s.videoJobs.put(job)
	s.proxylog.Infof("vid_gen: model %s started job %s (leased=%t)", data.ModelID, jobID, leased)
	go s.watchVideoJob(job)

	buf.flushTo(w)
}

// handleVideoJob serves GET /sdcpp/v1/jobs/{id} from the watcher's cached
// document rather than from the upstream.
//
// That is not a shortcut, it is the only answer that works: the lease drops the
// moment a render completes, so the model is evictable from that instant, and a
// client polling a second later would find the process gone and its video with
// it. The watcher already holds the terminal document, including the encoded
// clip, so the client gets the same bytes the upstream would have sent.
func (s *Server) handleVideoJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.videoJobs.get(r.PathValue("id"))
	if !ok {
		// Same shape sd-server itself uses, so a client written against the
		// native API needs no special case for us.
		writeVideoJobError(w, http.StatusNotFound, "job not found")
		return
	}
	code, doc, _ := job.snapshot()
	if len(doc) == 0 {
		writeVideoJobError(w, http.StatusAccepted, "job pending")
		return
	}
	if code == 0 {
		code = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(doc)
}

// handleVideoJobCancel proxies POST /sdcpp/v1/jobs/{id}/cancel to the model the
// job belongs to. The registry is what makes it routable; the watcher picks up
// the resulting "cancelled" on its next poll and drops the lease there, so
// cancellation and completion release through exactly one code path.
func (s *Server) handleVideoJobCancel(w http.ResponseWriter, r *http.Request) {
	job, ok := s.videoJobs.get(r.PathValue("id"))
	if !ok {
		writeVideoJobError(w, http.StatusNotFound, "job not found")
		return
	}
	buf := newBufferedResponseWriter()
	s.dispatchToModel(buf, r, job)
	if buf.code < http.StatusBadRequest && !buf.over {
		job.observe(buf.code, buf.body.Bytes())
	}
	buf.flushTo(w)
}

// dispatchToModel forwards r to the model that owns job, bypassing the
// body/query model extraction the ordinary routes rely on (a job path names no
// model). Mirrors handleUpstream's pin-and-switch.
func (s *Server) dispatchToModel(w http.ResponseWriter, r *http.Request, job *videoJob) {
	*r = *r.WithContext(shared.SetContext(r.Context(), shared.ReqContextData{
		Model:    job.model,
		ModelID:  job.modelID,
		Metadata: make(map[string]string),
	}))
	switch {
	case s.local.Handles(job.modelID):
		s.local.ServeHTTP(w, r)
	case s.peer.Handles(job.modelID):
		s.peer.ServeHTTP(w, r)
	default:
		writeVideoJobError(w, http.StatusNotFound, "no router for model "+job.modelID)
	}
}

// watchVideoJob polls one job to completion, then releases its lease and expires
// it from the registry after the grace window.
func (s *Server) watchVideoJob(job *videoJob) {
	defer job.releaseLease()

	ticker := time.NewTicker(videoPollInterval)
	defer ticker.Stop()
	deadline := time.After(videoJobMaxLife)

	for {
		select {
		case <-s.shutdownCtx.Done():
			// The whole process is going away, and every upstream with it.
			// Releasing here keeps the scheduler's books balanced during the
			// drain; the job itself is lost either way.
			s.videoJobs.drop(job.id)
			return
		case <-deadline:
			s.proxylog.Warnf("vid_gen: job %s on model %s exceeded %s without finishing, releasing its lease", job.id, job.modelID, videoJobMaxLife)
			s.videoJobs.drop(job.id)
			return
		case <-ticker.C:
		}

		code, doc, ok := s.pollVideoJob(job)
		if !ok {
			// The upstream could not be reached at all (the process died, the
			// model was force-unloaded). Nothing is rendering any more, so
			// holding the lease only pins a model nobody is using.
			s.proxylog.Warnf("vid_gen: job %s on model %s is no longer reachable, releasing its lease", job.id, job.modelID)
			s.videoJobs.drop(job.id)
			return
		}
		if code == http.StatusNotFound {
			// sd-server forgets a job after its own expiry. Treat it the same
			// way: nothing left to protect.
			s.videoJobs.drop(job.id)
			return
		}
		if !job.observe(code, doc) {
			continue
		}

		// Terminal. Drop the lease NOW (the GPU is free and the next model
		// should be able to have it), but keep the document readable for a
		// while so the client's final poll still gets its video.
		_, _, status := job.snapshot()
		s.proxylog.Infof("vid_gen: job %s on model %s finished as %s after %s", job.id, job.modelID, status, time.Since(job.started).Round(time.Second))
		job.releaseLease()
		select {
		case <-s.shutdownCtx.Done():
		case <-time.After(videoJobGrace):
		}
		s.videoJobs.drop(job.id)
		return
	}
}

// pollVideoJob asks the upstream for one job document. ok is false when the
// request could not be served at all, which is the watcher's signal to give up.
func (s *Server) pollVideoJob(job *videoJob) (code int, doc []byte, ok bool) {
	req, err := http.NewRequestWithContext(s.shutdownCtx, http.MethodGet, "/sdcpp/v1/jobs/"+job.id, nil)
	if err != nil {
		return 0, nil, false
	}
	buf := newBufferedResponseWriter()
	s.dispatchToModel(buf, req, job)
	if buf.over {
		return 0, nil, false
	}
	if buf.code >= http.StatusInternalServerError {
		return buf.code, nil, false
	}
	return buf.code, append([]byte(nil), buf.body.Bytes()...), true
}

// writeVideoJobError answers in sd-server's own error shape, so a client written
// against the native job API handles our errors with no special case.
func writeVideoJobError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, "{%q:%q}\n", "error", strings.TrimSpace(msg))
}
