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
