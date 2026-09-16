package server

import (
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// The collision this exists for, measured on the dev box: audio.cpp's
// Qwen3-TTS-12Hz-0.6B-Base gguf and the qwentts.cpp conversion of the same
// weights both report modelKey "qwen3-tts-12hz-0.6b-base" from their headers,
// and both are Q8_0, so the Models table fused them into one row whose editor
// could only offer the qwentts backends.
func TestServer_engineScopedKey(t *testing.T) {
	const key = "qwen3-tts-12hz-0.6b-base"

	qwentts := config.ModelConfig{Cmd: "tts-server --model D:/LLM/Models/QwenTTS/qwen-talker-0.6b-base-Q8_0.gguf"}
	audiocpp := config.ModelConfig{AudioCpp: config.AudioCppConfig{
		Family: "qwen3_tts",
		Path:   "D:/LLM/Models/audio-cpp/qwen3-tts-12hz-0.6b-base-q8_0.gguf",
		Task:   "tts",
	}}

	got, want := engineScopedKey(key, qwentts), key
	if got != want {
		t.Errorf("non-audio.cpp key = %q, want %q unchanged", got, want)
	}
	if a, b := engineScopedKey(key, audiocpp), engineScopedKey(key, qwentts); a == b {
		t.Errorf("both packagings scoped to %q; they must not share a row", a)
	}
	// An empty key means the table falls back to the id, and suffixing "" would
	// invent a group out of nothing.
	if got := engineScopedKey("", audiocpp); got != "" {
		t.Errorf("empty key = %q, want \"\"", got)
	}
}
