package main

import (
	"flag"
	"os"
	"path/filepath"
)

// Packaged-install launch defaults.
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
	applyBundlePaths(flag.CommandLine, root)

	// Anchor the process at the bundle root. Relative paths inside the config
	// (logs/, .cache/, a models root written as a sibling folder) are resolved
	// against the CWD, and a shortcut or Run-key entry sets one we did not pick.
	_ = os.Chdir(root)

	return root
}

// applyBundlePaths is the FIRST of the two bundle stages: the file paths and the
// window mode. Split from the network defaults below because -generate is
// resolved here, and the stored app settings (which can override the listen
// addresses) cannot be read until it is. Sequence in main():
//
//	applyBundlePaths -> LoadAppSettings(-generate) -> applyAppSettings ->
//	applyBundleNetDefaults (fills only what is still unset)
//
// Split out from applyBundleDefaults so it can be exercised against a throwaway
// FlagSet instead of the process's own.
func applyBundlePaths(fs *flag.FlagSet, root string) {
	set := bundleSetter(fs)

	set("config", filepath.Join(root, "config", "config.yaml"))
	set("generate", filepath.Join(root, "config", bundleMarker))
	set("watch-config", "true")

	// The window is the default face of a double-click, but never an override:
	// asking for -tray (the login launch) must not also open one, which is the
	// whole point of starting minimised. -app implies -tray downstream. Note
	// this decides only whether a window is OPENED at startup -- a -tray start
	// still has one a click away, built on demand (see appLauncher).
	if !flagGiven(fs, "app") && !flagGiven(fs, "tray") {
		set("app", "true")
	}
}

// applyBundleNetDefaults is the SECOND bundle stage: the packaged listen
// addresses. It runs last, so it fills in only what neither argv nor the stored
// app settings supplied — those are the whole point of the setting, and a
// default that overwrote them would make the dashboard's port field inert.
func applyBundleNetDefaults(fs *flag.FlagSet, root string) {
	_ = root // reserved: the addresses are constants today, not install-derived
	set := bundleSetter(fs)
	set("listen", bundleListen)
	set("playground-port", bundlePlayground)
}

// bundleSetter returns a "set unless already set" helper. The already-set test
// is taken ONCE, when the setter is built, and it covers flags set
// programmatically as well as from argv — which is exactly what makes the two
// stages compose: whatever stage one (or applyAppSettings) wrote counts as
// given by the time stage two runs.
func bundleSetter(fs *flag.FlagSet) func(name, value string) {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	return func(name, value string) {
		if !given[name] {
			_ = fs.Set(name, value)
		}
	}
}

// flagGiven reports whether a flag has been set (from argv or programmatically).
func flagGiven(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
