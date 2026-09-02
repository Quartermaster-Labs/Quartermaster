package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

// The uninstaller's shutdown endpoint. It stops the server, so every gate on it
// is the feature: a page the user is reading in another tab must not be able to
// reach it, and neither must anything off the machine.
func TestServer_AppQuit(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		remoteAddr string
		header     bool
		hooked     bool
		wantCode   int
		wantQuit   bool
	}{
		{"loopback post with the header", http.MethodPost, "127.0.0.1:51000", true, true, http.StatusOK, true},
		{"loopback v6", http.MethodPost, "[::1]:51000", true, true, http.StatusOK, true},
		// No header means it could have come from a cross-origin form post,
		// which is the one shape a browser can produce without a preflight.
		{"loopback post without the header", http.MethodPost, "127.0.0.1:51000", false, true, http.StatusNotFound, false},
		// GET is what an <img> or a stray link would produce; the route is
		// registered POST-only, so the mux answers before the handler does.
		{"loopback get", http.MethodGet, "127.0.0.1:51000", true, true, http.StatusMethodNotAllowed, false},
		{"remote host", http.MethodPost, "192.0.2.9:51000", true, true, http.StatusNotFound, false},
		{"no shutdown hook", http.MethodPost, "127.0.0.1:51000", true, false, http.StatusNotFound, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))
			quit := make(chan struct{})
			if tc.hooked {
				s.SetShutdownHook(func() { close(quit) })
			}

			r := httptest.NewRequest(tc.method, appQuitPath, nil)
			r.RemoteAddr = tc.remoteAddr
			// Trusted by the access log, never by this handler.
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			if tc.header {
				r.Header.Set("X-Quartermaster-Quit", "1")
			}

			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d (body %q)", w.Code, tc.wantCode, w.Body.String())
			}
			// The hook runs in a goroutine so the reply is written first, so
			// this waits rather than reads a flag: a bare check would pass for
			// the wrong reason on a slow scheduler.
			select {
			case <-quit:
				if !tc.wantQuit {
					t.Error("shutdown was triggered by a request that must not be able to")
				}
			case <-time.After(300 * time.Millisecond):
				if tc.wantQuit {
					t.Error("shutdown was not triggered")
				}
			}
		})
	}
}
