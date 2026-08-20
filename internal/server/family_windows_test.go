//go:build windows

package server

import "testing"

// A Windows drive path only survives modelFamily's shlex split under the Windows
// dialect — POSIX rules eat the backslashes — so this case can't live in the
// shared table.
func TestServer_modelFamily_windowsPath(t *testing.T) {
	const cmd = `llama-server -m C:\Models\qwen.gguf -c 4096`
	if got := modelFamily(cmd); got != "C:/Models/qwen.gguf" {
		t.Fatalf("modelFamily(%q) = %q, want %q", cmd, got, "C:/Models/qwen.gguf")
	}
}
