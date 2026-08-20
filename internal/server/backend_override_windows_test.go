//go:build windows

package server

import "testing"

// Windows drive paths survive the swap intact only because SanitizeCommand picks
// the Windows shlex dialect there; under POSIX rules `E:\llama-vk\...` would come
// back as `E:llama-vk...`. Hence a Windows-only test rather than a shared one.
func TestSwapCmdExe_windowsPaths(t *testing.T) {
	assertSwapCmdExe(t,
		`E:\llama-hip\llama-server.exe --port 5801 -m model.gguf`,
		`E:\llama-vk\llama-server.exe`,
		`E:\llama-vk\llama-server.exe --port 5801 -m model.gguf`, false)

	// Backslash-continued multiline: written as raw strings so the line
	// continuations sit next to the backslashes that are part of the paths.
	assertSwapCmdExe(t,
		`E:\llama-hip\llama-server.exe \
  --port 5801 \
  -ngl 99`,
		`E:\llama-vk\llama-server.exe`,
		`E:\llama-vk\llama-server.exe \
  --port 5801 \
  -ngl 99`, false)
}
