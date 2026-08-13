package server

// Wire types for the -generate config editor: the JSON shapes the cogwheel
// modal exchanges with the server, and the conversions between them and
// autogen's Override/VariantSpec. Sparse by design — a zero field means "not
// given, keep auto-computing it", which is why every apply* helper tests for
// the zero value before writing.

import (
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

// variantDTO is the JSON shape of one named custom variant. It carries every
// VariantSpec field the UI can edit so a save round-trips them instead of
// dropping file-defined knobs (e.g. judge's ctxCheckpoints:0 / dry:false).
type variantDTO struct {
	Name string `json:"name"`
	// Backend is the registry entry id to launch with ("" => inherit / auto-pick).
	// Lets a script (e.g. a bench) render a one-off cmd against a specific backend
	// build without persisting a per-model override.
	Backend        string  `json:"backend"`
	Ctx            int     `json:"ctx"`
	VramTargetGB   float64 `json:"vramTargetGB"`
	KvK            string  `json:"kvK"`
	KvV            string  `json:"kvV"`
	Spec           string  `json:"spec"`
	ReasoningFmt   string  `json:"reasoningFmt"`
	Ub             int     `json:"ub"`
	Dry            *bool   `json:"dry"`
	CtxCheckpoints *int    `json:"ctxCheckpoints"`
	Unlisted       bool    `json:"unlisted"`
	// PreserveThinking: nil => on (Qwen3.6 default), false => disabled.
	PreserveThinking *bool `json:"preserveThinking"`
	// SlotCache: nil => inherit the model-wide flag, true/false => explicit.
	SlotCache *bool `json:"slotCache"`
	// Engine knobs (variant carries the full launch shape; zero/empty => inherit).
	KvInRam          bool   `json:"kvInRam"`
	CpuOffload       int    `json:"cpuOffload"`
	FlashAttn        string `json:"flashAttn"`
	Mmap             string `json:"mmap"`
	Mlock            bool   `json:"mlock"`
	Threads          int    `json:"threads"`
	Parallel         int    `json:"parallel"`
	ExtraArgs        string `json:"extraArgs"`
	ChatTemplateFile string `json:"chatTemplateFile"`
	// Sampler / speculative sub-knobs (Dry on/off is the *bool field above).
	DryMultiplier    float64 `json:"dryMultiplier"`
	DryBase          float64 `json:"dryBase"`
	DryAllowedLength int     `json:"dryAllowedLength"`
	SpecDraftNMax    int     `json:"specDraftNMax"`
	SpecDefault      bool    `json:"specDefault"`
	SpecNgramSizeN   int     `json:"specNgramSizeN"`
	SpecNgramSizeM   int     `json:"specNgramSizeM"`
	SpecNgramMinHits int     `json:"specNgramMinHits"`
	// Advanced / power-user knobs; zero/empty => inherit the model-wide value.
	ThreadsBatch         int     `json:"threadsBatch"`
	Prio                 int     `json:"prio"`
	DirectIo             bool    `json:"directIo"`
	NoOpOffload          bool    `json:"noOpOffload"`
	NoRepack             bool    `json:"noRepack"`
	KvKDraft             string  `json:"kvKDraft"`
	KvVDraft             string  `json:"kvVDraft"`
	CacheReuse           int     `json:"cacheReuse"`
	CacheRamMB           int     `json:"cacheRamMB"`
	CacheIdleSlots       string  `json:"cacheIdleSlots"`
	SwaFull              bool    `json:"swaFull"`
	CheckpointMinStep    int     `json:"checkpointMinStep"`
	ContextShift         string  `json:"contextShift"`
	SpecDraftNMin        int     `json:"specDraftNMin"`
	SlotPromptSimilarity float64 `json:"slotPromptSimilarity"`
	RopeScaling          string  `json:"ropeScaling"`
	RopeScale            float64 `json:"ropeScale"`
	RopeFreqBase         float64 `json:"ropeFreqBase"`
	YarnOrigCtx          int     `json:"yarnOrigCtx"`
	SplitMode            string  `json:"splitMode"`
	TensorSplit          string  `json:"tensorSplit"`
	MainGpu              int     `json:"mainGpu"`
	OverrideTensor       string  `json:"overrideTensor"`
	// Image (sd-server) knobs; empty => inherit the model-wide override.
	VaePath         string  `json:"vaePath"`
	ClipLPath       string  `json:"clipLPath"`
	ClipGPath       string  `json:"clipGPath"`
	T5Path          string  `json:"t5Path"`
	TextEncoderPath string  `json:"textEncoderPath"`
	OffloadToCpu    string  `json:"offloadToCpu"`
	TeOnCpu         string  `json:"teOnCpu"`
	VaeOnCpu        string  `json:"vaeOnCpu"`
	VaeTiling       string  `json:"vaeTiling"`
	DiffusionFa     string  `json:"diffusionFa"`
	DefaultSteps    int     `json:"defaultSteps"`
	DefaultCfg      float64 `json:"defaultCfg"`
	DefaultSampler  string  `json:"defaultSampler"`
	DefaultWidth    int     `json:"defaultWidth"`
	DefaultHeight   int     `json:"defaultHeight"`
}

// overrideDTO is the curated JSON shape of a per-model override (the cogwheel
// fields) plus its named variants.
type overrideDTO struct {
	// Backend is the registry entry id this model launches with ("" => auto-pick
	// the class default). Its kind selects which knobs below apply.
	Backend string `json:"backend"`
	// vllm knobs (kind "vllm"): gpu-memory-utilization, tensor-parallel-size, and
	// the base-model tokenizer. --max-model-len comes from Ctx when set, else it
	// is sized against the VRAM budget.
	VllmGpuUtil        float64 `json:"vllmGpuUtil"`
	VllmTensorParallel int     `json:"vllmTensorParallel"`
	VllmTokenizer      string  `json:"vllmTokenizer"`
	Ctx                int     `json:"ctx"`
	KvK                string  `json:"kvK"`
	KvV                string  `json:"kvV"`
	KvInRam            bool    `json:"kvInRam"`
	VramTargetGB       float64 `json:"vramTargetGB"`
	CpuOffload         int     `json:"cpuOffload"`
	Spec               string  `json:"spec"`
	ReasoningFmt       string  `json:"reasoningFmt"`
	ReasoningBudget    int     `json:"reasoningBudget"`
	FlashAttn          string  `json:"flashAttn"`
	Mmap               string  `json:"mmap"`
	Mlock              bool    `json:"mlock"`
	Threads            int     `json:"threads"`
	Parallel           int     `json:"parallel"`
	Ub                 int     `json:"ub"`
	ExtraArgs          string  `json:"extraArgs"`
	ChatTemplateFile   string  `json:"chatTemplateFile"`
	Unlisted           bool    `json:"unlisted"`
	Skip               bool    `json:"skip"`
	SlotCache          *bool   `json:"slotCache"`   // opt this model into on-disk slot KV persistence; nil => default on
	CtxVariants        []int   `json:"ctxVariants"` // per-model ctx tiers (e.g. 32768, 65536)
	// PreserveThinking keeps prior-turn <think> in chat history (Qwen3.6+); only
	// meaningful when reasoning is on.
	PreserveThinking bool `json:"preserveThinking"`
	// CtxCheckpoints is the model-wide --ctx-checkpoints default. nil => omit
	// (auto); 0 disables. Variants inherit it unless they set their own.
	CtxCheckpoints *int         `json:"ctxCheckpoints"`
	Variants       []variantDTO `json:"variants"`
	// Dry sampler: nil => on with defaults, false => disabled. Values 0 => default.
	Dry              *bool   `json:"dry"`
	DryMultiplier    float64 `json:"dryMultiplier"`
	DryBase          float64 `json:"dryBase"`
	DryAllowedLength int     `json:"dryAllowedLength"`
	// Speculative-decode sub-knobs, emitted per Spec backend; 0/false => omit.
	SpecDraftNMax    int  `json:"specDraftNMax"`
	SpecDefault      bool `json:"specDefault"`
	SpecNgramSizeN   int  `json:"specNgramSizeN"`
	SpecNgramSizeM   int  `json:"specNgramSizeM"`
	SpecNgramMinHits int  `json:"specNgramMinHits"`
	// Advanced / power-user knobs; zero/empty => omit the flag.
	ThreadsBatch         int     `json:"threadsBatch"`
	Prio                 int     `json:"prio"`
	DirectIo             bool    `json:"directIo"`
	NoOpOffload          bool    `json:"noOpOffload"`
	NoRepack             bool    `json:"noRepack"`
	KvKDraft             string  `json:"kvKDraft"`
	KvVDraft             string  `json:"kvVDraft"`
	CacheReuse           int     `json:"cacheReuse"`
	CacheRamMB           int     `json:"cacheRamMB"`
	CacheIdleSlots       string  `json:"cacheIdleSlots"`
	SwaFull              bool    `json:"swaFull"`
	CheckpointMinStep    int     `json:"checkpointMinStep"`
	ContextShift         string  `json:"contextShift"`
	SpecDraftNMin        int     `json:"specDraftNMin"`
	SlotPromptSimilarity float64 `json:"slotPromptSimilarity"`
	RopeScaling          string  `json:"ropeScaling"`
	RopeScale            float64 `json:"ropeScale"`
	RopeFreqBase         float64 `json:"ropeFreqBase"`
	YarnOrigCtx          int     `json:"yarnOrigCtx"`
	SplitMode            string  `json:"splitMode"`
	TensorSplit          string  `json:"tensorSplit"`
	MainGpu              int     `json:"mainGpu"`
	OverrideTensor       string  `json:"overrideTensor"`
	// Image (sd-server) knobs; ignored for llama models.
	VaePath         string  `json:"vaePath"`
	ClipLPath       string  `json:"clipLPath"`
	ClipGPath       string  `json:"clipGPath"`
	T5Path          string  `json:"t5Path"`
	TextEncoderPath string  `json:"textEncoderPath"`
	OffloadToCpu    string  `json:"offloadToCpu"`
	TeOnCpu         string  `json:"teOnCpu"`
	VaeOnCpu        string  `json:"vaeOnCpu"`
	VaeTiling       string  `json:"vaeTiling"`
	DiffusionFa     string  `json:"diffusionFa"`
	DefaultSteps    int     `json:"defaultSteps"`
	DefaultCfg      float64 `json:"defaultCfg"`
	DefaultSampler  string  `json:"defaultSampler"`
	DefaultWidth    int     `json:"defaultWidth"`
	DefaultHeight   int     `json:"defaultHeight"`
}

type modelConfigResp struct {
	Id          string `json:"id"`
	Gguf        string `json:"gguf"`
	Cmd         string `json:"cmd"`
	MaxCtx      int    `json:"maxCtx"`     // trained context length (slider ceiling); 0 if unknown
	BlockCount  int    `json:"blockCount"` // transformer layers (denominator for -ngl); 0 if unknown
	IsMTP       bool   `json:"isMTP"`      // model has nextn/MTP layers, or an mtp-* sidecar => draft-mtp usable
	IsDflash    bool   `json:"isDflash"`   // paired *-dflash-*.gguf sidecar in the model's dir => draft-dflash usable
	IsImage     bool   `json:"isImage"`    // diffusion model (sd-server) => image config form
	IsAudio     bool   `json:"isAudio"`    // TTS or ASR model => audio config form
	IsSam       bool   `json:"isSam"`      // SAM segmentation (sam3_server) => minimal segment form
	// Class is the backend class this model resolves against (autogen kindClass):
	// llm / image / tts / asr / segment. The UI filters the backend picker by it —
	// TTS and ASR share one config form but not their engines, so the form flags
	// above cannot stand in for it.
	Class string `json:"class"`
	HasOverride bool   `json:"hasOverride"`
	// DisplayName is the UI-chosen advertised name for this base id ("" => none;
	// the model advertises its real id). Renaming cascades to variant ids.
	DisplayName string       `json:"displayName"`
	Override    *overrideDTO `json:"override"`
	// DefaultVariants are the fleet-wide settings.defaultVariants (e.g. game),
	// shared by every model. Editable here but saved globally (PUT /api/default-variants).
	DefaultVariants []variantDTO `json:"defaultVariants"`
	// Backends is the registry so the editor can offer a per-model backend picker
	// (filtered client-side to the model's class). Empty => no configured registry,
	// the editor shows only the default backend.
	Backends []backendEntryDTO `json:"backends"`
}

func variantToDTO(v autogen.VariantSpec) variantDTO {
	return variantDTO{
		Name: v.Name, Ctx: v.Ctx, VramTargetGB: v.VramTargetGB,
		KvK: v.KvK, KvV: v.KvV, Spec: v.Spec, ReasoningFmt: v.ReasoningFmt,
		Ub: v.Ub, Dry: v.Dry, CtxCheckpoints: v.CtxCheckpoints,
		Unlisted: v.Unlisted, PreserveThinking: v.PreserveThinking, SlotCache: v.SlotCache,
		KvInRam: v.KvInRam, CpuOffload: v.CpuOffload,
		FlashAttn: v.FlashAttn, Mmap: v.Mmap, Mlock: v.Mlock,
		Threads: v.Threads, Parallel: v.Parallel, ExtraArgs: v.ExtraArgs,
		ChatTemplateFile: v.ChatTemplateFile,
		DryMultiplier:    v.DryMultiplier, DryBase: v.DryBase, DryAllowedLength: v.DryAllowedLength,
		SpecDraftNMax: v.SpecDraftNMax, SpecDefault: v.SpecDefault,
		SpecNgramSizeN: v.SpecNgramSizeN, SpecNgramSizeM: v.SpecNgramSizeM, SpecNgramMinHits: v.SpecNgramMinHits,
		ThreadsBatch: v.ThreadsBatch, Prio: v.Prio, DirectIo: v.DirectIo, NoOpOffload: v.NoOpOffload, NoRepack: v.NoRepack,
		KvKDraft: v.KvKDraft, KvVDraft: v.KvVDraft, CacheReuse: v.CacheReuse, CacheRamMB: v.CacheRamMB, CacheIdleSlots: v.CacheIdleSlots,
		SwaFull: v.SwaFull, CheckpointMinStep: v.CheckpointMinStep, ContextShift: v.ContextShift,
		SpecDraftNMin: v.SpecDraftNMin, SlotPromptSimilarity: v.SlotPromptSimilarity,
		RopeScaling: v.RopeScaling, RopeScale: v.RopeScale, RopeFreqBase: v.RopeFreqBase, YarnOrigCtx: v.YarnOrigCtx,
		SplitMode: v.SplitMode, TensorSplit: v.TensorSplit, MainGpu: v.MainGpu, OverrideTensor: v.OverrideTensor,
		VaePath: v.VaePath, ClipLPath: v.ClipLPath, ClipGPath: v.ClipGPath,
		T5Path: v.T5Path, TextEncoderPath: v.TextEncoderPath,
		OffloadToCpu: v.OffloadToCpu, TeOnCpu: v.TeOnCpu, VaeOnCpu: v.VaeOnCpu, VaeTiling: v.VaeTiling, DiffusionFa: v.DiffusionFa,
		DefaultSteps: v.DefaultSteps, DefaultCfg: v.DefaultCfg, DefaultSampler: v.DefaultSampler,
		DefaultWidth: v.DefaultWidth, DefaultHeight: v.DefaultHeight,
	}
}

func toOverrideDTO(o autogen.Override) *overrideDTO {
	dto := &overrideDTO{
		Backend: o.Backend, VllmGpuUtil: o.VllmGpuUtil, VllmTensorParallel: o.VllmTensorParallel,
		VllmTokenizer: o.VllmTokenizer,
		Ctx:           o.Ctx, KvK: o.KvK, KvV: o.KvV, KvInRam: o.KvInRam,
		VramTargetGB: o.VramTargetGB, CpuOffload: o.CpuOffload,
		Spec: o.Spec, ReasoningFmt: o.ReasoningFmt, ReasoningBudget: o.ReasoningBudget,
		FlashAttn: o.FlashAttn, Mmap: o.Mmap, Mlock: o.Mlock,
		Threads: o.Threads, Parallel: o.Parallel, Ub: o.Ub,
		ExtraArgs:        o.ExtraArgs,
		ChatTemplateFile: o.ChatTemplateFile,
		Unlisted:         o.Unlisted, Skip: o.Skip, SlotCache: o.SlotCache,
		CtxVariants: o.CtxVariants, CtxCheckpoints: o.CtxCheckpoints,
		PreserveThinking: o.PreserveThinking,
		Dry:              o.Dry,
		DryMultiplier:    o.DryMultiplier, DryBase: o.DryBase, DryAllowedLength: o.DryAllowedLength,
		SpecDraftNMax: o.SpecDraftNMax, SpecDefault: o.SpecDefault,
		SpecNgramSizeN: o.SpecNgramSizeN, SpecNgramSizeM: o.SpecNgramSizeM, SpecNgramMinHits: o.SpecNgramMinHits,
		ThreadsBatch: o.ThreadsBatch, Prio: o.Prio, DirectIo: o.DirectIo, NoOpOffload: o.NoOpOffload, NoRepack: o.NoRepack,
		KvKDraft: o.KvKDraft, KvVDraft: o.KvVDraft, CacheReuse: o.CacheReuse, CacheRamMB: o.CacheRamMB, CacheIdleSlots: o.CacheIdleSlots,
		SwaFull: o.SwaFull, CheckpointMinStep: o.CheckpointMinStep, ContextShift: o.ContextShift,
		SpecDraftNMin: o.SpecDraftNMin, SlotPromptSimilarity: o.SlotPromptSimilarity,
		RopeScaling: o.RopeScaling, RopeScale: o.RopeScale, RopeFreqBase: o.RopeFreqBase, YarnOrigCtx: o.YarnOrigCtx,
		SplitMode: o.SplitMode, TensorSplit: o.TensorSplit, MainGpu: o.MainGpu, OverrideTensor: o.OverrideTensor,
		VaePath: o.VaePath, ClipLPath: o.ClipLPath, ClipGPath: o.ClipGPath,
		T5Path: o.T5Path, TextEncoderPath: o.TextEncoderPath,
		OffloadToCpu: o.OffloadToCpu, TeOnCpu: o.TeOnCpu, VaeOnCpu: o.VaeOnCpu, VaeTiling: o.VaeTiling, DiffusionFa: o.DiffusionFa,
		DefaultSteps: o.DefaultSteps, DefaultCfg: o.DefaultCfg, DefaultSampler: o.DefaultSampler,
		DefaultWidth: o.DefaultWidth, DefaultHeight: o.DefaultHeight,
	}
	for _, v := range o.Variants {
		dto.Variants = append(dto.Variants, variantToDTO(v))
	}
	return dto
}

func toVariantSpec(v variantDTO) autogen.VariantSpec {
	return autogen.VariantSpec{
		Name: v.Name, Ctx: v.Ctx, VramTargetGB: v.VramTargetGB,
		KvK: v.KvK, KvV: v.KvV, Spec: v.Spec, ReasoningFmt: v.ReasoningFmt,
		Ub: v.Ub, Dry: v.Dry, CtxCheckpoints: v.CtxCheckpoints,
		Unlisted: v.Unlisted, PreserveThinking: v.PreserveThinking, SlotCache: v.SlotCache,
		KvInRam: v.KvInRam, CpuOffload: v.CpuOffload,
		FlashAttn: v.FlashAttn, Mmap: v.Mmap, Mlock: v.Mlock,
		Threads: v.Threads, Parallel: v.Parallel, ExtraArgs: v.ExtraArgs,
		ChatTemplateFile: v.ChatTemplateFile,
		DryMultiplier:    v.DryMultiplier, DryBase: v.DryBase, DryAllowedLength: v.DryAllowedLength,
		SpecDraftNMax: v.SpecDraftNMax, SpecDefault: v.SpecDefault,
		SpecNgramSizeN: v.SpecNgramSizeN, SpecNgramSizeM: v.SpecNgramSizeM, SpecNgramMinHits: v.SpecNgramMinHits,
		ThreadsBatch: v.ThreadsBatch, Prio: v.Prio, DirectIo: v.DirectIo, NoOpOffload: v.NoOpOffload, NoRepack: v.NoRepack,
		KvKDraft: v.KvKDraft, KvVDraft: v.KvVDraft, CacheReuse: v.CacheReuse, CacheRamMB: v.CacheRamMB, CacheIdleSlots: v.CacheIdleSlots,
		SwaFull: v.SwaFull, CheckpointMinStep: v.CheckpointMinStep, ContextShift: v.ContextShift,
		SpecDraftNMin: v.SpecDraftNMin, SlotPromptSimilarity: v.SlotPromptSimilarity,
		RopeScaling: v.RopeScaling, RopeScale: v.RopeScale, RopeFreqBase: v.RopeFreqBase, YarnOrigCtx: v.YarnOrigCtx,
		SplitMode: v.SplitMode, TensorSplit: v.TensorSplit, MainGpu: v.MainGpu, OverrideTensor: v.OverrideTensor,
		VaePath: v.VaePath, ClipLPath: v.ClipLPath, ClipGPath: v.ClipGPath,
		T5Path: v.T5Path, TextEncoderPath: v.TextEncoderPath,
		OffloadToCpu: v.OffloadToCpu, TeOnCpu: v.TeOnCpu, VaeOnCpu: v.VaeOnCpu, VaeTiling: v.VaeTiling, DiffusionFa: v.DiffusionFa,
		DefaultSteps: v.DefaultSteps, DefaultCfg: v.DefaultCfg, DefaultSampler: v.DefaultSampler,
		DefaultWidth: v.DefaultWidth, DefaultHeight: v.DefaultHeight,
	}
}

// applyOverrideDTO copies the editor's curated fields (and variants) from the
// JSON body onto an Override, leaving Match untouched. Shared by the override PUT
// and the command-preview endpoint.
func applyOverrideDTO(ov *autogen.Override, body overrideDTO) {
	ov.Backend = strings.TrimSpace(body.Backend)
	ov.VllmGpuUtil = body.VllmGpuUtil
	ov.VllmTensorParallel = body.VllmTensorParallel
	ov.VllmTokenizer = strings.TrimSpace(body.VllmTokenizer)
	ov.Ctx = body.Ctx
	ov.KvK = body.KvK
	ov.KvV = body.KvV
	ov.KvInRam = body.KvInRam
	ov.VramTargetGB = body.VramTargetGB
	ov.CpuOffload = body.CpuOffload
	ov.Spec = body.Spec
	ov.ReasoningFmt = body.ReasoningFmt
	ov.ReasoningBudget = body.ReasoningBudget
	ov.FlashAttn = body.FlashAttn
	ov.Mmap = body.Mmap
	ov.Mlock = body.Mlock
	ov.Threads = body.Threads
	ov.Parallel = body.Parallel
	ov.Ub = body.Ub
	ov.ExtraArgs = strings.TrimSpace(body.ExtraArgs)
	ov.ChatTemplateFile = strings.TrimSpace(body.ChatTemplateFile)
	ov.Unlisted = body.Unlisted
	ov.Skip = body.Skip
	ov.SlotCache = body.SlotCache
	ov.CtxVariants = body.CtxVariants
	ov.CtxCheckpoints = body.CtxCheckpoints
	ov.PreserveThinking = body.PreserveThinking
	ov.Dry = body.Dry
	ov.DryMultiplier = body.DryMultiplier
	ov.DryBase = body.DryBase
	ov.DryAllowedLength = body.DryAllowedLength
	ov.SpecDraftNMax = body.SpecDraftNMax
	ov.SpecDefault = body.SpecDefault
	ov.SpecNgramSizeN = body.SpecNgramSizeN
	ov.SpecNgramSizeM = body.SpecNgramSizeM
	ov.SpecNgramMinHits = body.SpecNgramMinHits
	ov.ThreadsBatch = body.ThreadsBatch
	ov.Prio = body.Prio
	ov.DirectIo = body.DirectIo
	ov.NoOpOffload = body.NoOpOffload
	ov.NoRepack = body.NoRepack
	ov.KvKDraft = body.KvKDraft
	ov.KvVDraft = body.KvVDraft
	ov.CacheReuse = body.CacheReuse
	ov.CacheRamMB = body.CacheRamMB
	ov.CacheIdleSlots = body.CacheIdleSlots
	ov.SwaFull = body.SwaFull
	ov.CheckpointMinStep = body.CheckpointMinStep
	ov.ContextShift = body.ContextShift
	ov.SpecDraftNMin = body.SpecDraftNMin
	ov.SlotPromptSimilarity = body.SlotPromptSimilarity
	ov.RopeScaling = body.RopeScaling
	ov.RopeScale = body.RopeScale
	ov.RopeFreqBase = body.RopeFreqBase
	ov.YarnOrigCtx = body.YarnOrigCtx
	ov.SplitMode = body.SplitMode
	ov.TensorSplit = strings.TrimSpace(body.TensorSplit)
	ov.MainGpu = body.MainGpu
	ov.OverrideTensor = strings.TrimSpace(body.OverrideTensor)
	ov.VaePath = strings.TrimSpace(body.VaePath)
	ov.ClipLPath = strings.TrimSpace(body.ClipLPath)
	ov.ClipGPath = strings.TrimSpace(body.ClipGPath)
	ov.T5Path = strings.TrimSpace(body.T5Path)
	ov.TextEncoderPath = strings.TrimSpace(body.TextEncoderPath)
	ov.OffloadToCpu = body.OffloadToCpu
	ov.TeOnCpu = body.TeOnCpu
	ov.VaeOnCpu = body.VaeOnCpu
	ov.VaeTiling = body.VaeTiling
	ov.DiffusionFa = body.DiffusionFa
	ov.DefaultSteps = body.DefaultSteps
	ov.DefaultCfg = body.DefaultCfg
	ov.DefaultSampler = strings.TrimSpace(body.DefaultSampler)
	ov.DefaultWidth = body.DefaultWidth
	ov.DefaultHeight = body.DefaultHeight
	ov.Variants = ov.Variants[:0]
	for _, v := range body.Variants {
		ov.Variants = append(ov.Variants, toVariantSpec(v))
	}
}

// applyVariantPatch layers only the NON-ZERO fields of a variantDTO patch onto
// an Override, leaving everything else (already seeded from the model's
// effective override) untouched. Unlike applyOverrideDTO (a full-snapshot
// replace), this treats the body as a sparse diff — the same "zero/empty =
// inherit" convention VariantSpec already uses for named variants.
func applyVariantPatch(ov *autogen.Override, p variantDTO) {
	if strings.TrimSpace(p.Backend) != "" {
		ov.Backend = strings.TrimSpace(p.Backend)
	}
	if p.Ctx != 0 {
		ov.Ctx = p.Ctx
	}
	if p.VramTargetGB != 0 {
		ov.VramTargetGB = p.VramTargetGB
	}
	if p.KvK != "" {
		ov.KvK = p.KvK
	}
	if p.KvV != "" {
		ov.KvV = p.KvV
	}
	if p.Spec != "" {
		ov.Spec = p.Spec
	}
	if p.ReasoningFmt != "" {
		ov.ReasoningFmt = p.ReasoningFmt
	}
	if p.Ub != 0 {
		ov.Ub = p.Ub
	}
	if p.Dry != nil {
		ov.Dry = p.Dry
	}
	if p.CtxCheckpoints != nil {
		ov.CtxCheckpoints = p.CtxCheckpoints
	}
	if p.PreserveThinking != nil {
		ov.PreserveThinking = *p.PreserveThinking
	}
	if p.SlotCache != nil {
		ov.SlotCache = p.SlotCache
	}
	if p.KvInRam {
		ov.KvInRam = true
	}
	if p.CpuOffload != 0 {
		ov.CpuOffload = p.CpuOffload
	}
	if p.FlashAttn != "" {
		ov.FlashAttn = p.FlashAttn
	}
	if p.Mmap != "" {
		ov.Mmap = p.Mmap
	}
	if p.Mlock {
		ov.Mlock = true
	}
	if p.Threads != 0 {
		ov.Threads = p.Threads
	}
	if p.Parallel != 0 {
		ov.Parallel = p.Parallel
	}
	if strings.TrimSpace(p.ExtraArgs) != "" {
		ov.ExtraArgs = strings.TrimSpace(p.ExtraArgs)
	}
	if strings.TrimSpace(p.ChatTemplateFile) != "" {
		ov.ChatTemplateFile = strings.TrimSpace(p.ChatTemplateFile)
	}
	if p.DryMultiplier != 0 {
		ov.DryMultiplier = p.DryMultiplier
	}
	if p.DryBase != 0 {
		ov.DryBase = p.DryBase
	}
	if p.DryAllowedLength != 0 {
		ov.DryAllowedLength = p.DryAllowedLength
	}
	if p.SpecDraftNMax != 0 {
		ov.SpecDraftNMax = p.SpecDraftNMax
	}
	if p.SpecDefault {
		ov.SpecDefault = true
	}
	if p.SpecNgramSizeN != 0 {
		ov.SpecNgramSizeN = p.SpecNgramSizeN
	}
	if p.SpecNgramSizeM != 0 {
		ov.SpecNgramSizeM = p.SpecNgramSizeM
	}
	if p.SpecNgramMinHits != 0 {
		ov.SpecNgramMinHits = p.SpecNgramMinHits
	}
	if p.ThreadsBatch != 0 {
		ov.ThreadsBatch = p.ThreadsBatch
	}
	if p.Prio != 0 {
		ov.Prio = p.Prio
	}
	if p.DirectIo {
		ov.DirectIo = true
	}
	if p.NoOpOffload {
		ov.NoOpOffload = true
	}
	if p.NoRepack {
		ov.NoRepack = true
	}
	if p.KvKDraft != "" {
		ov.KvKDraft = p.KvKDraft
	}
	if p.KvVDraft != "" {
		ov.KvVDraft = p.KvVDraft
	}
	if p.CacheReuse != 0 {
		ov.CacheReuse = p.CacheReuse
	}
	if p.CacheRamMB != 0 {
		ov.CacheRamMB = p.CacheRamMB
	}
	if p.CacheIdleSlots != "" {
		ov.CacheIdleSlots = p.CacheIdleSlots
	}
	if p.SwaFull {
		ov.SwaFull = true
	}
	if p.CheckpointMinStep != 0 {
		ov.CheckpointMinStep = p.CheckpointMinStep
	}
	if p.ContextShift != "" {
		ov.ContextShift = p.ContextShift
	}
	if p.SpecDraftNMin != 0 {
		ov.SpecDraftNMin = p.SpecDraftNMin
	}
	if p.SlotPromptSimilarity != 0 {
		ov.SlotPromptSimilarity = p.SlotPromptSimilarity
	}
	if p.RopeScaling != "" {
		ov.RopeScaling = p.RopeScaling
	}
	if p.RopeScale != 0 {
		ov.RopeScale = p.RopeScale
	}
	if p.RopeFreqBase != 0 {
		ov.RopeFreqBase = p.RopeFreqBase
	}
	if p.YarnOrigCtx != 0 {
		ov.YarnOrigCtx = p.YarnOrigCtx
	}
	if p.SplitMode != "" {
		ov.SplitMode = p.SplitMode
	}
	if strings.TrimSpace(p.TensorSplit) != "" {
		ov.TensorSplit = strings.TrimSpace(p.TensorSplit)
	}
	if p.MainGpu != 0 {
		ov.MainGpu = p.MainGpu
	}
	if strings.TrimSpace(p.OverrideTensor) != "" {
		ov.OverrideTensor = strings.TrimSpace(p.OverrideTensor)
	}
	if strings.TrimSpace(p.VaePath) != "" {
		ov.VaePath = strings.TrimSpace(p.VaePath)
	}
	if strings.TrimSpace(p.ClipLPath) != "" {
		ov.ClipLPath = strings.TrimSpace(p.ClipLPath)
	}
	if strings.TrimSpace(p.ClipGPath) != "" {
		ov.ClipGPath = strings.TrimSpace(p.ClipGPath)
	}
	if strings.TrimSpace(p.T5Path) != "" {
		ov.T5Path = strings.TrimSpace(p.T5Path)
	}
	if strings.TrimSpace(p.TextEncoderPath) != "" {
		ov.TextEncoderPath = strings.TrimSpace(p.TextEncoderPath)
	}
	if p.OffloadToCpu != "" {
		ov.OffloadToCpu = p.OffloadToCpu
	}
	if p.TeOnCpu != "" {
		ov.TeOnCpu = p.TeOnCpu
	}
	if p.VaeOnCpu != "" {
		ov.VaeOnCpu = p.VaeOnCpu
	}
	if p.VaeTiling != "" {
		ov.VaeTiling = p.VaeTiling
	}
	if p.DiffusionFa != "" {
		ov.DiffusionFa = p.DiffusionFa
	}
	if p.DefaultSteps != 0 {
		ov.DefaultSteps = p.DefaultSteps
	}
	if p.DefaultCfg != 0 {
		ov.DefaultCfg = p.DefaultCfg
	}
	if strings.TrimSpace(p.DefaultSampler) != "" {
		ov.DefaultSampler = strings.TrimSpace(p.DefaultSampler)
	}
	if p.DefaultWidth != 0 {
		ov.DefaultWidth = p.DefaultWidth
	}
	if p.DefaultHeight != 0 {
		ov.DefaultHeight = p.DefaultHeight
	}
}
