package autogen

import (
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

	// draft-dflash defaults to a longer draft chain (6, vs mtp's 2) since a
	// diffusion block proposes many tokens per pass; 6 is the measured optimum
	// on Qwen3.6-35B (higher over-drafts and slows TG).
	got = joinCmd(s, &Override{Spec: "draft-dflash"})
	if !strings.Contains(got, "--spec-draft-n-max 6") {
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
// sidecar (both Qwen3.6-27B/35B do) auto-defaults to draft-dflash — the on-disk
// drafter wins over the baked layer. A draft-mtp backend must never attach that
// dflash-kind sidecar as its -md (arch mismatch = broken draft).
func TestBuildCmdLines_dflashWinsOverBakedMTP(t *testing.T) {
	meta := Metadata{IsMTP: true}
	// GPU-bound (cpuBound=false): dflash sidecar wins over the baked MTP head.
	if got := effectiveSpec(meta, nil, "dflash", false); got != "draft-dflash" {
		t.Fatalf("effectiveSpec = %q, want draft-dflash (dflash sidecar beats baked MTP when GPU-bound)", got)
	}
	// CPU-bound (dense weights on CPU): dflash's batched-verify + resident-draft
	// cost stops paying off, so fall back to the free baked-MTP head.
	if got := effectiveSpec(meta, nil, "dflash", true); got != "draft-mtp" {
		t.Fatalf("effectiveSpec = %q, want draft-mtp (CPU-bound downgrades dflash to baked MTP)", got)
	}
	if got := effectiveSpec(meta, nil, "", false); got != "draft-mtp" {
		t.Fatalf("effectiveSpec = %q, want draft-mtp (baked, no sidecar)", got)
	}
	// CPU-bound with a dflash sidecar but no baked MTP → model-less ngram, never dflash.
	if got := effectiveSpec(Metadata{}, nil, "dflash", true); got != "ngram-mod" {
		t.Fatalf("effectiveSpec = %q, want ngram-mod (CPU-bound, no baked MTP)", got)
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
