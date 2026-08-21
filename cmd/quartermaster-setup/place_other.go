//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/quartermaster-labs/quartermaster/internal/setup"
)

// place installs by copying, because there is no installer to drive.
//
// Windows gets an Inno package for the Add/Remove Programs entry and the
// uninstaller; unix has no equivalent worth embedding. A tarball unpacked into
// a directory IS the install convention here, and the in-app updater already
// replaces the binary in place, so a package manager would only add a second
// update path fighting the first.
func place(c setup.Choices, log func(string)) error {
	return placeCopy(c.Dir, log)
}

// launch starts the finished install, detached.
//
// Setsid matters: the wizard exits seconds later, and a child left in the same
// process group would take the terminal's next SIGINT with it -- or die with
// the ssh session that started the install.
func launch(dir string) error {
	exe, err := installedBinary(dir)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// installedBinary finds the server binary that place() put down. The name
// carries GOOS and GOARCH, so it is matched by glob rather than guessed.
func installedBinary(dir string) (string, error) {
	pattern := filepath.Join(dir, fmt.Sprintf("quartermaster-%s-*", runtime.GOOS))
	matches, _ := filepath.Glob(pattern)
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && !info.IsDir() {
			return m, nil
		}
	}
	if plain := filepath.Join(dir, "quartermaster"); exists(plain) {
		return plain, nil
	}
	return "", fmt.Errorf("nothing to launch in %s", dir)
}
