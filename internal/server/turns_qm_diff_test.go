package server

import (
	"strings"
	"testing"
)

// TestQmCmdDiff pins the variant-vs-base launch-command diff the
// quartermaster_inspect model target reports: changed values, added and dropped
// flags, and a swapped backend executable.
func TestQmCmdDiff(t *testing.T) {
	base := `llama-server.exe -m D:/m.gguf -c 32768 -ngl 99 --flash-attn --n-cpu-moe 4`

	t.Run("changed, added and dropped flags", func(t *testing.T) {
		variant := `llama-server.exe -m D:/m.gguf -c 4096 -ngl 99 --n-cpu-moe 12 --ubatch-size 1024`
		got := strings.Join(qmCmdDiff(base, variant), " | ")
		for _, want := range []string{
			"-c 4096 (base: 32768)",
			"--n-cpu-moe 12 (base: 4)",
			"--ubatch-size 1024 (base: not set)",
			"--flash-attn dropped",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("diff missing %q\ngot: %s", want, got)
			}
		}
		if strings.Contains(got, "-ngl") || strings.Contains(got, "-m ") {
			t.Errorf("unchanged flags should not be reported\ngot: %s", got)
		}
	})

	t.Run("backend swap", func(t *testing.T) {
		variant := `C:/backends/vulkan/llama-server-vulkan.exe -m D:/m.gguf -c 32768 -ngl 99 --flash-attn --n-cpu-moe 4`
		got := qmCmdDiff(base, variant)
		if len(got) != 1 || !strings.HasPrefix(got[0], "backend exe: llama-server-vulkan.exe") {
			t.Errorf("backend swap diff = %v, want just the exe line", got)
		}
	})

	t.Run("identical commands diff to nothing", func(t *testing.T) {
		if got := qmCmdDiff(base, base); len(got) != 0 {
			t.Errorf("identical cmds = %v, want none", got)
		}
	})

	// "-ngl -1" — the value is a negative number, not the next flag. Reading it as
	// a flag would report a phantom "-1" deviation and lose -ngl's value.
	t.Run("negative values are values", func(t *testing.T) {
		flags, order := qmCmdFlags(`llama-server.exe -ngl -1 --port 8080`)
		if flags["-ngl"] != "-1" {
			t.Errorf("-ngl = %q, want -1", flags["-ngl"])
		}
		if len(order) != 2 {
			t.Errorf("flags = %v, want exactly -ngl and --port", order)
		}
	})

	// Chained --spec-type is emitted twice; both values must survive or the diff
	// claims a change that isn't there.
	t.Run("repeated flags accumulate", func(t *testing.T) {
		flags, _ := qmCmdFlags(`llama-server.exe --spec-type draft-mtp --spec-type ngram-map-k4v`)
		if flags["--spec-type"] != "draft-mtp ngram-map-k4v" {
			t.Errorf("--spec-type = %q", flags["--spec-type"])
		}
	})
}
