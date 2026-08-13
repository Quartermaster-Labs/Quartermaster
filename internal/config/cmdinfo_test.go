package config

import "testing"

func TestParseCmd_ModelPath(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"llama -m", "llama-server -m /models/a.gguf -c 4096", "/models/a.gguf"},
		{"long form", "tts-server --model /models/b.gguf", "/models/b.gguf"},
		{"equals form", "llama-server --model=/models/c.gguf", "/models/c.gguf"},
		{"sd diffusion model", "sd-server --diffusion-model /models/d.gguf --max-vram 20", "/models/d.gguf"},
		{"first flag wins", "sd-server --diffusion-model /models/d.gguf --model /models/clip.gguf", "/models/d.gguf"},
		{"ttscpp model-path", "tts-server --model-path /models/Kokoro_no_espeak_Q8.gguf --port 9000", "/models/Kokoro_no_espeak_Q8.gguf"},
		{"no model flag", "some-upstream --serve 8080", ""},
		{"flag with no value", "llama-server -c 4096 -m", ""},
		{"unparseable", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseCmd(tc.cmd).ModelPath; got != tc.want {
				t.Errorf("ModelPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCmdInfo_HasIsTokenExact(t *testing.T) {
	info := ParseCmd("llama-server -m /models/a.gguf --slot-save-path /kv --model-draft /models/d.gguf")
	if !info.Has("--slot-save-path") {
		t.Error("expected --slot-save-path detected")
	}
	// A raw strings.Contains(cmd, "--model") would match --model-draft; the token
	// scan must not.
	if info.Has("--model") {
		t.Error("--model-draft must not register as --model")
	}
	if info.Has("--codec") {
		t.Error("absent flag reported present")
	}
	if !ParseCmd("sd-server --lora-model-dir=/loras").Has("--lora-model-dir") {
		t.Error("--flag=value form should count as present")
	}
}

// The emitter renders one flag per line, so a value can sit on the line after
// its flag. The old sniffs tested for the contiguous substring
// "--spec-type draft-mtp" and silently went blind whenever that happened.
func TestCmdInfo_ValueAcrossLineBreak(t *testing.T) {
	cmd := "llama-server\n-m /models/a.gguf\n--spec-type\ndraft-mtp\n"
	info := ParseCmd(cmd)
	if !info.HasValue("draft-mtp", "--spec-type") {
		t.Errorf("line-wrapped --spec-type draft-mtp not detected, argv=%q", info.Argv)
	}
	if info.HasValue("draft-dflash", "--spec-type") {
		t.Error("draft-dflash falsely detected")
	}
}

func TestCmdInfo_ValuesRepeatable(t *testing.T) {
	info := ParseCmd("llama-server --spec-type draft-mtp --spec-type draft-dflash")
	got := info.Values("--spec-type")
	if len(got) != 2 || got[0] != "draft-mtp" || got[1] != "draft-dflash" {
		t.Errorf("Values = %q, want both spec types in order", got)
	}
	if !info.HasValue("draft-dflash", "--spec-type") {
		t.Error("second occurrence must be visible")
	}
}

func TestCmdInfo_ValueAndInt(t *testing.T) {
	info := ParseCmd("llama-server -c 32768 --port 9001 -ngl abc")
	if n, ok := info.Int("-c", "--ctx-size"); !ok || n != 32768 {
		t.Errorf("Int(-c) = %d,%v want 32768,true", n, ok)
	}
	if v, ok := info.Value("--port", "-port"); !ok || v != "9001" {
		t.Errorf("Value(--port) = %q,%v want 9001,true", v, ok)
	}
	if _, ok := info.Int("-ngl"); ok {
		t.Error("non-numeric value must not parse as int")
	}
	if _, ok := info.Value("--missing"); ok {
		t.Error("absent flag must report ok=false")
	}
}

func TestParseCmd_Memoized(t *testing.T) {
	cmd := "llama-server -m /models/memo.gguf -c 8192"
	if a, b := ParseCmd(cmd), ParseCmd(cmd); a != b {
		t.Error("repeat parse of the same command should return the cached CmdInfo")
	}
}

func TestParseCmd_UnparseableIsSafe(t *testing.T) {
	info := ParseCmd("") // SanitizeCommand rejects an empty command
	if info == nil {
		t.Fatal("ParseCmd must never return nil")
	}
	if info.Argv != nil {
		t.Errorf("expected nil argv for unparseable command, got %q", info.Argv)
	}
	if info.Has("-m") {
		t.Error("nil argv must not match any flag")
	}
	if _, ok := info.Int("-c"); ok {
		t.Error("nil argv must not yield ints")
	}
}
