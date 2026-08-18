package autogen

// YAML emission helpers: the non-model sections of the generated config
// (slot cache, API keys, groups, listeners) plus the small per-model bits
// (display name, profile block, id slug/tag formatting).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// slotKvPath resolves the slot-cache snapshot dir as forward-slash text shared
// by the --slot-save-path flag and the emitted slotCache.path. Blank Path falls
// back to a ".cache" folder next to the quartermaster binary (kept in sync with
// config.DefaultSlotCachePath; duplicated here so autogen stays free of an
// internal/config import).
func slotKvPath(sc SlotCacheSettings) string {
	p := sc.Path
	if p == "" {
		if exe, err := os.Executable(); err == nil {
			p = filepath.Join(filepath.Dir(exe), ".cache", "slotkv")
		} else {
			p = filepath.Join(os.TempDir(), "quartermaster", "slotkv")
		}
	}
	return strings.ReplaceAll(p, "\\", "/")
}

// multiResidentOn reports whether VRAM-budget multi-load is enabled (default on).
func multiResidentOn(s Settings) bool {
	return s.MultiResident == nil || *s.MultiResident
}

// emitVramBudget writes the top-level vramBudgetGB the router admits against.
// Withheld when multiResident is off, which leaves the router on the legacy
// static swap/exclusive policy even though every model still carries its
// estVramGB (harmless, and it keeps the dashboard's numbers available).
func emitVramBudget(b *strings.Builder, s Settings) {
	if !multiResidentOn(s) || s.TargetVramGB <= 0 {
		return
	}
	b.WriteString("# multi-resident: models stay loaded side by side while their\n")
	b.WriteString("# estVramGB sum fits this budget; over it, the least-recently-used\n")
	b.WriteString("# ones are evicted until the incoming model fits.\n")
	fmt.Fprintf(b, "vramBudgetGB: %g\n\n", round2(s.TargetVramGB))
}

// writeEstVram emits a model's predicted VRAM footprint, the admission input for
// vramBudgetGB. Omitted when the caller has no estimate (0), which the router
// reads as "unknown" and handles conservatively.
func writeEstVram(b *strings.Builder, estGB float64) {
	if estGB <= 0 {
		return
	}
	fmt.Fprintf(b, "    estVramGB: %g\n", round2(estGB))
}

// writeEstRam emits the sizer's predicted RAM share (weights the GPU could not
// take). Informational only — the dashboard shows it beside estVramGB — so it
// is omitted for a fully-offloaded model rather than written as 0.
func writeEstRam(b *strings.Builder, estGB float64) {
	if estGB <= 0 {
		return
	}
	fmt.Fprintf(b, "    estRamGB: %g\n", round2(estGB))
}

// emitSlotCache writes the slotCache config block (consumed by the server) when
// the feature is enabled. Unset knobs are omitted so the server applies its
// defaults. The path is always emitted so it matches the --slot-save-path flag.
func emitSlotCache(b *strings.Builder, sc SlotCacheSettings) {
	if !sc.Enable {
		return
	}
	b.WriteString("slotCache:\n")
	b.WriteString("  enable: true\n")
	fmt.Fprintf(b, "  path: %q\n", slotKvPath(sc))
	if sc.MinSaveTokens > 0 {
		fmt.Fprintf(b, "  minSaveTokens: %d\n", sc.MinSaveTokens)
	}
	if sc.MaxDiskGB > 0 {
		fmt.Fprintf(b, "  maxDiskGB: %g\n", sc.MaxDiskGB)
	}
	if sc.MaxSessions > 0 {
		fmt.Fprintf(b, "  maxSessions: %d\n", sc.MaxSessions)
	}
	b.WriteString("\n")
}

// emitAPIKeys writes the apiKeys list and, for any key scoped to a model
// subset, the apiKeyModels map (key => allowed model IDs). Keys with no Models
// are unrestricted and appear only in apiKeys. No keys => nothing emitted.
func emitAPIKeys(b *strings.Builder, keys []APIKeyEntry) {
	if len(keys) == 0 {
		return
	}
	b.WriteString("apiKeys:\n")
	for _, k := range keys {
		fmt.Fprintf(b, "  - %q\n", k.Key)
	}
	scoped := false
	for _, k := range keys {
		if len(k.Models) > 0 {
			scoped = true
			break
		}
	}
	if scoped {
		b.WriteString("apiKeyModels:\n")
		for _, k := range keys {
			if len(k.Models) == 0 {
				continue
			}
			fmt.Fprintf(b, "  %q:\n", k.Key)
			for _, m := range k.Models {
				fmt.Fprintf(b, "    - %q\n", m)
			}
		}
	}
	b.WriteString("\n")
}

// emitGroupsAndListeners writes the groups block and, when settings.groups
// define listen addresses, a listeners block. The mechanism is use-case
// agnostic: each configured group has name-glob patterns (PowerShell -like);
// every emitted model is assigned to the FIRST group whose any pattern matches
// its name, so put specific groups before a "*" catch-all. Models matching no
// group fall into an implicit "default" group with no listener. Every group is
// exclusive, so loading on any listener still evicts whatever the others were
// running (one GPU, VRAM-exclusive); a group marked coexist additionally keeps its
// OWN members loaded side by side (swap:false) instead of swapping between them.
// With no settings.groups the output is a single "exclusive" group over every model
// (upstream default).
func emitGroupsAndListeners(b *strings.Builder, s Settings, emitted []string, cs coexistSets) {
	// SAM masks, CPU-only TTS.cpp voices and CPU-only Parakeet ASR models are tiny,
	// cost no VRAM, and are wanted ALONGSIDE whatever LLM/image model is loaded — a
	// read-aloud click or a dictation must not swap the chat model out. They go in
	// their own never-evicting groups (see writeCoexistGroup) and are kept out of
	// the exclusive swap groups below.
	groups := cs.groups(s)
	inCoexist := map[string]bool{}
	for _, g := range groups {
		for _, n := range g.members {
			inCoexist[n] = true
		}
	}
	swappable := emitted
	if len(inCoexist) > 0 {
		swappable = swappable[:0:0]
		for _, n := range emitted {
			if !inCoexist[n] {
				swappable = append(swappable, n)
			}
		}
	}

	if len(s.Groups) == 0 {
		b.WriteString("\ngroups:\n")
		writeGroup(b, "exclusive", swappable, false)
		for _, g := range groups {
			writeCoexistGroup(b, g.name, g.members)
		}
		return
	}
	coexist := make(map[string]bool, len(s.Groups))
	for _, g := range s.Groups {
		if g.Coexist {
			coexist[g.Name] = true
		}
	}

	// Assign each model to the first matching group (config order preserved).
	members := make(map[string][]string, len(s.Groups)+1)
	order := make([]string, 0, len(s.Groups)+1)
	seenGroup := map[string]bool{}
	addGroup := func(name string) {
		if !seenGroup[name] {
			seenGroup[name] = true
			order = append(order, name)
		}
	}
	for _, g := range s.Groups {
		addGroup(g.Name)
	}
	const defaultGroup = "default"
	for _, name := range swappable {
		assigned := ""
		for _, g := range s.Groups {
			for _, pat := range g.Match {
				if globLike(pat, name) {
					assigned = g.Name
					break
				}
			}
			if assigned != "" {
				break
			}
		}
		if assigned == "" {
			assigned = defaultGroup
			addGroup(defaultGroup)
		}
		members[assigned] = append(members[assigned], name)
	}

	b.WriteString("\ngroups:\n")
	for _, name := range order {
		writeGroup(b, name, members[name], coexist[name])
	}
	for _, g := range groups {
		writeCoexistGroup(b, g.name, g.members)
	}

	// listeners: address -> the groups it exposes (a group with no Listen binds
	// no dedicated port but still groups for eviction). The coexistence groups carry
	// no Listen of their own, so they are appended to EVERY listener: a curated port
	// must not lose its segmentation / read-aloud models purely because we moved them
	// out of the exclusive group.
	byAddr := map[string][]string{}
	var addrOrder []string
	for _, g := range s.Groups {
		if g.Listen == "" {
			continue
		}
		if _, ok := byAddr[g.Listen]; !ok {
			addrOrder = append(addrOrder, g.Listen)
		}
		byAddr[g.Listen] = append(byAddr[g.Listen], g.Name)
	}
	if len(addrOrder) == 0 {
		return
	}
	var everywhere []string
	for _, g := range groups {
		everywhere = append(everywhere, g.name)
	}
	b.WriteString("\nlisteners:\n")
	for _, addr := range addrOrder {
		groups := append(append([]string{}, byAddr[addr]...), everywhere...)
		fmt.Fprintf(b, "  %q:\n    groups: [%s]\n", addr, strings.Join(groups, ", "))
	}
}

// coexistSets carries the model classes that must never take part in eviction:
// they are tiny, run outside the VRAM budget, and are used WHILE a chat or image
// model is resident. Sam is GPU-capable but placed live (see liveoffload.go); the
// other two are CPU-only unless the user opts into GPU via extraArgs, an accepted
// under-charge — the alternative is evicting a 27B on every dictation.
type coexistSets struct {
	Sam []string // SAM segmentation (*.ggml, sam3_server)
	TTS []string // TTS.cpp voices (Kokoro & friends) — CPU-only, no CUDA/ROCm path
	ASR []string // Parakeet transcription — CPU-only by default
}

// coexistGroup is one emitted never-evicting group. Empty ones are dropped, so a
// config with no ASR models carries no "asr" group and no listener reference.
type coexistGroup struct {
	name    string
	members []string
}

func (cs coexistSets) groups(s Settings) []coexistGroup {
	var out []coexistGroup
	for _, g := range []coexistGroup{
		{"sam", cs.Sam},
		{"tts", cs.TTS},
		{"asr", cs.ASR},
	} {
		if len(g.members) > 0 {
			out = append(out, coexistGroup{freeGroupName(s, g.name), g.members})
		}
	}
	return out
}

// freeGroupName returns want, or want+"-auto" when a settings group already claims
// that name. The coexistence groups are synthesised by us, and emitting a duplicate
// YAML key would silently drop the user's group of the same name.
func freeGroupName(s Settings, want string) string {
	for _, g := range s.Groups {
		if strings.EqualFold(strings.TrimSpace(g.Name), want) {
			return want + "-auto"
		}
	}
	return want
}

// writeCoexistGroup emits a zero-VRAM coexistence group (SAM segmentation, CPU-only
// TTS.cpp voices, CPU-only Parakeet ASR): exclusive:false so loading a member evicts nothing,
// persistent:true so an exclusive LLM/image load never evicts the members,
// swap:false so several members coexist (they're all tiny). Nothing is emitted when
// the group has no members.
func writeCoexistGroup(b *strings.Builder, name string, members []string) {
	if len(members) == 0 {
		return
	}
	fmt.Fprintf(b, "  %s:\n", name)
	b.WriteString("    swap: false\n")
	b.WriteString("    exclusive: false\n")
	b.WriteString("    persistent: true\n")
	b.WriteString("    members:\n")
	for _, n := range members {
		fmt.Fprintf(b, "      - %q\n", n)
	}
}

// writeGroup emits one exclusive group with the given members. swap:true (the
// default) means only one member is loaded at a time; coexist emits swap:false so
// the members stay resident together. Either way exclusive:true keeps the group
// evicting the OTHER groups — one GPU.
func writeGroup(b *strings.Builder, name string, members []string, coexist bool) {
	fmt.Fprintf(b, "  %s:\n", name)
	if coexist {
		b.WriteString("    swap: false\n")
	} else {
		b.WriteString("    swap: true\n")
	}
	b.WriteString("    exclusive: true\n")
	b.WriteString("    members:\n")
	for _, n := range members {
		fmt.Fprintf(b, "      - %q\n", n)
	}
}

// emitProfile writes one model entry's YAML block.
// resolvePublicName returns the advertised name for a served id when its base
// model was renamed via settings.displayNames: the display name plus the served
// id's variant suffix. The map is keyed by BASE id; the longest matching base
// prefix wins so a renamed "foo" doesn't shadow a separately-renamed "foo-bar".
// "" => not renamed (advertise the real id unchanged).
func resolvePublicName(dn map[string]string, id string) string {
	best, bestName := "", ""
	for base, name := range dn {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if id == base || strings.HasPrefix(id, base+"-") {
			if len(base) > len(best) {
				best, bestName = base, name
			}
		}
	}
	if best == "" {
		return ""
	}
	return bestName + id[len(best):]
}

// writeDisplayName emits the config name: + a routable alias for a renamed served
// id (both = the advertised public name). The real served id stays the config
// key, so it still resolves; the alias makes the public name resolve too. No-op
// when the model wasn't renamed. Indent matches the 4-space model-field block.
func writeDisplayName(b *strings.Builder, s Settings, id string) {
	pub := resolvePublicName(s.DisplayNames, id)
	if pub == "" || pub == id {
		return
	}
	fmt.Fprintf(b, "    name: %q\n", pub)
	fmt.Fprintf(b, "    aliases: [%q]\n", pub)
}

func emitProfile(b *strings.Builder, s Settings, meta Metadata, row GgufRow, prof profile, ctx, ngl, ncpuMoe int, plan LoadPlan, kvK, kvV string, kvInRam bool, ov *Override) {
	fmt.Fprintf(b, "\n  # arch=%s size=%gGB blocks=%d moe=%v\n", meta.Architecture, meta.FileSizeGB, meta.BlockCount, meta.IsMoE)
	fmt.Fprintf(b, "  # est vram=%gGB ram=%gGB\n", plan.EstVramGB, plan.EstRamGB)
	fmt.Fprintf(b, "  %q:\n", prof.Name)
	b.WriteString("    cmd: >\n")
	for _, line := range buildCmdLines(s, meta, row, prof, ctx, ngl, ncpuMoe, kvK, kvV, kvInRam, ov) {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	writeEstVram(b, plan.EstVramGB)
	writeEstRam(b, plan.EstRamGB)
	writeDisplayName(b, s, prof.Name)
	if prof.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	effort := effortLevels(meta, ov)
	if prof.Vision || len(effort) > 0 {
		b.WriteString("    capabilities:\n")
		if prof.Vision {
			b.WriteString("      in: [text, image]\n")
			b.WriteString("      out: [text]\n")
		}
		if len(effort) > 0 {
			fmt.Fprintf(b, "      reasoningEffort: [%s]\n", strings.Join(effort, ", "))
		}
	}
}

// effortLevels reports the reasoning-effort ladder to advertise for a model:
// the values its baked chat template validates against, but ONLY when that
// template is the one actually being run. A --chat-template-file override
// (user-supplied or the built-in Qwen fix) replaces the template wholesale, so
// the gguf's ladder says nothing about what the running renderer accepts —
// advertising it there would have the server translate a client's
// reasoning_effort into a kwarg the live template ignores.
func effortLevels(meta Metadata, ov *Override) []string {
	if ov != nil && strings.TrimSpace(ov.ChatTemplateFile) != "" {
		return nil
	}
	if needsQwenFixedChatTemplate(meta) {
		return nil
	}
	return meta.ChatTemplateEffortLevels
}

func formatCtxTag(ctx int) string {
	if ctx >= 1048576 && ctx%1048576 == 0 {
		return fmt.Sprintf("%dm", ctx/1048576)
	}
	if ctx >= 1024 && ctx%1024 == 0 {
		return fmt.Sprintf("%dk", ctx/1024)
	}
	return fmt.Sprintf("%d", ctx)
}

// slugify lowercases and collapses non-alphanumerics to single dashes.
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// DefaultNow returns an RFC3339 timestamp for the generation header. Kept
// separate so Generate stays deterministic in tests.
func DefaultNow() string {
	return time.Now().Format(time.RFC3339)
}
