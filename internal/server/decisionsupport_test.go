package server

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The route string lives in the server library, not the launcher, and a match
// straddling a read boundary must still count.
func TestBackendServesSystemOne(t *testing.T) {
	write := func(p string, b []byte) {
		t.Helper()
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	newer := t.TempDir()
	exe := filepath.Join(newer, "llama-server.exe")
	write(exe, []byte("launcher"))
	pad := bytes.Repeat([]byte{0}, 1<<20-5) // needle crosses the 1 MiB chunk edge
	write(filepath.Join(newer, "llama-server-impl.dll"), append(pad, []byte("xx/v1/systemone\x00")...))
	if ok, known := backendServesSystemOne(exe); !known || !ok {
		t.Errorf("new build: ok=%v known=%v, want true true", ok, known)
	}

	older := t.TempDir()
	oldExe := filepath.Join(older, "llama-server.exe")
	write(oldExe, []byte("/v1/chat/completions /v1/embeddings"))
	write(filepath.Join(older, "ggml-base.dll"), []byte("systemone")) // not a llama/server lib
	if ok, known := backendServesSystemOne(oldExe); !known || ok {
		t.Errorf("old build: ok=%v known=%v, want false true", ok, known)
	}

	// Updating in place changes the fingerprint, so the cached "no" is dropped.
	write(oldExe, []byte("/v1/chat/completions systemone"))
	if ok, _ := backendServesSystemOne(oldExe); !ok {
		t.Error("updated build still reported as outdated (stale cache)")
	}

	if _, known := backendServesSystemOne(filepath.Join(older, "missing.exe")); known {
		t.Error("missing exe must be unknown")
	}
}
