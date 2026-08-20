package server

import "testing"

// Forward-slash exe paths are valid argv on both platforms, so the shared table
// exercises swapCmdExe's real job — replacing argv[0], keeping every other token
// and the line continuations. Backslash paths are Windows-only: POSIX shlex reads
// `\` as an escape, so they belong in backend_override_windows_test.go.
func TestSwapCmdExe(t *testing.T) {
	cases := []struct {
		name, cmd, newExe, want string
		wantErr                 bool
	}{
		{
			name:   "single line",
			cmd:    `/opt/llama-hip/llama-server --port 5801 -m model.gguf`,
			newExe: `/opt/llama-vk/llama-server`,
			want:   `/opt/llama-vk/llama-server --port 5801 -m model.gguf`,
		},
		{
			name:   "multiline continuation preserved",
			cmd:    "/opt/llama-hip/llama-server \\n  --port 5801 \\n  -ngl 99",
			newExe: "/opt/llama-vk/llama-server",
			want:   "/opt/llama-vk/llama-server \\n  --port 5801 \\n  -ngl 99",
		},
		{
			name:    "empty cmd errors",
			cmd:     "",
			newExe:  "x",
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertSwapCmdExe(t, c.cmd, c.newExe, c.want, c.wantErr)
		})
	}
}

func assertSwapCmdExe(t *testing.T, cmd, newExe, want string, wantErr bool) {
	t.Helper()
	got, err := swapCmdExe(cmd, newExe)
	if wantErr {
		if err == nil {
			t.Fatalf("expected error, got %q", got)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
