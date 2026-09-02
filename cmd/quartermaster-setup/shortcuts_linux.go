//go:build linux

package main

// XDG desktop entries: the Linux answer to the Start Menu entry, desktop icon
// and login-start that place_windows.go hands to Inno as /TASKS=.
//
// Linux had none of the three. The wizard finished, launched the server once,
// and left no way back in: the next run meant remembering the install directory
// and typing the binary's name. That is the gap this closes, and it closes it
// without cgo -- a .desktop file is three writes and a cache refresh, so none of
// it touches the CGO_ENABLED=0 cross-compile matrix the way a real native
// window would.

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/setup"
)

// appIcon is the same mark the Windows build compiles into its resources.
//
// It is a COPY of packaging/icons/mark-512.png rather than a reference to it:
// //go:embed cannot reach above its own package directory, which is the same
// constraint that puts favicon.ico in cmd/quartermaster. Regenerating the mark
// (packaging/icons/gen.py) means refreshing this file too.
//
//go:embed icon/quartermaster.png
var appIcon []byte

// desktopFile is the basename used in all three locations. Reverse-DNS is the
// modern convention, but the file is also what a user sees in ~/Desktop, and
// "quartermaster.desktop" is the one they can recognise there.
const desktopFile = "quartermaster.desktop"

// applyShortcuts writes (or removes) the three desktop entries the user asked
// for.
//
// It never fails the install. Every path here is cosmetic -- the server is
// already on disk and about to be launched -- and a read-only ~/.local or an
// unusual desktop is not a reason to throw away a working install. Problems are
// logged instead, where the wizard's own log surfaces them.
//
// Re-running the wizard applies the choices as written: an unticked box REMOVES
// the entry it made rather than leaving it behind, which is the same semantic
// the Windows [InstallDelete] section gives, and the reason the UI can describe
// these as the current state of the install rather than an add-only list.
func applyShortcuts(c setup.Choices, log func(string)) {
	if !c.StartMenu && !c.DesktopIcon && !c.Autostart {
		// Nothing wanted and nothing to clean up on a first install; a re-run
		// that unticks everything still lands in the loop below.
		if !anyEntryExists() {
			return
		}
	}

	exe, err := installedBinary(c.Dir)
	if err != nil {
		log(fmt.Sprintf("skipping shortcuts: %v", err))
		return
	}

	icon := writeIcon(log)

	// The menu and desktop entries open the dashboard; the autostart one must
	// not. -tray off Windows means "run, show nothing" (cmd/quartermaster/
	// tray_other.go), whereas the packaged default is -app, which falls back to
	// xdg-open here. Logging in should not throw a browser tab in your face.
	open := entry(exe, c.Dir, icon, "")
	quiet := entry(exe, c.Dir, icon, "-tray")

	for _, e := range []struct {
		path string
		want bool
		body string
		what string
	}{
		{filepath.Join(dataHome(), "applications", desktopFile), c.StartMenu, open, "application menu entry"},
		{filepath.Join(desktopDir(), desktopFile), c.DesktopIcon, open, "desktop shortcut"},
		{filepath.Join(configHome(), "autostart", desktopFile), c.Autostart, quiet, "login startup entry"},
	} {
		if err := writeOrRemove(e.path, e.want, e.body); err != nil {
			log(fmt.Sprintf("could not write the %s: %v", e.what, err))
			continue
		}
		if !e.want {
			continue
		}
		log(fmt.Sprintf("created the %s", e.what))
	}

	refreshMenu()
}

// entry renders a desktop entry.
//
// Path= is what makes Exec= enough on its own: the server resolves its config,
// models and backends against its own directory (cmd/quartermaster/bundle.go),
// and a menu launch inherits the desktop's working directory rather than the
// install's.
//
// StartupNotify is false deliberately. There is no window to map on Linux, so a
// desktop that shows a launch spinner would spin until it timed out on every
// start.
func entry(exe, dir, icon, args string) string {
	cmd := quoteExec(exe)
	if args != "" {
		cmd += " " + args
	}
	// A missing Icon= line draws the desktop's generic placeholder, which is
	// tidier than an Icon= naming something that was never written.
	b := &strings.Builder{}
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Name=Quartermaster\n")
	b.WriteString("Comment=Local inference server: text, image, speech\n")
	b.WriteString("Exec=" + cmd + "\n")
	b.WriteString("Path=" + dir + "\n")
	if icon != "" {
		b.WriteString("Icon=" + icon + "\n")
	}
	b.WriteString("Terminal=false\n")
	b.WriteString("StartupNotify=false\n")
	b.WriteString("Categories=Development;Science;Utility;\n")
	b.WriteString("Keywords=LLM;AI;inference;llama;\n")
	return b.String()
}

// quoteExec quotes a path for the Exec key.
//
// The desktop-entry spec gives Exec its own quoting rules, and they are not the
// shell's: a quoted argument escapes backslash, double quote, backtick and
// dollar with a backslash. Install directories with a space in them are common
// enough (~/Applications/Quartermaster works, "My Tools" happens), and an
// unquoted Exec would silently launch the wrong thing.
func quoteExec(path string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return `"` + r.Replace(path) + `"`
}

// writeOrRemove makes the entry match what the user ticked.
//
// Written to a temporary file and renamed for the same reason placeEmbedded
// does it: a desktop scanning ~/.config/autostart while a half-written file is
// there gets a parse error at login, and the mode is set at creation so the
// file is never briefly non-executable.
func writeOrRemove(path string, want bool, body string) error {
	if !want {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	// 0o755, not 0o644: a .desktop file dropped on the desktop has to be
	// executable before most file managers will offer to run it at all.
	if err := os.WriteFile(tmp, []byte(body), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	trust(path)
	return nil
}

// trust marks a desktop file as trusted, which GNOME requires before it will
// run one from the desktop rather than showing "Untrusted application launcher".
//
// Best effort by design: gio is GLib's, so it is absent on KDE and on a
// minimal install, and every other desktop either ignores the attribute or does
// not need it. A failure here costs one extra click, not the shortcut.
func trust(path string) {
	gio, err := exec.LookPath("gio")
	if err != nil {
		return
	}
	_ = exec.Command(gio, "set", path, "metadata::trusted", "true").Run()
}

// writeIcon puts the mark where the icon theme will find it and returns the
// name to reference, or "" if it could not be written.
//
// hicolor/512x512/apps is the fallback theme every other theme inherits from,
// so an icon there is found whatever the user's theme is. The Icon= key then
// carries a NAME, not a path: that is what lets the theme scale it, and what
// makes the entry survive the icon being replaced by a different size later.
func writeIcon(log func(string)) string {
	dir := filepath.Join(dataHome(), "icons", "hicolor", "512x512", "apps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log(fmt.Sprintf("could not write the icon: %v", err))
		return ""
	}
	path := filepath.Join(dir, "quartermaster.png")
	if err := os.WriteFile(path, appIcon, 0o644); err != nil {
		log(fmt.Sprintf("could not write the icon: %v", err))
		return ""
	}
	return "quartermaster"
}

// refreshMenu nudges the desktop's caches.
//
// Both tools are optional and both are no-ops on a desktop that watches the
// directories itself (most do now). They are still worth running: without them
// a menu that only reads its cache shows nothing until the next login, which
// looks exactly like the shortcut having failed.
func refreshMenu() {
	if bin, err := exec.LookPath("update-desktop-database"); err == nil {
		_ = exec.Command(bin, filepath.Join(dataHome(), "applications")).Run()
	}
	if bin, err := exec.LookPath("gtk-update-icon-cache"); err == nil {
		_ = exec.Command(bin, "-q", "-t", "-f", filepath.Join(dataHome(), "icons", "hicolor")).Run()
	}
}

// anyEntryExists reports whether a previous run left something to clean up, so
// an all-unticked re-run can skip the work without skipping the removals.
func anyEntryExists() bool {
	for _, p := range []string{
		filepath.Join(dataHome(), "applications", desktopFile),
		filepath.Join(desktopDir(), desktopFile),
		filepath.Join(configHome(), "autostart", desktopFile),
	} {
		if exists(p) {
			return true
		}
	}
	return false
}

// dataHome and configHome resolve the XDG base directories, honouring the
// environment first: a user who moved them expects everything to follow, and
// the defaults are only defaults.
func dataHome() string {
	if d := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".local", "share")
}

func configHome() string {
	if d := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".config")
}

// desktopDir finds the desktop folder, which is NOT always ~/Desktop: it is
// localised (~/Escritorio, ~/Schreibtisch), and a user can point it anywhere in
// ~/.config/user-dirs.dirs. xdg-user-dir is the tool that reads that file, so
// ask it first and only guess when it is missing.
func desktopDir() string {
	if bin, err := exec.LookPath("xdg-user-dir"); err == nil {
		if out, err := exec.Command(bin, "DESKTOP").Output(); err == nil {
			if d := strings.TrimSpace(string(out)); d != "" {
				return d
			}
		}
	}
	return filepath.Join(homeDir(), "Desktop")
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.Getenv("HOME")
}
