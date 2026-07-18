package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// slotCachePathOrDefault echoes p, or resolves the default snapshot dir when
// blank so the UI displays the real path.
func slotCachePathOrDefault(p string) string {
	if strings.TrimSpace(p) != "" {
		return p
	}
	return config.DefaultSlotCachePath()
}

// handleAPIPickFolder opens the host's native folder dialog and returns the
// chosen path ({path}); 204 when the user cancels. Unlike the category root
// picker it does NOT persist — the caller binds the path into a form field.
func (s *Server) handleAPIPickFolder(w http.ResponseWriter, r *http.Request) {
	path, err := pickFolder()
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "folder picker failed: "+err.Error())
		return
	}
	if strings.TrimSpace(path) == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, map[string]string{"path": path})
}

// AutogenAdmin carries everything the per-model config endpoints need to edit
// the UI-owned override sidecar, regenerate the config, and hot-reload. It is
// set by main only when the server was started with -generate; otherwise the
// endpoints return 501.
type AutogenAdmin struct {
	GeneratePath string // autogen control file (sidecar lives next to it)
	ConfigPath   string // generated config output path
	ModelsDir    string // --models-dir override ("" if unset)
	Reload       func() // triggers a hot config reload (same path as SIGHUP)
}

// SetAutogenAdmin enables the model-config editor endpoints. Call after New and
// after every reload-time rebuild.
func (s *Server) SetAutogenAdmin(a *AutogenAdmin) { s.autogen = a }

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
	KvInRam    bool   `json:"kvInRam"`
	CpuOffload int    `json:"cpuOffload"`
	FlashAttn  string `json:"flashAttn"`
	Mmap       string `json:"mmap"`
	Mlock      bool   `json:"mlock"`
	Threads    int    `json:"threads"`
	Parallel   int    `json:"parallel"`
	ExtraArgs  string `json:"extraArgs"`
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
	// vllm knobs (kind "vllm"): gpu-memory-utilization + tensor-parallel-size.
	// --max-model-len comes from Ctx.
	VllmGpuUtil        float64 `json:"vllmGpuUtil"`
	VllmTensorParallel int     `json:"vllmTensorParallel"`
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
	IsAudio     bool   `json:"isAudio"`    // Qwen3-TTS talker (tts-server) => audio config form
	IsSam       bool   `json:"isSam"`      // SAM segmentation (sam3_server) => minimal segment form
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
		DryMultiplier: v.DryMultiplier, DryBase: v.DryBase, DryAllowedLength: v.DryAllowedLength,
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
		Ctx: o.Ctx, KvK: o.KvK, KvV: o.KvV, KvInRam: o.KvInRam,
		VramTargetGB: o.VramTargetGB, CpuOffload: o.CpuOffload,
		Spec: o.Spec, ReasoningFmt: o.ReasoningFmt, ReasoningBudget: o.ReasoningBudget,
		FlashAttn: o.FlashAttn, Mmap: o.Mmap, Mlock: o.Mlock,
		Threads: o.Threads, Parallel: o.Parallel, Ub: o.Ub,
		ExtraArgs: o.ExtraArgs,
		Unlisted:  o.Unlisted, Skip: o.Skip, SlotCache: o.SlotCache,
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
		DryMultiplier: v.DryMultiplier, DryBase: v.DryBase, DryAllowedLength: v.DryAllowedLength,
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

// resolveModelGguf maps a requested model id/alias to (realID, gguf path, cmd).
// Returns ok=false (and writes the HTTP error) when the model or its gguf can't
// be found.
func (s *Server) resolveModelGguf(w http.ResponseWriter, r *http.Request) (realID, gguf, cmd string, ok bool) {
	if s.autogen == nil {
		shared.SendResponse(w, r, http.StatusNotImplemented, "config editing requires the server to run with -generate")
		return "", "", "", false
	}
	requested := strings.TrimPrefix(r.PathValue("model"), "/")
	cfg := s.config()
	realID, found := cfg.RealModelName(requested)
	if !found {
		shared.SendResponse(w, r, http.StatusNotFound, "model not found")
		return "", "", "", false
	}
	cmd = cfg.Models[realID].Cmd
	gguf = modelFamily(cmd)
	if gguf == "" {
		shared.SendResponse(w, r, http.StatusBadRequest, "model has no gguf path to override")
		return "", "", "", false
	}
	return realID, gguf, cmd, true
}

// findSidecarOverride returns the sidecar override whose Match equals gguf, or
// nil + a zero Override when none exists.
func (s *Server) findSidecarOverride(gguf string) (*autogen.Override, autogen.Override, error) {
	rows, err := autogen.LoadSidecarOverrides(s.autogen.GeneratePath)
	if err != nil {
		return nil, autogen.Override{}, err
	}
	for i := range rows {
		if strings.EqualFold(filepathSlash(rows[i].Match), filepathSlash(gguf)) {
			return &rows[i], rows[i], nil
		}
	}
	return nil, autogen.Override{Match: gguf}, nil
}

// regenAndReload writes the config from the (now-updated) sidecar + generate
// file and hot-reloads. Slow (reads gguf metadata) but it's a settings save.
func (s *Server) regenAndReload(w http.ResponseWriter, r *http.Request) bool {
	a := s.autogen
	if _, err := autogen.EnsureConfig(a.GeneratePath, a.ConfigPath, a.ModelsDir, func(m string) { s.proxylog.Info(m) }); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "regenerating config failed: "+err.Error())
		return false
	}
	if a.Reload != nil {
		a.Reload()
	}
	return true
}

// handleAPIModelConfigGet returns the model's launch command and effective
// UI override (if any).
func (s *Server) handleAPIModelConfigGet(w http.ResponseWriter, r *http.Request) {
	realID, gguf, cmd, ok := s.resolveModelGguf(w, r)
	if !ok {
		return
	}
	existing, _, err := s.findSidecarOverride(gguf)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	// Diffusion models (sd-server) get the image config form, not the llama one.
	// Prefer the declared capability (authoritative); fall back to the rendered
	// command. --diffusion-model catches most sd-server models, but pixel-native /
	// full-checkpoint archs (HiDream-O1, SD/SDXL) are served with -m, which the
	// cmd sniff misses — capabilities.out:[image] covers them.
	// SAM segmentation (sam3_server) gets a minimal segment form. The declared
	// capability is authoritative and MUST win over the image/audio sniffs below:
	// a SAM cmd carries both `--model ` (trips the tts discriminator) and
	// capabilities.out:[image] (trips the image one), so without this SAM would
	// render the wrong form.
	isSam := false
	if mc, ok := s.config().Models[realID]; ok && mc.Capabilities.Segmentation {
		isSam = true
	}
	isImage := strings.Contains(cmd, "--diffusion-model")
	if mc, ok := s.config().Models[realID]; ok && slices.Contains(mc.Capabilities.Out, "image") {
		isImage = true
	}
	// Qwen3-TTS talkers (tts-server) get the audio form: no KV/ctx/spec/estimate.
	// The tts-server cmd uses --model (llama/sd emit -m/--diffusion-model), so that
	// flag is a clean discriminator on the autogen-rendered command.
	isAudio := strings.Contains(cmd, "--codec") || strings.Contains(cmd, "--model ")
	if isSam {
		isImage, isAudio = false, false
	}
	resp := modelConfigResp{Id: realID, Gguf: gguf, Cmd: strings.TrimSpace(cmd), IsImage: isImage, IsAudio: isAudio, IsSam: isSam, HasOverride: existing != nil}
	if dn, err := autogen.LoadSidecarDisplayNames(s.autogen.GeneratePath); err == nil {
		resp.DisplayName = dn[realID]
	}
	// Show the EFFECTIVE override so the editor has the complete picture. The
	// sidecar wins when present (a UI save writes a superset that already carries
	// the file's fields); otherwise surface the hand-authored file override so its
	// ctx tiers / file-defined variants (e.g. judge) appear and round-trip on save
	// instead of being dropped. HasOverride still reflects only the sidecar (it
	// gates the "reset to default" action).
	if existing != nil {
		resp.Override = toOverrideDTO(*existing)
	} else if fileOv, found, ferr := autogen.ResolveFileOverride(s.autogen.GeneratePath, gguf); ferr == nil && found {
		resp.Override = toOverrideDTO(fileOv)
	} else if isImage {
		// Extra image models (safetensors) carry their base config in the settings
		// entry, not an override — seed the form from it so an unedited model shows
		// its real values (else the first save writes blanks and wipes the base).
		if gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir); err == nil {
			if m, ok := autogen.FindExtraImageModel(gf.Settings, gguf); ok {
				resp.Override = toOverrideDTO(autogen.ExtraImageAsOverride(m))
			}
		}
	}
	// Read trained ctx + MTP capability from the gguf header (cheap; header only).
	// Non-fatal: a missing/unreadable gguf just leaves the slider ceiling at 0.
	if meta, err := autogen.ReadGgufMetadataCached(gguf); err == nil {
		resp.MaxCtx = int(meta.ContextLength)
		resp.BlockCount = int(meta.BlockCount)
		// MTP-capable via baked-in nextn layers, a paired mtp-* sidecar, or an
		// already-active draft-mtp cmd. draft-dflash only via a paired sidecar
		// (there's no baked-in dflash arch signal) or an already-active cmd.
		_, draftKind, _ := autogen.DraftSidecarForDir(filepath.Dir(gguf))
		resp.IsMTP = meta.IsMTP || draftKind == "mtp" || strings.Contains(cmd, "--spec-type draft-mtp")
		resp.IsDflash = draftKind == "dflash" || strings.Contains(cmd, "--spec-type draft-dflash")
	}
	// Fleet-wide default variants (e.g. game) + the backend registry so the editor
	// can surface + edit them.
	if gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir); err == nil {
		for _, v := range gf.Settings.DefaultVariants {
			resp.DefaultVariants = append(resp.DefaultVariants, variantToDTO(v))
		}
		for _, e := range gf.Settings.Backends {
			resp.Backends = append(resp.Backends, backendEntryDTO{ID: e.ID, Kind: e.Kind, Name: e.Name, Path: e.Path, Default: e.Default})
		}
	}
	writeJSON(w, resp)
}

// handleAPIDefaultVariantsPut replaces the fleet-wide settings.defaultVariants
// (shared by every model) with the posted list, then regenerates + reloads. The
// per-model config editor surfaces these for editing; saving one writes the
// whole list globally.
func (s *Server) handleAPIDefaultVariantsPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	var body []variantDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	vs := make([]autogen.VariantSpec, 0, len(body))
	for _, v := range body {
		if strings.TrimSpace(v.Name) == "" {
			shared.SendResponse(w, r, http.StatusBadRequest, "every default variant needs a name")
			return
		}
		vs = append(vs, toVariantSpec(v))
	}
	if err := autogen.UpsertSidecarDefaultVariants(s.autogen.GeneratePath, vs); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleAPIModelOverridePut upserts the curated override fields, preserving any
// existing named variants, then regenerates + reloads.
func (s *Server) handleAPIModelOverridePut(w http.ResponseWriter, r *http.Request) {
	_, gguf, _, ok := s.resolveModelGguf(w, r)
	if !ok {
		return
	}
	var body overrideDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Base the sidecar row on the hand-authored FILE override so its file-only
	// fields (ctxVariants, quant) survive — the sidecar row shadows the file row
	// wholesale, so anything the editor doesn't carry would otherwise be lost. The
	// editor loaded the effective override via GET, so the body is authoritative
	// for every UI-modeled field (curated knobs + the full variants list); the
	// incremental variant endpoint remains for API callers that only add one.
	ov, _, err := autogen.ResolveFileOverride(s.autogen.GeneratePath, gguf)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	ov.Match = gguf
	applyOverrideDTO(&ov, body)
	if _, err := autogen.UpsertSidecarOverride(s.autogen.GeneratePath, ov); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleAPIModelOverrideDelete removes the model's sidecar override entirely
// (reset to the autogen default), then regenerates + reloads.
func (s *Server) handleAPIModelOverrideDelete(w http.ResponseWriter, r *http.Request) {
	_, gguf, _, ok := s.resolveModelGguf(w, r)
	if !ok {
		return
	}
	if _, err := autogen.DeleteSidecarOverride(s.autogen.GeneratePath, gguf); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "reset"})
}

// handleAPIModelDisplayNamePut sets the advertised display name for a base model
// id (cascades to its variant ids). A blank/absent name clears it. Validates the
// rename won't collide with another model's advertised id or alias BEFORE writing
// the sidecar, so a bad rename can't produce a config that fails to load.
func (s *Server) handleAPIModelDisplayNamePut(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	requested := strings.TrimPrefix(r.PathValue("model"), "/")
	cfg := s.config()
	base, found := cfg.RealModelName(requested)
	if !found {
		shared.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	name := strings.TrimSpace(body.Name)
	// A rename to a name carrying spaces or slashes would make an un-sendable model
	// id; keep it to the same shape as an autogen-generated id.
	if name != "" && strings.ContainsAny(name, " \t/\\\"") {
		shared.SendResponse(w, r, http.StatusBadRequest, "display name cannot contain spaces, slashes, or quotes")
		return
	}
	if err := s.validateRename(base, name); err != nil {
		shared.SendResponse(w, r, http.StatusConflict, err.Error())
		return
	}
	if _, err := autogen.UpsertSidecarDisplayName(s.autogen.GeneratePath, base, name); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// validateRename rejects a display name whose cascaded ids (the new base name
// plus each family variant suffix) would collide with another model's advertised
// id or alias. name=="" (clear) always passes. Duplicate aliases hard-fail config
// load, so this guard keeps a bad rename from bricking the generated config.
func (s *Server) validateRename(base, name string) error {
	if name == "" {
		return nil
	}
	cfg := s.config()
	inFamily := func(id string) bool { return id == base || strings.HasPrefix(id, base+"-") }
	// Every advertised id + alias NOT belonging to this family is taken.
	taken := map[string]string{} // public id -> owning model id
	for id, mc := range cfg.Models {
		if inFamily(id) {
			continue
		}
		pub := id
		if n := strings.TrimSpace(mc.Name); n != "" {
			pub = n
		}
		taken[pub] = id
		for _, a := range mc.Aliases {
			if a = strings.TrimSpace(a); a != "" {
				taken[a] = id
			}
		}
	}
	for id := range cfg.Models {
		if !inFamily(id) {
			continue
		}
		newPub := name + id[len(base):] // base -> name, keep the variant suffix
		if owner, clash := taken[newPub]; clash {
			return fmt.Errorf("name %q would collide with model %q", newPub, owner)
		}
	}
	return nil
}

// handleAPIModelDisplayNameDelete clears a model's display name (reverts to the
// real id), then regenerates + reloads.
func (s *Server) handleAPIModelDisplayNameDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	requested := strings.TrimPrefix(r.PathValue("model"), "/")
	cfg := s.config()
	base, found := cfg.RealModelName(requested)
	if !found {
		shared.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	if _, err := autogen.UpsertSidecarDisplayName(s.autogen.GeneratePath, base, ""); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "reset"})
}

// handleAPIModelVariantPost adds or replaces (by name) one named variant on the
// model's override, then regenerates + reloads.
func (s *Server) handleAPIModelVariantPost(w http.ResponseWriter, r *http.Request) {
	_, gguf, _, ok := s.resolveModelGguf(w, r)
	if !ok {
		return
	}
	var v variantDTO
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(v.Name) == "" {
		shared.SendResponse(w, r, http.StatusBadRequest, "variant name is required")
		return
	}
	side, ov, err := s.findSidecarOverride(gguf)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	// First sidecar write: seed from the file override so its ctxVariants/quant/
	// file-defined variants aren't lost when the new sidecar row shadows the file
	// row. An existing sidecar is already a superset, so keep editing it in place.
	if side == nil {
		if fileOv, found, ferr := autogen.ResolveFileOverride(s.autogen.GeneratePath, gguf); ferr == nil && found {
			ov = fileOv
		}
	}
	ov.Match = gguf
	spec := toVariantSpec(v)
	replaced := false
	for i := range ov.Variants {
		if strings.EqualFold(ov.Variants[i].Name, spec.Name) {
			ov.Variants[i] = spec
			replaced = true
			break
		}
	}
	if !replaced {
		ov.Variants = append(ov.Variants, spec)
	}
	if _, err := autogen.UpsertSidecarOverride(s.autogen.GeneratePath, ov); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleAPIModelEstimate previews the load plan (VRAM/RAM, ngl, n_cpu_moe,
// chosen ctx) for a candidate tuning without persisting anything. Query params:
// ctx, kvK, kvV, spec (strings/int), kvInRam (bool), vram (float target GB),
// cpuOffload (int layers pinned to CPU).
// Powers the editor's live memory estimate.
// estimateInputFromCmd reconstructs the placement-relevant inputs from a
// model's rendered llama-server command so an estimate reflects the actually
// loaded variant (ctx, checkpoints, spec, kv) instead of re-sizing the solo
// profile with defaults. Tokens are whitespace-split; unknown flags are ignored.
func estimateInputFromCmd(cmd string) autogen.EstimateInput {
	in := autogen.EstimateInput{}
	toks := strings.Fields(cmd)
	for i := 0; i < len(toks); i++ {
		next := func() (string, bool) {
			if i+1 < len(toks) {
				return toks[i+1], true
			}
			return "", false
		}
		switch toks[i] {
		case "-c", "--ctx-size":
			if v, ok := next(); ok {
				in.Ctx, _ = strconv.Atoi(v)
			}
		case "--ctx-checkpoints":
			if v, ok := next(); ok {
				if n, err := strconv.Atoi(v); err == nil {
					in.CtxCheckpoints = &n
				}
			}
		case "--spec-type":
			// Chained spec backends (draft-mtp + ngram-map-k4v) appear as repeated
			// --spec-type; accumulate into the "+"-joined list the sizer expects.
			if v, ok := next(); ok {
				if in.Spec == "" {
					in.Spec = v
				} else {
					in.Spec += "+" + v
				}
			}
		case "-ctk":
			if v, ok := next(); ok {
				in.KvK = v
			}
		case "-ctv":
			if v, ok := next(); ok {
				in.KvV = v
			}
		case "--no-kv-offload":
			in.KvInRam = true
		case "-md", "--model-draft", "--spec-draft-model":
			if v, ok := next(); ok {
				if fi, err := os.Stat(v); err == nil {
					in.DraftGB = float64(fi.Size()) / (1 << 30)
				}
			}
		}
	}
	return in
}

// forcedOffloadFromCmd maps a rendered command's GPU/CPU layer split to the
// EstimateInput.CpuOffload the sizer's applyForcedOffload expects, so an estimate
// can reproduce the exact placement a running process launched with (incl. the
// spawn-time LiveOffloadArgs guard's extra offload) rather than re-deriving it.
// MoE: --n-cpu-moe N is the offload count directly. Dense: -ngl G of BlockCount
// layers => BlockCount-G on CPU. ok=false when the argv carries no placement flag
// (or dims are unknown) so the caller leaves the sizer to choose.
func forcedOffloadFromCmd(cmd string, meta autogen.Metadata) (int, bool) {
	toks := strings.Fields(cmd)
	ngl, nglSet := 0, false
	ncpu, ncpuSet := 0, false
	for i := 0; i+1 < len(toks); i++ {
		switch toks[i] {
		case "-ngl", "--n-gpu-layers", "--gpu-layers":
			if v, err := strconv.Atoi(toks[i+1]); err == nil {
				ngl, nglSet = v, true
			}
		case "--n-cpu-moe", "--cpu-moe":
			if v, err := strconv.Atoi(toks[i+1]); err == nil {
				ncpu, ncpuSet = v, true
			}
		}
	}
	if meta.IsMoE {
		if ncpuSet {
			return ncpu, true
		}
		return 0, false
	}
	blocks := int(meta.BlockCount)
	if nglSet && blocks > 0 {
		n := blocks - ngl
		if n < 0 {
			n = 0
		}
		return n, true
	}
	return 0, false
}

// mmprojPathFromCmd returns the "--mmproj" projector path in a rendered command,
// or "" when the command loads no projector (non-vision model).
func mmprojPathFromCmd(cmd string) string {
	toks := strings.Fields(cmd)
	for i := 0; i+1 < len(toks); i++ {
		if toks[i] == "--mmproj" {
			return toks[i+1]
		}
	}
	return ""
}

func (s *Server) handleAPIModelEstimate(w http.ResponseWriter, r *http.Request) {
	realID, gguf, cmd, ok := s.resolveModelGguf(w, r)
	if !ok {
		return
	}
	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "loading settings failed: "+err.Error())
		return
	}
	meta, err := autogen.ReadGgufMetadataCached(gguf)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "reading gguf metadata failed: "+err.Error())
		return
	}

	q := r.URL.Query()
	// actual=true: seed from the loaded command so the preview reflects the variant
	// that's really running. Prefer the RUNNING cmd (post spawn-time LiveOffloadArgs
	// guard) over the config cmd: the guard can offload MORE layers than the baked
	// plan against live free VRAM, so the config cmd's -ngl is pre-guard and would
	// disagree with the staging area. Pin the estimate to the running placement so
	// the settings menu matches what's actually loaded. Otherwise (config editor,
	// unloaded, or an edited field) start blank so the sizer re-derives placement.
	var in autogen.EstimateInput
	if q.Get("actual") == "true" {
		seedCmd := cmd
		if rc, running := s.local.LaunchedCmd(realID); running && rc != "" {
			seedCmd = rc
		}
		in = estimateInputFromCmd(seedCmd)
		// Pin the actual GPU/CPU layer split from the running argv so EstimatePlan
		// reports the loaded placement instead of re-sizing it against the budget.
		if n, ok := forcedOffloadFromCmd(seedCmd, meta); ok {
			in.CpuOffload = n
		}
	}
	// Explicit query params override the seed (the config editor's form fields).
	if v := q.Get("kvK"); v != "" {
		in.KvK = v
	}
	if v := q.Get("kvV"); v != "" {
		in.KvV = v
	}
	if v := q.Get("spec"); v != "" {
		in.Spec = v
	}
	if v := q.Get("kvInRam"); v != "" {
		in.KvInRam = v == "true"
	}
	if v := q.Get("ctxCheckpoints"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			in.CtxCheckpoints = &n
		}
	}
	if v := q.Get("ctx"); v != "" {
		in.Ctx, _ = strconv.Atoi(v)
	}
	if v := q.Get("vram"); v != "" {
		in.TargetVramGB, _ = strconv.ParseFloat(v, 64)
	} else {
		// No explicit budget: mirror EnsureConfig's autoVram so the preview sizes
		// against the same live free-VRAM budget the config was baked with.
		// Otherwise the estimate uses the larger static targetVramGB and reports a
		// bigger ctx (e.g. 128k) than the config's actual -c (e.g. 98k).
		autogen.ResolveAutoVram(&gf.Settings, nil)
	}
	if v := q.Get("cpuOffload"); v != "" {
		in.CpuOffload, _ = strconv.Atoi(v)
	}
	// A paired draft sidecar (MTP/DFlash gguf in the model's dir) costs real VRAM
	// once the active spec is a draft backend. The config-editor path starts blank
	// (no -md in the cmd to stat), so seed DraftGB from the sidecar's on-disk size
	// — otherwise draftOverheadGB charges only its flat 0.1 GB pad and the estimate
	// bar under-reports the drafter's weights (0.4-1.3 GB here). Harmless for
	// non-draft specs: draftOverheadGB ignores DraftGB unless spec is draft-*.
	if in.DraftGB == 0 {
		if _, _, sizeGB := autogen.DraftSidecarForDir(filepath.Dir(gguf)); sizeGB > 0 {
			in.DraftGB = sizeGB
		}
	}
	// A "-vision" twin loads an mmproj projector whose weights + CLIP compute
	// buffer cost VRAM the bare-LLM sizer is blind to (the -m gguf carries no
	// vision info). Charge the same footprint generate-time bakes into the twin's
	// Overhead (mmprojVramGB) so the editor bar and the status-rail breakdown size
	// the vision load correctly — otherwise the sizer picks an unaffordably large
	// ctx and the projector's VRAM is misattributed to the CUDA slice.
	if in.MmprojGB == 0 {
		if mp := mmprojPathFromCmd(cmd); mp != "" {
			if fi, err := os.Stat(mp); err == nil {
				// Same footprint generate-time bakes: projector weights + the
				// per-projector CLIP compute buffer (modeled from mmproj hparams,
				// flat VisionOverheadGB fallback).
				in.MmprojGB = autogen.MmprojVramGB(mp, float64(fi.Size())/(1<<30), gf.Settings)
			}
		}
	}

	res, err := autogen.EstimatePlan(gf.Settings, meta, in)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "estimate failed: "+err.Error())
		return
	}
	writeJSON(w, res)
}

// --- Global settings editor (dashboard GPU-memory card) ---

type settingsDefaults struct {
	TargetVramGB   float64 `json:"targetVramGB"`
	VramOverheadGB float64 `json:"vramOverheadGB"`
	MaxRamGB       float64 `json:"maxRamGB"`
	TtlSec         int     `json:"ttlSec"`
}

type settingsResp struct {
	TargetVramGB   float64          `json:"targetVramGB"`
	VramOverheadGB float64          `json:"vramOverheadGB"`
	MaxRamGB       float64          `json:"maxRamGB"`
	TtlSec         int              `json:"ttlSec"` // idle-eviction timeout baked into every model's ttl (0 = never)
	AutoVram       bool             `json:"autoVram"`
	Overridden     bool             `json:"overridden"` // a UI sidecar patch is active
	Defaults       settingsDefaults `json:"defaults"`   // values a reset reverts to
	ModelsRoot     string           `json:"modelsRoot"` // the shared/fallback scan folder
	// CategoryRoots is the effective per-category scan folder ("" => uses ModelsRoot).
	CategoryRoots map[string]string `json:"categoryRoots"`
	SlotCache     slotCacheDTO      `json:"slotCache"`   // on-disk slot KV persistence
	Backends      backendsDTO       `json:"backends"`    // effective backend executable paths (legacy 3-slot view)
	BackendList   []backendEntryDTO `json:"backendList"` // full backend registry (add/remove list)
}

// backendsDTO mirrors the effective backend executable paths (llama-server /
// sd-server / tts-server) — the legacy 3-slot view, GET-only. Writes go through
// backendEntryDTO (the registry list); these three are derived from it.
type backendsDTO struct {
	ServerExe    string `json:"serverExe"`
	SdServerExe  string `json:"sdServerExe"`
	TtsServerExe string `json:"ttsServerExe"`
}

// backendEntryDTO mirrors autogen.BackendEntry — one row of the dashboard's
// backend registry.
type backendEntryDTO struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Default bool   `json:"default"` // the auto-pick for this backend's model class
}

// slotCacheDTO mirrors autogen.SlotCacheSettings for the dashboard slot-KV
// section. Zero values fall back to the server's defaults (30k / 10 GB / 20).
type slotCacheDTO struct {
	Enable        bool    `json:"enable"`
	Path          string  `json:"path"`
	MinSaveTokens int     `json:"minSaveTokens"`
	MaxDiskGB     float64 `json:"maxDiskGB"`
	MaxSessions   int     `json:"maxSessions"`
}

type settingsPutDTO struct {
	TargetVramGB   float64 `json:"targetVramGB"`
	VramOverheadGB float64 `json:"vramOverheadGB"`
	MaxRamGB       float64 `json:"maxRamGB"`
	TtlSec         int     `json:"ttlSec"`
}

// requireAutogen guards the settings endpoints, which need the autogen control
// file. Returns false (after writing 501) when the server was started without
// -generate.
func (s *Server) requireAutogen(w http.ResponseWriter, r *http.Request) bool {
	if s.autogen == nil {
		shared.SendResponse(w, r, http.StatusNotImplemented, "settings editing requires the server to run with -generate")
		return false
	}
	return true
}

// handleAPISettingsGet returns the effective global settings plus the base
// (no-patch) defaults and whether a UI patch is active.
func (s *Server) handleAPISettingsGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	a := s.autogen
	gf, err := autogen.LoadGenerateFile(a.GeneratePath, a.ModelsDir)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "loading settings failed: "+err.Error())
		return
	}
	base, err := autogen.LoadBaseSettings(a.GeneratePath, a.ModelsDir)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "loading defaults failed: "+err.Error())
		return
	}
	patch, err := autogen.LoadSidecarSettings(a.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	stored, err := autogen.LoadSidecarBackendList(a.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	// No stored registry yet => seed the list from the effective 3 exes so the
	// UI shows the current backends as editable rows instead of an empty list.
	backendList := make([]backendEntryDTO, 0, len(stored))
	if len(stored) == 0 {
		backendList = append(backendList,
			backendEntryDTO{ID: "llama", Kind: "llama", Name: "llama-server", Path: gf.Settings.ServerExe, Default: true},
			backendEntryDTO{ID: "sd", Kind: "sd", Name: "sd-server", Path: gf.Settings.SdServerExe, Default: true},
			backendEntryDTO{ID: "tts", Kind: "tts", Name: "tts-server", Path: gf.Settings.TtsServerExe, Default: true},
		)
	} else {
		for _, e := range stored {
			backendList = append(backendList, backendEntryDTO{ID: e.ID, Kind: e.Kind, Name: e.Name, Path: e.Path, Default: e.Default})
		}
	}
	writeJSON(w, settingsResp{
		TargetVramGB:   gf.Settings.TargetVramGB,
		VramOverheadGB: gf.Settings.VramOverheadGB,
		MaxRamGB:       gf.Settings.MaxRamGB,
		TtlSec:         gf.Settings.TtlSec,
		AutoVram:       gf.Settings.AutoVram,
		Overridden:     patch != nil,
		Defaults: settingsDefaults{
			TargetVramGB:   base.TargetVramGB,
			VramOverheadGB: base.VramOverheadGB,
			MaxRamGB:       base.MaxRamGB,
			TtlSec:         base.TtlSec,
		},
		ModelsRoot:    gf.Settings.ModelsRoot,
		CategoryRoots: gf.Settings.CategoryRoots,
		SlotCache: slotCacheDTO{
			Enable: gf.Settings.SlotCache.Enable,
			// Surface the resolved default dir (".cache" next to the binary) so the
			// dashboard shows a real path instead of a blank field.
			Path:          slotCachePathOrDefault(gf.Settings.SlotCache.Path),
			MinSaveTokens: gf.Settings.SlotCache.MinSaveTokens,
			MaxDiskGB:     gf.Settings.SlotCache.MaxDiskGB,
			MaxSessions:   gf.Settings.SlotCache.MaxSessions,
		},
		Backends: backendsDTO{
			ServerExe:    gf.Settings.ServerExe,
			SdServerExe:  gf.Settings.SdServerExe,
			TtsServerExe: gf.Settings.TtsServerExe,
		},
		BackendList: backendList,
	})
}

// handleAPIBackendsPut writes the dashboard's backend registry to the sidecar
// (independent of the VRAM patch), then regenerates + reloads. The legacy 3
// exes autogen consumes are re-derived from the list (first entry per kind).
// An empty list reverts to the generate file / sibling defaults.
func (s *Server) handleAPIBackendsPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	var body []backendEntryDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	list := make([]autogen.BackendEntry, 0, len(body))
	for _, e := range body {
		list = append(list, autogen.BackendEntry{ID: e.ID, Kind: e.Kind, Name: e.Name, Path: e.Path, Default: e.Default})
	}
	if err := autogen.UpsertSidecarBackendList(s.autogen.GeneratePath, list); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleAPISlotCachePut writes the dashboard's slot-KV settings to the sidecar
// (independent of the VRAM patch), then regenerates + reloads.
func (s *Server) handleAPISlotCachePut(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	var body slotCacheDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.MinSaveTokens < 0 || body.MaxDiskGB < 0 || body.MaxSessions < 0 {
		shared.SendResponse(w, r, http.StatusBadRequest, "minSaveTokens, maxDiskGB, maxSessions must be >= 0")
		return
	}
	// The UI displays the resolved default path (slotCachePathOrDefault) in the
	// form, so a plain save round-trips that absolute back here. Persisting it
	// would freeze an install-relative path into the sidecar: move/rename the
	// binary dir and slotKvPath keeps emitting the stale old location. Store
	// blank when the path IS the current default so the exe-dir fallback stays
	// live and tracks the binary.
	path := strings.TrimSpace(body.Path)
	if path == config.DefaultSlotCachePath() {
		path = ""
	}
	err := autogen.UpsertSidecarSlotCache(s.autogen.GeneratePath, autogen.SlotCacheSettings{
		Enable:        body.Enable,
		Path:          path,
		MinSaveTokens: body.MinSaveTokens,
		MaxDiskGB:     body.MaxDiskGB,
		MaxSessions:   body.MaxSessions,
	})
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleAPISettingsRootPick opens the host's native folder dialog and, when the
// user picks a folder, sets it as the scan folder for the given UI category
// (body {category}), then regenerates + reloads. 204 when the user cancels.
func (s *Server) handleAPISettingsRootPick(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	var body struct {
		Category string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Category) == "" {
		shared.SendResponse(w, r, http.StatusBadRequest, "body must be {category: <non-empty>}")
		return
	}
	path, err := pickFolder()
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "folder picker failed: "+err.Error())
		return
	}
	if strings.TrimSpace(path) == "" {
		w.WriteHeader(http.StatusNoContent) // user cancelled
		return
	}
	if _, err := autogen.UpsertSidecarRoot(s.autogen.GeneratePath, body.Category, path); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"path": path})
}

// handleAPIBackendPick opens the host's native open-file dialog and returns the
// chosen executable path. Does NOT persist — the Backends UI drops it into the
// field and autosaves via PUT /api/settings/backends. 204 when the user
// cancels; 501 when the platform has no native picker (UI keeps the text field).
func (s *Server) handleAPIBackendPick(w http.ResponseWriter, r *http.Request) {
	path, err := pickFile()
	if err != nil {
		shared.SendResponse(w, r, http.StatusNotImplemented, "file picker unavailable: "+err.Error())
		return
	}
	if strings.TrimSpace(path) == "" {
		w.WriteHeader(http.StatusNoContent) // cancelled
		return
	}
	writeJSON(w, map[string]string{"path": path})
}

// handleAPISettingsPut writes the UI settings patch (manual VRAM target +
// headroom + RAM cap), disabling autoVram so the choice sticks, then regenerates
// + reloads.
func (s *Server) handleAPISettingsPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	var body settingsPutDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.TargetVramGB <= 0 || body.VramOverheadGB < 0 || body.MaxRamGB <= 0 {
		shared.SendResponse(w, r, http.StatusBadRequest, "targetVramGB and maxRamGB must be > 0, vramOverheadGB >= 0")
		return
	}
	if body.TtlSec < 0 {
		shared.SendResponse(w, r, http.StatusBadRequest, "ttlSec must be >= 0 (0 = never auto-unload)")
		return
	}
	autoOff := false
	patch := autogen.SettingsPatch{
		TargetVramGB:   &body.TargetVramGB,
		VramOverheadGB: &body.VramOverheadGB,
		MaxRamGB:       &body.MaxRamGB,
		AutoVram:       &autoOff,
		TtlSec:         &body.TtlSec,
	}
	if err := autogen.UpsertSidecarSettings(s.autogen.GeneratePath, patch); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleAPISettingsDelete clears the UI settings patch (reset to the generate
// file's defaults), then regenerates + reloads.
func (s *Server) handleAPISettingsDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	if _, err := autogen.ClearSidecarSettings(s.autogen.GeneratePath); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "reset"})
}

// applyOverrideDTO copies the editor's curated fields (and variants) from the
// JSON body onto an Override, leaving Match untouched. Shared by the override PUT
// and the command-preview endpoint.
func applyOverrideDTO(ov *autogen.Override, body overrideDTO) {
	ov.Backend = strings.TrimSpace(body.Backend)
	ov.VllmGpuUtil = body.VllmGpuUtil
	ov.VllmTensorParallel = body.VllmTensorParallel
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

// handleAPIModelCmdPreview renders the full launch command for a candidate
// override (the editor's current form state) without persisting anything. Powers
// the two-way launch-parameters box: form edits POST here to refresh the command.
func (s *Server) handleAPIModelCmdPreview(w http.ResponseWriter, r *http.Request) {
	_, gguf, _, ok := s.resolveModelGguf(w, r)
	if !ok {
		return
	}
	var body overrideDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "loading settings failed: "+err.Error())
		return
	}
	ov := autogen.Override{Match: gguf}
	applyOverrideDTO(&ov, body)
	// Extra image models (safetensors) have no gguf header to arch-detect from, so
	// they render through the ExtraImageModel path directly, not the gguf sizer.
	if m, ok := autogen.FindExtraImageModel(gf.Settings, gguf); ok {
		cmd := autogen.RenderExtraImageCmd(gf.Settings, autogen.ApplyOverrideToExtraImage(m, &ov))
		writeJSON(w, map[string]string{"cmd": cmd})
		return
	}
	meta, err := autogen.ReadGgufMetadataCached(gguf)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "reading gguf metadata failed: "+err.Error())
		return
	}
	cmd, err := autogen.RenderSoloCmd(gf.Settings, meta, autogen.GgufRow{FullPath: gguf}, ov)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "rendering command failed: "+err.Error())
		return
	}
	writeJSON(w, map[string]string{"cmd": cmd})
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

// handleAPIModelAdhocCmd renders a one-time launch command for a model with
// ad-hoc flag overrides layered on top of its normal effective override (the
// same auto-compute-unless-given semantics as a named variant). Pure compute:
// no sidecar write, no EnsureConfig, no reload, nothing persists. Meant for
// scripts (e.g. bench scripts) that want a properly VRAM-sized command for a
// one-off flag combo without adding a permanent catalog entry.
func (s *Server) handleAPIModelAdhocCmd(w http.ResponseWriter, r *http.Request) {
	_, gguf, _, ok := s.resolveModelGguf(w, r)
	if !ok {
		return
	}
	var patch variantDTO
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	cmd, err := s.renderAdhocCmd(gguf, patch)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"cmd": cmd})
}

// renderAdhocCmd layers a sparse variant patch onto the model's effective
// override (sidecar > file > blank) and renders the full VRAM-sized launch
// command (with a `${PORT}` placeholder). Shared by adhoc-cmd (render only) and
// adhoc-load (render + inject into the live router).
func (s *Server) renderAdhocCmd(gguf string, patch variantDTO) (string, error) {
	var ov autogen.Override
	if existing, _, err := s.findSidecarOverride(gguf); err != nil {
		return "", err
	} else if existing != nil {
		ov = *existing
	} else if fileOv, found, ferr := autogen.ResolveFileOverride(s.autogen.GeneratePath, gguf); ferr == nil && found {
		ov = fileOv
	} else {
		ov = autogen.Override{Match: gguf}
	}
	applyVariantPatch(&ov, patch)
	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		return "", fmt.Errorf("loading settings failed: %w", err)
	}
	meta, err := autogen.ReadGgufMetadataCached(gguf)
	if err != nil {
		return "", fmt.Errorf("reading gguf metadata failed: %w", err)
	}
	cmd, err := autogen.RenderSoloCmd(gf.Settings, meta, autogen.GgufRow{FullPath: gguf}, ov)
	if err != nil {
		return "", fmt.Errorf("rendering command failed: %w", err)
	}
	return cmd, nil
}

// handleAPIModelAdhocLoad renders a one-time launch command for a model with
// ad-hoc flag overrides (same semantics as adhoc-cmd) and injects it into the
// LIVE router as the model's next-spawn cmd — so requests to this model id
// through the normal proxy (/v1/chat/completions etc.) load it with the custom
// args and are served through the full quartermaster path (promptCanon,
// slotcache, reverse proxy). Nothing is persisted to disk: the mutation lives
// only in the in-memory config until adhoc-unload (or any file reload) reverts
// it. The model id, allocated port, group, listener scope, API-key scope and
// capabilities are all unchanged — only the launch args differ. The model is
// unloaded so its next request spawns fresh with the new args.
//
// Meant for benching arg combos through the real proxy without adding a
// permanent catalog entry. DELETE reverts.
func (s *Server) handleAPIModelAdhocLoad(w http.ResponseWriter, r *http.Request) {
	realID, gguf, baseCmd, ok := s.resolveModelGguf(w, r)
	if !ok {
		return
	}
	var patch variantDTO
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	cmd, err := s.renderAdhocCmd(gguf, patch)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	// The rendered cmd carries a ${PORT} placeholder; the live model already has a
	// concrete port allocated at config load. Reuse it so the proxy target still
	// matches, then inject the cmd into a COW copy of the live config.
	port := portFromCmd(baseCmd)
	if port == "" {
		shared.SendResponse(w, r, http.StatusInternalServerError, "could not determine model port from base cmd")
		return
	}
	cmd = strings.ReplaceAll(cmd, "${PORT}", port)

	newCfg := s.config()
	models := make(map[string]config.ModelConfig, len(newCfg.Models))
	for k, v := range newCfg.Models {
		models[k] = v
	}
	mc := models[realID]
	mc.Cmd = cmd
	models[realID] = mc
	newCfg.Models = models
	if err := s.ApplyConfig(newCfg); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "applying config failed: "+err.Error())
		return
	}
	// Force the next request to spawn fresh with the new args (a running process
	// keeps its old args until restart).
	s.local.Unload(apiUnloadTimeout, realID)
	writeJSON(w, map[string]string{"status": "loaded", "model": realID, "port": port, "cmd": cmd})
}

// handleAPIModelAdhocUnload reverts an adhoc-load by regenerating the config
// from the on-disk generate + sidecar files (restoring the model's original
// launch args) and hot-reloading, then unloading so the next request spawns
// with the restored args.
func (s *Server) handleAPIModelAdhocUnload(w http.ResponseWriter, r *http.Request) {
	realID, _, _, ok := s.resolveModelGguf(w, r)
	if !ok {
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	s.local.Unload(apiUnloadTimeout, realID)
	writeJSON(w, map[string]string{"status": "reverted", "model": realID})
}

// portFromCmd extracts the value following --port / -port in a launch command
// (already-allocated concrete port from config load). Empty if not found.
func portFromCmd(cmd string) string {
	argv, err := config.SanitizeCommand(cmd)
	if err != nil {
		return ""
	}
	for i, a := range argv {
		if (a == "--port" || a == "-port") && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// handleAPIBackendsList returns the backend registry (llama/vllm/sd/tts/custom)
// with an `exists` flag per entry (whether its exe is on disk). Read-only —
// lets a script discover which backend ids are installed before rendering an
// ad-hoc cmd against one (adhoc-cmd's `backend` field).
func (s *Server) handleAPIBackendsList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "loading settings failed: "+err.Error())
		return
	}
	type backendListEntry struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Path    string `json:"path"`
		Default bool   `json:"default"`
		Exists  bool   `json:"exists"`
	}
	out := make([]backendListEntry, 0, len(gf.Settings.Backends))
	for _, e := range gf.Settings.Backends {
		exists := false
		if p := strings.TrimSpace(e.Path); p != "" {
			if _, statErr := os.Stat(p); statErr == nil {
				exists = true
			}
		}
		out = append(out, backendListEntry{ID: e.ID, Kind: e.Kind, Name: e.Name, Path: e.Path, Default: e.Default, Exists: exists})
	}
	writeJSON(w, out)
}

// ensureBackendVariant makes model realID routable on an ALTERNATE backend
// (registry entry backendID) without touching its configured backend. It clones
// the model's live config with the backend's exe swapped into argv[0], registers
// it under a synthetic id "<realID>@<backendID>" in the SAME swap group (so it
// evicts/loads against the same VRAM and shows on the dashboard), and returns
// that id to route to. Idempotent: a no-op once the variant is already live.
// Requires -generate — the backend registry lives in the autogen sidecar.
//
// ponytail: the synthetic model reuses the base model's already-allocated
// ${PORT} and proxy; safe because both share one exclusive group, so only one
// ever runs at a time. Two concurrent first-requests for the same (model,backend)
// may both build+ApplyConfig — the second is a harmless idempotent re-plan.
func (s *Server) ensureBackendVariant(realID, backendID string) (string, error) {
	if s.autogen == nil {
		return "", fmt.Errorf("backend override requires -generate")
	}
	syntheticID := realID + "@" + backendID
	if s.local.Handles(syntheticID) {
		return syntheticID, nil
	}
	cfg := s.config()
	base, ok := cfg.Models[realID]
	if !ok {
		return "", fmt.Errorf("model %q not found", realID)
	}

	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		return "", fmt.Errorf("loading backend registry: %w", err)
	}
	var exe, name string
	for _, e := range gf.Settings.Backends {
		if e.ID == backendID {
			exe = strings.TrimSpace(e.Path)
			name = e.Name
			break
		}
	}
	if exe == "" {
		return "", fmt.Errorf("backend %q not in registry (or has no path)", backendID)
	}
	newCmd, err := swapCmdExe(base.Cmd, exe)
	if err != nil {
		return "", err
	}

	variant := base // struct copy
	variant.Cmd = newCmd
	variant.Unlisted = true // routable + dashboard-visible, but out of the /v1/models catalog
	if name == "" {
		name = backendID
	}
	if variant.Name == "" {
		variant.Name = realID
	}
	variant.Name += " @ " + name

	// COW: build fresh Models + Groups maps; never mutate the live config in place.
	newModels := make(map[string]config.ModelConfig, len(cfg.Models)+1)
	for k, v := range cfg.Models {
		newModels[k] = v
	}
	newModels[syntheticID] = variant
	cfg.Models = newModels

	oldGroups := cfg.Routing.Router.Settings.Groups
	newGroups := make(map[string]config.GroupConfig, len(oldGroups))
	for gid, g := range oldGroups {
		if containsStr(g.Members, realID) {
			m := make([]string, len(g.Members), len(g.Members)+1)
			copy(m, g.Members)
			g.Members = append(m, syntheticID)
		}
		newGroups[gid] = g
	}
	cfg.Routing.Router.Settings.Groups = newGroups

	if err := s.ApplyConfig(cfg); err != nil {
		return "", fmt.Errorf("registering backend variant: %w", err)
	}
	return syntheticID, nil
}

// swapCmdExe replaces the executable (argv[0]) of a model cmd with newExe,
// preserving the rest of the command byte-for-byte (flags, newlines, comments).
func swapCmdExe(cmd, newExe string) (string, error) {
	argv, err := config.SanitizeCommand(cmd)
	if err != nil || len(argv) == 0 {
		return "", fmt.Errorf("cannot parse model cmd")
	}
	oldExe := argv[0]
	i := strings.Index(cmd, oldExe)
	if i < 0 {
		// ponytail: only trips when the exe token is quoted/space-containing;
		// every generated cmd emits an unquoted exe path, so this is a hand-edit
		// edge case, not the normal path.
		return "", fmt.Errorf("cannot locate exe token in cmd")
	}
	return cmd[:i] + newExe + cmd[i+len(oldExe):], nil
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// filepathSlash normalizes separators for case/sep-insensitive path compares.
func filepathSlash(p string) string { return strings.ReplaceAll(p, "\\", "/") }
