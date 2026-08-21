//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// it would silently opt a user in to a task they left unticked. Every task
	// the script declares is decided here, and the ones left out are actively
	// removed by [InstallDelete] -- so re-running the wizard can take a
	// shortcut away again, not just add one.
	var tasks []string
	if c.StartMenu {
		tasks = append(tasks, "startmenu")
	}
	if c.DesktopIcon {
		tasks = append(tasks, "desktopicon")
	}
	if c.Autostart {
		tasks = append(tasks, "autostart")
	}

	logPath := filepath.Join(os.TempDir(), "quartermaster-install.log")
	log("running the installer")
	cmd := exec.Command(path,
		"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART",
		"/DIR="+c.Dir,
		"/TASKS="+strings.Join(tasks, ","),
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
// Straight to the exe, with no arguments: it reads config\quartermaster-generate.yaml
// next to itself and fills in its own flag set (both listeners, the config
// paths, -watch-config, -app). The old start.cmd hop existed only because that
// argv used to live in the script; duplicating it here would have meant two
// places to update.
func launch(dir string) error {
	// Quartermaster.exe is what the installer lays down; the lowercase
	// build-artifact name is what a dev-tree placeCopy brings along, and what an
	// install from before the rename still has.
	var exe string
	for _, name := range []string{"Quartermaster.exe", "quartermaster-windows-amd64.exe"} {
		if p := filepath.Join(dir, name); exists(p) {
			exe = p
			break
		}
	}
	if exe == "" {
		return fmt.Errorf("nothing to launch in %s", dir)
	}
	cmd := exec.Command(exe)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}
