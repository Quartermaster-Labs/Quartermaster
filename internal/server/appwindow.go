package server

import (
	"net"
	"net/http"
	"os"
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

// appQuitPath asks the process to shut down gracefully.
//
// This exists for the INSTALLER. Quartermaster is a tray app: closing its window
// hides it, so an uninstall (or an upgrade) walks into an exe that is still
// running, files that cannot be deleted, and the "some components could not be
// removed" dialog at the end. Inno's Restart Manager pass does not help, because
// what it asks a window to do -- close -- is precisely what this app is designed
// to survive.
//
// A kill would not do either: the spawned llama-server children are what hold
// the backend files open, and orphaning them leaves the VRAM allocated and the
// directory just as undeletable. Only the process itself knows how to take its
// children with it, so the uninstaller asks it to, through the same hook the
// auto-updater's restart uses.
const appQuitPath = "/api/app/quit"

// handleAPIAppQuit shuts the process down at the request of the installer.
//
// Three gates, and all three are load-bearing:
//
//   - loopback only, checked against RemoteAddr and no header, exactly as
//     handleAPIAppShow does and for a stronger version of the same reason: a
//     tailnet peer that can reach a LAN listener must not be able to stop the
//     server;
//   - POST, so it cannot be reached by an <img> or a top-level navigation;
//   - a custom request header, which forces a CORS preflight the browser will
//     not send cross-origin. Without it any web page the user happens to be
//     reading could shut Quartermaster down with a form post.
//
// The PID in the reply is the caller's escape hatch: an instance wedged in a
// shutdown it cannot finish still has to be removed before the uninstaller can
// delete its directory, and killing a named PID's tree is a far narrower act
// than killing every process that shares an image name -- which on a developer's
// box is somebody else's build.
func (s *Server) handleAPIAppQuit(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Header.Get("X-Quartermaster-Quit") == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if s.shutdownHook == nil {
		http.Error(w, "no shutdown hook", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"app": "quartermaster", "quitting": true, "pid": os.Getpid()})
	// After the response, and in a goroutine, because the hook tears down the
	// very server that has to deliver it. Graceful shutdown waits for in-flight
	// handlers, so this reply is on the wire before anything closes; calling it
	// inline would race the write against the listener going away.
	go s.shutdownHook()
}
