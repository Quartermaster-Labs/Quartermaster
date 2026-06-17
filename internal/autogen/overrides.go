package autogen

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// GenerateFile is the external autogen control surface: tuning knobs (Settings)
// plus the per-model Overrides table. It replaces the PowerShell
// Generate-Config.ps1 parameters + $Overrides array so models can be tuned
// without recompiling.
type GenerateFile struct {
	Settings  Settings   `yaml:"settings"`
	Overrides []Override `yaml:"overrides"`
}

// Settings are the global generation knobs. Defaults mirror the PowerShell
// Generate-Config.ps1 parameter defaults; applyDefaults fills any zero value.
type Settings struct {
	ModelsRoot       string  `yaml:"modelsRoot"`
	ServerExe        string  `yaml:"serverExe"`
	TargetVramGB     float64 `yaml:"targetVramGB"`
	AutoVram         bool    `yaml:"autoVram"` // measure free VRAM at gen time, use it as TargetVramGB (minus VramOverheadGB)
	VramOverheadGB   float64 `yaml:"vramOverheadGB"`
	GameTargetVramGB float64 `yaml:"gameTargetVramGB"`
	GameCtxTarget    int     `yaml:"gameCtxTarget"`
	// Groups optionally split the emitted models across named groups bound to
	// separate listen addresses (use-case agnostic: membership is by model-name
	// glob, first match wins). Empty => one group, one port (upstream default).
	Groups             []GroupSpec `yaml:"groups"`
	NoGameProfile      bool        `yaml:"noGameProfile"`
	MaxRamGB           float64     `yaml:"maxRamGB"`
	MoeCtxTarget       int         `yaml:"moeCtxTarget"`
	DenseCtxLadder     []int       `yaml:"denseCtxLadder"`
	DenseMinCtx        int         `yaml:"denseMinCtx"`
	Threads            int         `yaml:"threads"`
	TtlSec             int         `yaml:"ttlSec"`
	HealthCheckTimeout int         `yaml:"healthCheckTimeout"`
}

// GroupSpec defines one output group and (optionally) the listen address that
// exposes it. Match is a list of model-name globs (PowerShell -like); a model
// joins the first group it matches. Listen empty => the group binds no
// dedicated port but still groups its members for eviction.
type GroupSpec struct {
	Name   string   `yaml:"name"`
	Listen string   `yaml:"listen"`
	Match  []string `yaml:"match"`
}

// Override supplies what gguf metadata can't, matched by a path glob against the
// gguf's full path (first match wins; optional Quant scopes the match to one
// quant of a multi-quant repo). Mirrors the PowerShell $Overrides rows.
type Override struct {
	Match        string   `yaml:"match"`
	Quant        string   `yaml:"quant"`
	Aliases      []string `yaml:"aliases"`
	Spec         string   `yaml:"spec"`         // "draft-mtp" | "" (=> ngram-mod)
	ReasoningFmt string   `yaml:"reasoningFmt"` // "auto" | "" (=> none)
	CtxVariants  []int    `yaml:"ctxVariants"`
	Ctx          int      `yaml:"ctx"`
	KvK          string   `yaml:"kvK"`
	KvV          string   `yaml:"kvV"`
	KvInRam      bool     `yaml:"kvInRam"`
	Unlisted     bool     `yaml:"unlisted"`
	Skip         bool     `yaml:"skip"`
	// Variants are named custom profiles emitted in addition to the solo model:
	// each becomes "<model>-<name>" with its own ctx/VRAM/kv/spec. Use-case
	// agnostic — the UI's "create variant" flow writes these.
	Variants []VariantSpec `yaml:"variants"`
}

// VariantSpec is one named custom variant of a model (UI-created). Zero/empty
// fields inherit: VramTargetGB => settings.TargetVramGB, KvK/KvV/Spec/
// ReasoningFmt => the model-wide override. Name is the suffix appended after
// the model id ("<model>-<name>").
type VariantSpec struct {
	Name         string   `yaml:"name"`
	Ctx          int      `yaml:"ctx"`
	VramTargetGB float64  `yaml:"vramTargetGB"`
	KvK          string   `yaml:"kvK"`
	KvV          string   `yaml:"kvV"`
	Spec         string   `yaml:"spec"`
	ReasoningFmt string   `yaml:"reasoningFmt"`
	Unlisted     bool     `yaml:"unlisted"`
	Aliases      []string `yaml:"aliases"`
}

// applyDefaults fills zero-valued settings with the PowerShell defaults.
func (s *Settings) applyDefaults() {
	if s.ServerExe == "" {
		s.ServerExe = "llama-server"
	}
	if s.TargetVramGB == 0 {
		s.TargetVramGB = 7
	}
	if s.VramOverheadGB == 0 {
		s.VramOverheadGB = 1.0
	}
	if s.GameTargetVramGB == 0 {
		s.GameTargetVramGB = 5.5
	}
	if s.GameCtxTarget == 0 {
		s.GameCtxTarget = 16384
	}
	if s.MaxRamGB == 0 {
		s.MaxRamGB = 24
	}
	if s.MoeCtxTarget == 0 {
		s.MoeCtxTarget = 65536
	}
	if len(s.DenseCtxLadder) == 0 {
		s.DenseCtxLadder = []int{131072, 65536, 32768}
	}
	if s.DenseMinCtx == 0 {
		s.DenseMinCtx = 32768
	}
	if s.Threads == 0 {
		s.Threads = 7
	}
	if s.TtlSec == 0 {
		s.TtlSec = 600
	}
	if s.HealthCheckTimeout == 0 {
		s.HealthCheckTimeout = 300
	}
}

// LoadGenerateFile reads and validates an autogen control file. Settings
// defaults are applied. modelsDirOverride (from --models-dir) wins over the
// file's modelsRoot when non-empty.
func LoadGenerateFile(path, modelsDirOverride string) (GenerateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return GenerateFile{}, err
	}
	var gf GenerateFile
	if err := yaml.Unmarshal(data, &gf); err != nil {
		return GenerateFile{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	gf.Settings.applyDefaults()
	if modelsDirOverride != "" {
		gf.Settings.ModelsRoot = modelsDirOverride
	}

	// Merge the UI-owned sidecar overrides ahead of the hand-authored ones so
	// per-model edits from the web editor win (override resolution is
	// first-match). The sidecar is fully owned by the UI and may be absent.
	sideRows, err := LoadSidecarOverrides(path)
	if err != nil {
		return GenerateFile{}, err
	}
	if len(sideRows) > 0 {
		gf.Overrides = append(append([]Override{}, sideRows...), gf.Overrides...)
	}
	// A blank modelsRoot is not an error: the server boots with an empty catalog
	// so a setup UI can point it at a models folder later. Discovery/hashing
	// short-circuit on empty (no cwd scan).
	for i, o := range gf.Overrides {
		if strings.TrimSpace(o.Match) == "" {
			return GenerateFile{}, fmt.Errorf("overrides[%d]: match is required", i)
		}
	}
	return gf, nil
}

// ResolveOverride returns the first override whose Match globs the gguf path and
// whose optional Quant equals the row's quant. nil when none match.
func ResolveOverride(row GgufRow, overrides []Override) *Override {
	for i := range overrides {
		o := &overrides[i]
		if !globLike(o.Match, row.FullPath) {
			continue
		}
		if o.Quant != "" && !strings.EqualFold(o.Quant, row.Quant) {
			continue
		}
		return o
	}
	return nil
}

var globCache = map[string]*regexp.Regexp{}

// globLike emulates PowerShell's -like: case-insensitive, '*' matches any run of
// characters (including path separators), '?' matches one. Both pattern and
// subject are normalized to forward slashes so a pattern written either way
// matches a Windows path.
func globLike(pattern, s string) bool {
	re, ok := globCache[pattern]
	if !ok {
		var b strings.Builder
		b.WriteString("(?i)^")
		for _, r := range strings.ReplaceAll(pattern, "\\", "/") {
			switch r {
			case '*':
				b.WriteString(".*")
			case '?':
				b.WriteString(".")
			default:
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		b.WriteString("$")
		re = regexp.MustCompile(b.String())
		globCache[pattern] = re
	}
	return re.MatchString(strings.ReplaceAll(s, "\\", "/"))
}
