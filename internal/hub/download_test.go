package hub

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSource is a Source backed by an httptest server, so the download manager
// can be exercised end to end without touching Hugging Face.
type fakeSource struct {
	base  string
	files []File
}

func (f *fakeSource) ID() string   { return "fake" }
func (f *fakeSource) Name() string { return "Fake" }
func (f *fakeSource) Search(context.Context, Query) (Page, error) {
	return Page{Models: []Model{{ID: "o/r", Source: "fake"}}}, nil
}
func (f *fakeSource) Detail(_ context.Context, repo string) (ModelDetail, error) {
	return ModelDetail{Model: Model{ID: repo, Source: "fake"}, Files: f.files}, nil
}
func (f *fakeSource) FileURL(repo, path string) (string, error) {
	return f.base + "/" + repo + "/" + path, nil
}
func (f *fakeSource) CheckURL(raw string) error {
	if !strings.HasPrefix(raw, f.base) {
		return fmt.Errorf("refusing %s", raw)
	}
	return nil
}
func (f *fakeSource) Authorize(*http.Request) {}

// serveBlob answers a GET with Range support, optionally cutting the response
// short after cutAfter bytes to simulate a dropped connection.
func serveBlob(w http.ResponseWriter, r *http.Request, blob []byte, cutAfter int) {
	start := 0
	if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"))
		if err == nil {
			start = n
		}
		if start >= len(blob) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(blob)-1, len(blob)))
		w.WriteHeader(http.StatusPartialContent)
	}
	body := blob[start:]
	if cutAfter > 0 && cutAfter < len(body) {
		body = body[:cutAfter]
		w.Write(body)
		// No Content-Length was set, so a short write plus a flush and a panic
		// closes the connection mid-body the way a dropped line does.
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		panic(http.ErrAbortHandler)
	}
	w.Write(body)
}

// discardLogger silences httptest's "connection hijacked/aborted" noise for the
// tests that deliberately drop a connection.
func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func waitJob(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := m.Job(id)
		if ok && j.Done() {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	j, _ := m.Job(id)
	t.Fatalf("job %s did not finish, stuck in phase %q", id, j.Phase)
	return Job{}
}

func blobOf(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func TestManager_DownloadsEveryFile(t *testing.T) {
	a, b := blobOf(4096), blobOf(1500)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "b.gguf") {
			serveBlob(w, r, b, 0)
			return
		}
		serveBlob(w, r, a, 0)
	}))
	defer srv.Close()

	root := t.TempDir()
	src := &fakeSource{base: srv.URL, files: []File{
		{Path: "a-00001-of-00002.gguf", SizeBytes: int64(len(a))},
		{Path: "sub/b.gguf", SizeBytes: int64(len(b))},
	}}
	var completed atomic.Int32
	m := NewManager(func() string { return root }, nil, src)
	m.OnComplete = func(Job) error { completed.Add(1); return nil }

	id, err := m.Start(context.Background(), StartRequest{
		Source: "fake", Repo: "o/r", Files: []string{"a-00001-of-00002.gguf", "sub/b.gguf"},
	})
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, id)
	if job.Phase != PhaseDone {
		t.Fatalf("phase = %q, err = %q", job.Phase, job.Err)
	}
	dir := filepath.Join(root, "o", "r")
	if got, err := os.ReadFile(filepath.Join(dir, "a-00001-of-00002.gguf")); err != nil || !bytes.Equal(got, a) {
		t.Errorf("shard 1 mismatch: err %v, %d bytes", err, len(got))
	}
	if got, err := os.ReadFile(filepath.Join(dir, "sub", "b.gguf")); err != nil || !bytes.Equal(got, b) {
		t.Errorf("nested file mismatch: err %v, %d bytes", err, len(got))
	}
	if job.Downloaded != job.Total {
		t.Errorf("progress ended at %d/%d", job.Downloaded, job.Total)
	}
	if completed.Load() != 1 {
		t.Errorf("OnComplete ran %d times, want 1", completed.Load())
	}
	// Nothing partial may survive: model discovery walks this folder.
	if _, err := os.Stat(filepath.Join(dir, "a-00001-of-00002.gguf"+partSuffix)); !os.IsNotExist(err) {
		t.Error(".part file left behind after a successful download")
	}
}

func TestManager_ResumesAfterDroppedConnection(t *testing.T) {
	blob := blobOf(8192)
	var hits atomic.Int32
	var sawRange atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			sawRange.Store(true)
		}
		// Cut the first attempt short; serve the rest normally.
		cut := 0
		if hits.Add(1) == 1 {
			cut = 3000
		}
		serveBlob(w, r, blob, cut)
	}))
	defer srv.Close()
	srv.Config.ErrorLog = discardLogger()

	root := t.TempDir()
	src := &fakeSource{base: srv.URL, files: []File{{Path: "m.gguf", SizeBytes: int64(len(blob))}}}
	m := NewManager(func() string { return root }, nil, src)

	id, err := m.Start(context.Background(), StartRequest{Source: "fake", Repo: "o/r", Files: []string{"m.gguf"}})
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, id)
	if job.Phase != PhaseDone {
		t.Fatalf("phase = %q, err = %q", job.Phase, job.Err)
	}
	if !sawRange.Load() {
		t.Error("retry did not send a Range header — the transfer restarted from zero")
	}
	got, err := os.ReadFile(filepath.Join(root, "o", "r", "m.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, blob) {
		t.Errorf("resumed file is %d bytes and does not match the source", len(got))
	}
}

func TestManager_SkipsFileAlreadyOnDisk(t *testing.T) {
	blob := blobOf(2048)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		serveBlob(w, r, blob, 0)
	}))
	defer srv.Close()

	root := t.TempDir()
	dir := filepath.Join(root, "o", "r")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m.gguf"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{base: srv.URL, files: []File{{Path: "m.gguf", SizeBytes: int64(len(blob))}}}
	m := NewManager(func() string { return root }, nil, src)

	id, err := m.Start(context.Background(), StartRequest{Source: "fake", Repo: "o/r", Files: []string{"m.gguf"}})
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, id)
	if job.Phase != PhaseDone {
		t.Fatalf("phase = %q, err = %q", job.Phase, job.Err)
	}
	if hits.Load() != 0 {
		t.Errorf("refetched a file already on disk (%d requests)", hits.Load())
	}
	if !job.Files[0].Skipped || job.Downloaded != job.Total {
		t.Errorf("skipped file not accounted for: %+v (%d/%d)", job.Files[0], job.Downloaded, job.Total)
	}
}

func TestManager_GatedRepoFailsFastAsAuthError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("access to model o/r is restricted"))
	}))
	defer srv.Close()

	root := t.TempDir()
	src := &fakeSource{base: srv.URL, files: []File{{Path: "m.gguf", SizeBytes: 10}}}
	m := NewManager(func() string { return root }, nil, src)

	id, err := m.Start(context.Background(), StartRequest{Source: "fake", Repo: "o/r", Files: []string{"m.gguf"}})
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, id)
	if job.Phase != PhaseError || !job.Gated {
		t.Fatalf("phase = %q gated = %v, want error + gated", job.Phase, job.Gated)
	}
	// A license wall is terminal: retrying it five times only delays telling
	// the user to accept the license.
	if hits.Load() != 1 {
		t.Errorf("retried a 403 %d times", hits.Load())
	}
	if !strings.Contains(job.Err, "accept the license") {
		t.Errorf("error does not tell the user what to do: %q", job.Err)
	}
}

// heldServer serves the first `head` bytes of blob and then holds the
// connection open until the test lets go, so a transfer can be caught mid-flight
// and interrupted deterministically.
func heldServer(t *testing.T, blob []byte, head int) (*httptest.Server, func()) {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(blob[:head])
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-release
	}))
	srv.Config.ErrorLog = discardLogger()
	return srv, func() { close(release); srv.Close() }
}

func waitPhase(t *testing.T, m *Manager, id, phase string) Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := m.Job(id); ok && j.Phase == phase {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	j, _ := m.Job(id)
	t.Fatalf("job %s never reached %q, stuck in %q", id, phase, j.Phase)
	return Job{}
}

func TestManager_PauseKeepsPartialAndResumeFinishes(t *testing.T) {
	blob := blobOf(1 << 20)
	srv, stop := heldServer(t, blob, 4096)

	root := t.TempDir()
	src := &fakeSource{base: srv.URL, files: []File{{Path: "m.gguf", SizeBytes: int64(len(blob))}}}
	m := NewManager(func() string { return root }, nil, src)

	id, err := m.Start(context.Background(), StartRequest{Source: "fake", Repo: "o/r", Files: []string{"m.gguf"}})
	if err != nil {
		t.Fatal(err)
	}
	waitPhase(t, m, id, PhaseDownloading)
	if err := m.Pause(id); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, m, id, PhasePaused)

	// The partial must survive — that is the entire difference from cancel.
	part := filepath.Join(root, "o", "r", "m.gguf"+partSuffix)
	st, err := os.Stat(part)
	if err != nil {
		t.Fatalf("pause discarded the partial file: %v", err)
	}
	if st.Size() == 0 {
		t.Fatal("pause left an empty partial")
	}
	if _, err := os.Stat(filepath.Join(root, "o", "r", "m.gguf")); !os.IsNotExist(err) {
		t.Error("a paused download left a file that looks complete")
	}

	// Resume against a server that now answers in full: the job keeps its id and
	// finishes from the bytes already on disk.
	stop()
	full := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveBlob(w, r, blob, 0)
	}))
	defer full.Close()
	src.base = full.URL

	if err := m.Resume(id); err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, id)
	if job.Phase != PhaseDone {
		t.Fatalf("phase = %q, err = %q", job.Phase, job.Err)
	}
	got, err := os.ReadFile(filepath.Join(root, "o", "r", "m.gguf"))
	if err != nil || !bytes.Equal(got, blob) {
		t.Errorf("resumed file mismatch: err %v, %d bytes", err, len(got))
	}
}

func TestManager_CancelDiscardsPartial(t *testing.T) {
	blob := blobOf(1 << 20)
	srv, stop := heldServer(t, blob, 4096)
	defer stop()

	root := t.TempDir()
	src := &fakeSource{base: srv.URL, files: []File{{Path: "m.gguf", SizeBytes: int64(len(blob))}}}
	m := NewManager(func() string { return root }, nil, src)

	id, err := m.Start(context.Background(), StartRequest{Source: "fake", Repo: "o/r", Files: []string{"m.gguf"}})
	if err != nil {
		t.Fatal(err)
	}
	waitPhase(t, m, id, PhaseDownloading)
	if err := m.Cancel(id); err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, m, id)
	if job.Phase != PhaseCanceled {
		t.Fatalf("phase = %q, want canceled (err %q)", job.Phase, job.Err)
	}
	// Cancel means discard: an abandoned .part is a multi-GB orphan nothing ever
	// mentions again.
	if _, err := os.Stat(filepath.Join(root, "o", "r", "m.gguf"+partSuffix)); !os.IsNotExist(err) {
		t.Errorf("cancel kept the partial file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "o", "r", "m.gguf")); !os.IsNotExist(err) {
		t.Error("a canceled download left a file that looks complete")
	}
	// The empty folders it created go with it.
	if _, err := os.Stat(filepath.Join(root, "o")); !os.IsNotExist(err) {
		t.Error("cancel left the publisher folder behind")
	}
}

func TestManager_CancelDiscardsFilesTheJobFinished(t *testing.T) {
	// Two files: one lands complete, the second is still running when the job is
	// canceled. A lone shard is not a model, so the finished one must go too —
	// but a file that was already there before the job (Skipped) must not.
	done, running := blobOf(2048), blobOf(1<<20)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "a.gguf") {
			serveBlob(w, r, done, 0)
			return
		}
		w.Write(running[:4096])
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-release
	}))
	defer srv.Close()
	defer close(release)
	srv.Config.ErrorLog = discardLogger()

	root := t.TempDir()
	dir := filepath.Join(root, "o", "r")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pre := blobOf(512)
	if err := os.WriteFile(filepath.Join(dir, "pre.gguf"), pre, 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{base: srv.URL, files: []File{
		{Path: "pre.gguf", SizeBytes: int64(len(pre))},
		{Path: "a.gguf", SizeBytes: int64(len(done))},
		{Path: "b.gguf", SizeBytes: int64(len(running))},
	}}
	m := NewManager(func() string { return root }, nil, src)

	id, err := m.Start(context.Background(), StartRequest{
		Source: "fake", Repo: "o/r", Files: []string{"pre.gguf", "a.gguf", "b.gguf"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// b.gguf is the one that hangs, so wait until a.gguf has landed.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "a.gguf")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.Cancel(id); err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, id)

	if _, err := os.Stat(filepath.Join(dir, "a.gguf")); !os.IsNotExist(err) {
		t.Error("cancel kept a file the job itself downloaded")
	}
	if got, err := os.ReadFile(filepath.Join(dir, "pre.gguf")); err != nil || !bytes.Equal(got, pre) {
		t.Errorf("cancel deleted a file that predated the job: err %v", err)
	}
}

func TestSweepPartials_AgeGated(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "o", "r")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old.gguf"+partSuffix)
	fresh := filepath.Join(dir, "fresh.gguf"+partSuffix)
	keep := filepath.Join(dir, "model.gguf")
	for _, p := range []string{old, fresh, keep} {
		if err := os.WriteFile(p, blobOf(1024), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}

	n, freed := SweepPartials(root, 24*time.Hour, nil)
	if n != 1 || freed != 1024 {
		t.Errorf("swept %d files / %d bytes, want 1 / 1024", n, freed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the stale partial survived the sweep")
	}
	// A partial is resumable, so a recent one is work in progress, not garbage.
	if _, err := os.Stat(fresh); err != nil {
		t.Error("the sweep ate a fresh partial")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("the sweep touched a real model file")
	}
}

func TestManager_QueuesSecondJobForSameRepo(t *testing.T) {
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		_, _ = w.Write(make([]byte, 1<<20))
	}))
	defer srv.Close()

	root := t.TempDir()
	src := &fakeSource{base: srv.URL, files: []File{
		{Path: "a.gguf", SizeBytes: 1 << 20},
		{Path: "b.gguf", SizeBytes: 1 << 20},
	}}
	m := NewManager(func() string { return root }, nil, src)

	first, err := m.Start(context.Background(), StartRequest{Source: "fake", Repo: "o/r", Files: []string{"a.gguf"}})
	if err != nil {
		t.Fatal(err)
	}
	// Picking a second file from the same repo is the ordinary case (two quants,
	// or a model and its projector) — it waits its turn instead of erroring.
	second, err := m.Start(context.Background(), StartRequest{Source: "fake", Repo: "o/r", Files: []string{"b.gguf"}})
	if err != nil {
		t.Fatalf("second job for the same repo was refused: %v", err)
	}
	if j, _ := m.Job(second); j.Phase != PhaseQueued {
		t.Errorf("queued job is in phase %q, want %q", j.Phase, PhaseQueued)
	}
	if got := hits.Load(); got > 1 {
		t.Errorf("%d concurrent transfers for one repo, want 1", got)
	}

	close(release)
	waitJob(t, m, first)
	waitJob(t, m, second)
	if j, _ := m.Job(second); j.Phase != PhaseDone {
		t.Errorf("queued job ended in %q (%s), want %q", j.Phase, j.Err, PhaseDone)
	}
	if _, err := os.Stat(filepath.Join(root, "o", "r", "b.gguf")); err != nil {
		t.Errorf("the queued job never ran: %v", err)
	}
}

// A job canceled before its turn takes nothing with it: the bytes under that
// repo belong to whatever is running there.
func TestManager_CancelQueuedJob(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	root := t.TempDir()
	src := &fakeSource{base: srv.URL, files: []File{{Path: "m.gguf", SizeBytes: 1 << 20}}}
	m := NewManager(func() string { return root }, nil, src)

	first, err := m.Start(context.Background(), StartRequest{Source: "fake", Repo: "o/r", Files: []string{"m.gguf"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), StartRequest{Source: "fake", Repo: "o/r", Files: []string{"m.gguf"}}); err == nil {
		t.Error("second concurrent job for the same repo was admitted")
	}
	m.Cancel(first)
	waitJob(t, m, first)
}

func TestManager_RejectsFileNotInRepo(t *testing.T) {
	root := t.TempDir()
	src := &fakeSource{base: "http://127.0.0.1:1", files: []File{{Path: "m.gguf", SizeBytes: 10}}}
	m := NewManager(func() string { return root }, nil, src)
	// A path the hub never listed has no size, so neither the disk check nor
	// the progress bar would have a denominator — and it is how a crafted
	// request would try to reach an arbitrary file.
	if _, err := m.Start(context.Background(), StartRequest{Source: "fake", Repo: "o/r", Files: []string{"../../etc/passwd"}}); err == nil {
		t.Error("Start accepted a file that is not in the repo listing")
	}
}

func TestManager_NoModelsRootIsAClearError(t *testing.T) {
	src := &fakeSource{base: "http://127.0.0.1:1", files: []File{{Path: "m.gguf", SizeBytes: 10}}}
	m := NewManager(func() string { return "" }, nil, src)
	_, err := m.Start(context.Background(), StartRequest{Source: "fake", Repo: "o/r", Files: []string{"m.gguf"}})
	if err == nil || !strings.Contains(err.Error(), "models folder") {
		t.Errorf("err = %v, want a models-folder message", err)
	}
}
