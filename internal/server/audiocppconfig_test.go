package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// redirectAudioCppDir points the materializer at a temp directory for the life
// of one test, so no test writes into the runner's real cache dir.
func redirectAudioCppDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := audioCppConfigDir
	audioCppConfigDir = func() string { return dir }
	t.Cleanup(func() { audioCppConfigDir = old })
	return dir
}

func audioCppTestServer(models map[string]config.ModelConfig) *Server {
	s := &Server{}
	s.cfg.Store(&config.Config{Models: models})
	return s
}

// The file the server reads at startup has to state the containment outright:
// one model, eager load, no idle unloads, no memory guard of its own.
func TestWriteAudioCppConfig_Shape(t *testing.T) {
	dir := redirectAudioCppDir(t)
	path, err := writeAudioCppConfig("moss-tts-nano", config.AudioCppConfig{
		Family: "moss_tts_nano",
		Path:   "/models/speech/moss-tts-nano-100m-q8_0.gguf",
		Task:   "tts",
	})
	if err != nil {
		t.Fatalf("writeAudioCppConfig: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("wrote to %q, want a file under %q", path, dir)
	}
	var doc audioCppServerConfig
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if doc.LazyLoad {
		t.Error("lazy_load = true: the listen socket would no longer mean the weights are resident")
	}
	if doc.MaxLoadedModels != 1 || doc.IdleUnloadMs != 0 || doc.MinFreeMemoryMB != 0 {
		t.Errorf("residency knobs = %+v, want 1/0/0 (audio.cpp must make no residency decisions)", doc)
	}
	if len(doc.Models) != 1 {
		t.Fatalf("models = %d, want exactly one", len(doc.Models))
	}
	m := doc.Models[0]
	// The id has to be OUR model id: that is what the router forwards in the
	// request's "model" field, and audio.cpp matches on it.
	if m.ID != "moss-tts-nano" || m.Family != "moss_tts_nano" || m.Task != "tts" {
		t.Errorf("model entry = %+v", m)
	}
	if !strings.HasSuffix(m.Path, "moss-tts-nano-100m-q8_0.gguf") {
		t.Errorf("path = %q", m.Path)
	}
	// No .tmp left behind: the write is temp+rename and the temp must be gone.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}
}

// Two ids that sanitize to the same readable string must not share one file:
// each spawn would otherwise launch whichever model wrote last.
func TestAudioCppConfigName_CollisionSafe(t *testing.T) {
	a := audioCppConfigName("speech/moss:nano")
	b := audioCppConfigName("speech_moss_nano")
	if a == b {
		t.Errorf("both ids map to %q", a)
	}
	for _, n := range []string{a, b} {
		if strings.ContainsAny(n, `/\:*?"<>|`) {
			t.Errorf("name %q is not filesystem-safe", n)
		}
	}
	if got := audioCppConfigName("x"); got != audioCppConfigName("x") {
		t.Error("name is not stable across calls")
	}
	// A pathological id still yields a bounded name.
	if n := audioCppConfigName(strings.Repeat("long-id-", 40)); len(n) > 100 {
		t.Errorf("name length %d, want bounded", len(n))
	}
}

func TestAudioCppSpawnArgs(t *testing.T) {
	redirectAudioCppDir(t)
	s := audioCppTestServer(map[string]config.ModelConfig{
		"tts-model": {AudioCpp: config.AudioCppConfig{Family: "moss_tts_nano", Path: "/m/a.gguf", Task: "tts"}},
		"llm-model": {},
		"pinned":    {AudioCpp: config.AudioCppConfig{Family: "vibevoice", Path: "/m/b.gguf"}},
	})

	args, err := s.audioCppSpawnArgs("tts-model", []string{"audiocpp_server", "--port", "9000"})
	if err != nil {
		t.Fatalf("audioCppSpawnArgs: %v", err)
	}
	if len(args) != 5 || args[3] != "--config" {
		t.Fatalf("args = %v, want --config appended", args)
	}
	if _, err := os.Stat(args[4]); err != nil {
		t.Errorf("--config points at no file: %v", err)
	}

	// Every other backend's spawn passes through untouched.
	in := []string{"llama-server", "--model", "x.gguf"}
	out, err := s.audioCppSpawnArgs("llm-model", in)
	if err != nil || len(out) != len(in) {
		t.Errorf("non-audio.cpp model rewritten: %v (%v)", out, err)
	}
	if out, err := s.audioCppSpawnArgs("not-in-config", in); err != nil || len(out) != len(in) {
		t.Errorf("unknown model rewritten: %v (%v)", out, err)
	}

	// A hand-written --config is the user taking the wheel: they may be pointing
	// at a multi-model file of their own, so it is left exactly as written.
	pinned := []string{"audiocpp_server", "--config", "/etc/mine.json"}
	out, err = s.audioCppSpawnArgs("pinned", pinned)
	if err != nil {
		t.Fatalf("audioCppSpawnArgs: %v", err)
	}
	if len(out) != len(pinned) || out[2] != "/etc/mine.json" {
		t.Errorf("overrode a user --config: %v", out)
	}
}

// An unwritable config directory must refuse the spawn rather than let the
// server come up with an empty models array and 400 every request.
func TestAudioCppSpawnArgs_WriteFailureRefusesSpawn(t *testing.T) {
	dir := redirectAudioCppDir(t)
	// A regular file where the directory should be: MkdirAll cannot succeed.
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	audioCppConfigDir = func() string { return blocker }
	s := audioCppTestServer(map[string]config.ModelConfig{
		"tts-model": {AudioCpp: config.AudioCppConfig{Family: "moss_tts_nano", Path: "/m/a.gguf"}},
	})
	if _, err := s.audioCppSpawnArgs("tts-model", []string{"audiocpp_server"}); err == nil {
		t.Error("spawn allowed despite an unwritable config dir")
	}
}
