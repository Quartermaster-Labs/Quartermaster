//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/quartermaster-labs/quartermaster/internal/setup"
)

// innoSetup is the Inno Setup installer, embedded at build time.
//
// The wizard drives it silently instead of replacing it, because the parts of
// installing that Inno does are the parts nobody wants to reimplement: the
// Add/Remove Programs record, the Start Menu group, the uninstaller, and the
// per-user registration that makes an upgrade replace in place. What Inno is
// NOT good at -- asking questions about hardware it cannot see -- is what moved
// into this wizard. See internal/setup for the split.
//
// packaging/windows/build-release.ps1 copies the compiled installer over the
// placeholder before this binary is built. A dev build embeds the placeholder,
// which is why placeInno checks the size rather than assuming.
//
//go:embed inno/setup.exe
var innoSetup []byte

// minInstallerBytes distinguishes a real installer from the committed
// placeholder. A compiled Inno setup is tens of megabytes; nothing legitimate
// is under this.
const minInstallerBytes = 512 << 10

// place puts the application's files into the chosen directory.
func place(c setup.Choices, log func(string)) error {
	if len(innoSetup) >= minInstallerBytes {
		return placeInno(c, log)
	}
	log("no installer embedded (dev build), copying files instead")
	return placeCopy(c.Dir, log)
}

// placeInno runs the embedded installer with every question already answered.
//
// /VERYSILENT suppresses the wizard AND the progress window, which is the whole
// point: the user is looking at our window, and a second progress dialog
// appearing over it would give the game away. /SUPPRESSMSGBOXES stops a
// silent run from blocking forever on a modal nobody can see.
func placeInno(c setup.Choices, log func(string)) error {
	tmp, err := os.CreateTemp("", "quartermaster-inno-*.exe")
	if err != nil {
		return fmt.Errorf("staging the installer: %w", err)
	}
	path := tmp.Name()
	defer os.Remove(path)

	if _, err := tmp.Write(innoSetup); err != nil {
		tmp.Close()
		return fmt.Errorf("staging the installer: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("staging the installer: %w", err)
	}

	// /TASKS is passed unconditionally, including empty. Inno's default when
	// the switch is absent is "whatever the script marks checked", so omitting
	// it would silently opt a user in to a task they left unticked.
	tasks := ""
	if c.Autostart {
		tasks = "autostart"
	}

	logPath := filepath.Join(os.TempDir(), "quartermaster-install.log")
	log("running the installer")
	cmd := exec.Command(path,
		"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART",
		"/DIR="+c.Dir,
		"/TASKS="+tasks,
		"/LOG="+logPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		// Inno's own log is the only place that says WHY, and it outlives this
		// process, so name it rather than swallowing a bare exit code.
		return fmt.Errorf("the installer failed (%w). Details: %s", err, logPath)
	}
	return nil
}

// launch starts the finished install.
//
// start.cmd is preferred over the exe: it carries the flag set the app is meant
// to run with (both listeners, the config paths, -watch-config, -tray), and
// duplicating that argv here would mean two places to update whenever it
// changes.
func launch(dir string) error {
	if script := filepath.Join(dir, "start.cmd"); exists(script) {
		cmd := exec.Command("cmd", "/c", "start.cmd")
		cmd.Dir = dir
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		return cmd.Start()
	}
	exe := filepath.Join(dir, "quartermaster-windows-amd64.exe")
	if !exists(exe) {
		return fmt.Errorf("nothing to launch in %s", dir)
	}
	cmd := exec.Command(exe)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
