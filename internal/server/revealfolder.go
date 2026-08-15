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
		abs, err := filepath.Abs(want)
		if err != nil {
			return "", fmt.Errorf("invalid path %q", want)
		}
		rel, err := filepath.Rel(rootAbs, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("refusing to open %q — it is outside the models folder", want)
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

// openInFileManager hands one directory to the platform's file manager.
//
// Started, never waited on: Explorer exits non-zero on perfectly successful
// opens, and `xdg-open` can outlive the request handler. A failure to *spawn*
// is still reported — that is the case worth telling the user about (no
// xdg-open installed), whereas an exit code here carries no information.
func openInFileManager(dir string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	hideConsole(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Nothing waits on it, so reap the child rather than leaving a zombie.
	go func() { _ = cmd.Wait() }()
	return nil
}
