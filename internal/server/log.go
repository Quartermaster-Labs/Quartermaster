package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/chain"
	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// NewLoggers builds the proxy, upstream, and combined (mux) log monitors,
// wiring each one's output per the logToStdout config value. The proxy and
// upstream monitors write into muxlog (rather than os.Stdout directly) so
// muxlog accumulates a combined history for the /logs endpoints, while each
// monitor keeps its own per-source history and event subscribers.
//
// Behaviour matches the legacy ProxyManager:
//
//   - none:     everything discarded
//   - both:     proxy + upstream both routed to muxlog -> stdout
//   - upstream: only upstream routed to muxlog -> stdout; proxy discarded
//   - proxy:    only proxy routed to muxlog -> stdout; upstream discarded
//
// An empty or unrecognised value behaves like "proxy".
func NewLoggers(logToStdout string) (muxlog, proxylog, upstreamlog *logmon.Monitor) {
	switch logToStdout {
	case config.LogToStdoutNone:
		muxlog = logmon.NewWriter(io.Discard)
		proxylog = logmon.NewWriter(io.Discard)
		upstreamlog = logmon.NewWriter(io.Discard)
	case config.LogToStdoutBoth:
		muxlog = logmon.NewWriter(os.Stdout)
		proxylog = logmon.NewWriter(muxlog)
		upstreamlog = logmon.NewWriter(muxlog)
	case config.LogToStdoutUpstream:
		muxlog = logmon.NewWriter(os.Stdout)
		proxylog = logmon.NewWriter(io.Discard)
		upstreamlog = logmon.NewWriter(muxlog)
	default:
		// config.LogToStdoutProxy, and the fallback for an unset value.
		muxlog = logmon.NewWriter(os.Stdout)
		proxylog = logmon.NewWriter(muxlog)
		upstreamlog = logmon.NewWriter(io.Discard)
	}
	return muxlog, proxylog, upstreamlog
}

// handleLogs serves the historical proxy/upstream log. HTML clients are
// redirected to the UI. `?source=proxy|upstream` selects one monitor; the
// default (or any other value) is the combined mux log, preserving old behavior.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/ui/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	log := s.muxlog
	switch r.URL.Query().Get("source") {
	case "proxy":
		log = s.proxylog
	case "upstream":
		log = s.upstreamlog
	}
	w.Write(log.GetHistory())
}

// getLogger resolves a log monitor by id. An empty id maps to the combined
// muxlog; "proxy" and "upstream" select the respective monitors.
func (s *Server) getLogger(logMonitorID string) (*logmon.Monitor, error) {
	switch logMonitorID {
	case "":
		return s.muxlog, nil
	case "proxy":
		return s.proxylog, nil
	case "upstream":
		return s.upstreamlog, nil
	default:
		if _, modelID, _, found := findModelInPath(s.config(), "/"+logMonitorID); found {
			if log, ok := s.local.ProcessLogger(modelID); ok {
				return log, nil
			}
		}
		return nil, fmt.Errorf("invalid logger. Use 'proxy', 'upstream' or a model's ID")
	}
}

// handleLogStream tails a log monitor: it writes the history then streams live
// log data until the client disconnects or the server shuts down.
func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// prevent nginx from buffering streamed logs
	w.Header().Set("X-Accel-Buffering", "no")

	logMonitorID := strings.TrimPrefix(r.PathValue("logMonitorID"), "/")
	// Strip a query string if it leaked into the path segment.
	if idx := strings.Index(logMonitorID, "?"); idx != -1 {
		logMonitorID = logMonitorID[:idx]
	}

	logger, err := s.getLogger(logMonitorID)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		shared.SendResponse(w, r, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	_, skipHistory := r.URL.Query()["no-history"]
	if !skipHistory {
		if history := logger.GetHistory(); len(history) != 0 {
			w.Write(history)
			flusher.Flush()
		}
	}

	sendChan := make(chan []byte, 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cancelSub := logger.OnLogData(func(data []byte) {
		select {
		case sendChan <- data:
		case <-ctx.Done():
		default:
		}
	})
	defer cancelSub()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.shutdownCtx.Done():
			return
		case data := <-sendChan:
			w.Write(data)
			flusher.Flush()
		}
	}
}

// noticeLogger adapts a leveled Monitor to the plain func(string) progress
// callback the subsystem managers (hub, backends, autogen, the updater) take.
// Those managers report success and failure down the same channel, so "hub:
// download failed: …" used to land at INFO right next to "downloaded" —
// indistinguishable at a glance, invisible under logLevel: warn, and now
// uncoloured in the log pane. Classifying on the message is crude, but it is
// the only signal the callback carries, and the alternative is a leveled
// callback threaded through four packages.
func noticeLogger(lg *logmon.Monitor) func(string) {
	return func(m string) {
		l := strings.ToLower(m)
		switch {
		case strings.Contains(l, "failed"), strings.Contains(l, "error"),
			strings.Contains(l, "panic"), strings.Contains(l, "warning"):
			lg.Warn(m)
		default:
			lg.Info(m)
		}
	}
}

// NoticeLogger is noticeLogger for callers outside this package (the root
// command wires the same callback into autogen at startup).
func NoticeLogger(lg *logmon.Monitor) func(string) { return noticeLogger(lg) }

// requestLogPathSkips lists path prefixes excluded from the access log because
// they are polled frequently and would drown out useful entries.
var requestLogPathSkips = []string{"/wol-health", "/api/performance", "/api/kvcache", "/metrics"}

// statusRecorder wraps an http.ResponseWriter to capture the response status
// code and the number of body bytes written, so the access log can report
// them. Flush is forwarded so streaming handlers (SSE) still work, and Hijack
// is forwarded so httputil.ReverseProxy can upgrade websocket connections.
type statusRecorder struct {
	http.ResponseWriter
	status int
	size   int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	n, err := sr.ResponseWriter.Write(b)
	sr.size += n
	return n, err
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying ResponseWriter so httputil.ReverseProxy can
// take over the connection for websocket upgrades.
func (sr *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := sr.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
}

// clientIP resolves the originating client address, preferring proxy headers
// over the raw connection address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, found := strings.Cut(xff, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// isLoopbackIP reports whether an address string is this machine talking to
// itself — the common case, whose IP is noise in the access log.
func isLoopbackIP(ip string) bool {
	if ip == "" || ip == "::1" || ip == "localhost" {
		return true
	}
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}

// shortAgent condenses a User-Agent down to something that fits on a log line:
// browsers all collapse to "browser", and a tool keeps its "name/version"
// token ("curl/8.5.0", "python-requests/2.31"). The rest of a modern UA string
// is boilerplate that buries the parts of the line worth reading.
func shortAgent(ua string) string {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return ""
	}
	first := ua
	if idx := strings.IndexByte(first, ' '); idx != -1 {
		first = first[:idx]
	}
	if strings.HasPrefix(first, "Mozilla/") {
		return "browser"
	}
	if len(first) > 28 {
		first = first[:28]
	}
	return first
}

// logSize renders a response body size in units a human reads at a glance.
func logSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fkB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}

// logDuration renders a request duration at a fixed, low precision.
// time.Duration's own String() prints full nanosecond precision
// ("8.123456789s"), which is the single noisiest field in the old access log.
func logDuration(d time.Duration) string {
	switch {
	case d < 100*time.Microsecond:
		return "<0.1ms"
	case d < time.Millisecond:
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// quietRequest reports whether a completed request is dashboard chatter rather
// than something the operator cares about. The web UI fires a steady stream of
// GETs at /api/… and /ui/… just by being open; logging those at INFO buries
// the inference traffic. They drop to DEBUG instead of being skipped outright,
// so `logLevel: debug` still shows everything. Anything that failed, and every
// non-GET, stays at its normal level.
func quietRequest(method, path string, status int) bool {
	if status >= 400 || method != http.MethodGet {
		return false
	}
	return strings.HasPrefix(path, "/api/") || path == "/api" ||
		strings.HasPrefix(path, "/ui/") || path == "/ui" ||
		path == "/favicon.ico"
}

// CreateRequestLogMiddleware returns middleware that records one access-log
// line per request to proxylog:
//
//	POST /v1/chat/completions 200 44.1kB 8.1s
//
// followed by the client IP when it is not loopback and by a short agent name
// when the caller is not a browser. The HTTP version, the raw byte count, the
// nanosecond-precision duration and the full User-Agent that the old format
// carried are all dropped — they pushed the status and the path off the side of
// the log pane without ever being the thing anyone was looking for.
//
// The level reflects the outcome, so the UI's colouring (and a logLevel of
// warn) surfaces failures directly: 5xx logs at ERROR, 4xx at WARN, dashboard
// polling at DEBUG, everything else at INFO.
//
// Frequently-polled health/metrics paths are skipped entirely. The path is
// captured before next runs because /upstream rewrites the request URL in place.
func CreateRequestLogMiddleware(proxylog *logmon.Monitor) chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, prefix := range requestLogPathSkips {
				if strings.HasPrefix(r.URL.Path, prefix) {
					next.ServeHTTP(w, r)
					return
				}
			}

			start := time.Now()
			ip, method, path, ua := clientIP(r), r.Method, r.URL.Path, r.UserAgent()

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			line := fmt.Sprintf("%s %s %d %s %s",
				method, path, rec.status, logSize(rec.size), logDuration(time.Since(start)))
			if !isLoopbackIP(ip) {
				line += " " + ip
			}
			if agent := shortAgent(ua); agent != "" && agent != "browser" {
				line += " " + agent
			}

			switch {
			case rec.status >= 500:
				proxylog.Error(line)
			case rec.status >= 400:
				proxylog.Warn(line)
			case quietRequest(method, path, rec.status):
				proxylog.Debug(line)
			default:
				proxylog.Info(line)
			}
		})
	}
}
