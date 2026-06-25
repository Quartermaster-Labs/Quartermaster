//go:build linux

package server

import (
	"errors"
	"os/exec"
	"strings"
)

// pickFolder opens a native folder-selection dialog via zenity and returns the
// chosen absolute path ("" when the user cancels). ponytail: requires zenity on
// PATH (ships with GNOME; `apt install zenity` otherwise). The dialog opens on
// the server host's display — meaningful only for a local desktop install. Swap
// to kdialog if you run KDE-only.
func pickFolder() (string, error) {
	out, err := exec.Command("zenity", "--file-selection", "--directory", "--title=Select models folder").Output()
	if err != nil {
		// zenity exits 1 on cancel (no selection); treat as a clean no-op.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
