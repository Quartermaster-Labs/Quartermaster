package main

// The installer's way of getting Quartermaster out of its own directory.
//
// An uninstall used to end in "some components could not be removed", and the
// reason is the whole point of a tray app: closing the window hides it, the
// process keeps serving, and Inno arrives to delete an exe that is running. The
// Restart Manager pass Inno makes first cannot fix it either, because the only
// thing it knows how to ask a program to do is close its window.
//
// So the installer runs the binary it is about to delete with -quit, and this is
// what that does: ask the instance already on the port to shut down the way the
// tray's Exit does, wait for the port to go quiet, and only then give up. It is
// the same shape as handOffToRunningInstance -- a request to whoever owns the
// port, verified by a marker in the reply -- because the alternative (a pid file,
// a mutex) goes stale after a crash and would leave an uninstall blocked by a
// process that no longer exists.

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// How long to wait for a graceful shutdown before resorting to force. The
	// server evicts models and waits out in-flight requests on its way down, and
	// a large model releasing several GB of VRAM is not instant.
	quitGraceWait = 30 * time.Second

	// How long to wait after a force kill. Nothing is being asked politely at
	// that point, so this only covers the OS reaping the tree.
	quitForceWait = 5 * time.Second
)

// quitRunningInstance stops the Quartermaster serving on addr and reports a
// process exit code: 0 when the port is free (including when nothing was
// listening), 1 when something is still there.
//
// Never an error to find nothing running. The uninstaller calls this
// unconditionally, and the common case is an app that was already closed.
func quitRunningInstance(addr string, useTLS bool) int {
	pid, ok := requestQuit(addr, useTLS)
	if !ok && portFree(addr) {
		// Nothing answered and nothing is listening: already gone, or never
		// started. Either way there is nothing to wait for.
		return 0
	}
	if waitPortFree(addr, quitGraceWait) {
		return 0
	}
	// It answered and then did not go away -- a shutdown wedged on a subprocess
	// that will not exit. The directory still has to be deletable, so take the
	// tree down by the pid it reported. Nothing happens without one: killing by
	// image name would reach every other Quartermaster on the machine, which on
	// a developer's box is a different build entirely.
	if pid > 0 {
		forceQuit(pid)
		if waitPortFree(addr, quitForceWait) {
			return 0
		}
	}
	return 1
}

// requestQuit asks the instance on addr to shut down, returning its pid.
//
// The marker in the reply is checked for the same reason handOffToRunningInstance
// checks it: something unrelated may own that port, and a 200 from it is not
// permission to assume Quartermaster is going away.
func requestQuit(addr string, useTLS bool) (pid int, ok bool) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return 0, false
	}
	scheme := "http"
	client := &http.Client{Timeout: 5 * time.Second}
	if useTLS {
		scheme = "https"
		// Loopback, and almost always a self-signed certificate: there is no
		// name to verify and no network in between.
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	req, err := http.NewRequest(http.MethodPost, scheme+"://127.0.0.1:"+port+"/api/app/quit", strings.NewReader(""))
	if err != nil {
		return 0, false
	}
	// The header is the endpoint's cross-origin gate, not decoration: without
	// it the request is refused. See handleAPIAppQuit.
	req.Header.Set("X-Quartermaster-Quit", "1")

	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var out struct {
		App      string `json:"app"`
		Quitting bool   `json:"quitting"`
		PID      int    `json:"pid"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || json.Unmarshal(body, &out) != nil {
		return 0, false
	}
	if out.App != "quartermaster" || !out.Quitting {
		return 0, false
	}
	return out.PID, true
}

// portFree reports whether nothing is accepting on addr.
func portFree(addr string) bool {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return true
	}
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
	if err != nil {
		return true
	}
	c.Close()
	return false
}

// waitPortFree blocks until nothing is accepting on addr, or timeout passes.
//
// The port is the signal rather than the process handle because it is what the
// next thing to run cares about, and because it is true for exactly as long as
// the server can still be holding files open.
func waitPortFree(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if portFree(addr) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
