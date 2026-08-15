package server

import (
	"io"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// withCookie builds a request carrying a raw pg_user cookie value.
func withCookie(val string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	if val != "" {
		r.AddCookie(&http.Cookie{Name: pgCookie, Value: val})
	}
	return r
}

func TestPlayground_CookieRoundTrip(t *testing.T) {
	p := &Playground{DataDir: t.TempDir()}

	if got := p.userFromRequest(withCookie(p.cookieValue("radu"))); got != "radu" {
		t.Fatalf("signed cookie should authenticate: got %q", got)
	}
}

// The whole point of the HMAC: a hand-written cookie (the pre-fix format, and
// what document.cookie='pg_user=victim' produces) must not authenticate.
func TestPlayground_CookieRejectsForgery(t *testing.T) {
	p := &Playground{DataDir: t.TempDir()}
	valid := p.cookieValue("radu")

	cases := map[string]string{
		"bare username (legacy format)": "victim",
		"empty":                         "",
		"username with empty mac":       "victim.",
		"username with garbage mac":     "victim.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"another user's mac":            "victim." + valid[len("radu."):],
		"invalid username charset":      p.cookieValue("../etc"),
	}
	for name, val := range cases {
		if got := p.userFromRequest(withCookie(val)); got != "" {
			t.Errorf("%s: expected rejection, authenticated as %q", name, got)
		}
	}
}

// A restart must not log everyone out, so the key is persisted next to the
// user data and reloaded rather than re-minted.
func TestPlayground_CookieSecretPersists(t *testing.T) {
	dir := t.TempDir()
	first := (&Playground{DataDir: dir}).cookieValue("radu")

	if _, err := os.Stat(filepath.Join(dir, ".cookie-secret")); err != nil {
		t.Fatalf("secret not persisted: %v", err)
	}
	reborn := &Playground{DataDir: dir} // fresh process, same data dir
	if got := reborn.userFromRequest(withCookie(first)); got != "radu" {
		t.Fatalf("cookie should survive a restart: got %q", got)
	}
}

// Two installs with separate data dirs must not accept each other's cookies.
func TestPlayground_CookieSecretIsPerInstall(t *testing.T) {
	a := &Playground{DataDir: t.TempDir()}
	b := &Playground{DataDir: t.TempDir()}

	if got := b.userFromRequest(withCookie(a.cookieValue("radu"))); got != "" {
		t.Fatalf("cookie from a different install authenticated as %q", got)
	}
}

// post drives one credential endpoint against a server with the playground on.
func postCreds(t *testing.T, s *Server, path, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(`{"username":"` + user + `","password":"` + pass + `"}`)
	r := httptest.NewRequest(http.MethodPost, path, body)
	w := httptest.NewRecorder()
	switch path {
	case "/auth/signup":
		s.handlePlaygroundSignup(w, r)
	default:
		s.handlePlaygroundLogin(w, r)
	}
	return w
}

// Signup creates; login authenticates. A typo'd username at the login form must
// not silently mint a second empty account (the old behaviour).
func TestPlayground_SignupThenLogin(t *testing.T) {
	s := &Server{playground: &Playground{DataDir: t.TempDir()}}

	if w := postCreds(t, s, "/auth/login", "alice", "hunter22"); w.Code != http.StatusUnauthorized {
		t.Fatalf("login before signup should 401, got %d", w.Code)
	}
	if w := postCreds(t, s, "/auth/signup", "alice", "hunter22"); w.Code != http.StatusOK {
		t.Fatalf("signup should succeed, got %d: %s", w.Code, w.Body)
	}
	if w := postCreds(t, s, "/auth/signup", "alice", "other123"); w.Code != http.StatusConflict {
		t.Fatalf("duplicate signup should 409, got %d", w.Code)
	}
	if w := postCreds(t, s, "/auth/login", "alice", "wrongpw1"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password should 401, got %d", w.Code)
	}
	if w := postCreds(t, s, "/auth/login", "alice", "hunter22"); w.Code != http.StatusOK {
		t.Fatalf("correct password should log in, got %d", w.Code)
	}
	if w := postCreds(t, s, "/auth/signup", "bob", "short"); w.Code != http.StatusBadRequest {
		t.Fatalf("short password should 400, got %d", w.Code)
	}
}

// The password must not be recoverable from users.json.
func TestPlayground_PasswordStoredHashed(t *testing.T) {
	dir := t.TempDir()
	s := &Server{playground: &Playground{DataDir: dir}}
	postCreds(t, s, "/auth/signup", "alice", "hunter22")

	raw, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatalf("read users.json: %v", err)
	}
	if strings.Contains(string(raw), "hunter22") {
		t.Fatalf("password stored in the clear: %s", raw)
	}
}

// An install written by an older build holds plaintext. Those users must still
// be able to log in, and the entry must be rewritten as a hash when they do.
func TestPlayground_LegacyPlaintextUpgrades(t *testing.T) {
	dir := t.TempDir()
	p := &Playground{DataDir: dir}
	if err := p.saveUsers(map[string]string{"alice": "hunter22"}); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	s := &Server{playground: p, proxylog: logmon.NewWriter(io.Discard)}

	if w := postCreds(t, s, "/auth/login", "alice", "hunter22"); w.Code != http.StatusOK {
		t.Fatalf("legacy plaintext login should work, got %d: %s", w.Code, w.Body)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "users.json"))
	if strings.Contains(string(raw), "hunter22") {
		t.Fatalf("legacy entry not upgraded to a hash: %s", raw)
	}
	if w := postCreds(t, s, "/auth/login", "alice", "hunter22"); w.Code != http.StatusOK {
		t.Fatalf("login should still work after the upgrade, got %d", w.Code)
	}
}
