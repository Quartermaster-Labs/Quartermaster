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
		"quartermaster-setup-windows-amd64-v0.8.0.exe",
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

// CheckNow is what a user's "check for updates" click runs. It has to do two
// things the six-hourly poll never had to: stamp when it ran, so the UI can say
// the answer on screen is fresh, and RETURN the failure instead of only logging
// it, so an offline machine gets an error rather than a button that appears to
// do nothing.
func TestUpdate_CheckNowStampsAndReportsFailure(t *testing.T) {
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
	}{
		Name:   name,
		URL:    "https://github.com/o/r/releases/download/v9.9.9/" + name,
		Digest: "sha256:" + strings.Repeat("a", 64),
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(rel)
	}))
	defer srv.Close()

	c := New("o/r", "v0.1.0", nil)
	c.client = srv.Client()
	c.client.Transport = rewriteHost{to: srv.Listener.Addr().String()}

	if st := c.Status(); !st.Checked.IsZero() {
		t.Fatalf("a fresh checker claims to have checked at %v", st.Checked)
	}
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	st := c.Status()
	if st.Checked.IsZero() {
		t.Error("a successful check left no timestamp, so the UI cannot tell it ran")
	}
	if !st.Available || st.Latest != "v9.9.9" {
		t.Errorf("verifiable newer release was not offered: %+v", st)
	}
	if !st.Enabled {
		t.Error("a semver build on a published platform reported enabled=false")
	}

	// Unreachable feed: the stamp must NOT move, or the UI would report a stale
	// answer as freshly confirmed.
	was := st.Checked
	srv.Close()
	if err := c.CheckNow(context.Background()); err == nil {
		t.Error("CheckNow swallowed a dead release feed")
	}
	if got := c.Status().Checked; !got.Equal(was) {
		t.Errorf("a failed check moved the timestamp: %v -> %v", was, got)
	}
}

// A build from source has no release to compare against, so the check is not
// merely useless there -- it must refuse, or the UI would offer a button that
// polls GitHub and can never do anything with the answer.
func TestUpdate_CheckNowRefusesForDevBuilds(t *testing.T) {
	c := New("o/r", "local_abc1234", nil)
	if err := c.CheckNow(context.Background()); err == nil {
		t.Error("a dev build checked for updates")
	}
	if st := c.Status(); st.Enabled {
		t.Error("a dev build reported enabled=true")
	}
}

// The four published asset names live in three places: assetName's switch, the
// `dist` target in the Makefile, and the $targets table in
// packaging/windows/build-release.ps1. assetName matches an asset EXACTLY and a
// miss is silent -- checkOnce simply finds no asset for this platform and
// reports no update -- so a rename on one side cuts every installed copy of
// that platform off from updates with nothing logged anywhere. This test is the
// only thing that notices.
//
// It reads the release scripts as text rather than deriving anything from
// runtime.GOOS, so all four are checked from whichever OS runs the suite. The
// literal list below is a fourth copy on purpose: it is the assertion, and the
// point is that changing one copy without the others fails here.
func TestUpdate_AssetNamesMatchReleaseScripts(t *testing.T) {
	names := []string{
		"quartermaster-windows-amd64.exe",
		"quartermaster-linux-amd64",
		"quartermaster-linux-arm64",
		"quartermaster-darwin-arm64",
	}
	if n := assetName(); n != "" {
		found := false
		for _, want := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("assetName() = %q, which is not one of the published assets %v", n, names)
		}
	}

	// Only the lines that write into $(DIST_DIR) count: PKG_NIX_BIN_* and the
	// per-platform dev targets carry similar-looking names, and matching those
	// would let the actual release target drift while the test stayed green.
	mk, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	var dist []string
	for _, ln := range strings.Split(string(mk), "\n") {
		if strings.Contains(ln, "$(DIST_DIR)/") {
			dist = append(dist, strings.ReplaceAll(ln, "$(APP_NAME)", "quartermaster"))
		}
	}
	if len(dist) == 0 {
		t.Fatal("Makefile has no $(DIST_DIR)/ lines; the dist target moved or was renamed")
	}
	mkText := strings.Join(dist, "\n")

	ps, err := os.ReadFile(filepath.Join("..", "..", "packaging", "windows", "build-release.ps1"))
	if err != nil {
		t.Fatalf("read build-release.ps1: %v", err)
	}

	for _, want := range names {
		if !strings.Contains(mkText, want) {
			t.Errorf("Makefile dist target does not build %q; the updater will never find it", want)
		}
		if !strings.Contains(string(ps), want) {
			t.Errorf("build-release.ps1 does not build %q; the updater will never find it", want)
		}
	}
}
