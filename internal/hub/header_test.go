package hub

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// serveRange answers a closed byte range (`bytes=0-N`), which is what FetchHead
// asks for — serveBlob only understands the open-ended form the downloader uses.
func serveRange(w http.ResponseWriter, r *http.Request, blob []byte) {
	rng := r.Header.Get("Range")
	if !strings.HasPrefix(rng, "bytes=0-") {
		w.Write(blob)
		return
	}
	end, err := strconv.Atoi(strings.TrimPrefix(rng, "bytes=0-"))
	if err != nil {
		w.Write(blob)
		return
	}
	if end >= len(blob) {
		end = len(blob) - 1
	}
	w.WriteHeader(http.StatusPartialContent)
	w.Write(blob[:end+1])
}

func TestFetchHead_RangeAndCap(t *testing.T) {
	blob := blobOf(64 << 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveRange(w, r, blob)
	}))
	defer srv.Close()

	src := &fakeSource{base: srv.URL, files: []File{{Path: "a.gguf", SizeBytes: int64(len(blob))}}}
	m := NewManager(func() string { return t.TempDir() }, nil, src)

	got, err := m.FetchHead(context.Background(), src, "o/r", "a.gguf", 4096)
	if err != nil {
		t.Fatalf("FetchHead: %v", err)
	}
	if len(got) != 4096 || !bytes.Equal(got, blob[:4096]) {
		t.Fatalf("got %d bytes, want the first 4096", len(got))
	}

	// A file shorter than the ask comes back whole rather than erroring: only the
	// caller parsing the prefix can tell whether what arrived was enough.
	got, err = m.FetchHead(context.Background(), src, "o/r", "a.gguf", 1<<20)
	if err != nil {
		t.Fatalf("FetchHead short file: %v", err)
	}
	if len(got) != len(blob) {
		t.Fatalf("got %d bytes, want the whole %d-byte file", len(got), len(blob))
	}
}

func TestFetchHead_RejectsBadInput(t *testing.T) {
	src := &fakeSource{base: "http://example.invalid"}
	m := NewManager(func() string { return t.TempDir() }, nil, src)

	// A path escaping the repo and an oversized ask both have to fail BEFORE any
	// request is built — the repo id and path are interpolated into a URL.
	if _, err := m.FetchHead(context.Background(), src, "o/r", "../etc/passwd", 4096); err == nil {
		t.Fatal("expected a traversing path to be refused")
	}
	if _, err := m.FetchHead(context.Background(), src, "o/r", "a.gguf", MaxHeaderBytes+1); err == nil {
		t.Fatal("expected an oversized header request to be refused")
	}
	if _, err := m.FetchHead(context.Background(), src, "not-a-repo-id/", "a.gguf", 4096); err == nil {
		t.Fatal("expected an invalid repo id to be refused")
	}
}
