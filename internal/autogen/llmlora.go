package autogen

import (
	"fmt"
	"strconv"
	"strings"
)

// LoRA adapters for llama-server models.
//
// This is deliberately NOT the shape the sd-server side uses, because the two
// backends disagree about when a LoRA is chosen. sd-server takes
// `--lora-model-dir <DIR>` at launch, lists whatever is in it via
// /sdapi/v1/loras, and each REQUEST names the ones it wants. llama.cpp has no
// directory flag: `--lora FNAME` names one adapter file and binds it into the
// process at spawn, so the set a model runs with is fixed for the life of that
// process. A request can only re-weight what is already loaded (POST
// /lora-adapters, or `"lora":[{"id":N,"scale":F}]` on a completion).
//
// The consequence for the UI: settings.loraDirs["llm"] is a base path and a
// browse root, never a flag. On its own it emits nothing at all. Only
// Override.Loras reaches the backend.

// resolveLlmLoraDir is the folder a model's bare LoRA filenames resolve
// against: settings.loraDirs["llm"], else the fleet-wide settings.loraDir, else
// the directory the model gguf itself sits in.
//
// The last fallback mirrors the image side and buys the same zero-config case:
// an adapter dropped next to the checkpoint it was trained for needs no setting
// at all. There is no per-model override in this ladder on purpose - a model
// that wants an adapter from somewhere else writes an absolute Path, which
// skips the ladder entirely and is clearer than a second directory knob.
func resolveLlmLoraDir(s Settings, modelPath string) string {
	if d := strings.TrimSpace(s.LoraDirs["llm"]); d != "" {
		return d
	}
	if d := strings.TrimSpace(s.LoraDir); d != "" {
		return d
	}
	p := strings.ReplaceAll(strings.TrimSpace(modelPath), "\\", "/")
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return ""
}

// isAbsLoraPath reports whether p already names a location on its own, so the
// LoRA folder must not be prepended.
//
// Hand-rolled rather than filepath.IsAbs because a generated config is shared
// across platforms and is routinely written on one and read on another: a Linux
// build of quartermaster reading a config written on Windows must still
// recognise "D:/loras/x.gguf" as absolute, which filepath.IsAbs would not (it
// compiles per-GOOS). Covers drive letters, UNC/POSIX roots, and a bare
// leading slash.
func isAbsLoraPath(p string) bool {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "/") { // POSIX root, and //server/share
		return true
	}
	// "C:/..." or bare "C:" style drive-relative; both are location-bearing.
	if len(p) >= 2 && p[1] == ':' {
		c := p[0]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return false
}

// resolveLoraPath joins a LoRA reference onto dir unless it is already
// absolute. Separator-normalised to "/" the way cmdPath expects.
func resolveLoraPath(dir, p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" {
		return ""
	}
	if isAbsLoraPath(p) {
		return p
	}
	dir = strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(dir), "\\", "/"), "/")
	if dir == "" {
		return p
	}
	return dir + "/" + strings.TrimLeft(p, "/")
}

// llmLoraLines renders one launch flag per attached adapter. Empty (not nil
// slice semantics, just no lines) for a model with none, which is every model
// that predates this field, so existing configs emit byte-identically.
//
// A blank Path is skipped rather than erroring: the editor's list can hold a
// half-typed row while the launch-command preview re-renders on every
// keystroke, and a hard failure there would blank the preview mid-edit.
func llmLoraLines(s Settings, ov *Override, modelPath string) []string {
	if ov == nil || len(ov.Loras) == 0 {
		return nil
	}
	dir := resolveLlmLoraDir(s, modelPath)
	lines := make([]string, 0, len(ov.Loras))
	for _, l := range ov.Loras {
		full := resolveLoraPath(dir, l.Path)
		if full == "" {
			continue
		}
		if l.Scale == nil {
			lines = append(lines, "--lora "+cmdPath(full))
			continue
		}
		// %g, matching every other float the emitter writes, so 1 renders as "1"
		// rather than "1.000000" and the generated YAML stays diffable.
		lines = append(lines, fmt.Sprintf("--lora-scaled %s %s", cmdPath(full), strconv.FormatFloat(*l.Scale, 'g', -1, 64)))
	}
	return lines
}

// LlmLoraDir is resolveLlmLoraDir for callers outside the package: the config
// editor lists this folder so a model's adapters can be picked rather than
// typed. modelPath may be "" to ask only about the configured folders.
func LlmLoraDir(s Settings, modelPath string) string {
	return resolveLlmLoraDir(s, modelPath)
}

// ResolveLoraPath is resolveLoraPath for callers outside the package, so the
// editor can show the absolute path a bare filename will become without
// duplicating the join rules (which are NOT filepath's - see isAbsLoraPath).
func ResolveLoraPath(dir, p string) string {
	return resolveLoraPath(dir, p)
}
