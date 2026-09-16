package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServer_writeAudioCppVoice(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "voices") // deliberately absent: it is created
	wav := []byte("RIFF....WAVEfmt ")
	req := audioCppVoiceReq{
		Name:    "radu",
		WavB64:  base64.StdEncoding.EncodeToString(wav),
		RefText: "the quick brown fox",
	}
	if err := writeAudioCppVoice(dir, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The voice list audio.cpp builds is the set of *.wav stems in this directory.
	got, err := os.ReadFile(filepath.Join(dir, "radu.wav"))
	if err != nil || string(got) != string(wav) {
		t.Fatalf("radu.wav = %q, %v; want the posted clip", got, err)
	}
	prompt, err := os.ReadFile(filepath.Join(dir, "prompt_text"))
	if err != nil || strings.TrimSpace(string(prompt)) != "radu|the quick brown fox" {
		t.Fatalf("prompt_text = %q, %v", prompt, err)
	}

	// Re-cloning the same name REPLACES its transcript. audio.cpp's reader takes
	// the first matching line, so an appended second line would never be read.
	req.RefText = "a second take"
	if err := writeAudioCppVoice(dir, req); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	prompt, _ = os.ReadFile(filepath.Join(dir, "prompt_text"))
	if strings.Count(string(prompt), "radu|") != 1 || !strings.Contains(string(prompt), "a second take") {
		t.Fatalf("prompt_text after re-clone = %q, want one updated line", prompt)
	}

	// A data URI is the shape a hand-written client reaches for first.
	req.Name = "uri"
	req.WavB64 = "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(wav)
	if err := writeAudioCppVoice(dir, req); err != nil {
		t.Fatalf("data uri: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "uri.wav")); err != nil {
		t.Errorf("data-uri clip not written: %v", err)
	}

	// Refusals: an empty clip, and a name that could climb out of the voice dir.
	if err := writeAudioCppVoice(dir, audioCppVoiceReq{Name: "empty", WavB64: ""}); err == nil {
		t.Error("empty clip accepted")
	}
	for _, bad := range []string{"", ".", "..", "../escape", `a\b`, "C:evil"} {
		if validVoiceName(bad) {
			t.Errorf("validVoiceName(%q) = true, want false", bad)
		}
	}
	if !validVoiceName("my voice 2") {
		t.Error("an ordinary name was refused")
	}
}

func TestServer_removePromptText(t *testing.T) {
	dir := t.TempDir()
	wav := base64.StdEncoding.EncodeToString([]byte("RIFF"))
	for _, n := range []string{"a", "b"} {
		if err := writeAudioCppVoice(dir, audioCppVoiceReq{Name: n, WavB64: wav, RefText: n + " text"}); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	if err := removePromptText(dir, "a"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "prompt_text"))
	if strings.Contains(string(prompt), "a|") || !strings.Contains(string(prompt), "b|") {
		t.Fatalf("prompt_text = %q, want only b", prompt)
	}
	// Removing the last entry takes the file with it rather than leaving an empty
	// mapping behind.
	if err := removePromptText(dir, "b"); err != nil {
		t.Fatalf("remove last: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "prompt_text")); !os.IsNotExist(err) {
		t.Errorf("prompt_text survived its last entry: %v", err)
	}
}

func TestServer_listAudioCppVoices(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "model.gguf") // a file: embeddings/ hangs off the dir
	voiceDir := filepath.Join(root, "voices")
	if err := os.MkdirAll(filepath.Join(root, "embeddings"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ethan.safetensors", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, "embeddings", n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wav := base64.StdEncoding.EncodeToString([]byte("RIFF"))
	for _, n := range []string{"radu", "aaa"} {
		if err := writeAudioCppVoice(voiceDir, audioCppVoiceReq{Name: n, WavB64: wav}); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}

	// The model path names a gguf, so embeddings/ is looked for beside it.
	got := listAudioCppVoices(voiceDir, filepath.Dir(model))
	want := []audioCppVoice{
		{Name: "aaa", Kind: "registered"},
		{Name: "ethan", Kind: "speaker"},
		{Name: "radu", Kind: "registered"},
	}
	if len(got) != len(want) {
		t.Fatalf("listAudioCppVoices = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("voice %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// prompt_text is not a voice, and a model with neither directory is not an
	// error - it is a model nobody has cloned onto yet.
	if v := listAudioCppVoices(filepath.Join(root, "nope"), filepath.Join(root, "nope")); len(v) != 0 {
		t.Errorf("absent dirs listed %+v", v)
	}
}
