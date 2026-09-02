package main

import "sync"

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
