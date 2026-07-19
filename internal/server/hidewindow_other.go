//go:build !windows

package server

import "os/exec"

// hideConsole is a no-op off Windows (no console-window concept).
func hideConsole(cmd *exec.Cmd) {}
