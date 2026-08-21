package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// blockedReason explains why a binary swap cannot work in this environment, or
// "" when it can. It is checked at every poll, not once at startup, because an
// install can move between these states (a directory's permissions change, a
// bind-mount appears) without the process restarting.
func blockedReason() string {
	if inContainer() {
		return "running in a container — update the image instead (the swapped binary would vanish with the container)"
	}
	exe, err := exePath()
	if err != nil {
		return "cannot locate the running executable"
	}
	if err := writable(filepath.Dir(exe)); err != nil {
		return fmt.Sprintf("the install directory (%s) is not writable — update via your package manager or reinstall", filepath.Dir(exe))
	}
	return ""
}

// inContainer detects Docker/Podman/containerd. Only meaningful on linux; the
// Windows and macOS builds are never containerised in practice.
func inContainer() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil { // podman
		return true
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		if strings.Contains(s, "docker") || strings.Contains(s, "containerd") || strings.Contains(s, "kubepods") {
			return true
		}
	}
	return false
}

// writable probes a directory by creating and removing a file in it. Checking
// the mode bits is not enough: an ACL, a read-only mount, or a different owner
// all produce a directory that looks writable and is not.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".qm-write-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// restartMode reports who brings the new binary up after a swap. Supervised
// installs return RestartManual: relaunching ourselves out from under systemd
// or a Windows service would leave an orphan the supervisor does not track,
// while the supervisor's own restart picks up the swapped binary correctly.
func restartMode() string {
	if supervised() {
		return RestartManual
	}
	return RestartAuto
}

// Spawn starts the (already swapped) binary at the same path, with the same
// arguments and working directory, detached from this process. Called by main
// AFTER teardown completes, so the replacement never races us for the listen
// sockets.
func Spawn() error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = filepath.Dir(exe)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	cmd.SysProcAttr = detachedAttr()
	return cmd.Start()
}
