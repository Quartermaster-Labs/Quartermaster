//go:build !windows

package update

import (
	"os"
	"syscall"
)

// supervised reports whether an init system owns this process.
//
// systemd sets INVOCATION_ID in every unit it starts (and JOURNAL_STREAM
// whenever the unit's output goes to the journal, which is the default), so no
// unit-file change is needed to detect an existing install. QM_SUPERVISED is
// the manual escape hatch for anything else that supervises us — runit, s6, a
// container-less orchestrator, an operator who simply wants the app to never
// restart itself.
func supervised() bool {
	if os.Getenv("QM_SUPERVISED") != "" {
		return true
	}
	if os.Getenv("INVOCATION_ID") != "" || os.Getenv("JOURNAL_STREAM") != "" {
		return true
	}
	return false
}

// detachedAttr puts the replacement in its own session so it is not killed with
// this process's group and does not hold the terminal.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
