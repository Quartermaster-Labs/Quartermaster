package server

import (
	"strings"
	"testing"
)

func TestServer_AutostartExeOf(t *testing.T) {
	cases := map[string]string{
		`"C:\Program Files\qm\quartermaster.exe" -tray`: `C:\Program Files\qm\quartermaster.exe`,
		`C:\qm\quartermaster.exe -tray -config x.yaml`:  `C:\qm\quartermaster.exe`,
		`C:\qm\quartermaster.exe`:                       `C:\qm\quartermaster.exe`,
		``:                                              ``,
	}
	for cmd, want := range cases {
		if got := autostartExeOf(cmd); got != want {
			t.Errorf("autostartExeOf(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestServer_AutostartSameExe(t *testing.T) {
	if !sameExe(`C:\qm\Quartermaster.exe`, `C:\qm\quartermaster.exe`) {
		t.Error("expected case-insensitive match")
	}
	if sameExe(`C:\qm\quartermaster.exe`, `D:\other\quartermaster.exe`) {
		t.Error("different installs must not match")
	}
	if sameExe("", `C:\qm\quartermaster.exe`) {
		t.Error("empty owner must not match")
	}
}

func TestServer_AutostartQuoteArg(t *testing.T) {
	if got := quoteArg(`C:\Program Files\qm.exe`); got != `"C:\Program Files\qm.exe"` {
		t.Errorf("spaces must be quoted, got %q", got)
	}
	if got := quoteArg("-tray"); got != "-tray" {
		t.Errorf("plain arg must not be quoted, got %q", got)
	}
}

// The Run key launches with an arbitrary working directory, so the rebuilt
// command must carry an absolute exe and always end in -tray exactly once.
func TestServer_AutostartSelfCommand(t *testing.T) {
	exe, cmd, err := autostartSelfCommand()
	if err != nil {
		t.Fatal(err)
	}
	if exe == "" || cmd == "" {
		t.Fatalf("empty command: exe=%q cmd=%q", exe, cmd)
	}
	if autostartExeOf(cmd) != exe {
		t.Errorf("exe %q not recoverable from cmd %q", exe, cmd)
	}
	if n := strings.Count(cmd, "-tray"); n != 1 {
		t.Errorf("want exactly one -tray, got %d in %q", n, cmd)
	}
}
