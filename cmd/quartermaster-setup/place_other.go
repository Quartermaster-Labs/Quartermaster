//go:build !windows

package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/quartermaster-labs/quartermaster/internal/setup"
	"github.com/quartermaster-labs/quartermaster/internal/update"
)

// serverBinary is the quartermaster server for THIS platform, embedded at build
// time -- the unix counterpart of the Inno installer the Windows wizard carries
// (place_windows.go). One wizard is built per GOOS/GOARCH, and
// packaging/windows/build-release.ps1 copies the matching server binary over the
// placeholder before each one, so the payload always belongs to the wizard
// around it.
//
// It exists because the release publishes setup programs only. Without a
// payload, a wizard downloaded on its own had nothing to install and fell back
// to fetching a release asset that is no longer there. Embedding is also what
// Windows already does, so "the setup program contains the application" is now
// true on every platform rather than on one.
//
// The committed file is a zero-byte placeholder: a dev build embeds that and
// falls through to the copy and download branches, which is what keeps
// `go build ./cmd/quartermaster-setup` a runnable end-to-end test of the wizard.
//
//go:embed payload/server
var serverBinary []byte

// minServerBytes tells a real payload from the placeholder. The server binary
// embeds the whole web UI and runs to tens of megabytes; nothing legitimate is
// anywhere near this small. Same guard, same reason, as minInstallerBytes on
// Windows.
const minServerBytes = 4 << 20

// place installs by copying what came in the box, or by fetching the release
// binary when nothing did.
//
// Windows gets an Inno package for the Add/Remove Programs entry and the
// uninstaller; unix has no equivalent worth embedding. A tarball unpacked into
// a directory IS the install convention here, and the in-app updater already
// replaces the binary in place, so a package manager would only add a second
// update path fighting the first.
//
// Three sources, in this order: a binary beside this program, the embedded
// payload, then the network.
//
// A sibling wins over the payload because a tarball's binary is the one the
// user chose to run, and because it keeps a dev tree exercising the same code
// path it always did. The download stays last as a fallback rather than being
// deleted: a wizard built without a payload (a dev build, or -SkipInstaller)
// still installs when the release happens to carry binaries, and the failure it
// reports names something a user can act on.
func place(c setup.Choices, log func(string)) error {
	if err := placeBinary(c, log); err != nil {
		return err
	}
	// After the binary, and never fatal: the shortcuts point AT the installed
	// file, and an install with no menu entry still works.
	applyShortcuts(c, log)
	return nil
}

func placeBinary(c setup.Choices, log func(string)) error {
	if hasSiblingBinary() {
		return placeCopy(c.Dir, log)
	}
	if len(serverBinary) >= minServerBytes {
		return placeEmbedded(c.Dir, log)
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

// placeEmbedded writes the payload into the install directory under the name
// the rest of the program expects: installedBinary globs quartermaster-<GOOS>-*
// and the in-app updater swaps by path, so the file has to carry GOOS and
// GOARCH rather than being called "quartermaster".
//
// Written to a temporary name in the DESTINATION directory and renamed, not
// streamed into place: rename is atomic within a filesystem, so an install
// interrupted half way leaves no half-written binary for launch() to find and
// run. Same reasoning as copyFile, and the same .part suffix.
func placeEmbedded(dir string, log func(string)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	dst := filepath.Join(dir, fmt.Sprintf("quartermaster-%s-%s", runtime.GOOS, runtime.GOARCH))
	tmp := dst + ".part"

	// 0o755 at creation, not a later chmod: the file is executable from the
	// moment it exists, so there is no window in which the rename could publish
	// a binary nobody can run.
	if err := os.WriteFile(tmp, serverBinary, 0o755); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	log(fmt.Sprintf("installed %s (%d MB)", filepath.Base(dst), len(serverBinary)>>20))
	return nil
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
