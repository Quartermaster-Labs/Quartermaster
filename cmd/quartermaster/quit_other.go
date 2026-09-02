//go:build !windows

package main

import (
	"os"
	"syscall"
)

// forceQuit kills a process that would not shut down on request.
//
// No tree walk here, unlike Windows: -quit exists for the Windows installer,
// and the unix packaging (the XDG desktop entries in cmd/quartermaster-setup)
// removes files the process is free to keep open. This is the honest version of
// the same last resort for anyone who runs the flag anyway, and the children
// are left to be reaped by init rather than signalled blind.
func forceQuit(pid int) {
	if pid <= 0 || pid == os.Getpid() {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(syscall.SIGKILL)
	}
}
