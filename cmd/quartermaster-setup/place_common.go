package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// devCopyGlobs are the things a dev build brings along when there is no
// installer embedded. This is deliberately a short allow-list rather than a
// whole-directory copy: the setup binary lives in build/ next to every
// cross-compiled artifact, and copying that wholesale would put a linux binary
// and a 120MB update payload into the install directory.
var devCopyGlobs = []string{
	// filepath.Glob is case-sensitive even on Windows, so the installed name
	// needs its own entry -- "quartermaster-*.exe" will not match it.
	"Quartermaster.exe",
	"quartermaster-*.exe",
	"quartermaster-linux-*",
	"quartermaster-darwin-*",
	"start.cmd",
	"LICENSE*",
	"README*",
}

// placeCopy is the dev-tree stand-in for the installer: it copies the binaries
// sitting beside the setup executable into the install directory.
//
// It exists so the wizard can be exercised end to end from a plain "go build"
// without an Inno toolchain in the loop. It does not register an uninstaller or
// a Start Menu entry, which is exactly why it is not the shipping path.
func placeCopy(dir string, log func(string)) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating this program: %w", err)
	}
	src := filepath.Dir(self)

	var copied int
	for _, glob := range devCopyGlobs {
		matches, _ := filepath.Glob(filepath.Join(src, glob))
		for _, m := range matches {
			// Never copy ourselves in: the setup binary is not part of an
			// install, and on Windows the running image cannot be overwritten
			// on a later run anyway.
			if strings.EqualFold(m, self) {
				continue
			}
			if info, err := os.Stat(m); err != nil || info.IsDir() {
				continue
			}
			if err := copyFile(m, filepath.Join(dir, filepath.Base(m))); err != nil {
				return err
			}
			copied++
		}
	}

	// The example generate file is what internal/setup seeds the live config
	// from, so it has to land before the configuring phase runs.
	cfgDir := filepath.Join(dir, "config")
	examples, _ := filepath.Glob(filepath.Join(src, "config", "*.example.yaml"))
	if len(examples) > 0 {
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", cfgDir, err)
		}
		for _, m := range examples {
			if err := copyFile(m, filepath.Join(cfgDir, filepath.Base(m))); err != nil {
				return err
			}
			copied++
		}
	}

	if copied == 0 {
		return fmt.Errorf("found no quartermaster files next to %s", self)
	}
	log(fmt.Sprintf("copied %d files from %s", copied, src))
	return nil
}

// copyFile writes src to dst, preserving the executable bit that matters on
// unix. dst is written whole rather than in place: a partially overwritten
// binary is worse than one that was never touched.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}

	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}

// exists reports whether path is present. Any stat error is treated as absent:
// the callers are choosing between candidates, and a path they cannot stat is
// not a path they can run.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
