package autogen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSidecarBackends_OverrideAndSiblings verifies a UI backend override wins
// over the generate file and that a blank sd/tts derives as a sibling of the
// UI-set llama exe (overlay must run BEFORE applyDefaults).
func TestSidecarBackends_OverrideAndSiblings(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "quartermaster-generate.yaml")
	if err := os.WriteFile(gen, []byte("settings:\n  serverExe: C:/cuda/llama-server.exe\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Point only llama-server at a Vulkan build; leave sd/tts blank.
	vulkan := "D:/vulkan/bin/llama-server.exe"
	if err := UpsertSidecarBackends(gen, BackendExes{ServerExe: vulkan}); err != nil {
		t.Fatal(err)
	}

	gf, err := LoadGenerateFile(gen, "")
	if err != nil {
		t.Fatal(err)
	}
	if gf.Settings.ServerExe != vulkan {
		t.Errorf("ServerExe = %q, want %q (UI override should win over file)", gf.Settings.ServerExe, vulkan)
	}
	wantSd := filepath.Join("D:/vulkan/bin", "sd-server")
	if gf.Settings.SdServerExe != wantSd {
		t.Errorf("SdServerExe = %q, want sibling %q", gf.Settings.SdServerExe, wantSd)
	}
	if !strings.HasPrefix(gf.Settings.TtsServerExe, filepath.Join("D:/vulkan/bin", "tts-server")) {
		t.Errorf("TtsServerExe = %q, want sibling of the vulkan exe", gf.Settings.TtsServerExe)
	}

	// Clearing all fields reverts to the generate file value.
	if err := UpsertSidecarBackends(gen, BackendExes{}); err != nil {
		t.Fatal(err)
	}
	gf, err = LoadGenerateFile(gen, "")
	if err != nil {
		t.Fatal(err)
	}
	if gf.Settings.ServerExe != "C:/cuda/llama-server.exe" {
		t.Errorf("after clear, ServerExe = %q, want the generate-file value", gf.Settings.ServerExe)
	}
}
