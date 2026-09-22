package autogen

import (
	"fmt"
	"strings"
)

// This file is the single description of the llama-server flags the emitter can
// produce, each grouped under the setting it controls (its "knob"). It is the
// source of three behaviours:
//
//   - composition (customargs.go): a knob present in the user's custom launch
//     arguments drops every generated occurrence of that knob,
//   - ownership (config editor): a control is token-owned when the custom text
//     carries this knob's flag, so the form never silently disagrees with the
//     text,
//   - validation: a flag missing from the table and from the selected backend's
//     --help is an error, and autocomplete comes from the union.
//
// Adding a flag to an emitter without adding it here is a test failure. That
// test is the whole point: -cram shipped without a parser case and duplicated
// itself on every save, and the same class of bug has recurred with every new
// emitter flag since.
//
// Aliases are the other spellings llama-server accepts for the same setting
// (its getopt pairs almost every short flag with a long one). They matter for
// suppression: a user's `--ctx-size 32768` must drop the generated `-c 8192`,
// not duplicate it. Only well-known aliases belong here; a spelling llama.cpp
// does not document would suppress a generated flag the user meant to keep.

// RepeatPolicy says what a custom occurrence does to the generated ones.
type RepeatPolicy int

const (
	// ReplaceAll: the custom occurrence replaces every generated occurrence of
	// the knob. This is right for every single-valued flag and also for
	// --spec-type, whose generated occurrences form one chain that a custom
	// value must not half-replace.
	ReplaceAll RepeatPolicy = iota
	// Additive: llama-server accumulates multiple values (--lora, --alias), so a
	// custom occurrence must not drop the generated ones. Nothing the emitter
	// writes uses this yet; it exists so validation and the editor can tell the
	// two cases apart.
	Additive
)

// FlagDef describes one flag the launch-args layer understands.
type FlagDef struct {
	Name    string   // canonical spelling, as the emitter writes it
	Aliases []string // other spellings llama-server accepts for the same knob
	Knob    string   // setting the flag controls; "" = structural, no override field
	Value   bool     // takes at least one value token
	// ExtraValues is how many value tokens follow the first. Only
	// --lora-scaled (FNAME SCALE) needs it today. It matters because the
	// token walk in customargs.go consumes a flag's values along with it: an
	// under-counted flag leaves its trailing value stranded as a bare token,
	// which then looks like a positional argument to everything downstream.
	ExtraValues int
	Repeat      RepeatPolicy
}

// llamaFlagTable lists every llama-server flag quartermaster emits, plus the
// legacy spellings the config form still understands (--no-mmap, --mlock,
// -dio). Keep it sorted by area, not alphabetically: the areas are what a
// reader checks against the emitter.
var llamaFlagTable = []FlagDef{
	// Model / server identity.
	{Name: "-m", Aliases: []string{"--model"}, Knob: "model", Value: true},
	{Name: "--port", Knob: "port", Value: true},
	{Name: "--host", Knob: "host", Value: true},
	{Name: "--cors-origins", Knob: "corsOrigins", Value: true},
	{Name: "--device", Knob: "device", Value: true},

	// Sizing: layers, context, batches, slots.
	{Name: "-ngl", Aliases: []string{"--n-gpu-layers"}, Knob: "ngl", Value: true},
	{Name: "-c", Aliases: []string{"--ctx-size"}, Knob: "ctx", Value: true},
	{Name: "-ub", Aliases: []string{"--ubatch-size"}, Knob: "ub", Value: true},
	{Name: "-b", Aliases: []string{"--batch-size"}, Knob: "batch", Value: true},
	{Name: "--parallel", Aliases: []string{"-np"}, Knob: "parallel", Value: true},
	{Name: "--n-cpu-moe", Aliases: []string{"-ncmoe"}, Knob: "nCpuMoe", Value: true},

	// KV cache / attention.
	{Name: "-fa", Aliases: []string{"--flash-attn"}, Knob: "flashAttn", Value: true},
	{Name: "-ctk", Aliases: []string{"--cache-type-k"}, Knob: "kvK", Value: true},
	{Name: "-ctv", Aliases: []string{"--cache-type-v"}, Knob: "kvV", Value: true},
	{Name: "--kv-unified", Knob: "kvUnified"},
	{Name: "--no-kv-offload", Aliases: []string{"-nkvo"}, Knob: "kvInRam"},

	// Load mode. --no-mmap / --mlock / -dio are the pre---load-mode spellings;
	// the emitter writes --load-mode, but an old sidecar or a hand-written
	// command may still carry them and they own the same knob.
	{Name: "--load-mode", Aliases: []string{"-lm"}, Knob: "loadMode", Value: true},
	{Name: "--no-mmap", Knob: "loadMode"},
	{Name: "--mlock", Knob: "loadMode"},
	{Name: "-dio", Aliases: []string{"--direct-io"}, Knob: "loadMode"},

	// Server hygiene (always emitted, no override field).
	{Name: "--no-warmup", Knob: "noWarmup"},
	{Name: "--no-ui", Aliases: []string{"--no-webui"}, Knob: "noUi"},
	{Name: "--metrics", Knob: "metrics"},
	{Name: "--props", Knob: "props"},

	// Vision projector.
	{Name: "--mmproj", Knob: "mmprojFile", Value: true},
	{Name: "--no-mmproj-offload", Knob: "mmprojOffload"},

	// Multi-GPU placement.
	{Name: "-sm", Aliases: []string{"--split-mode"}, Knob: "splitMode", Value: true},
	{Name: "-ts", Aliases: []string{"--tensor-split"}, Knob: "tensorSplit", Value: true},
	{Name: "-mg", Aliases: []string{"--main-gpu"}, Knob: "mainGpu", Value: true},

	// Speculative decoding. --spec-type is emitted once per chained backend and
	// is still ReplaceAll: a custom value replaces the whole chain.
	{Name: "--spec-type", Knob: "spec", Value: true, Repeat: ReplaceAll},
	{Name: "--spec-default", Knob: "specDefault"},
	{Name: "--spec-draft-n-max", Knob: "specDraftNMax", Value: true},
	{Name: "--spec-draft-n-min", Knob: "specDraftNMin", Value: true},
	{Name: "--spec-ngram-map-k4v-size-n", Knob: "specNgramSizeN", Value: true},
	{Name: "--spec-ngram-map-k4v-size-m", Knob: "specNgramSizeM", Value: true},
	{Name: "--spec-ngram-map-k4v-min-hits", Knob: "specNgramMinHits", Value: true},
	{Name: "-md", Aliases: []string{"--model-draft"}, Knob: "draftModel", Value: true},
	{Name: "-ngld", Aliases: []string{"--n-gpu-layers-draft"}, Knob: "ngld", Value: true},
	{Name: "-ctkd", Aliases: []string{"--cache-type-k-draft"}, Knob: "kvKDraft", Value: true},
	{Name: "-ctvd", Aliases: []string{"--cache-type-v-draft"}, Knob: "kvVDraft", Value: true},

	// Reasoning / templates.
	{Name: "--jinja", Knob: "jinja"},
	{Name: "--reasoning-format", Knob: "reasoningFmt", Value: true},
	{Name: "--reasoning", Knob: "reasoningFmt", Value: true},
	{Name: "--reasoning-budget", Knob: "reasoningBudget", Value: true},
	{Name: "--reasoning-preserve", Knob: "reasoningPreserve"},
	{Name: "--no-reasoning-preserve", Knob: "reasoningPreserve"},
	{Name: "--chat-template-file", Knob: "chatTemplateFile", Value: true},
	{Name: "--chat-template-kwargs", Knob: "chatTemplateKwargs", Value: true},

	// Context checkpoints.
	{Name: "--ctx-checkpoints", Knob: "ctxCheckpoints", Value: true},
	{Name: "-cms", Aliases: []string{"--checkpoint-min-step"}, Knob: "checkpointMinStep", Value: true},

	// DRY sampler.
	{Name: "--dry-multiplier", Knob: "dryMultiplier", Value: true},
	{Name: "--dry-base", Knob: "dryBase", Value: true},
	{Name: "--dry-allowed-length", Knob: "dryAllowedLength", Value: true},

	// Sampler defaults.
	{Name: "--temp", Aliases: []string{"--temperature"}, Knob: "temp", Value: true},
	{Name: "--top-k", Knob: "topK", Value: true},
	{Name: "--top-p", Knob: "topP", Value: true},
	{Name: "--min-p", Knob: "minP", Value: true},
	{Name: "--presence-penalty", Knob: "presencePenalty", Value: true},

	// Threads, priority, slot cache.
	{Name: "-t", Aliases: []string{"--threads"}, Knob: "threads", Value: true},
	{Name: "-tb", Aliases: []string{"--threads-batch"}, Knob: "threadsBatch", Value: true},
	{Name: "--prio", Knob: "prio", Value: true},
	{Name: "--slot-save-path", Knob: "slotSavePath", Value: true},

	// Engine knobs.
	{Name: "--no-op-offload", Knob: "noOpOffload"},
	{Name: "--no-repack", Knob: "noRepack"},
	{Name: "--cache-reuse", Knob: "cacheReuse", Value: true},
	{Name: "-cram", Aliases: []string{"--cache-ram"}, Knob: "cacheRam", Value: true},
	{Name: "-lv", Aliases: []string{"--log-verbosity"}, Knob: "logVerbosity", Value: true},
	{Name: "--cache-idle-slots", Knob: "cacheIdleSlots"},
	{Name: "--no-cache-idle-slots", Knob: "cacheIdleSlots"},
	{Name: "--swa-full", Knob: "swaFull"},
	{Name: "--context-shift", Knob: "contextShift"},
	{Name: "--no-context-shift", Knob: "contextShift"},
	{Name: "-sps", Aliases: []string{"--slot-prompt-similarity"}, Knob: "slotPromptSimilarity", Value: true},
	{Name: "--rope-scaling", Knob: "ropeScaling", Value: true},
	{Name: "--rope-scale", Knob: "ropeScale", Value: true},
	{Name: "--rope-freq-base", Knob: "ropeFreqBase", Value: true},
	{Name: "--yarn-orig-ctx", Knob: "yarnOrigCtx", Value: true},
	{Name: "-ot", Aliases: []string{"--override-tensor"}, Knob: "overrideTensor", Value: true},

	// LoRA adapters. Additive, unlike almost everything else here: llama-server
	// accumulates --lora occurrences, so a user who adds one in custom args
	// means "and this one too", not "instead of the model's". Both spellings
	// share the knob so ownership is coherent either way.
	{Name: "--lora", Knob: "loras", Value: true, Repeat: Additive},
	{Name: "--lora-scaled", Knob: "loras", Value: true, ExtraValues: 1, Repeat: Additive},
}

// FlagTable is ONE backend's flag vocabulary: the ordered defs plus an index
// over every spelling they accept. There are two of them (llama-server here,
// sd-server in flagtable_sd.go) because a spelling only means something against
// a specific binary: -H is --height to sd-server and --hf-repo to llama-server,
// and -l is --listen-ip to one and nothing at all to the other. Resolving a
// diffusion command against the llama table is not a near miss, it is how every
// sd-server flag read as unknown, owned no knob, suppressed nothing, and left
// the user's text to pile up beside the generated copy.
type FlagTable struct {
	Backend string // the binary these spellings belong to, for error text
	Defs    []FlagDef
	index   map[string]FlagDef
}

// newFlagTable builds the spelling index once, at package init.
func newFlagTable(backend string, defs []FlagDef) *FlagTable {
	t := &FlagTable{Backend: backend, Defs: defs, index: make(map[string]FlagDef, len(defs)*2)}
	for _, d := range defs {
		t.index[d.Name] = d
		for _, a := range d.Aliases {
			t.index[a] = d
		}
	}
	return t
}

// Lookup resolves a flag spelling to its definition. ok is false for a flag the
// table does not know, which is not an error by itself: it may be a
// user-written flag with no emitter counterpart (validate.go checks those
// against the backend's --help).
func (t *FlagTable) Lookup(name string) (FlagDef, bool) {
	if t == nil {
		return FlagDef{}, false
	}
	d, ok := t.index[name]
	return d, ok
}

// LookupFlag resolves a spelling against the llama-server table. It and the
// other three package-level wrappers below exist so the dozen llama-only
// callers (pins.go, validate.go, the estimate preview) read exactly as they did
// before the table became a parameter; anything that can serve either backend
// takes a *FlagTable instead.
func LookupFlag(name string) (FlagDef, bool) { return LlamaFlags.Lookup(name) }

// SplitFlagToken splits a token into flag name and inline value, accepting
// llama's `--flag=value` form. hasValue is true when the value was inline, so
// the caller knows not to consume the next token.
func SplitFlagToken(tok string) (name, value string, hasValue bool) {
	if !strings.HasPrefix(tok, "--") {
		return tok, "", false
	}
	if i := strings.IndexByte(tok, '='); i >= 0 {
		return tok[:i], tok[i+1:], true
	}
	return tok, "", false
}

// TokenKnob returns the knob a command token belongs to, resolving both
// spellings and the --flag=value form. "" for values, for structural flags, and
// for anything the table does not know.
func (t *FlagTable) TokenKnob(tok string) string {
	if !strings.HasPrefix(tok, "-") {
		return ""
	}
	name, _, _ := SplitFlagToken(tok)
	if d, ok := t.Lookup(name); ok {
		return d.Knob
	}
	return ""
}

// TokenKnob resolves against the llama-server table.
func TokenKnob(tok string) string { return LlamaFlags.TokenKnob(tok) }

// OwnedKnobs returns the knobs a token list sets. Flag tokens carry their knob;
// an unknown flag contributes nothing (it cannot suppress a generated flag the
// table cannot map it to). The token after a known value-taking flag is skipped
// even when it starts with "-", so a negative value never reads as a flag.
func (t *FlagTable) OwnedKnobs(tokens []string) map[string]bool {
	owned := map[string]bool{}
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if !strings.HasPrefix(tok, "-") {
			continue
		}
		name, _, inline := SplitFlagToken(tok)
		d, ok := t.Lookup(name)
		if !ok {
			continue
		}
		// An Additive flag is never "owned": llama-server accumulates its
		// occurrences, so a custom --lora means "and this one too". Marking it
		// owned would drop every generated occurrence AND have
		// ClearOwnedFields wipe the model's adapter list, turning an addition
		// into a silent replacement.
		if d.Knob != "" && d.Repeat != Additive {
			owned[d.Knob] = true
		}
		if d.Value && !inline {
			i += 1 + d.ExtraValues
		}
	}
	return owned
}

// OwnedKnobs resolves against the llama-server table.
func OwnedKnobs(tokens []string) map[string]bool { return LlamaFlags.OwnedKnobs(tokens) }

// ClearOwnedFields zeroes every structured override field whose knob the custom
// text owns, and returns the knobs it cleared (sorted by the caller). The
// fields would otherwise sit invisible under the user's flag and resurface the
// moment the flag is deleted from the text, which is issue #38's mmap reset in
// another costume.
//
// One knob can span several fields (loadMode covers Mmap, Mlock and DirectIo;
// dry covers the flag plus its three values), so the switch clears them as a
// group.
func ClearOwnedFields(ov *Override, owned map[string]bool) []string {
	if ov == nil {
		return nil
	}
	var cleared []string
	clear := func(knob string, fields ...any) {
		if !owned[knob] {
			return
		}
		for _, f := range fields {
			switch p := f.(type) {
			case *string:
				*p = ""
			case *int:
				*p = 0
			case *float64:
				*p = 0
			case *bool:
				*p = false
			case **int:
				*p = nil
			case **float64:
				*p = nil
			case **bool:
				*p = nil
			default:
				// A typo'd or missing pointer clears nothing silently, which is
				// how issue #38's mmap pin survived a save it should not have.
				panic(fmt.Sprintf("ClearOwnedFields: unsupported field type %T for knob %q", f, knob))
			}
		}
		cleared = append(cleared, knob)
	}
	clear("ctx", &ov.Ctx)
	clear("ub", &ov.Ub)
	clear("parallel", &ov.Parallel)
	clear("flashAttn", &ov.FlashAttn)
	clear("kvK", &ov.KvK)
	clear("kvV", &ov.KvV)
	clear("kvInRam", &ov.KvInRam)
	clear("loadMode", &ov.Mmap, &ov.Mlock, &ov.DirectIo)
	clear("spec", &ov.Spec)
	clear("specDefault", &ov.SpecDefault)
	clear("specDraftNMax", &ov.SpecDraftNMax)
	clear("specDraftNMin", &ov.SpecDraftNMin)
	clear("specNgramSizeN", &ov.SpecNgramSizeN)
	clear("specNgramSizeM", &ov.SpecNgramSizeM)
	clear("specNgramMinHits", &ov.SpecNgramMinHits)
	clear("kvKDraft", &ov.KvKDraft)
	clear("kvVDraft", &ov.KvVDraft)
	clear("reasoningFmt", &ov.ReasoningFmt)
	clear("reasoningBudget", &ov.ReasoningBudget)
	clear("reasoningPreserve", &ov.PreserveThinking)
	clear("ctxCheckpoints", &ov.CtxCheckpoints)
	clear("checkpointMinStep", &ov.CheckpointMinStep)
	clear("dryMultiplier", &ov.DryMultiplier)
	clear("dryBase", &ov.DryBase)
	clear("dryAllowedLength", &ov.DryAllowedLength)
	clear("temp", &ov.Temp)
	clear("topK", &ov.TopK)
	clear("topP", &ov.TopP)
	clear("minP", &ov.MinP)
	clear("presencePenalty", &ov.PresencePenalty)
	clear("threads", &ov.Threads)
	clear("threadsBatch", &ov.ThreadsBatch)
	clear("prio", &ov.Prio)
	clear("noOpOffload", &ov.NoOpOffload)
	clear("noRepack", &ov.NoRepack)
	clear("cacheReuse", &ov.CacheReuse)
	clear("cacheRam", &ov.CacheRamMB)
	clear("logVerbosity", &ov.LogVerbosity)
	clear("cacheIdleSlots", &ov.CacheIdleSlots)
	clear("swaFull", &ov.SwaFull)
	clear("contextShift", &ov.ContextShift)
	clear("slotPromptSimilarity", &ov.SlotPromptSimilarity)
	clear("ropeScaling", &ov.RopeScaling)
	clear("ropeScale", &ov.RopeScale)
	clear("ropeFreqBase", &ov.RopeFreqBase)
	clear("yarnOrigCtx", &ov.YarnOrigCtx)
	clear("splitMode", &ov.SplitMode)
	clear("tensorSplit", &ov.TensorSplit)
	clear("mainGpu", &ov.MainGpu)
	clear("overrideTensor", &ov.OverrideTensor)
	clear("chatTemplateFile", &ov.ChatTemplateFile)
	clear("mmprojFile", &ov.MmprojFile)
	clear("mmprojOffload", &ov.Mmproj)
	clear("slotSavePath", &ov.SlotCache)

	// sd-server knobs. They share this switch rather than getting a twin of it
	// because an Override carries BOTH backends' fields and only one table is
	// ever consulted for a given model, so a llama knob set can never name one of
	// these. Flags with no field behind them (--max-vram, --params-backend,
	// --auto-fit) are absent on purpose: they suppress their generated copy
	// through the table and have nothing to clear.
	clear("vaePath", &ov.VaePath)
	clear("audioVaePath", &ov.AudioVaePath)
	clear("clipLPath", &ov.ClipLPath)
	clear("clipGPath", &ov.ClipGPath)
	clear("t5Path", &ov.T5Path)
	clear("textEncoderPath", &ov.TextEncoderPath)
	// llm_vision owns the gate as well as the path: leaving LlmVision "on" under
	// a hand-written --llm_vision would re-derive a projector beside the encoder
	// and emit a second copy of the flag.
	clear("llmVisionPath", &ov.LlmVisionPath, &ov.LlmVision)
	clear("loraDir", &ov.LoraDir)
	clear("offloadToCpu", &ov.OffloadToCpu)
	clear("vaeOnCpu", &ov.VaeOnCpu)
	clear("teOnCpu", &ov.TeOnCpu)
	clear("streamLayers", &ov.StreamLayers)
	clear("diffusionFa", &ov.DiffusionFa)
	clear("vaeTiling", &ov.VaeTiling)
	clear("temporalTiling", &ov.TemporalTiling)
	clear("defaultSteps", &ov.DefaultSteps)
	clear("defaultCfg", &ov.DefaultCfg)
	clear("defaultSampler", &ov.DefaultSampler)
	clear("defaultWidth", &ov.DefaultWidth)
	clear("defaultHeight", &ov.DefaultHeight)
	clear("defaultFrames", &ov.DefaultFrames)
	clear("defaultFps", &ov.DefaultFps)
	return cleared
}
