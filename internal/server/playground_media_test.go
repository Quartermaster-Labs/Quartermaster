package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlayground_extractMedia(t *testing.T) {
	dir := t.TempDir()
	p := &Playground{DataDir: dir}

	// wav bytes -> base64 data URL inline in a JSON blob, plus a timestamp we
	// require to survive byte-for-byte (regression: no JSON re-marshal).
	raw := []byte("RIFF....data....") // stand-in audio bytes
	b64 := base64.StdEncoding.EncodeToString(raw)
	in := []byte(`[{"ts":1700000000000,"audio":"data:audio/wav;base64,` + b64 + `","x":"data:audio/wav;base64,` + b64 + `"}]`)

	out := string(p.extractMedia("radu", in))

	// data URL gone, replaced by a /api/media ref; both (identical) blobs share one file.
	if strings.Contains(out, "base64,") {
		t.Fatalf("data URL not stripped: %s", out)
	}
	if !strings.Contains(out, "/api/media/audio/") || !strings.HasSuffix(strings.SplitN(out[strings.Index(out, "/api/media/"):], `"`, 2)[0], ".wav") {
		t.Fatalf("no audio .wav media ref: %s", out)
	}
	if !strings.Contains(out, `"ts":1700000000000`) {
		t.Fatalf("timestamp not byte-preserved: %s", out)
	}

	audioDir := filepath.Join(p.mediaDir("radu"), "audio")
	files, _ := os.ReadDir(audioDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 deduped media file, got %d", len(files))
	}
	got, _ := os.ReadFile(filepath.Join(audioDir, files[0].Name()))
	if string(got) != string(raw) {
		t.Fatalf("media bytes corrupted: %q != %q", got, raw)
	}

	// idempotent: feeding the rewritten refs back changes nothing.
	if string(p.extractMedia("radu", []byte(out))) != out {
		t.Fatalf("extractMedia not idempotent")
	}
}

func TestPlayground_gcMedia(t *testing.T) {
	defer func(g time.Duration) { mediaGCGrace = g }(mediaGCGrace)
	mediaGCGrace = 0
	dir := t.TempDir()
	p := &Playground{DataDir: dir}

	// two distinct media files: one kept (referenced), one orphaned.
	kept := string(p.extractMedia("radu", []byte(`"data:audio/wav;base64,`+base64.StdEncoding.EncodeToString([]byte("keep-me"))+`"`)))
	orphan := string(p.extractMedia("radu", []byte(`"data:audio/wav;base64,`+base64.StdEncoding.EncodeToString([]byte("drop-me"))+`"`)))
	if kept == orphan {
		t.Fatal("distinct bytes should yield distinct refs")
	}
	audioDir := filepath.Join(p.mediaDir("radu"), "audio")
	if files, _ := os.ReadDir(audioDir); len(files) != 2 {
		t.Fatalf("expected 2 media files pre-GC, got %d", len(files))
	}

	// only `kept` is referenced by a tab JSON.
	os.WriteFile(p.speechChatsPath("radu"), []byte(`[{"audio":`+kept+`}]`), 0o644)
	p.gcMedia("radu")

	files, _ := os.ReadDir(audioDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file after GC, got %d", len(files))
	}
	keptName := filepath.Base(strings.Trim(kept, `"`))
	if files[0].Name() != keptName {
		t.Fatalf("GC deleted the referenced file: kept %s, on disk %s", keptName, files[0].Name())
	}
}

// A vision follow-up replays history to the MODEL, so refs must rehydrate to
// real bytes — extract -> inline is a round-trip back to the original blob.
func TestPlayground_inlineMedia(t *testing.T) {
	dir := t.TempDir()
	p := &Playground{DataDir: dir}

	png := []byte("\x89PNG\r\n\x1a\n fake pixels")
	in := []byte(`[{"ts":1700000000000,"image_url":{"url":"data:image/png;base64,` +
		base64.StdEncoding.EncodeToString(png) + `"}}]`)

	refs := p.extractMedia("radu", in)
	if !strings.Contains(string(refs), "/api/media/image/") {
		t.Fatalf("no media ref to inline: %s", refs)
	}
	if got := string(p.inlineMedia("radu", refs)); got != string(in) {
		t.Fatalf("round-trip mismatch:\n got %s\nwant %s", got, in)
	}

	// A ref whose file is gone stays put rather than becoming a broken data URL.
	gone := []byte(`"/api/media/image/deadbeefdeadbeef.png"`)
	if got := string(p.inlineMedia("radu", gone)); got != string(gone) {
		t.Fatalf("missing file should be left alone, got %s", got)
	}

	// Traversal can't escape the media dir: the ref grammar admits no "..".
	if err := os.WriteFile(filepath.Join(dir, "secret.png"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	esc := []byte(`"/api/media/image/../../secret.png"`)
	if got := string(p.inlineMedia("radu", esc)); strings.Contains(got, "base64,") {
		t.Fatalf("traversal ref resolved: %s", got)
	}
}

// A file younger than mediaGCGrace survives a GC even though no tab names it:
// that is the window between POST /api/media and the PUT that lands its ref.
func TestPlayground_gcMediaGrace(t *testing.T) {
	p := &Playground{DataDir: t.TempDir()}
	ref := string(p.extractMedia("radu", []byte(`"data:audio/wav;base64,`+base64.StdEncoding.EncodeToString([]byte("young"))+`"`)))
	file := filepath.Join(p.mediaDir("radu"), "audio", filepath.Base(strings.Trim(ref, `"`)))

	p.gcMedia("radu")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("young unreferenced file was collected: %v", err)
	}

	old := time.Now().Add(-mediaGCGrace - time.Minute)
	os.Chtimes(file, old, old)
	p.gcMedia("radu")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("old unreferenced file survived GC: %v", err)
	}
}

func TestPlayground_mediaUpload(t *testing.T) {
	p := &Playground{DataDir: t.TempDir()}
	s := &Server{playground: p}
	post := func(ct string, body []byte, cookie bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/media", bytes.NewReader(body))
		r.Header.Set("Content-Type", ct)
		if cookie {
			r.AddCookie(&http.Cookie{Name: pgCookie, Value: p.cookieValue("radu")})
		}
		w := httptest.NewRecorder()
		s.handlePlaygroundMediaUpload(w, r)
		return w
	}

	if w := post("audio/wav", []byte("RIFF"), false); w.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: got %d, want 401", w.Code)
	}
	if w := post("application/json", []byte("{}"), true); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("json body: got %d, want 415", w.Code)
	}
	if w := post("audio/wav", nil, true); w.Code != http.StatusBadRequest {
		t.Fatalf("empty body: got %d, want 400", w.Code)
	}

	w := post("audio/wav; codecs=1", []byte("RIFF....song"), true)
	if w.Code != http.StatusOK {
		t.Fatalf("upload: got %d: %s", w.Code, w.Body.String())
	}
	var got struct{ URL string }
	json.Unmarshal(w.Body.Bytes(), &got)
	// The same ref extractMedia writes for the same bytes, so an upload and an
	// inline save of one clip share a file.
	inline := strings.Trim(string(p.extractMedia("radu", []byte(`"data:audio/wav;base64,`+base64.StdEncoding.EncodeToString([]byte("RIFF....song"))+`"`))), `"`)
	if got.URL != inline {
		t.Fatalf("upload ref %q, extractMedia ref %q", got.URL, inline)
	}
	data, err := os.ReadFile(filepath.Join(p.mediaDir("radu"), filepath.FromSlash(strings.TrimPrefix(got.URL, "/api/media/"))))
	if err != nil || string(data) != "RIFF....song" {
		t.Fatalf("stored bytes %q, err %v", data, err)
	}

	// A re-upload refreshes the mtime, which is what holds it inside the grace.
	file := filepath.Join(p.mediaDir("radu"), filepath.FromSlash(strings.TrimPrefix(got.URL, "/api/media/")))
	old := time.Now().Add(-time.Hour)
	os.Chtimes(file, old, old)
	if w := post("audio/wav", []byte("RIFF....song"), true); w.Code != http.StatusOK {
		t.Fatalf("re-upload: got %d", w.Code)
	}
	if info, _ := os.Stat(file); time.Since(info.ModTime()) > time.Minute {
		t.Fatalf("re-upload did not refresh mtime: %v", info.ModTime())
	}
}
