package autogen

import (
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// intPtr / floatPtr keep the kitchen-sink override below readable.
func intPtr(i int) *int           { return &i }
func floatPtr(f float64) *float64 { return &f }

// Every flag the llama emitter can produce must be in the table. The fixture is
// a kitchen-sink override that turns on every optional flag, so a new emitter
// flag fails here until the table knows it (and therefore until custom-args
// suppression can handle it).
func TestFlagTable_CoversEveryEmittedFlag(t *testing.T) {
	yes := true
	s := Settings{
		ServerExe: "llama-server",
		Threads:   8,
		TtlSec:    600,
		SlotCache: SlotCacheSettings{Enable: true, Path: "C:/cache/slotkv"},
	}
	meta := Metadata{
		Architecture:     "qwen3moe",
		BlockCount:       32,
		IsMoE:            true,
		IsMTP:            true,
		FullAttnInterval: 4,
	}
	row := GgufRow{
		FullPath:   "/models/foo.gguf",
		MmprojPath: "/models/mmproj.gguf",
		DraftPath:  "/models/mtp-draft.gguf",
		DraftKind:  "mtp",
	}
	prof := profile{
		Name:         "foo",
		Target:       24,
		Overhead:     2,
		Ctx:          8192,
		IsLong:       true,
		TensorSplit:  []float64{2, 1},
		Vision:       true,
		CpuMmproj:    true,
		ReasoningFmt: "deepseek",
	}
	ov := &Override{
		Mmap: "off", DirectIo: true, // --load-mode dio
		FlashAttn: "on", Threads: 8, Parallel: 4, Ub: 512,
		KvInRam: true, KvK: "q8_0", KvV: "q8_0", KvKDraft: "q8_0", KvVDraft: "q8_0",
		Spec: "draft-mtp+ngram-mod", SpecDraftNMax: 3, SpecDraftNMin: 1, SpecDefault: true,
		SpecNgramSizeN: 4, SpecNgramSizeM: 8, SpecNgramMinHits: 2,
		ReasoningFmt: "deepseek", ReasoningBudget: 4096,
		CtxCheckpoints: intPtr(6), CheckpointMinStep: 2048,
		Dry: &yes, DryMultiplier: 0.9, DryBase: 1.5, DryAllowedLength: 4,
		Temp: floatPtr(0.7), TopK: intPtr(20), TopP: floatPtr(0.95), MinP: floatPtr(0.05), PresencePenalty: floatPtr(0.2),
		ThreadsBatch: 8, Prio: 1, NoOpOffload: true, NoRepack: true, CacheReuse: 64,
		CacheRamMB: 2048, LogVerbosity: 2, CacheIdleSlots: "on", SwaFull: true,
		ContextShift: "on", SlotPromptSimilarity: 0.5, RopeScaling: "yarn", RopeScale: 2,
		RopeFreqBase: 1e6, YarnOrigCtx: 4096, SplitMode: "layer", TensorSplit: "3,1",
		OverrideTensor: "exps=CPU", ChatTemplateFile: "/models/tmpl.jinja",
		MmprojFile: "/models/mmproj.gguf", SlotCache: &yes,
	}

	lines := buildCmdLines(s, meta, row, prof, 8192, 99, 8, "q8_0", "q8_0", true, ov)
	argv, err := config.SanitizeCommand(strings.Join(lines, " "))
	if err != nil {
		t.Fatalf("generated command does not split: %v", err)
	}
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if !strings.HasPrefix(tok, "-") {
			continue
		}
		name, _, inline := SplitFlagToken(tok)
		def, ok := LookupFlag(name)
		if !ok {
			t.Errorf("emitted flag %q is missing from llamaFlagTable (add it there and to the knob-clearing switch)", tok)
			continue
		}
		if def.Value && !inline {
			i++ // skip the value, which may itself start with '-' (negative samplers)
		}
	}
}

// The table must not contain a spelling twice, and every alias must resolve
// back to its own entry.
func TestFlagTable_NoDuplicateSpellings(t *testing.T) {
	seen := map[string]string{}
	for _, d := range llamaFlagTable {
		for _, name := range append([]string{d.Name}, d.Aliases...) {
			if prev, dup := seen[name]; dup {
				t.Errorf("flag %q appears in both %q and %q", name, prev, d.Name)
			}
			seen[name] = d.Name
		}
	}
	for _, d := range llamaFlagTable {
		for _, name := range append([]string{d.Name}, d.Aliases...) {
			got, ok := LookupFlag(name)
			if !ok || got.Name != d.Name {
				t.Errorf("LookupFlag(%q) = %q/%v, want %q", name, got.Name, ok, d.Name)
			}
		}
	}
}

func TestSplitFlagToken(t *testing.T) {
	cases := []struct {
		in       string
		name     string
		value    string
		hasValue bool
	}{
		{"-c", "-c", "", false},
		{"--ctx-size", "--ctx-size", "", false},
		{"--ctx-size=32768", "--ctx-size", "32768", true},
		{"-c=32768", "-c=32768", "", false}, // short '=' form is not a thing
		{"q8_0", "q8_0", "", false},
	}
	for _, c := range cases {
		name, value, has := SplitFlagToken(c.in)
		if name != c.name || value != c.value || has != c.hasValue {
			t.Errorf("SplitFlagToken(%q) = %q/%q/%v, want %q/%q/%v", c.in, name, value, has, c.name, c.value, c.hasValue)
		}
	}
}

func TestOwnedKnobs(t *testing.T) {
	tokens := []string{"--ctx-size", "32768", "--temp", "-0.5", "-cram", "2048", "--foo", "bar", "-m", "/x.gguf"}
	owned := OwnedKnobs(tokens)
	for _, want := range []string{"ctx", "temp", "cacheRam", "model"} {
		if !owned[want] {
			t.Errorf("knob %q should be owned (owned=%v)", want, owned)
		}
	}
	if len(owned) != 4 {
		t.Errorf("owned = %v, want exactly ctx/temp/cacheRam/model", owned)
	}
}

func TestClearOwnedFields(t *testing.T) {
	ov := &Override{
		Ctx: 8192, Mmap: "off", Mlock: true, DirectIo: true,
		Temp: floatPtr(0.5), TopK: intPtr(20), CacheRamMB: 4096, LogVerbosity: 3,
		PreserveThinking: boolPtr(false), CtxCheckpoints: intPtr(6),
	}
	cleared := ClearOwnedFields(ov, map[string]bool{"ctx": true, "loadMode": true, "cacheRam": true, "temp": true})
	if ov.Ctx != 0 || ov.Mmap != "" || ov.Mlock || ov.DirectIo || ov.CacheRamMB != 0 || ov.Temp != nil {
		t.Errorf("owned fields not cleared: %+v", ov)
	}
	if ov.TopK == nil || *ov.TopK != 20 || ov.LogVerbosity != 3 || ov.PreserveThinking == nil || ov.CtxCheckpoints == nil {
		t.Errorf("unowned fields must survive: %+v", ov)
	}
	want := map[string]bool{"ctx": true, "loadMode": true, "cacheRam": true, "temp": true}
	if len(cleared) != len(want) {
		t.Fatalf("cleared = %v, want %v", cleared, want)
	}
	for _, k := range cleared {
		if !want[k] {
			t.Errorf("unexpected cleared knob %q", k)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
