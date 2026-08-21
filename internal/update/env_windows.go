//go:build windows

package update

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows/svc"
)

// supervised reports whether a service manager owns this process.
//
// svc.IsWindowsService covers a real SCM service. WinSW (what
// packaging/windows/quartermaster-service.xml drives) instead runs the exe as a
// plain child of its own service process, which that check cannot see — so the
// service definition also sets QM_SUPERVISED=1, and either signal is enough.
func supervised() bool {
	if os.Getenv("QM_SUPERVISED") != "" {
		return true
	}
	is, err := svc.IsWindowsService()
	return err == nil && is
}

// detachedAttr starts the replacement with no console and no process-group tie
// to this one, so it survives us exiting and does not flash a window. The
// release binary is linked -H=windowsgui and has no console of its own; without
// DETACHED_PROCESS it would inherit whatever console launched us and die with it.
func detachedAttr() *syscall.SysProcAttr {
	const (
		detachedProcess    = 0x00000008
		createNewProcGroup = 0x00000200
	)
	return &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcGroup,
		HideWindow:    true,
	}
}
