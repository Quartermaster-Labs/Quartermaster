package server

import "testing"

func TestSwapCmdExe(t *testing.T) {
	cases := []struct {
		name, cmd, newExe, want string
		wantErr                 bool
	}{
		{
			name:   "single line",
			cmd:    `E:\llama-hip\llama-server.exe --port 5801 -m model.gguf`,
			newExe: `E:\llama-vk\llama-server.exe`,
			want:   `E:\llama-vk\llama-server.exe --port 5801 -m model.gguf`,
		},
		{
			name:   "multiline continuation preserved",
			cmd:    "E:\\llama-hip\\llama-server.exe \\\n  --port 5801 \\\n  -ngl 99",
			newExe: "E:\\llama-vk\\llama-server.exe",
			want:   "E:\\llama-vk\\llama-server.exe \\\n  --port 5801 \\\n  -ngl 99",
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
			got, err := swapCmdExe(c.cmd, c.newExe)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}
