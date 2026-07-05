//go:build !windows

package perf

import "os/exec"

func hideConsole(cmd *exec.Cmd) {}
