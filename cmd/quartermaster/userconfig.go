package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

// Bare-binary launch defaults: the per-user config directory.
//
// This is the third and last tier of the startup layering, below argv and below
// the packaged-install defaults in bundle.go:
//
//	argv  >  stored app settings  >  bundle (exe-relative)  >  user config dir
//
// It exists for the install shape neither of the others covers: a binary that
// arrived without the setup wizard -- built from source, unpacked from a
// tarball, dropped on $PATH by a package manager -- and is run with no flags.
// That used to be a bare "-config is required", which is a strange first
// impression for a program whose whole point is that it configures itself.
//
// The binary stays wherever it is; only the state moves. See userConfigRoot for
// why this is not literally XDG.
const (
	userConfigName   = "config.yaml"
	userGenerateName = "quartermaster-generate.yaml"
)

// userConfigRoot reports the per-user config directory for quartermaster.
//
// os.UserConfigDir rather than a hand-rolled $XDG_CONFIG_HOME: on Linux it IS
// the XDG lookup (the variable, else ~/.config), and on the other two desktop
// platforms it gives the native answer instead -- ~/Library/Application Support
// on macOS, %AppData% on Windows -- which is what a user of those platforms
// actually expects. Spelling XDG out by hand would be right on one platform and
// wrong on two.
func userConfigRoot() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "quartermaster"), nil
}

// applyUserConfigDefaults fills in -config and -generate from the per-user
// config directory. Returns the directory when the defaults were applied, ""
// when they were not.
//
// create says whether it may bring the directory into existence: true for a
// start, which is the whole point, and false for -quit, which stops a server
// rather than starting one and must not leave a config directory behind as a
// side effect. A non-creating call still resolves the paths when the directory
// is already there, which is what lets -quit read the stored listen address and
// find the instance it was asked to stop.
//
// The two flags default as a PAIR, and only when argv named neither. That is
// the safety rule of the whole change: -generate names an input control file
// and -config names the output that autogen OVERWRITES, so defaulting
// -generate next to a config the user named themselves would quietly clobber a
// hand-written config on the next boot. Own both paths or neither.
//
// A user who wants autogen against their own config still has the explicit
// form, which is unchanged: -config mine.yaml -generate mine-generate.yaml.
func applyUserConfigDefaults(fs *flag.FlagSet, argvGiven map[string]bool, root string, create bool) (string, error) {
	if argvGiven["config"] || argvGiven["generate"] {
		return "", nil
	}
	genPath := filepath.Join(root, userGenerateName)
	if create {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", fmt.Errorf("creating %s: %w", root, err)
		}
		if err := seedGenerateFile(genPath); err != nil {
			return "", err
		}
	} else if _, err := os.Stat(root); err != nil {
		return "", nil
	}
	set := bundleSetter(fs)
	set("config", filepath.Join(root, userConfigName))
	set("generate", genPath)
	return root, nil
}

// seedGenerateFile creates a generate control file at path if none is there.
//
// autogen.EnsureConfig fails hard on a missing control file, so a first launch
// cannot leave this to the user: with nothing seeded, the defaults above would
// name a path that does not exist and the process would exit on the very run
// that is supposed to need no setup.
//
// The example that ships next to the binary is preferred purely for its
// comments -- the knobs behave identically either way, since applyDefaults
// fills every unset one. A packaged layout keeps it under config/, a source
// tree next to the binary; both are checked before falling back to the minimal
// form.
func seedGenerateFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	body := []byte(autogen.MinimalGenerateFile)
	if b, err := os.ReadFile(exampleGeneratePath()); err == nil && len(b) > 0 {
		body = b
	}
	return os.WriteFile(path, body, 0o644)
}

// exampleGeneratePath locates the annotated example shipped alongside the
// binary, if one is. Returns a path that may not exist; the caller treats a
// read failure as "no example".
func exampleGeneratePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	const name = "quartermaster-generate.example.yaml"
	for _, p := range []string{
		filepath.Join(dir, "config", name),
		filepath.Join(dir, name),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
