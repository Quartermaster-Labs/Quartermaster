// Package apppaths answers one question for the whole program: where does
// quartermaster keep its things on THIS machine.
//
// There are two answers, and which one applies is a property of the install, not
// of the caller:
//
//   - A packaged install owns its directory. Config, backends and cache all live
//     under the exe, which is what the Windows installer lays down, what the
//     setup wizard writes into, and what makes the folder self-contained enough
//     to zip up, back up or delete in one piece. BundleRoot detects it.
//   - Anything else -- a `go build` output, a release tarball unpacked into
//     ~/bin, a binary a package manager dropped on $PATH -- owns nothing. The
//     binary may sit on a read-only or root-owned path, so state goes to the
//     user's own per-platform directories instead.
//
// The second case is why this package exists. Before it, every runtime path was
// resolved against os.Executable(), which is correct for an install and wrong
// for a binary in /usr/local/bin: the first backend download or KV snapshot
// tried to write next to a file the user cannot write to.
//
// Go's os.User*Dir functions do the platform switch, and they are used rather
// than a hand-rolled XDG lookup because XDG is the right answer on exactly one
// of the three desktop platforms. On Linux they ARE the XDG lookup (the
// environment variable, else the spec's default); on macOS and Windows they give
// the native location instead.
package apppaths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// appDir is the per-user subdirectory name under every base directory below.
const appDir = "quartermaster"

// bundleMarker is the file whose presence next to the exe means "packaged
// install". It is the generate control file: the installer seeds it and the
// setup wizard edits it, so it exists in an install and in nothing else. A dev
// build that happens to sit next to a stray config.yaml is not an install.
var bundleMarker = filepath.Join("config", "quartermaster-generate.yaml")

// BundleRoot reports the packaged install directory, and whether this process is
// running inside one.
func BundleRoot() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	return BundleRootOf(exe)
}

// BundleRootOf is BundleRoot with the executable path passed in, so a test can
// point it at a layout on disk instead of at the test binary.
func BundleRootOf(exe string) (string, bool) {
	// EvalSymlinks so a symlinked exe resolves to the real install, not to the
	// link's directory (packagers and $HOME/bin links both do this).
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	root := filepath.Dir(exe)
	if _, err := os.Stat(filepath.Join(root, bundleMarker)); err != nil {
		return "", false
	}
	return root, true
}

// ConfigDir is where config.yaml and the generate control file live: the install
// directory's config/ folder, else the per-user config directory.
func ConfigDir() string {
	if root, ok := BundleRoot(); ok {
		return filepath.Join(root, "config")
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, appDir)
	}
	return filepath.Join(fallbackRoot(), "config")
}

// DataDir is where downloaded backends and anything else the program installs
// for itself live: the install directory, else the per-user data directory.
//
// Data and config are separate on Linux only, where the XDG spec separates them
// ($XDG_DATA_HOME vs $XDG_CONFIG_HOME) and users expect a several-gigabyte
// backend tree to stay out of ~/.config. Windows and macOS have no such split in
// practice, so both land in the one per-user application directory.
func DataDir() string {
	if root, ok := BundleRoot(); ok {
		return root
	}
	if runtime.GOOS == "linux" {
		if d := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); d != "" {
			return filepath.Join(d, appDir)
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", appDir)
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, appDir)
	}
	return fallbackRoot()
}

// CacheDir is where regenerable bulk data lives -- today the slot-KV snapshots,
// which are hundreds of megabytes and reconstructible from the models.
//
// Under an install it stays the .cache folder in the install directory, which is
// what makes "delete the folder" a complete uninstall. Elsewhere it is the
// platform cache directory, which is the one the OS is allowed to reclaim: that
// is exactly the right promise for a snapshot the server can rebuild.
func CacheDir() string {
	if root, ok := BundleRoot(); ok {
		return filepath.Join(root, ".cache")
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, appDir)
	}
	return filepath.Join(fallbackRoot(), ".cache")
}

// fallbackRoot is the last resort when the OS cannot name a home directory at
// all -- an empty HOME in a container, a service account with no profile. The
// temp directory is writable by definition, which is the only property the
// callers actually need to keep running.
func fallbackRoot() string {
	return filepath.Join(os.TempDir(), appDir)
}
