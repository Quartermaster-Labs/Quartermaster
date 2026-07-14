//go:build windows

package server

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// pickerPrelude sets up a foreground native dialog. QM runs as a windowsgui/tray
// process with no console and is NOT the foreground app, so Windows' foreground
// lock keeps a plain ShowDialog window BEHIND the browser (it opens but you can't
// see it; the request then blocks until it's found & closed, so QM looks stuck).
// A TopMost owner alone doesn't beat the lock — the dialog itself must be forced
// foreground. So: a 1x1 off-screen (-4000,-4000, no taskbar = invisible) TopMost
// owner form, plus a WinForms Timer that ticks DURING the modal dialog loop and
// AttachThreadInput-bypasses the foreground lock to yank the dialog to the front.
// dialogPS then builds its dialog, calls ShowDialog($o), and emits the chosen
// path; runPicker appends `$o.Close()`.
const pickerPrelude = `Add-Type -AssemblyName System.Windows.Forms;` +
	`Add-Type -AssemblyName System.Drawing;` +
	"$sig=@'\n" +
	`[DllImport("user32.dll")] public static extern IntPtr GetActiveWindow();` + "\n" +
	`[DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();` + "\n" +
	`[DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);` + "\n" +
	`[DllImport("user32.dll")] public static extern bool BringWindowToTop(IntPtr h);` + "\n" +
	`[DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, IntPtr pid);` + "\n" +
	`[DllImport("kernel32.dll")] public static extern uint GetCurrentThreadId();` + "\n" +
	`[DllImport("user32.dll")] public static extern bool AttachThreadInput(uint a, uint b, bool f);` + "\n" +
	`[DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr h, IntPtr after, int x,int y,int cx,int cy,uint flags);` + "\n" +
	"'@\n" +
	`Add-Type -MemberDefinition $sig -Name U -Namespace W;` +
	`$o = New-Object System.Windows.Forms.Form;` +
	`$o.StartPosition='Manual'; $o.Location = New-Object System.Drawing.Point(-4000,-4000);` +
	`$o.Size = New-Object System.Drawing.Size(1,1); $o.TopMost=$true; $o.ShowInTaskbar=$false;` +
	`[void]$o.Show();` +
	`$script:n=0;` +
	`$ft = New-Object System.Windows.Forms.Timer; $ft.Interval=150;` +
	`$ft.Add_Tick({` +
	`  $script:n++;` +
	`  $h=[W.U]::GetActiveWindow();` +
	`  if($h -ne [IntPtr]::Zero -and $h -ne $o.Handle){` +
	`    $fg=[W.U]::GetForegroundWindow();` +
	`    $fth=[W.U]::GetWindowThreadProcessId($fg,[IntPtr]::Zero);` +
	`    $ct=[W.U]::GetCurrentThreadId();` +
	`    [void][W.U]::AttachThreadInput($fth,$ct,$true);` +
	`    [void][W.U]::BringWindowToTop($h);` +
	`    [void][W.U]::SetForegroundWindow($h);` +
	`    [void][W.U]::SetWindowPos($h,[IntPtr]-1,0,0,0,0,0x0003);` +
	`    [void][W.U]::AttachThreadInput($fth,$ct,$false);` +
	`    $ft.Stop();` +
	`  }` +
	`  if($script:n -ge 8){ $ft.Stop() }` +
	`});` +
	`$ft.Start();`

// runPicker runs a WinForms picker snippet with the foreground-owner prelude and a
// timeout, so a wedged dialog can never block a request goroutine forever. Returns
// the trimmed path ("" on cancel).
func runPicker(dialogPS string) (string, error) {
	// 10 min is generous for a human browsing; past it, kill powershell and fail
	// the request instead of leaking a stuck goroutine + hidden dialog.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-STA", "-NonInteractive", "-Command", pickerPrelude+dialogPS+`;$o.Close();`)
	// HideWindow/CREATE_NO_WINDOW suppress powershell's own console; the dialog is a
	// separate window and still shows.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// pickFolder opens the native Windows folder-browser dialog and returns the
// chosen absolute path ("" when the user cancels). ponytail: shells to PowerShell
// WinForms — the dialog pops on the SERVER host, which is the user's own machine
// for a local install; not meaningful for a remotely-served instance.
func pickFolder() (string, error) {
	return runPicker(
		`$d = New-Object System.Windows.Forms.FolderBrowserDialog;` +
			`if ($d.ShowDialog($o) -eq [System.Windows.Forms.DialogResult]::OK) { $d.SelectedPath }`)
}

// pickFile opens the native Windows open-file dialog (executable filter) and
// returns the chosen absolute path ("" when the user cancels). Same local-host
// caveat as pickFolder.
func pickFile() (string, error) {
	return runPicker(
		`$d = New-Object System.Windows.Forms.OpenFileDialog;` +
			`$d.Title = 'Select backend executable';` +
			`$d.Filter = 'Executables (*.exe)|*.exe|All files (*.*)|*.*';` +
			`if ($d.ShowDialog($o) -eq [System.Windows.Forms.DialogResult]::OK) { $d.FileName }`)
}
