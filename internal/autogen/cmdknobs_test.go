package autogen

import (
	"math"
	"strings"
	"testing"
)

// boolptr is a tiny helper for the *bool DRY toggle.
func boolptr(b bool) *bool { return &b }

func joinCmd(s Settings, ov *Override) string {
	s.applyDefaults()
	prof := profile{Name: "t", Ctx: 8192}
	return strings.Join(
		buildCmdLines(s, Metadata{}, GgufRow{FullPath: "/m.gguf"}, prof, 8192, 99, 0, "q8_0", "q8_0", false, ov),
		" ",
	)
}

// DRY defaults off; Dry=true emits the canonical values; custom values replace
// the defaults independently.
func TestBuildCmdLines_dry(t *testing.T) {
	s := Settings{}

	if got := joinCmd(s, &Override{}); strings.Contains(got, "--dry-") {
		t.Fatalf("DRY should be off by default: %s", got)
	}
	if got := joinCmd(s, &Override{Dry: boolptr(true)}); !strings.Contains(got, "--dry-multiplier 0.8 --dry-base 1.75 --dry-allowed-length 3") {
		t.Fatalf("Dry=true should emit canonical DRY flags: %s", got)
	}
	got := joinCmd(s, &Override{Dry: boolptr(true), DryMultiplier: 0.5, DryBase: 2, DryAllowedLength: 4})
	if !strings.Contains(got, "--dry-multiplier 0.5 --dry-base 2 --dry-allowed-length 4") {
		t.Fatalf("custom DRY values not emitted: %s", got)
	}
}

func f64ptr(f float64) *float64 { return &f }
func intptr(i int) *int         { return &i }

// joinCmdMeta is joinCmd with the gguf metadata under test (the sampler baseline
// is arch-derived, so it can't use the zero Metadata).
func joinCmdMeta(s Settings, meta Metadata, ov *Override) string {
	s.applyDefaults()
	prof := profile{Name: "t", Ctx: 8192}
	return strings.Join(
		buildCmdLines(s, meta, GgufRow{FullPath: "/m.gguf"}, prof, 8192, 99, 0, "q8_0", "q8_0", false, ov),
		" ",
	)
}

// No sampler flags for an unknown arch; the Qwen3 family gets the top-k/min-p
// baseline its model cards specify (neither is reachable through the OpenAI API,
// so the launch flag is the only thing that can set them).
func TestBuildCmdLines_samplerBaseline(t *testing.T) {
	s := Settings{}

	for _, flag := range []string{"--temp", "--top-k", "--top-p", "--min-p", "--presence-penalty"} {
		if got := joinCmdMeta(s, Metadata{Architecture: "llama"}, &Override{}); strings.Contains(got, flag) {
			t.Fatalf("unknown arch should emit no sampler flags, got %s in: %s", flag, got)
		}
	}
	for _, arch := range []string{"qwen3", "qwen3moe", "qwen35", "qwen35moe"} {
		got := joinCmdMeta(s, Metadata{Architecture: arch}, &Override{})
		if !strings.Contains(got, "--top-k 20") || !strings.Contains(got, "--min-p 0") {
			t.Fatalf("%s should get the Qwen sampler baseline: %s", arch, got)
		}
		// Mode-dependent values stay unset: one process serves both thinking and
		// instruct traffic, and the client sends these anyway.
		if strings.Contains(got, "--temp") || strings.Contains(got, "--presence-penalty") {
			t.Fatalf("%s should not seed mode-dependent samplers: %s", arch, got)
		}
	}
}

// Muse Glimmer pins top-k 64 (wider than llama's 40) but documents no min-p, so
// the baseline must emit the one and leave the other to llama rather than
// inventing a pairing. Prefix-matched, like the Qwen family.
func TestBuildCmdLines_samplerMuseGlimmer(t *testing.T) {
	s := Settings{}

	for _, arch := range []string{"muse-glimmer", "muse-glimmermoe"} {
		got := joinCmdMeta(s, Metadata{Architecture: arch}, &Override{})
		if !strings.Contains(got, "--top-k 64") {
			t.Fatalf("%s should get top-k 64: %s", arch, got)
		}
		if strings.Contains(got, "--min-p") {
			t.Fatalf("%s documents no min-p, so none should be emitted: %s", arch, got)
		}
		for _, flag := range []string{"--temp", "--top-p", "--presence-penalty"} {
			if strings.Contains(got, flag) {
				t.Fatalf("%s should not seed client-settable %s: %s", arch, flag, got)
			}
		}
	}

	// An override still reaches the min-p the baseline deliberately left unset.
	got := joinCmdMeta(s, Metadata{Architecture: "muse-glimmer"}, &Override{MinP: f64ptr(0)})
	if !strings.Contains(got, "--min-p 0") || !strings.Contains(got, "--top-k 64") {
		t.Fatalf("override min-p should apply alongside the baseline top-k: %s", got)
	}
}

// An override wins over the baseline, and an explicit 0 is a value (greedy temp,
// min-p disabled) rather than "unset" — the whole reason these fields are
// pointers.
func TestBuildCmdLines_samplerOverride(t *testing.T) {
	s := Settings{}

	got := joinCmdMeta(s, Metadata{Architecture: "qwen35"}, &Override{TopK: intptr(64), MinP: f64ptr(0.1)})
	if !strings.Contains(got, "--top-k 64") || !strings.Contains(got, "--min-p 0.1") {
		t.Fatalf("override should beat the arch baseline: %s", got)
	}

	got = joinCmdMeta(s, Metadata{Architecture: "llama"}, &Override{
		Temp: f64ptr(0), TopP: f64ptr(0.8), MinP: f64ptr(0), PresencePenalty: f64ptr(1.5),
	})
	for _, want := range []string{"--temp 0", "--top-p 0.8", "--min-p 0", "--presence-penalty 1.5"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in: %s", want, got)
		}
	}
	if strings.Contains(got, "--top-k") {
		t.Fatalf("unset top-k should stay unset on a non-Qwen arch: %s", got)
	}
}

// Speculative sub-knobs emit only for the matching backend; draft-n-max defaults
// to 2 and is overridable; ngram-map-k4v knobs emit when set.
func TestBuildCmdLines_specKnobs(t *testing.T) {
	s := Settings{}

	got := joinCmd(s, &Override{Spec: "draft-mtp"})
	if !strings.Contains(got, "--spec-draft-n-max 2") {
		t.Fatalf("draft-mtp default n-max missing: %s", got)
	}
	got = joinCmd(s, &Override{Spec: "draft-mtp", SpecDraftNMax: 5})
	if !strings.Contains(got, "--spec-draft-n-max 5") {
		t.Fatalf("draft-n-max override not applied: %s", got)
	}

	// draft-dflash defaults to a longer draft chain (5, vs mtp's 2) since a
	// diffusion block proposes many tokens per pass; 5 is the measured optimum
	// on Qwen3.6-35B-A3B (own n-max sweep; higher over-drafts and slows TG).
	got = joinCmd(s, &Override{Spec: "draft-dflash"})
	if !strings.Contains(got, "--spec-draft-n-max 5") {
		t.Fatalf("draft-dflash default n-max missing: %s", got)
	}
	got = joinCmd(s, &Override{Spec: "draft-dflash", SpecDraftNMax: 8})
	if !strings.Contains(got, "--spec-draft-n-max 8") {
		t.Fatalf("draft-dflash n-max override not applied: %s", got)
	}

	// A paired draft gguf (DraftPath+DraftKind set at discovery) emits -md + -ngld
	// 99 when its kind matches the active backend (draft-dflash <-> "dflash").
	s2 := Settings{}
	s2.applyDefaults()
	prof := profile{Name: "t", Ctx: 8192}
	got = strings.Join(buildCmdLines(s2, Metadata{}, GgufRow{FullPath: "/m.gguf", DraftPath: "/draft.gguf", DraftKind: "dflash"}, prof, 8192, 99, 0, "q8_0", "q8_0", false, &Override{Spec: "draft-dflash"}), " ")
	for _, want := range []string{"-md /draft.gguf", "-ngld 99"} {
		if !strings.Contains(got, want) {
			t.Fatalf("draft-dflash with DraftPath missing %q: %s", want, got)
		}
	}

	got = joinCmd(s, &Override{Spec: "ngram-map-k4v", SpecDefault: true, SpecNgramSizeN: 16, SpecNgramSizeM: 24, SpecNgramMinHits: 1})
	for _, want := range []string{
		"--spec-default",
		"--spec-ngram-map-k4v-size-n 16",
		"--spec-ngram-map-k4v-size-m 24",
		"--spec-ngram-map-k4v-min-hits 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ngram knob %q missing: %s", want, got)
		}
	}
	// Unset ngram knobs on a non-ngram backend stay absent.
	if got := joinCmd(s, &Override{Spec: "none"}); strings.Contains(got, "--spec-ngram") || strings.Contains(got, "--spec-draft-n-max") {
		t.Fatalf("spec sub-knobs leaked on spec=none: %s", got)
	}
}

// A model carrying BOTH a baked-in MTP nextn layer (IsMTP) and a paired DFlash
// sidecar (both Qwen3.6-27B/35B do) auto-defaults to draft-mtp: dflash wins a
// short flat-prompt bench but craters over a long real session (resident draft
// weights + own full-context KV crowd VRAM), so it is never auto-picked — only
// an explicit `spec: draft-dflash` override selects it. A draft-mtp backend
// must never attach a dflash-kind sidecar as its -md (arch mismatch = broken
// draft).
func TestBuildCmdLines_dflashNotAutoDefault(t *testing.T) {
	meta := Metadata{IsMTP: true}
	// Baked MTP head always wins the auto-default (chained with ngram-mod),
	// dflash sidecar or not.
	if got := effectiveSpec(meta, nil, "dflash"); got != "draft-mtp+ngram-mod" {
		t.Fatalf("effectiveSpec = %q, want draft-mtp+ngram-mod (dflash never auto-defaults)", got)
	}
	if got := effectiveSpec(meta, nil, ""); got != "draft-mtp+ngram-mod" {
		t.Fatalf("effectiveSpec = %q, want draft-mtp+ngram-mod (baked, no sidecar)", got)
	}
	// No baked MTP, dflash sidecar present but not auto-selected → model-less ngram.
	if got := effectiveSpec(Metadata{}, nil, "dflash"); got != "ngram-mod" {
		t.Fatalf("effectiveSpec = %q, want ngram-mod (no baked MTP, dflash not auto-picked)", got)
	}
	// Explicit override still selects dflash.
	if got := effectiveSpec(meta, &Override{Spec: "draft-dflash"}, "dflash"); got != "draft-dflash" {
		t.Fatalf("effectiveSpec = %q, want draft-dflash (explicit override)", got)
	}
	s := Settings{}
	s.applyDefaults()
	prof := profile{Name: "t", Ctx: 8192}
	row := GgufRow{FullPath: "/m.gguf", DraftPath: "/m-DFlash.gguf", DraftKind: "dflash"}
	got := strings.Join(buildCmdLines(s, meta, row, prof, 8192, 99, 0, "q8_0", "q8_0", false, &Override{Spec: "draft-mtp"}), " ")
	if strings.Contains(got, "-md ") {
		t.Fatalf("draft-mtp must not attach a dflash-kind sidecar as -md: %s", got)
	}
}

// No flag appears twice in a draft-dflash cmd line, incl. a paired DraftPath.
func TestBuildCmdLines_dflashNoDoubledFlags(t *testing.T) {
	s := Settings{}
	s.applyDefaults()
	prof := profile{Name: "t", Ctx: 8192}
	row := GgufRow{FullPath: "/m.gguf", DraftPath: "/m-DFlash-Q8_0.gguf", DraftKind: "dflash", DraftSizeGB: 1.2}
	lines := buildCmdLines(s, Metadata{}, row, prof, 8192, 99, 0, "q8_0", "q8_0", false, &Override{Spec: "draft-dflash"})

	seen := map[string]int{}
	for _, l := range lines {
		flag := strings.Fields(l)[0]
		if strings.HasPrefix(flag, "-") {
			seen[flag]++
		}
	}
	for flag, n := range seen {
		if n > 1 {
			t.Fatalf("flag %q appears %d times: %v", flag, n, lines)
		}
	}
}

// The VRAM charge for a draft sidecar must follow the same kind gate the
// emitter uses for -md. Qwen3.8-27B pairs a ~1 GB DFlash drafter to a model
// that auto-defaults to its baked-in MTP head: the drafter is never attached,
// so charging its weights reserved ~0.8 GB of VRAM nothing loads into.
func TestDraftOverheadGB_kindGate(t *testing.T) {
	const dflashGB = 1.06
	mtpSpec := "draft-mtp+ngram-mod"

	// DFlash sidecar in the dir, running on the baked-in MTP head -> flat 0.34.
	if got := draftOverheadGB(mtpSpec, matchedDraftSizeGB(mtpSpec, "dflash", dflashGB)); got != 0.34 {
		t.Fatalf("draft-mtp with a dflash sidecar charged %.2f GB, want the baked-in 0.34", got)
	}
	// Explicitly selected: the drafter really loads, charge its weights + pad.
	if got := draftOverheadGB("draft-dflash", matchedDraftSizeGB("draft-dflash", "dflash", dflashGB)); math.Abs(got-(dflashGB+0.1)) > 1e-9 {
		t.Fatalf("draft-dflash charged %.2f GB, want %.2f", got, dflashGB+0.1)
	}
	// Mirror case: an mtp sidecar must not be charged against a dflash spec.
	if got := draftOverheadGB("draft-dflash", matchedDraftSizeGB("draft-dflash", "mtp", 0.46)); got != 0 {
		t.Fatalf("draft-dflash with an mtp sidecar charged %.2f GB, want 0", got)
	}
	// A matched mtp sidecar (Gemma-4) still charges its real weights.
	if got := draftOverheadGB(mtpSpec, matchedDraftSizeGB(mtpSpec, "mtp", 0.46)); math.Abs(got-0.56) > 1e-9 {
		t.Fatalf("draft-mtp with an mtp sidecar charged %.2f GB, want %.2f", got, 0.46+0.1)
	}
	// No draft spec at all charges nothing, sidecar or not.
	if got := draftOverheadGB("ngram-mod", matchedDraftSizeGB("ngram-mod", "dflash", dflashGB)); got != 0 {
		t.Fatalf("ngram-mod charged %.2f GB, want 0", got)
	}
}

// The sizing charge and the emitted cmd must agree: whenever no -md is emitted,
// no sidecar weights may be charged.
func TestBuildCmdLines_draftChargeMatchesMd(t *testing.T) {
	s := Settings{ServerExe: "llama-server.exe"}
	prof := profile{Name: "test", Target: 24, Ctx: 8192}
	row := GgufRow{FullPath: "/m.gguf", DraftPath: "/m-DFlash-Q4_K_M.gguf", DraftKind: "dflash", DraftSizeGB: 1.06}
	meta := Metadata{IsMTP: true} // baked-in head -> auto spec is draft-mtp

	spec := effectiveSpec(meta, nil, row.DraftKind)
	got := strings.Join(buildCmdLines(s, meta, row, prof, 8192, 99, 0, "q8_0", "q8_0", false, nil), " ")
	hasMd := strings.Contains(got, "-md ")
	charged := matchedDraftSizeGB(spec, row.DraftKind, row.DraftSizeGB) > 0
	if hasMd != charged {
		t.Fatalf("-md emitted = %v but sidecar weights charged = %v (spec %q): %s", hasMd, charged, spec, got)
	}
}
