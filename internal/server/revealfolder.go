package server

// Open a downloaded model's folder in the OS file manager.
//
// The whole point of the model browser is that acquiring a model stops being
// out-of-band — but the moment something goes wrong (a stray `.part`, a repo
// autogen didn't pick up, a file to move), the user needs the folder itself,
// and reading a path out of the UI and pasting it into Explorer is the step
// this removes.
//
// It shells out on the SERVER, which is only sane because the dashboard is
// already loopback-gated (`adminChain`): the browser cannot open a local folder
// on its own, and quartermaster is a local tool whose UI and models tree live
// on one box. Two guards regardless: the target must resolve INSIDE the models
// root, and it must already exist. Nothing here interpolates request text into
// a shell — the path is passed as one argv element.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

type hubRevealReq struct {
	// Empty opens the models root itself. Anything else must be under it.
	Path string `json:"path"`
}

func (s *Server) handleAPIHubReveal(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimSpace(s.hubModelsRoot())
	if root == "" {
		shared.SendResponse(w, r, http.StatusNotImplemented, "the models folder is not configured in this build")
		return
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "cannot resolve the models folder: "+err.Error())
		return
	}

	var body hubRevealReq
	// A missing or empty body means "the models root" — that is the common
	// call, so it must not need a JSON payload to say nothing.
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	target, err := revealTarget(rootAbs, body.Path)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	// Refuse rather than spawn into the void. The UI reads the same signal from
	// /api/hub/sources and shows the in-app listing instead, so this is the
	// belt-and-braces path for a client that asked anyway. The 409 is the part
	// worth keying off: it means "not here", not "broken".
	if !canReveal(r) {
		shared.SendResponse(w, r, http.StatusConflict,
			"this server cannot open a file manager for you (no desktop session, or the dashboard is open from another machine)")
		return
	}
	if err := openInFileManager(target); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "could not open the folder: "+err.Error())
		return
	}
	writeJSON(w, map[string]string{"opened": target})
}

// revealTarget resolves a requested path to a directory inside rootAbs.
//
// A file resolves to its containing folder rather than failing: the download
// list holds repo directories, but a caller pointing at a `.gguf` means "show
// me where that lives", which is the same intent.
func revealTarget(rootAbs, want string) (string, error) {
	want = strings.TrimSpace(want)
	target := rootAbs
	if want != "" {
		// A relative path is relative to the MODELS ROOT, not to the process
		// CWD: the UI walks the tree by the `rel` this package hands it back
		// ("Qwen3-27B/gguf"), and resolving that against wherever the service
		// happened to be started from would land outside the root and be
		// refused. Absolute paths (what the download list carries) are
		// unaffected.
		abs := want
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(rootAbs, filepath.FromSlash(abs))
		}
		abs, err := filepath.Abs(abs)
		if err != nil {
			return "", fmt.Errorf("invalid path %q", want)
		}
		// Compare the REAL paths: a models tree is full of user-made links, and
		// a symlink under the root pointing at /etc passes a textual prefix
		// check while resolving straight out of the sandbox. Both sides are
		// resolved so the comparison stays apples-to-apples (on Windows this
		// also normalises the on-disk casing of both, which a raw Rel does
		// not). An unresolvable side falls back to its literal path, which can
		// only ever be stricter than the resolved one.
		rel, err := filepath.Rel(realPath(rootAbs), realPath(abs))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("refusing to open %q: it is outside the models folder", want)
		}
		target = abs
	}
	st, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("that folder is not on disk any more: %s", target)
	}
	if !st.IsDir() {
		target = filepath.Dir(target)
	}
	return target, nil
}

// realPath resolves symlinks, falling back to the input when it cannot (a path
// that does not exist yet, or a permission error partway down). Callers use it
// for containment checks only, where the fallback is the conservative answer.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// canReveal reports whether opening a folder on the SERVER could do this caller
// any good.
//
// Two ways it cannot. The browser may be on another machine — admin access can
// be widened past loopback (-admin-allow/-admin-open), and then a file manager
// on the server opens on a screen nobody is looking at. Or there may be no
// desktop at all: a container has no `xdg-open`, which is how issue #66
// surfaced. Either way the UI wants to know BEFORE it offers the button, so it
// can show the in-app listing instead of a dead end.
//
// r.RemoteAddr on purpose, not the X-Forwarded-For-aware clientIP: a reverse
// proxy on the box would otherwise make every remote browser look local.
func canReveal(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !isLoopbackIP(strings.Trim(host, "[]")) {
		return false
	}
	return fileManagerAvailable()
}

// fileManagerAvailable reports whether this OS has something to open a folder
// WITH. Windows and macOS always do (explorer and open ship with the OS); a
// Linux box only does if xdg-utils is installed, which a headless container
// deliberately is not.
func fileManagerAvailable() bool {
	switch runtime.GOOS {
	case "windows", "darwin":
		return true
	default:
		_, err := exec.LookPath("xdg-open")
		return err == nil
	}
}

// openInFileManager hands one directory to the platform's file manager.
//
// Started, never waited on: Explorer exits non-zero on perfectly successful
// opens, and `xdg-open` can outlive the request handler. A failure to *spawn*
// is still reported — that is the case worth telling the user about (no
// xdg-open installed), whereas an exit code here carries no information.
//
// Deliberately NOT run through hideConsole: all three of these are GUI
// launchers with no console of their own to hide, and on Windows the SW_HIDE
// in that STARTUPINFO is inherited by the window the shell opens on our
// behalf — explorer then "succeeds" while nothing appears on screen, which is
// exactly the silent no-op this button had.
func openInFileManager(dir string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// Explorer wants a native path; a request can carry forward slashes.
		cmd = exec.Command("explorer", filepath.FromSlash(dir))
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Nothing waits on it, so reap the child rather than leaving a zombie.
	go func() { _ = cmd.Wait() }()
	return nil
}
