//go:build !windows

package process

import (
	"os"
	"testing"
)

func TestProcessCommand_exeLibEnv_prependsExeDir(t *testing.T) {
	got := exeLibEnv([]string{"PATH=/usr/bin"}, "/opt/bundles/llama-b1/llama-server")
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(got), got)
	}
	want := "LD_LIBRARY_PATH=/opt/bundles/llama-b1"
	if got[0] != want {
		t.Fatalf("got %q, want %q", got[0], want)
	}
	if got[1] != "PATH=/usr/bin" {
		t.Fatalf("rest mangled: %v", got)
	}
}

func TestProcessCommand_exeLibEnv_preservesExistingValue(t *testing.T) {
	got := exeLibEnv(
		[]string{"LD_LIBRARY_PATH=/existing/dir", "A=1"},
		"/opt/bundles/llama-b1/llama-server",
	)
	want := "LD_LIBRARY_PATH=/opt/bundles/llama-b1" + string(os.PathListSeparator) + "/existing/dir"
	if got[0] != want {
		t.Fatalf("got %q, want %q", got[0], want)
	}
	if got[1] != "A=1" {
		t.Fatalf("rest mangled: %v", got)
	}
}

func TestProcessCommand_exeLibEnv_skipsDefaultLibDirs(t *testing.T) {
	env := []string{"PATH=/usr/bin"}
	for _, exe := range []string{
		"/usr/bin/llama-server",
		"/usr/lib/llama-server",
		"/lib64/llama-server",
		"/usr/local/lib/llama-server",
	} {
		if got := exeLibEnv(env, exe); len(got) != len(env) {
			t.Fatalf("%s: env changed: %v", exe, got)
		}
	}
}

func TestProcessCommand_exeLibEnv_skipsBareAndRelativeNames(t *testing.T) {
	env := []string{"PATH=/usr/bin"}
	for _, exe := range []string{"llama-server", "./llama-server", ""} {
		if got := exeLibEnv(env, exe); len(got) != len(env) {
			t.Fatalf("%q: env changed: %v", exe, got)
		}
	}
}
