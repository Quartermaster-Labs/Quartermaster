//go:build !windows

package main

import "errors"

// appWindow has no native implementation off Windows. The desktop window is a
// WebView2 embedding, and the alternatives (WebKitGTK, WKWebView) drag CGO and
// a GTK toolchain into the binary that ships in the Docker image. Everything
// the window shows is served over HTTP anyway, so the fallback is the browser
// -- the same fallback the wizard already takes.
type appWindow struct{}

func startAppWindow(url string) *appWindow { return &appWindow{} }

func (aw *appWindow) Ready() error {
	return errors.New("the native window is Windows-only; open the dashboard in a browser")
}

func (aw *appWindow) Show()  {}
func (aw *appWindow) Close() {}
