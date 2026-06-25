//go:build windows

package server

import (
	"os/exec"
	"strings"
)

// pickFolder opens the native Windows folder-browser dialog and returns the
// chosen absolute path ("" when the user cancels). ponytail: shells to
// PowerShell's WinForms FolderBrowserDialog — the dialog pops on the SERVER
// host, which is the user's own machine for a local install; not meaningful for
// a remotely-served instance.
func pickFolder() (string, error) {
	const ps = `Add-Type -AssemblyName System.Windows.Forms;` +
		`$d = New-Object System.Windows.Forms.FolderBrowserDialog;` +
		`if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { $d.SelectedPath }`
	// -STA: WinForms dialogs require a single-threaded apartment.
	out, err := exec.Command("powershell", "-NoProfile", "-STA", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
