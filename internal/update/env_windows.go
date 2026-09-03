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

const (
	detachedProcess    = 0x00000008
	createNewProcGroup = 0x00000200
	breakawayFromJob   = 0x01000000
)

// detachedAttr starts the replacement with no console, no process-group tie to
// this one, and OUT OF OUR JOB OBJECT, so it survives us exiting and does not
// flash a window.
//
// DETACHED_PROCESS: the release binary is linked -H=windowsgui and has no
// console of its own; without it the child would inherit whatever console
// launched us and die with it.
//
// CREATE_BREAKAWAY_FROM_JOB is the one that made an update look like it had
// simply quit the app. process.SetupTreeCleanup puts quartermaster itself in a
// job with KILL_ON_JOB_CLOSE so a crash reaps the backends it spawned; job
// membership is inherited, so the replacement joined that same job, and the
// moment we exited the OS killed it too. The relaunch always appeared to
// succeed, because cmd.Start() returns before Windows has anything to object to.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcGroup | breakawayFromJob,
		HideWindow:    true,
	}
}

// detachedAttrFallback drops the breakaway. CreateProcess fails outright with
// ERROR_ACCESS_DENIED when a job in the chain does not allow it (our own does,
// but an outer job we were launched into may not), and a replacement that is
// running and might be reaped beats one that was never started at all.
func detachedAttrFallback() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcGroup,
		HideWindow:    true,
	}
}
