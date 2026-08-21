package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The endpoint pops a window onto the operator's screen, so the guard is the
// whole feature. httptest.NewRequest defaults RemoteAddr to 192.0.2.1:1234 --
// a non-loopback address -- which is why each case sets it explicitly.
func TestServer_AppShow(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		hooked     bool
		wantCode   int
		wantShown  bool
	}{
		{"loopback with a window", "127.0.0.1:51000", true, http.StatusOK, true},
		{"loopback v6 with a window", "[::1]:51000", true, http.StatusOK, true},
		{"loopback without a window", "127.0.0.1:51000", false, http.StatusNotFound, false},
		// A LAN or tailnet host that can reach a non-loopback listener must not
		// be able to raise the window, and must not learn there is one: 404,
		// the same answer a headless server gives.
		{"remote host with a window", "192.0.2.9:51000", true, http.StatusNotFound, false},
		// Spoofing the forwarded-for header must change nothing; the handler
		// reads RemoteAddr and nothing else.
		{"remote host claiming loopback", "192.0.2.9:51000", true, http.StatusNotFound, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))
			shown := false
			if tc.hooked {
				s.SetShowAppHook(func() { shown = true })
			}

			r := httptest.NewRequest(http.MethodGet, appWindowShowPath, nil)
			r.RemoteAddr = tc.remoteAddr
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("X-Real-IP", "127.0.0.1")

			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d (body %q)", w.Code, tc.wantCode, w.Body.String())
			}
			if shown != tc.wantShown {
				t.Errorf("window shown = %v, want %v", shown, tc.wantShown)
			}
		})
	}
}
