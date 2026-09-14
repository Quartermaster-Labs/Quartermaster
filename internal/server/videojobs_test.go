package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A render is only over when sd.cpp says one of its three terminal words. An
// unknown status must stay non-terminal: treating it as done would drop the
// lease while the sampler is still running.
func TestProxyManager_VideoTerminalStatus(t *testing.T) {
	for _, s := range []string{"completed", "failed", "cancelled"} {
		if !terminalVideoStatus(s) {
			t.Errorf("%q should be terminal", s)
		}
	}
	for _, s := range []string{"queued", "generating", "", "paused"} {
		if terminalVideoStatus(s) {
			t.Errorf("%q must NOT be terminal", s)
		}
	}
}

// observe reports the terminal EDGE exactly once, and an unparseable document
// leaves the status alone rather than resetting it.
func TestProxyManager_VideoJobObserve(t *testing.T) {
	j := &videoJob{id: "job_1", status: "queued"}

	if j.observe(200, []byte(`{"id":"job_1","status":"generating"}`)) {
		t.Error("queued -> generating is not a terminal edge")
	}
	if j.observe(200, []byte(`not json`)) {
		t.Error("an unparseable document must not signal completion")
	}
	if _, _, st := j.snapshot(); st != "generating" {
		t.Errorf("status = %q, want generating (garbage must not clear it)", st)
	}
	if !j.observe(200, []byte(`{"status":"completed"}`)) {
		t.Fatal("generating -> completed should report the terminal edge")
	}
	if j.observe(200, []byte(`{"status":"completed"}`)) {
		t.Error("the terminal edge must fire only once")
	}
	if j.doneAt.IsZero() {
		t.Error("doneAt should be stamped on the terminal edge")
	}
}

// The lease is released once no matter how many paths call it: the watcher
// releases on the terminal edge AND again from its deferred call.
func TestProxyManager_VideoJobReleaseOnce(t *testing.T) {
	n := 0
	j := &videoJob{id: "job_1", release: func() { n++ }}
	j.releaseLease()
	j.releaseLease()
	j.releaseLease()
	if n != 1 {
		t.Errorf("release called %d times, want 1", n)
	}
	// A job with no local lease (peer-hosted) must not panic.
	(&videoJob{id: "job_2"}).releaseLease()
}

func TestProxyManager_VideoJobRegistry(t *testing.T) {
	reg := newVideoJobs()
	if _, ok := reg.get("nope"); ok {
		t.Error("empty registry should not resolve a job")
	}
	reg.put(&videoJob{id: "job_1", modelID: "h3"})
	j, ok := reg.get("job_1")
	if !ok || j.modelID != "h3" {
		t.Fatalf("get = %+v, %v; want the h3 job", j, ok)
	}
	reg.drop("job_1")
	if _, ok := reg.get("job_1"); ok {
		t.Error("dropped job should be gone")
	}
}

// An unknown id answers in sd-server's own error shape, so a client written
// against the native job API needs no special case for us.
func TestProxyManager_VideoJobUnknownID(t *testing.T) {
	s := &Server{videoJobs: newVideoJobs()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sdcpp/v1/jobs/job_missing", nil)
	req.SetPathValue("id", "job_missing")
	s.handleVideoJob(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, rec.Body.String())
	}
	if body["error"] == "" {
		t.Errorf("error body = %v, want an error message", body)
	}
}

// A job whose watcher has cached a terminal document is served from that cache,
// not from the upstream: by then the lease is gone and the model may already
// have been evicted, taking the only other copy of the clip with it.
func TestProxyManager_VideoJobServedFromCache(t *testing.T) {
	s := &Server{videoJobs: newVideoJobs()}
	doc := `{"id":"job_1","status":"completed","result":{"b64_json":"AAAA","mime_type":"video/webm","frame_count":25,"fps":24}}`
	job := &videoJob{id: "job_1", modelID: "h3", started: time.Now()}
	job.observe(http.StatusOK, []byte(doc))
	s.videoJobs.put(job)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sdcpp/v1/jobs/job_1", nil)
	req.SetPathValue("id", "job_1")
	s.handleVideoJob(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != doc {
		t.Errorf("body = %s\nwant %s", got, doc)
	}
}

// A tracked job that has not been polled yet has no document to serve. 202 says
// "ask again" without inventing a status the upstream never reported.
func TestProxyManager_VideoJobPending(t *testing.T) {
	s := &Server{videoJobs: newVideoJobs()}
	s.videoJobs.put(&videoJob{id: "job_1", modelID: "h3"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sdcpp/v1/jobs/job_1", nil)
	req.SetPathValue("id", "job_1")
	s.handleVideoJob(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("code = %d, want 202", rec.Code)
	}
}

func TestProxyManager_VideoJobIDOf(t *testing.T) {
	if got := videoJobIDOf([]byte(`{"id":"job_abc","status":"queued"}`)); got != "job_abc" {
		t.Errorf("id = %q, want job_abc", got)
	}
	if got := videoJobIDOf([]byte(`{"status":"queued"}`)); got != "" {
		t.Errorf("missing id should yield %q, got %q", "", got)
	}
	if got := videoJobIDOf([]byte(`<html>`)); got != "" {
		t.Errorf("non-JSON should yield %q, got %q", "", got)
	}
}

// The buffered writer exists so vid_gen can take the lease before the client
// sees the job id. It must replay headers and body faithfully, and it must
// refuse rather than hold an unbounded body in RAM.
func TestProxyManager_BufferedResponseWriter(t *testing.T) {
	b := newBufferedResponseWriter()
	b.Header().Set("Content-Type", "application/json")
	b.Header().Set("Content-Length", "999")
	b.WriteHeader(http.StatusAccepted)
	_, _ = b.Write([]byte(`{"id":"job_1"}`))

	rec := httptest.NewRecorder()
	b.flushTo(rec)
	if rec.Code != http.StatusAccepted {
		t.Errorf("code = %d, want 202", rec.Code)
	}
	if rec.Body.String() != `{"id":"job_1"}` {
		t.Errorf("body = %q", rec.Body.String())
	}
	// The replayed body is a different length than the upstream announced.
	if cl := rec.Header().Get("Content-Length"); cl != "" {
		t.Errorf("Content-Length should be dropped on replay, got %q", cl)
	}

	over := newBufferedResponseWriter()
	_, _ = over.Write(make([]byte, videoJobDocLimit+1))
	if !over.over {
		t.Error("a body past videoJobDocLimit should trip the over flag")
	}
	if over.body.Len() != 0 {
		t.Errorf("an over-limit body must not be buffered, held %d bytes", over.body.Len())
	}
}
