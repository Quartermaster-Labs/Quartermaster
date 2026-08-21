package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdate_Newer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.7.0", "v0.7.1", true},
		{"v0.7.0", "v0.8.0", true},
		{"v0.7.0", "v1.0.0", true},
		{"v0.7.0", "v0.7.0", false},
		{"v0.8.0", "v0.7.9", false},
		{"v0.9.0", "v0.10.0", true}, // not string ordering
		// Dev builds and prereleases are unparseable on purpose: neither side
		// may ever be told it is out of date.
		{"local_abc123", "v9.9.9", false},
		{"v0.7.0", "v1.0.0-rc1", false},
		{"", "v1.0.0", false},
	}
	for _, c := range cases {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestUpdate_ValidAssetURL(t *testing.T) {
	ok := []string{
		"https://github.com/o/r/releases/download/v1/quartermaster-linux-amd64",
		"https://objects.githubusercontent.com/x",
	}
	bad := []string{
		"http://github.com/o/r/x",        // not https
		"https://evil.com/quartermaster", // not GitHub
		"https://github.com.evil.com/x",  // suffix trick
		"file:///C:/Windows/System32/x.exe",
	}
	for _, u := range ok {
		if err := validAssetURL(u); err != nil {
			t.Errorf("validAssetURL(%q) = %v, want nil", u, err)
		}
	}
	for _, u := range bad {
		if err := validAssetURL(u); err == nil {
			t.Errorf("validAssetURL(%q) = nil, want an error", u)
		}
	}
}

// The Windows binary and the first-install wizard are both .exe assets on the
// same release. Matching must be exact so an update can never download the
// installer and rename it over the running binary.
func TestUpdate_AssetNameIsExact(t *testing.T) {
	name := assetName()
	if name == "" {
		t.Skip("no published binary for this platform")
	}
	if strings.Contains(name, "setup") {
		t.Fatalf("asset name %q must not match the installer", name)
	}
	for _, other := range []string{
		"quartermaster-setup-v0.8.0.exe",
		"SHA256SUMS",
		"quartermaster-linux-amd64.tar.gz",
	} {
		if other == name {
			t.Fatalf("asset name %q collides with %q", name, other)
		}
	}
}

func TestUpdate_FetchSumParsesSha256sumFormat(t *testing.T) {
	body := "" +
		"1111111111111111111111111111111111111111111111111111111111111111  quartermaster-linux-amd64\n" +
		"2222222222222222222222222222222222222222222222222222222222222222 *quartermaster-windows-amd64.exe\n" +
		"garbage line\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := New("o/r", "v0.1.0", nil)
	// validAssetURL would reject the test server, so exercise the parser via a
	// client pointed at it and a GitHub-shaped URL rewritten by the transport.
	c.client = srv.Client()
	c.client.Transport = rewriteHost{to: srv.Listener.Addr().String()}

	got := c.fetchSum(context.Background(), "https://github.com/o/r/releases/download/v1/SHA256SUMS", "quartermaster-linux-amd64")
	if got != "1111111111111111111111111111111111111111111111111111111111111111" {
		t.Errorf("plain entry: got %q", got)
	}
	// sha256sum's binary-mode "*" prefix must not defeat the match.
	got = c.fetchSum(context.Background(), "https://github.com/o/r/releases/download/v1/SHA256SUMS", "quartermaster-windows-amd64.exe")
	if got != "2222222222222222222222222222222222222222222222222222222222222222" {
		t.Errorf("binary-mode entry: got %q", got)
	}
	if got := c.fetchSum(context.Background(), "https://github.com/o/r/releases/download/v1/SHA256SUMS", "absent"); got != "" {
		t.Errorf("absent entry: got %q, want empty", got)
	}
}

type rewriteHost struct{ to string }

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme, req.URL.Host = "http", r.to
	return http.DefaultTransport.RoundTrip(req)
}

// A release whose binary asset has no digest — no `digest` field and no
// SHA256SUMS — must not be offered. Executing an unverified download is the one
// failure this package cannot walk back.
func TestUpdate_UnverifiableReleaseIsNotOffered(t *testing.T) {
	name := assetName()
	if name == "" {
		t.Skip("no published binary for this platform")
	}
	rel := ghRelease{TagName: "v9.9.9", HTMLURL: "https://example.invalid/r"}
	rel.Assets = append(rel.Assets, struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
	}{Name: name, URL: "https://github.com/o/r/releases/download/v9.9.9/" + name})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(rel)
	}))
	defer srv.Close()

	c := New("o/r", "v0.1.0", nil)
	c.client = srv.Client()
	c.client.Transport = rewriteHost{to: srv.Listener.Addr().String()}
	c.checkOnce(context.Background())

	if st := c.Status(); st.Available {
		t.Errorf("release with no digest was offered as available: %+v", st)
	}
	// The version is still reported, so the UI can link to the release notes.
	if st := c.Status(); st.Latest != "v9.9.9" {
		t.Errorf("latest = %q, want v9.9.9", st.Latest)
	}
}

// swap must be reversible. Two renames within one directory, and if the second
// fails the first is undone — the install is never left without a binary at its
// own path.
func TestUpdate_SwapAndRollback(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "quartermaster.exe")
	if err := os.WriteFile(exe, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(dir, stageDir)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(stage, "quartermaster.exe.new")
	if err := os.WriteFile(newBin, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := swap(exe, newBin, stage); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "NEW" {
		t.Errorf("after swap, exe = %q, want NEW", b)
	}
	old := filepath.Join(stage, "quartermaster.exe"+oldSuffix)
	if b, _ := os.ReadFile(old); string(b) != "OLD" {
		t.Errorf("outgoing binary not kept at %s (got %q)", old, b)
	}

	// Rollback: point the second rename at a source that does not exist. The
	// original must be back in place afterwards.
	if err := os.WriteFile(exe, []byte("OLD2"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := swap(exe, filepath.Join(stage, "does-not-exist"), stage)
	if err == nil {
		t.Fatal("swap with a missing new binary should fail")
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD2" {
		t.Errorf("rollback failed: exe = %q, want OLD2", b)
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("error should say the install is unchanged, got: %v", err)
	}
}

// SweepOld clears the previous update's leftovers and takes the scratch dir with
// them, so a settled install carries no debris.
func TestUpdate_SweepOld(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, stageDir)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"qm.exe" + oldSuffix, "qm.exe.new", "keep.txt"} {
		if err := os.WriteFile(filepath.Join(stage, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sweepDir(stage, nil)

	if _, err := os.Stat(filepath.Join(stage, "qm.exe"+oldSuffix)); !os.IsNotExist(err) {
		t.Error(".old not swept")
	}
	if _, err := os.Stat(filepath.Join(stage, "qm.exe.new")); !os.IsNotExist(err) {
		t.Error(".new not swept")
	}
	// Anything else is left alone, and the dir stays while it holds something.
	if _, err := os.Stat(filepath.Join(stage, "keep.txt")); err != nil {
		t.Error("unrelated file was removed")
	}
	if _, err := os.Stat(stage); err != nil {
		t.Error("non-empty staging dir was removed")
	}

	os.Remove(filepath.Join(stage, "keep.txt"))
	sweepDir(stage, nil)
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Error("empty staging dir should be removed")
	}
}

func TestUpdate_ApplyRefusesWithoutAVerifiedTarget(t *testing.T) {
	c := New("o/r", "v0.1.0", nil)
	if err := c.Apply(context.Background()); err == nil {
		t.Fatal("Apply with nothing available should fail")
	}
	c.mu.Lock()
	c.status.Available = true
	c.status.assetURL = "https://github.com/o/r/releases/download/v1/x"
	c.status.sha256 = "" // no digest
	c.mu.Unlock()
	if err := c.Apply(context.Background()); err == nil {
		t.Fatal("Apply without a digest should fail")
	}
}
