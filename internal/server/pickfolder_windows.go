//go:build windows

package server

import (
	"os/exec"
	"strings"
	"syscall"
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
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-NonInteractive", "-Command", ps)
	// HideWindow only suppresses powershell's own console; the FolderBrowserDialog
	// itself is a real window and still shows.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// pickFile opens the native Windows open-file dialog (executable filter) and
// returns the chosen absolute path ("" when the user cancels). Same
// local-host caveat as pickFolder.
func pickFile() (string, error) {
	const ps = `Add-Type -AssemblyName System.Windows.Forms;` +
		`$d = New-Object System.Windows.Forms.OpenFileDialog;` +
		`$d.Title = 'Select backend executable';` +
		`$d.Filter = 'Executables (*.exe)|*.exe|All files (*.*)|*.*';` +
		`if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { $d.FileName }`
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-NonInteractive", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
