//go:build !windows

package server

import "errors"

// Autostart is Windows-only for now; the dashboard hides the toggle when
// /api/autostart reports supported:false.
func autostartSupported() bool { return false }

func autostartRead() (string, error) { return "", nil }

func autostartWrite(string) error { return errors.New("autostart is only supported on Windows") }

func autostartClear() error { return nil }
