//go:build !windows && !linux

package server

import "errors"

// pickFolder has no native dialog on this platform (e.g. darwin). Add an
// osascript-backed implementation when serving from one. The handler maps this
// error to a 501 so the UI can fall back to editing categoryRoots by hand.
func pickFolder() (string, error) {
	return "", errors.New("native folder picker not supported on this platform")
}

// pickFile has no native dialog on this platform. The handler maps this to a
// 501 so the UI falls back to the text field.
func pickFile() (string, error) {
	return "", errors.New("native file picker not supported on this platform")
}
