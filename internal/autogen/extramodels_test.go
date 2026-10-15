package autogen

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"gopkg.in/yaml.v3"
)

// generateFromYAML writes a generate file with the given settings tail and
// runs the full LoadGenerateFile -> Generate trip over an empty models root.
func generateFromYAML(t *testing.T, settingsTail string) string {
	t.Helper()
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	if err := os.MkdirAll(models, 0o755); err != nil {
		t.Fatal(err)
	}
	gen := filepath.Join(dir, "quartermaster-generate.yaml")
	yml := "settings:\n  modelsRoot: " + filepath.ToSlash(models) + "\n" + settingsTail
	if err := os.WriteFile(gen, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	gf, err := LoadGenerateFile(gen, "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(gf, "test")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The point of extraModels: a hand-written block survives a regen, loads
// through the real config loader, and lands in the exclusive swap group so the
// scheduler evicts around it like any discovered model.
func TestGenerate_ExtraModelEmittedAsWritten(t *testing.T) {
	out := generateFromYAML(t, ""+
		"  extraModels:\n"+
		"    strata-qwen:\n"+
		"      cmd: >\n"+
		"        strata-server --port ${PORT}\n"+
		"        --model D:/models/qwen\n"+
		"      proxy: http://127.0.0.1:${PORT}\n"+
		"      checkEndpoint: /v1/models\n"+
		"      estVramGB: 18.5\n"+
		"      ttl: 600\n")

	if !slices.Contains(modelKeys(out), "strata-qwen") {
		t.Fatalf("extra model not emitted:\n%s", out)
	}
	cfg, err := config.LoadConfigFromReader(strings.NewReader(out))
	if err != nil {
		t.Fatalf("generated config does not load: %v\n%s", err, out)
	}
	m, ok := cfg.Models["strata-qwen"]
	if !ok {
		t.Fatalf("loader dropped the extra model:\n%s", out)
	}
	if m.EstVramGB != 18.5 || m.CheckEndpoint != "/v1/models" || m.UnloadAfter != 600 {
		t.Fatalf("fields did not round-trip: %+v", m)
	}
	if !strings.Contains(m.Cmd, "strata-server --port") || !strings.Contains(m.Cmd, "--model D:/models/qwen") {
		t.Fatalf("cmd did not round-trip: %q", m.Cmd)
	}
	g, ok := cfg.Groups["exclusive"]
	if !ok || !slices.Contains(g.Members, "strata-qwen") || !g.Exclusive {
		t.Fatalf("extra model is not in the exclusive swap group: %+v", cfg.Groups)
	}
}

// A broken entry is skipped with a reason and never takes the rest of the
// config (or the good entry beside it) down with it.
func TestGenerate_ExtraModelInvalidSkipped(t *testing.T) {
	cases := map[string]struct{ block, reason string }{
		"typo": {
			"      cmd: strata-server --port ${PORT}\n      estVramGB: 4\n      chekEndpoint: /health\n",
			"chekEndpoint",
		},
		"no-cmd": {
			"      proxy: http://127.0.0.1:${PORT}\n      estVramGB: 4\n",
			"cmd is required",
		},
		"no-estimate": {
			"      cmd: strata-server --port ${PORT}\n",
			"set estVramGB",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out := generateFromYAML(t, ""+
				"  extraModels:\n"+
				"    bad:\n"+c.block+
				"    good:\n"+
				"      cmd: other-server --port ${PORT}\n"+
				"      estRamGB: 2\n")
			if slices.Contains(modelKeys(out), "bad") {
				t.Fatalf("invalid extra model was emitted:\n%s", out)
			}
			if !strings.Contains(out, `# SKIPPED extra model "bad": `) || !strings.Contains(out, c.reason) {
				t.Fatalf("no in-band reason containing %q:\n%s", c.reason, out)
			}
			cfg, err := config.LoadConfigFromReader(strings.NewReader(out))
			if err != nil {
				t.Fatalf("generated config does not load: %v\n%s", err, out)
			}
			if _, ok := cfg.Models["good"]; !ok {
				t.Fatalf("valid sibling entry was lost:\n%s", out)
			}
		})
	}
}

// A name a discovered model already serves keeps its owner.
func TestGenerate_ExtraModelNameClash(t *testing.T) {
	root := t.TempDir()
	seededGguf(t, root, "pub/repo/Model-7B-Q4_K_M.gguf")

	gf := GenerateFile{Settings: Settings{ModelsRoot: root}}
	gf.Settings.ExtraModels = mustNode(t, "model-7b-q4_k_m:\n  cmd: strata-server --port ${PORT}\n  estVramGB: 4\n")
	out, err := Generate(gf, "test")
	if err != nil {
		t.Fatal(err)
	}
	assertUniqueKeys(t, out)
	if strings.Contains(out, "strata-server") {
		t.Fatalf("clashing extra model was emitted:\n%s", out)
	}
	if !strings.Contains(out, `# SKIPPED extra model "model-7b-q4_k_m": that name is already served`) {
		t.Fatalf("clash left no in-band reason:\n%s", out)
	}
}

func mustNode(t *testing.T, src string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	return *doc.Content[0]
}
