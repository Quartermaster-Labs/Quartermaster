package server

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
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

// GET /api/inference-key hands a working key to logged-in REMOTE playground
// users: the /api/apikeys list stays admin-gated to the server host by design,
// but the playground browser needs a key for its direct /v1 calls (chat
// titles, auto-compaction, image/speech). Local callers read it without
// logging in (they already have the open admin surface); remote callers must
// carry a valid session cookie.
func TestPlayground_InferenceKey(t *testing.T) {
	newKeyServer := func() *Server {
		s := &Server{playground: &Playground{DataDir: t.TempDir()}}
		s.SetAdminAccess(true, nil) // production default: admin surface is local-only
		s.cfg.Store(&config.Config{
			RequiredAPIKeys: []string{"qm-scoped", "qm-full"},
			APIKeyModels:    map[string][]string{"qm-scoped": {"m1"}},
		})
		return s
	}
	hit := func(s *Server, addr, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/inference-key", nil)
		r.RemoteAddr = addr
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: pgCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		s.handlePlaygroundInferenceKey(w, r)
		return w
	}
	bodyKey := func(t *testing.T, w *httptest.ResponseRecorder) string {
		t.Helper()
		var out struct{ Key string }
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatalf("decode %q: %v", w.Body.String(), err)
		}
		return out.Key
	}
	s := newKeyServer()
	p := s.playground

	if w := hit(s, "203.0.113.7:51000", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("remote unauthenticated: want 401, got %d", w.Code)
	}
	if w := hit(s, "203.0.113.7:51000", "radu."); w.Code != http.StatusUnauthorized {
		t.Fatalf("forged cookie: want 401, got %d", w.Code)
	}
	if got := bodyKey(t, hit(s, "203.0.113.7:51000", p.cookieValue("radu"))); got != "qm-full" {
		t.Fatalf("logged-in remote: want the unscoped key, got %q", got)
	}
	if got := bodyKey(t, hit(s, "127.0.0.1:51001", "")); got != "qm-full" {
		t.Fatalf("local admin without login: want the unscoped key, got %q", got)
	}

	s.cfg.Store(&config.Config{}) // no keys => auth middleware is a pass-through
	if got := bodyKey(t, hit(s, "203.0.113.7:51002", p.cookieValue("radu"))); got != "" {
		t.Fatalf("no keys configured: want empty key, got %q", got)
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

// GET /auth/accounts is what makes the login form open on the sign-up pane the
// first time the app is launched, so it must read false on an empty data dir
// and true the moment an account exists.
func TestPlayground_AccountsAny(t *testing.T) {
	s := &Server{playground: &Playground{DataDir: t.TempDir()}}

	any := func() bool {
		t.Helper()
		w := httptest.NewRecorder()
		s.handlePlaygroundAccounts(w, httptest.NewRequest(http.MethodGet, "/auth/accounts", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("accounts should 200, got %d: %s", w.Code, w.Body)
		}
		var body struct {
			Any bool `json:"any"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		return body.Any
	}

	if any() {
		t.Fatal("fresh install should report no accounts")
	}
	if w := postCreds(t, s, "/auth/signup", "alice", "hunter22"); w.Code != http.StatusOK {
		t.Fatalf("signup should succeed, got %d: %s", w.Code, w.Body)
	}
	if !any() {
		t.Fatal("should report an account after signup")
	}
}
