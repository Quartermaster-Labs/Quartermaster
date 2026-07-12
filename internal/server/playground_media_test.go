package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
