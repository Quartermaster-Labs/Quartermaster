//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/setup"
)

// The wizard has no way to show a broken desktop entry: a malformed file is
// simply ignored by the desktop, so the failure looks like "the shortcut did
// not appear" with nothing logged. These assert the two things that actually
// break it -- the Exec quoting and the autostart flag.

func TestSetup_EntryQuotesExecAndSetsPath(t *testing.T) {
	got := entry("/home/a b/qm/quartermaster-linux-amd64", "/home/a b/qm", "quartermaster", "")

	if !strings.Contains(got, `Exec="/home/a b/qm/quartermaster-linux-amd64"`) {
		t.Errorf("Exec is not quoted for a path with a space:\n%s", got)
	}
	if !strings.Contains(got, "Path=/home/a b/qm\n") {
		t.Errorf("Path= missing; a menu launch would run in the desktop's cwd:\n%s", got)
	}
	if !strings.Contains(got, "Icon=quartermaster\n") {
		t.Errorf("Icon= missing:\n%s", got)
	}
	if !strings.HasPrefix(got, "[Desktop Entry]\n") {
		t.Errorf("entry must open with the group header:\n%s", got)
	}
}

func TestSetup_EntryOmitsIconWhenUnwritten(t *testing.T) {
	// An Icon= naming a file that was never written draws a broken-image icon;
	// no Icon= at all draws the desktop's generic one.
	if got := entry("/opt/qm/qm", "/opt/qm", "", ""); strings.Contains(got, "Icon=") {
		t.Errorf("empty icon must omit the key entirely:\n%s", got)
	}
}

func TestSetup_EntryEscapesReservedExecChars(t *testing.T) {
	got := entry(`/home/u/q"m$x/qm`, "/home/u", "", "")
	if !strings.Contains(got, `Exec="/home/u/q\"m\$x/qm"`) {
		t.Errorf("reserved Exec characters not escaped:\n%s", got)
	}
}

func TestSetup_AutostartEntryStartsQuiet(t *testing.T) {
	// -tray off Windows means "run, show nothing". Without it the packaged
	// default is -app, which falls back to xdg-open: a browser tab at every
	// login.
	quiet := entry("/opt/qm/qm", "/opt/qm", "", "-tray")
	if !strings.Contains(quiet, `Exec="/opt/qm/qm" -tray`) {
		t.Errorf("autostart entry must pass -tray:\n%s", quiet)
	}
	if open := entry("/opt/qm/qm", "/opt/qm", "", ""); strings.Contains(open, "-tray") {
		t.Errorf("the menu entry must NOT be quiet:\n%s", open)
	}
}

func TestSetup_IconURIEscapes(t *testing.T) {
	// An ELF carries no icon, so this URI is the only thing standing between the
	// installed binary and a generic gear in the file manager. It is parsed as a
	// URI, and plenty of real home directories have a space in them.
	if got, want := iconURI("/home/a b/.local/share/icons/q.png"), "file:///home/a%20b/.local/share/icons/q.png"; got != want {
		t.Errorf("iconURI = %q, want %q", got, want)
	}
}

func TestSetup_ApplyShortcutsWritesAndRemoves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	// xdg-user-dir on the test machine would answer for the REAL user, so the
	// desktop path is the only one left to the fallback; assert against that.
	t.Setenv("PATH", "")

	dir := t.TempDir()
	exe := filepath.Join(dir, "quartermaster-linux-amd64")
	if err := os.WriteFile(exe, []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	menu := filepath.Join(home, "data", "applications", desktopFile)
	auto := filepath.Join(home, "cfg", "autostart", desktopFile)
	desk := filepath.Join(home, "Desktop", desktopFile)

	applyShortcuts(setup.Choices{Dir: dir, StartMenu: true, Autostart: true}, func(string) {})

	if !exists(menu) {
		t.Error("no application menu entry written")
	}
	if !exists(auto) {
		t.Error("no autostart entry written")
	}
	if exists(desk) {
		t.Error("desktop shortcut written without being asked for")
	}
	// Written unconditionally: every entry references it by theme name, and the
	// per-file icon needs a URI for it.
	if !exists(filepath.Join(home, "data", "icons", "hicolor", "512x512", "apps", "quartermaster.png")) {
		t.Error("icon not written into the hicolor theme")
	}

	// A re-run is the current state of the install, not an add-only list.
	applyShortcuts(setup.Choices{Dir: dir, StartMenu: true}, func(string) {})
	if !exists(menu) {
		t.Error("re-run removed the entry it was told to keep")
	}
	if exists(auto) {
		t.Error("unticking autostart must remove the entry, not leave it behind")
	}
}
