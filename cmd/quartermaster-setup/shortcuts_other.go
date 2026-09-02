//go:build !windows && !linux

package main

import "github.com/quartermaster-labs/quartermaster/internal/setup"

// applyShortcuts has no implementation on darwin, and the wizard's UI does not
// offer the three options there, so this is never reached with anything set.
//
// The macOS equivalents are a real .app bundle (Info.plist, Contents/MacOS, a
// .icns) for the Launchpad entry and a ~/Library/LaunchAgents plist for the
// login start. Both are worth doing, and neither is the shape of a .desktop
// file, so they are a separate piece of work rather than a branch in here. See
// TODO.md.
func applyShortcuts(setup.Choices, func(string)) {}
