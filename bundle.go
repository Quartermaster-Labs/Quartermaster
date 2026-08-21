package main

import (
	"flag"
	"os"
	"path/filepath"
)

// Packaged-install launch defaults — what start.cmd used to pass.
//
// A packaged quartermaster is meant to be double-clicked, so the exe fills in
// its own argv instead of a .cmd wrapper doing it. The launcher script was the
// only thing that knew the flag set, which meant a shortcut pointing at the exe
// (or a Run-key entry, or a user who just double-clicked the binary) got a bare
// "-config is required" instead of the app.
//
// Only the exe knows where it lives, and that is the whole trick: everything
// here resolves against the executable's directory, so the same defaults work
// from a Start-menu shortcut, the Startup folder, or a terminal in any CWD.
const (
	// The API listens on the LAN/tailnet; the dashboard and admin endpoints on
	// that same port stay loopback-only (see -admin-allow), so this is the
	// inference surface only.
	bundleListen = "0.0.0.0:1250"
	// The standalone playground app (per-user login + chat history).
	bundlePlayground = "0.0.0.0:8081"
	// The file whose presence next to the exe means "packaged install".
	bundleMarker = "quartermaster-generate.yaml"
)

// bundleRoot reports the packaged install directory, and whether this process
// is running inside one.
//
// The marker is config/quartermaster-generate.yaml: it is what the installer
// seeds and the setup wizard edits, so its presence next to the exe means "this
// is an install, not a `go build` output or a binary on someone's PATH". A dev
// build keeps the old behaviour — no flags, no server, "-config is required".
func bundleRoot() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	return bundleRootOf(exe)
}

// bundleRootOf is bundleRoot with the executable path passed in, so a test can
// point it at a layout on disk instead of at the test binary.
func bundleRootOf(exe string) (string, bool) {
	// EvalSymlinks so a symlinked exe resolves to the real install, not to the
	// link's directory (packagers and $HOME/bin links both do this).
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	root := filepath.Dir(exe)
	if _, err := os.Stat(filepath.Join(root, "config", bundleMarker)); err != nil {
		return "", false
	}
	return root, true
}

// applyBundleDefaults fills in the flags a double-click cannot pass.
//
// Per-flag, not all-or-nothing: `quartermaster.exe -listen :9000` still gets
// the packaged config paths and the playground, and only the port it asked for.
// A flag the user typed is never overwritten — flag.Visit reports exactly the
// ones that came from argv.
//
// Returns the install root when defaults were applied, "" otherwise.
func applyBundleDefaults() string {
	root, ok := bundleRoot()
	if !ok {
		return ""
	}
	applyBundleFlags(flag.CommandLine, root)

	// Same as start.cmd's `cd /d "%~dp0"`. Relative paths inside the config
	// (logs/, .cache/, a models root written as a sibling folder) are resolved
	// against the CWD, and a shortcut or Run-key entry sets one we did not pick.
	_ = os.Chdir(root)

	return root
}

// applyBundleFlags is the argv half of applyBundleDefaults, split out so it can
// be exercised against a throwaway FlagSet instead of the process's own.
func applyBundleFlags(fs *flag.FlagSet, root string) {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	set := func(name, value string) {
		if !given[name] {
			_ = fs.Set(name, value)
		}
	}

	set("config", filepath.Join(root, "config", "config.yaml"))
	set("generate", filepath.Join(root, "config", bundleMarker))
	set("listen", bundleListen)
	set("playground-port", bundlePlayground)
	set("watch-config", "true")

	// The window is the default face of a double-click, but never an override:
	// asking for -tray (the login launch) must not also open a window, which is
	// the whole point of starting minimised. -app implies -tray downstream.
	switch {
	case given["app"] || given["tray"]:
		// The user said which one; leave it alone.
	case trayOnlyRequested(fs):
		set("tray", "true")
	default:
		set("app", "true")
	}
}

// trayOnlyRequested honours the bare word `background`, which is how the
// pre-exe autostart shortcut asked start.cmd for a tray-only launch. Kept so an
// upgrade over an old install does not turn its silent login start into a
// window appearing at every logon.
func trayOnlyRequested(fs *flag.FlagSet) bool {
	for _, a := range fs.Args() {
		if a == "background" {
			return true
		}
	}
	return false
}
