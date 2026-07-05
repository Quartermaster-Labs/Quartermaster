//go:build !windows

package main

// runTray has no system-tray implementation off Windows (avoids the GTK/CGO
// dependency on Linux builds). It just blocks until shutdown.
func runTray(openURL string, onExit func(), exitChan <-chan struct{}) {
	<-exitChan
}
