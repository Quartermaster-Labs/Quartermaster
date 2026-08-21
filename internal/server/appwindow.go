package server

import (
	"net"
	"net/http"
)

// showAppWindow, when set, raises the process's native desktop window. It is
// nil in every headless deployment (Docker, systemd, no -app), which is what
// GET /api/app/show reports 404 for.
//
// The only caller that matters is a SECOND launch of the binary: it finds the
// port already bound, hits this endpoint, and exits. Double-clicking the icon
// while Quartermaster is running then focuses the window that exists instead
// of failing with "address already in use". See appWindowShowPath.
const appWindowShowPath = "/api/app/show"

// SetShowAppHook wires the native window's raise action. Called from main once
// the window is up; never called when there is no window.
func (s *Server) SetShowAppHook(fn func()) { s.showAppWindow = fn }

// handleAPIAppShow raises the desktop window.
//
// Loopback is checked against r.RemoteAddr and NOTHING else. The forwarded-for
// headers the access log is happy to trust are attacker-controlled, and this
// endpoint pops a window onto someone's screen: a tailnet peer or a LAN host
// that can reach a non-loopback listener must not be able to do that, header or
// no header. A reverse proxy in front of Quartermaster therefore cannot reach
// this route, which is correct -- there is no window on the other end of one.
//
// The body carries the "quartermaster" marker the second instance verifies
// before it decides the port belongs to us and quietly exits. Some unrelated
// program answering 200 on that port must not be mistaken for a running
// Quartermaster, or a genuine port conflict would look like a successful
// hand-off and the user would get no window and no error.
func (s *Server) handleAPIAppShow(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if s.showAppWindow == nil {
		http.Error(w, "no desktop window", http.StatusNotFound)
		return
	}
	s.showAppWindow()
	writeJSON(w, map[string]any{"app": "quartermaster", "shown": true})
}
