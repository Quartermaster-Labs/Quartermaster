//go:build windows

package perf

import (
	"os/exec"
	"syscall"
)

// hideConsole stops a console subprocess (nvidia-smi) from popping its own
// window when the parent is a -H=windowsgui binary with no console of its own.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
