// Package nativewin is the desktop-window layer: it turns a go-webview2 window
// into the chrome-less app frame the Svelte UI draws its own title bar into,
// and exposes the OS dialogs a page cannot reach on its own.
//
// It is shared by the two programs that show a window -- the first-run wizard
// (cmd/quartermaster-setup) and the server's app window -- so the two cannot
// drift into looking like different applications.
//
// Everything real is behind //go:build windows. This file carries no build tag
// on purpose: without it `go build ./...` on Linux fails the package outright
// with "build constraints exclude all Go files", which would break the Docker
// build for a package it never imports.
package nativewin
