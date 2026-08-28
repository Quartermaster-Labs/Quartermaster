//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/quartermaster-labs/quartermaster/internal/setup"
	"github.com/quartermaster-labs/quartermaster/internal/update"
)

// place installs by copying what came in the box, or by fetching the release
// binary when nothing did.
//
// Windows gets an Inno package for the Add/Remove Programs entry and the
// uninstaller; unix has no equivalent worth embedding. A tarball unpacked into
// a directory IS the install convention here, and the in-app updater already
// replaces the binary in place, so a package manager would only add a second
// update path fighting the first.
//
// The download branch is what makes the setup program worth publishing on its
// own for unix. Downloaded alone into ~/Downloads it has nothing beside it to
// copy, so it fetches the same verified asset the updater would, and the user
// gets the models-folder and backend steps from a single file rather than
// having to know that Settings can do the same job after the fact. Copying
// wins when both are present: it is faster, it works offline, and a tarball's
// binary is the one the user chose to run.
func place(c setup.Choices, log func(string)) error {
	if hasSiblingBinary() {
		return placeCopy(c.Dir, log)
	}
	log("no binary alongside this installer; fetching the latest release")
	// Not the request's context: Start runs on context.Background for the same
	// reason, and a page reload part way through a 40MB download must not
	// cancel it. update.FetchBinary applies its own download deadline.
	_, err := update.FetchBinary(context.Background(), update.Repo, c.Dir, func(done, total int64) {
		if total > 0 {
			log(fmt.Sprintf("downloading Quartermaster: %d%%", done*100/total))
		}
	}, log)
	return err
}

// hasSiblingBinary reports whether a server binary sits next to this program,
// which is the difference between an unpacked tarball (or a dev tree) and a
// lone setup download.
func hasSiblingBinary() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	dir := filepath.Dir(self)
	for _, glob := range binaryGlobs {
		matches, _ := filepath.Glob(filepath.Join(dir, glob))
		for _, m := range matches {
			// The wizard is not its own payload. It never matches binaryGlobs
			// by name (quartermaster-setup-linux-amd64 is not
			// quartermaster-linux-*), but the check is cheap and the failure
			// mode -- an install that copies the installer and launches
			// nothing -- is not.
			if m == self {
				continue
			}
			if info, err := os.Stat(m); err == nil && !info.IsDir() {
				return true
			}
		}
	}
	return false
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
