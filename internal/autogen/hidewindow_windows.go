//go:build windows

package autogen

import (
	"os/exec"
	"syscall"
)

// hideConsole stops a backend probe (--list-devices, --help) from flashing
// its own console window when the parent is a -H=windowsgui binary with no
// console of its own. Every launch probes each installed backend, so without
// this the user sees a burst of windows open and close.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
