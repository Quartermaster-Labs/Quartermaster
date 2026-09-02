package main

import (
	"net"
	"net/url"
	"sync"
	"time"
)

// serverWait bounds how long the first window waits for the listener to start
// accepting. Generous, because the cost of being wrong in each direction is not
// symmetric: waiting a moment longer than necessary is invisible, and navigating
// too early is a window that shows an error page (or nothing) and never retries.
const serverWait = 10 * time.Second

// appLauncher owns the native window LAZILY: nothing is built until someone
// actually asks for it.
//
// This is what makes the tray's "Open Quartermaster" work in a -tray start. The
// login launch (the Run-key entry the autostart setting writes) passes -tray and
// not -app, because starting with the system means starting minimised, not
// throwing a window at someone who has just logged in. Building the window
// eagerly there would cost a WebView2 host process for a window nobody may open;
// not building it at all -- what used to happen -- left the tray with no way
// into the app and no hook for /api/app/show, so an autostarted Quartermaster
// was browser-only until it was restarted by hand.
//
// Lazy gets both: nothing runs at login, and the first click gets a window.
type appLauncher struct {
	url     string
	onFail  func(error) // reports a window that could not be created
	browser func()      // fallback for a box that cannot host one

	// mu serialises open(), so two fast clicks cannot race two windows into
	// existence. Held across creation, which is why Open dispatches to a
	// goroutine: the tray's click loop must stay responsive to Exit while a
	// window is coming up.
	mu     sync.Mutex
	win    *appWindow
	broken bool
}

// Open raises the window, creating it on first use. Safe from any thread and
// never blocks the caller.
func (l *appLauncher) Open() { go l.open() }

func (l *appLauncher) open() {
	l.mu.Lock()
	defer l.mu.Unlock()

	switch {
	case l.broken:
		// Tried once, no runtime to host it. Retrying per click would re-pay
		// the COM timeout every time for the same answer.
		l.browser()
	case l.win != nil:
		l.win.Show()
	default:
		// The listener is started as a goroutine a few lines before this is
		// first called, so "the server is up" was an assumption, not a fact. It
		// is almost always true within a microsecond, and the almost is the
		// whole bug: a navigation that loses that race gets ERR_CONNECTION_
		// REFUSED, and WebView2 has no reload button and no retry, so the window
		// stays on that page until the process is restarted.
		waitForServer(l.url)
		win := startAppWindow(l.url)
		if err := win.Ready(); err != nil {
			l.broken = true
			l.onFail(err)
			l.browser()
			return
		}
		l.win = win
		win.Show()
	}
}

// waitForServer blocks until the loopback listener accepts a connection, or
// serverWait passes.
//
// A successful dial is proof enough: net/http is serving the moment its
// listener is bound, so there is no window where the socket accepts and the
// handler is not there yet. A timeout falls through and navigates anyway --
// whatever is wrong at that point, an error page the user can see beats a
// window that never opens.
func waitForServer(rawURL string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return
	}
	deadline := time.Now().Add(serverWait)
	for {
		// Dialing the host as written, not a resolved address: "localhost" can
		// resolve to ::1 first while the server holds 127.0.0.1, and the
		// dialer's own fallback across the resolved addresses is what makes
		// this probe agree with what the webview will do a moment later.
		c, err := net.DialTimeout("tcp", u.Host, time.Second)
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Close tears down a window that was actually created. A launcher nobody ever
// clicked has nothing to wait for, which is the common shutdown path for an
// autostarted instance.
func (l *appLauncher) Close() {
	l.mu.Lock()
	win := l.win
	l.mu.Unlock()
	if win != nil {
		win.Close()
	}
}
