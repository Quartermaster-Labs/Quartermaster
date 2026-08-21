package setup

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// ui_dist holds the wizard's own Svelte bundle, built by
// `npm run build:setup` (vite.setup.config.ts).
//
// It is a SECOND bundle rather than a route inside the dashboard's, because the
// wizard has to render before anything is installed and must not carry the
// dashboard's chart/markdown/katex weight to do it. `all:` so the committed
// .gitkeep keeps this compiling on a tree where the UI has never been built.
//
//go:embed all:ui_dist
var uiFS embed.FS

// scanTimeout bounds one folder-scan request. The user is typing, so a slow
// network share has to answer "still scanning" rather than hold the connection.
const scanTimeout = 6 * time.Second

// Handler is the wizard's HTTP surface: the UI bundle plus a handful of
// endpoints under /api/setup/.
//
// # Why a token, when this only listens on loopback
//
// Loopback is not a trust boundary on a desktop. Every process on the machine
// can reach 127.0.0.1, and so can any web page the user has open, which is the
// sharper problem: without a check, a page in a background tab could POST to
// /api/setup/install and drive a silent installer into a directory of its
// choosing. The token is minted per run and injected into index.html at serve
// time, so it never appears in a URL where a referrer header could carry it
// off-machine. Requiring it as a CUSTOM HEADER is doing double duty: a
// cross-origin request that sets one is no longer a "simple" request, so the
// browser preflights it, and we answer no CORS headers at all.
func (w *Wizard) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/setup/probe", w.guard(func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, http.StatusOK, NewProbe(w.opts.DefaultDir))
	}))

	mux.HandleFunc("POST /api/setup/scan", w.guard(func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<10)).Decode(&body); err != nil {
			writeJSON(rw, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), scanTimeout)
		defer cancel()
		writeJSON(rw, http.StatusOK, Scan(ctx, strings.TrimSpace(body.Path)))
	}))

	mux.HandleFunc("POST /api/setup/install", w.guard(func(rw http.ResponseWriter, r *http.Request) {
		var c Choices
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<10)).Decode(&c); err != nil {
			writeJSON(rw, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// The install runs on the wizard's own lifetime, not the request's: it
		// downloads hundreds of megabytes, and a page reload mid-download must
		// not abort a job already writing to disk. Same reasoning as
		// internal/update's apply path.
		if err := w.Start(context.Background(), c); err != nil {
			writeJSON(rw, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(rw, http.StatusAccepted, w.Status())
	}))

	mux.HandleFunc("GET /api/setup/status", w.guard(func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, http.StatusOK, w.Status())
	}))

	mux.HandleFunc("POST /api/setup/finish", w.guard(func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Launch bool `json:"launch"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(rw, r.Body, 4<<10)).Decode(&body)
		if err := w.Finish(body.Launch); err != nil {
			writeJSON(rw, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(rw, http.StatusOK, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("/", w.serveUI)
	return mux
}

// guard enforces the token and the loopback Host on every API call.
func (w *Wizard) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		// Compared with a plain != rather than a constant-time compare: the
		// token is a 128-bit random value read from a local response body, not
		// a derived secret, and there is no remote attacker to time this from.
		if r.Header.Get("X-QM-Setup-Token") != w.token {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		next(rw, r)
	}
}

// isLoopbackHost rejects a Host header naming anything but this machine, which
// is what stops a DNS-rebinding page from talking to the wizard through a name
// that resolves to 127.0.0.1.
func isLoopbackHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// serveUI serves the embedded bundle, injecting the run's token into index.html.
func (w *Wizard) serveUI(rw http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(uiFS, "ui_dist")
	if err != nil {
		http.Error(rw, "ui not embedded", http.StatusInternalServerError)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	data, err := fs.ReadFile(sub, name)
	if err != nil {
		// Unknown path: fall back to index.html so a client-side route survives
		// a reload. A genuinely missing bundle is reported instead.
		name = "index.html"
		if data, err = fs.ReadFile(sub, name); err != nil {
			http.Error(rw, uiMissing, http.StatusNotFound)
			return
		}
	}
	if name == "index.html" {
		data = bytes.Replace(data, []byte("<head>"),
			[]byte("<head><script>window.__QM_SETUP_TOKEN="+jsonString(w.token)+"</script>"), 1)
		rw.Header().Set("Cache-Control", "no-store")
	}
	http.ServeContent(rw, r, name, time.Time{}, bytes.NewReader(data))
}

// uiMissing is what a binary built before `npm run build:setup` serves. Saying
// so beats a blank window that looks like a crash.
const uiMissing = "The setup UI bundle was not built into this binary.\n" +
	"Run `npm run build:setup` in ui-svelte/, then rebuild."

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func writeJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(v)
}

// Listen starts the wizard's HTTP server on a free loopback port and returns
// the URL to point a window at, plus a shutdown func.
//
// Port 0: the wizard is short-lived and may run while a previous quartermaster
// still holds its usual ports, so a fixed one would be a collision waiting to
// happen. 127.0.0.1 explicitly, never 0.0.0.0 -- this server can write files
// and run an installer, and must not be reachable from the network for even
// the seconds the wizard is up.
func (w *Wizard) Listen() (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: w.Handler()}
	go func() { _ = srv.Serve(ln) }()

	u := url.URL{Scheme: "http", Host: ln.Addr().String(), Path: "/"}
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	return u.String(), stop, nil
}
