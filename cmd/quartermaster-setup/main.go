// Command quartermaster-setup is quartermaster's first-run wizard.
//
// It is a separate binary from the server, not a flag on it. The window it
// opens is a WebView2 control, and linking that into the binary that runs
// headless in Docker and under systemd is exactly the poisoning TODO.md's
// "Desktop app - second binary" decision exists to prevent. Everything
// platform-shaped lives here; internal/setup is a plain HTTP server that has
// never heard of a window.
//
// Shape of a run:
//
//	listen on 127.0.0.1:0  ->  open a window on it  ->  user answers  ->
//	place files  ->  write config  ->  fetch backends  ->  launch  ->  exit
//
// The window is chrome-less and sized like an app dialog, so the wizard reads
// as a native installer rather than as a browser pointed at localhost. When
// WebView2 is unavailable the same UI opens in the default browser; the wizard
// is fully usable either way, and nothing about the flow changes.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/setup"
)

// finishGrace lets the POST /api/setup/finish response reach the page before
// the process exits. Without it the window can be torn down mid-write and the
// user sees a network error on the very last click of a successful install.
const finishGrace = 400 * time.Millisecond

func main() {
	// The WebView2 message loop is thread-affine: it must run on the thread
	// that created the window, and Go will otherwise migrate this goroutine.
	runtime.LockOSThread()

	var (
		dir     = flag.String("dir", defaultInstallDir(), "default install directory")
		browser = flag.Bool("browser", false, "skip the native window and use the default browser")
		verbose = flag.Bool("v", false, "log progress to stderr")
	)
	flag.Parse()

	logf := func(string) {}
	if *verbose {
		lg := log.New(os.Stderr, "setup: ", log.Ltime)
		logf = func(s string) { lg.Println(s) }
	}

	wiz := setup.New(setup.Options{
		DefaultDir: *dir,
		Place:      place,
		Launch:     launch,
		Log:        logf,
	})

	url, stop, err := wiz.Listen()
	if err != nil {
		fatal("could not start the setup server: %v", err)
	}
	defer stop()
	logf("serving " + url)

	if !*browser {
		if err := runWindow(url, wiz.Done()); err == nil {
			time.Sleep(finishGrace)
			return
		} else {
			// Not fatal, and not even unusual: WebView2 ships with Windows 11
			// and with Edge on 10, but a stripped image or a Server SKU may not
			// have it. The browser shows the identical UI.
			logf("native window unavailable (" + err.Error() + "); falling back to the browser")
		}
	}

	if err := openBrowser(url); err != nil {
		fatal("could not open a window or a browser.\n\nOpen this address manually:\n%s\n\n%v", url, err)
	}
	<-wiz.Done()
	time.Sleep(finishGrace)
}

// defaultInstallDir is where the wizard proposes to install.
//
// On Windows this must match installer.iss's DefaultDirName: the embedded
// installer is what actually places the files, and proposing a different
// default here would mean the path shown in the wizard is not the path the
// install lands in whenever the two disagree.
func defaultInstallDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	switch runtime.GOOS {
	case "windows":
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			return filepath.Join(la, "Programs", "quartermaster")
		}
		return filepath.Join(home, "AppData", "Local", "Programs", "quartermaster")
	case "darwin":
		return filepath.Join(home, "Applications", "quartermaster")
	default:
		return filepath.Join(home, ".local", "share", "quartermaster")
	}
}

// fatal reports a startup failure the user can actually see.
//
// This binary is built -H=windowsgui so it has no console: a message written to
// stderr goes nowhere, and a setup program that exits silently is
// indistinguishable from one that crashed. showError puts it on screen.
func fatal(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	_, _ = io.WriteString(os.Stderr, msg+"\n")
	showError(msg)
	os.Exit(1)
}
