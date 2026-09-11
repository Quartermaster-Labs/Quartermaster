//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// redirectStderr points the process stderr HANDLE at the log file, so the
// Go runtime's last-resort output — the `fatal error:` dumps (concurrent map
// write, runtime out-of-memory) and the panic stacks of goroutines nobody
// recovers — lands in the log instead of the console a GUI-launched process
// does not have.
//
// Reassigning os.Stderr alone is NOT enough: the runtime writes fd 2
// directly, bypassing the Go-level variable, which is why both steps happen
// (the caller reassigns os.Stderr, this re-points the handle). Best effort:
// if it fails, the app-level streams still reach the file and only the
// unrecoverable dumps are lost — strictly better than today's void.
func redirectStderr(f *os.File) {
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
}
