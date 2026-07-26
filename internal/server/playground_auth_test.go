package server

import (
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
