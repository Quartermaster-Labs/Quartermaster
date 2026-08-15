package server

import (
	"net"
	"net/http"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/chain"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// Admin access control (fork).
//
// The inference API is meant to be reachable from the LAN/tailnet (that is the
// point of binding a non-loopback address), but the dashboard, ops and config
// editor routes carry no auth at all — API keys deliberately gate only the
// inference + discovery chains so enabling them can never lock the operator out
// of their own UI. Exposing the socket therefore has to be paired with a
// separate rule for the admin surface, otherwise every host on the network can
// rewrite launch args, unload models and read the logs.
//
// The split is by REMOTE ADDRESS rather than by listen address: the API and the
// dashboard share one port (e.g. :1250), so a per-listener flag cannot separate
// them. Admin routes answer only to loopback plus any operator-configured CIDRs;
// everything else — /v1/*, /health, and the playground app — is unaffected.
type adminAccess struct {
	localOnly bool
	allow     []*net.IPNet
}

// SetAdminAccess configures who may reach the admin surface. localOnly=false
// restores the legacy wide-open behaviour. allow holds extra IPs/CIDRs that are
// treated like loopback (e.g. "100.64.0.0/10" to admin over a tailnet).
//
// Call once at startup, before the listeners start serving: the value is read
// per request but never written again.
func (s *Server) SetAdminAccess(localOnly bool, allow []*net.IPNet) {
	s.admin = adminAccess{localOnly: localOnly, allow: allow}
}

// ParseAdminAllow parses a comma-separated list of IPs and CIDRs into networks.
// A bare IP becomes a /32 (or /128). Empty entries are skipped.
func ParseAdminAllow(list string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, part := range strings.Split(list, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(p); err == nil {
			out = append(out, n)
			continue
		}
		ip := net.ParseIP(p)
		if ip == nil {
			return nil, &net.ParseError{Type: "IP address or CIDR", Text: p}
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return out, nil
}

// adminAllowed reports whether a request may reach the admin surface.
//
// The listen address plays no part: every listener — playground included —
// shares this one mux, so a per-listener exemption would publish the whole admin
// surface on that port. The playground's own needs are handled by an explicit
// allowlist instead (requirePlaygroundOrAdmin), never by widening this.
func (s *Server) adminAllowed(r *http.Request) bool {
	if !s.admin.localOnly {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range s.admin.allow {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// requireAdmin is the middleware form of adminAllowed. Denied requests get a
// 403 that names the flag, so an operator who locked themselves out of a remote
// dashboard can see why from the response alone.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.adminAllowed(r) {
			s.proxylog.Warnf("denied admin request %s %s from %s (not loopback; use -admin-allow or -admin-open)",
				r.Method, r.URL.Path, r.RemoteAddr)
			shared.SendResponse(w, r, http.StatusForbidden,
				"forbidden: admin endpoints are restricted to this host (start quartermaster with -admin-allow <cidr> or -admin-open to widen)")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminMiddleware returns the guard as a chain.Middleware.
func (s *Server) adminMiddleware() chain.Middleware { return s.requireAdmin }

// requirePlaygroundOrAdmin guards the handful of admin-chain routes the
// playground app genuinely needs in the browser (see the pgChain registrations
// in routes()): the SPA bundle, the model-status stream, and the chat tools'
// fetch paths — web search, YouTube metadata, the image proxy, FX rates.
//
// A remote playground user reaches exactly these and nothing else; the config
// editor, backend installer, hub downloader, log stream and /upstream
// passthrough stay on adminChain and answer to this host only. loginRequired
// distinguishes the SPA bundle (must be served to a logged-out browser, else
// there is no login form) from the data routes behind it.
//
// Whoever adds the next /api route: the default is adminChain. Putting it here
// means "a stranger on the playground port may call this".
func (s *Server) requirePlaygroundOrAdmin(loginRequired bool) chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.adminAllowed(r) {
				next.ServeHTTP(w, r)
				return
			}
			if isPlaygroundRequest(r) && s.playground != nil {
				if !loginRequired || s.playground.userFromRequest(r) != "" {
					next.ServeHTTP(w, r)
					return
				}
				shared.SendResponse(w, r, http.StatusUnauthorized, "not logged in")
				return
			}
			s.proxylog.Warnf("denied admin request %s %s from %s (not loopback; use -admin-allow or -admin-open)",
				r.Method, r.URL.Path, r.RemoteAddr)
			shared.SendResponse(w, r, http.StatusForbidden,
				"forbidden: admin endpoints are restricted to this host (start quartermaster with -admin-allow <cidr> or -admin-open to widen)")
		})
	}
}
