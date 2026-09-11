//go:build !windows

package main

import "os"

// redirectStderr is a no-op off Windows: there fd 2 is where the launcher put
// it (a terminal, or /dev/null under a service manager) and re-pointing it
// from here would fight whatever the operator chose. The app-level streams
// (slog, the log monitors) still reach the file; only a runtime `fatal
// error:` under a headless service stays on the console.
func redirectStderr(*os.File) {}
