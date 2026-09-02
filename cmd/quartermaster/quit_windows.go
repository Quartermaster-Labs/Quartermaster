//go:build windows

package main

import (
	"os"
	"os/exec"
	"strconv"
)

// forceQuit kills a process tree that would not shut down on request.
//
// /T is the reason this shells out instead of using os.Process.Kill: the
// backends are CHILDREN of the server, they are what hold the files under
// bin\ open, and killing the parent alone leaves them running with the VRAM
// still allocated and the directory still undeletable. taskkill is the one
// call that walks the tree.
//
// Best effort and silent. This is the path after a graceful shutdown has
// already failed, during an uninstall: there is nobody to report to, and the
// caller re-checks the port either way.
func forceQuit(pid int) {
	if pid <= 0 || pid == os.Getpid() {
		return
	}
	_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
}
