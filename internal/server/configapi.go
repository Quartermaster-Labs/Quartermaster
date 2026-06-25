package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mostlygeek/llama-swap/internal/autogen"
	"github.com/mostlygeek/llama-swap/internal/shared"
)

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
	Name           string   `json:"name"`
	Ctx            int      `json:"ctx"`
	VramTargetGB   float64  `json:"vramTargetGB"`
	KvK            string   `json:"kvK"`
	KvV            string   `json:"kvV"`
	Spec           string   `json:"spec"`
	ReasoningFmt   string   `json:"reasoningFmt"`
	Ub             int      `json:"ub"`
	Dry            *bool    `json:"dry"`
	CtxCheckpoints *int     `json:"ctxCheckpoints"`
	Unlisted       bool     `json:"unlisted"`
	Aliases        []string `json:"aliases"`
	// PreserveThinking: nil => on (Qwen3.6 default), false => disabled.
	PreserveThinking *bool `json:"preserveThinking"`
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
	// Image (sd-server) knobs; empty => inherit the model-wide override.
	VaePath         string  `json:"vaePath"`
	ClipLPath       string  `json:"clipLPath"`
	ClipGPath       string  `json:"clipGPath"`
	T5Path          string  `json:"t5Path"`
	TextEncoderPath string  `json:"textEncoderPath"`
	OffloadToCpu    string  `json:"offloadToCpu"`
	TeOnCpu         string  `json:"teOnCpu"`
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
	Ctx             int      `json:"ctx"`
	KvK             string   `json:"kvK"`
	KvV             string   `json:"kvV"`
	KvInRam         bool     `json:"kvInRam"`
	VramTargetGB    float64  `json:"vramTargetGB"`
	CpuOffload      int      `json:"cpuOffload"`
	Spec            string   `json:"spec"`
	ReasoningFmt    string   `json:"reasoningFmt"`
	ReasoningBudget int      `json:"reasoningBudget"`
	FlashAttn       string   `json:"flashAttn"`
	Mmap            string   `json:"mmap"`
	Mlock           bool     `json:"mlock"`
	Threads         int      `json:"threads"`
	Parallel        int      `json:"parallel"`
	Ub              int      `json:"ub"`
	ExtraArgs       string   `json:"extraArgs"`
	Aliases         []string `json:"aliases"`
	Unlisted        bool     `json:"unlisted"`
	Skip            bool     `json:"skip"`
	CtxVariants     []int    `json:"ctxVariants"` // per-model ctx tiers (e.g. 32768, 65536)
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
	// Image (sd-server) knobs; ignored for llama models.
	VaePath         string  `json:"vaePath"`
	ClipLPath       string  `json:"clipLPath"`
	ClipGPath       string  `json:"clipGPath"`
	T5Path          string  `json:"t5Path"`
	TextEncoderPath string  `json:"textEncoderPath"`
	OffloadToCpu    string  `json:"offloadToCpu"`
	TeOnCpu         string  `json:"teOnCpu"`
	VaeTiling       string  `json:"vaeTiling"`
	DiffusionFa     string  `json:"diffusionFa"`
	DefaultSteps    int     `json:"defaultSteps"`
	DefaultCfg      float64 `json:"defaultCfg"`
	DefaultSampler  string  `json:"defaultSampler"`
	DefaultWidth    int     `json:"defaultWidth"`
	DefaultHeight   int     `json:"defaultHeight"`
}

type modelConfigResp struct {
	Id          string       `json:"id"`
	Gguf        string       `json:"gguf"`
	Cmd         string       `json:"cmd"`
	MaxCtx      int          `json:"maxCtx"`     // trained context length (slider ceiling); 0 if unknown
	BlockCount  int          `json:"blockCount"` // transformer layers (denominator for -ngl); 0 if unknown
	IsMTP       bool         `json:"isMTP"`      // model has nextn/MTP layers => draft-mtp usable
	IsImage     bool         `json:"isImage"`    // diffusion model (sd-server) => image config form
	HasOverride bool         `json:"hasOverride"`
	Override    *overrideDTO `json:"override"`
	// DefaultVariants are the fleet-wide settings.defaultVariants (e.g. game),
	// shared by every model. Editable here but saved globally (PUT /api/default-variants).
	DefaultVariants []variantDTO `json:"defaultVariants"`
}

func variantToDTO(v autogen.VariantSpec) variantDTO {
	return variantDTO{
		Name: v.Name, Ctx: v.Ctx, VramTargetGB: v.VramTargetGB,
		KvK: v.KvK, KvV: v.KvV, Spec: v.Spec, ReasoningFmt: v.ReasoningFmt,
		Ub: v.Ub, Dry: v.Dry, CtxCheckpoints: v.CtxCheckpoints,
		Unlisted: v.Unlisted, Aliases: v.Aliases, PreserveThinking: v.PreserveThinking,
		KvInRam: v.KvInRam, CpuOffload: v.CpuOffload,
		FlashAttn: v.FlashAttn, Mmap: v.Mmap, Mlock: v.Mlock,
		Threads: v.Threads, Parallel: v.Parallel, ExtraArgs: v.ExtraArgs,
		DryMultiplier: v.DryMultiplier, DryBase: v.DryBase, DryAllowedLength: v.DryAllowedLength,
		SpecDraftNMax: v.SpecDraftNMax, SpecDefault: v.SpecDefault,
		SpecNgramSizeN: v.SpecNgramSizeN, SpecNgramSizeM: v.SpecNgramSizeM, SpecNgramMinHits: v.SpecNgramMinHits,
		VaePath: v.VaePath, ClipLPath: v.ClipLPath, ClipGPath: v.ClipGPath,
		T5Path: v.T5Path, TextEncoderPath: v.TextEncoderPath,
		OffloadToCpu: v.OffloadToCpu, TeOnCpu: v.TeOnCpu, VaeTiling: v.VaeTiling, DiffusionFa: v.DiffusionFa,
		DefaultSteps: v.DefaultSteps, DefaultCfg: v.DefaultCfg, DefaultSampler: v.DefaultSampler,
		DefaultWidth: v.DefaultWidth, DefaultHeight: v.DefaultHeight,
	}
}

func toOverrideDTO(o autogen.Override) *overrideDTO {
	dto := &overrideDTO{
		Ctx: o.Ctx, KvK: o.KvK, KvV: o.KvV, KvInRam: o.KvInRam,
		VramTargetGB: o.VramTargetGB, CpuOffload: o.CpuOffload,
		Spec: o.Spec, ReasoningFmt: o.ReasoningFmt, ReasoningBudget: o.ReasoningBudget,
		FlashAttn: o.FlashAttn, Mmap: o.Mmap, Mlock: o.Mlock,
		Threads: o.Threads, Parallel: o.Parallel, Ub: o.Ub,
		ExtraArgs: o.ExtraArgs,
		Aliases:   o.Aliases, Unlisted: o.Unlisted, Skip: o.Skip,
		CtxVariants: o.CtxVariants, CtxCheckpoints: o.CtxCheckpoints,
		PreserveThinking: o.PreserveThinking,
		Dry:              o.Dry,
		DryMultiplier:    o.DryMultiplier, DryBase: o.DryBase, DryAllowedLength: o.DryAllowedLength,
		SpecDraftNMax: o.SpecDraftNMax, SpecDefault: o.SpecDefault,
		SpecNgramSizeN: o.SpecNgramSizeN, SpecNgramSizeM: o.SpecNgramSizeM, SpecNgramMinHits: o.SpecNgramMinHits,
		VaePath: o.VaePath, ClipLPath: o.ClipLPath, ClipGPath: o.ClipGPath,
		T5Path: o.T5Path, TextEncoderPath: o.TextEncoderPath,
		OffloadToCpu: o.OffloadToCpu, TeOnCpu: o.TeOnCpu, VaeTiling: o.VaeTiling, DiffusionFa: o.DiffusionFa,
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
		Unlisted: v.Unlisted, Aliases: v.Aliases, PreserveThinking: v.PreserveThinking,
		KvInRam: v.KvInRam, CpuOffload: v.CpuOffload,
		FlashAttn: v.FlashAttn, Mmap: v.Mmap, Mlock: v.Mlock,
		Threads: v.Threads, Parallel: v.Parallel, ExtraArgs: v.ExtraArgs,
		DryMultiplier: v.DryMultiplier, DryBase: v.DryBase, DryAllowedLength: v.DryAllowedLength,
		SpecDraftNMax: v.SpecDraftNMax, SpecDefault: v.SpecDefault,
		SpecNgramSizeN: v.SpecNgramSizeN, SpecNgramSizeM: v.SpecNgramSizeM, SpecNgramMinHits: v.SpecNgramMinHits,
		VaePath: v.VaePath, ClipLPath: v.ClipLPath, ClipGPath: v.ClipGPath,
		T5Path: v.T5Path, TextEncoderPath: v.TextEncoderPath,
		OffloadToCpu: v.OffloadToCpu, TeOnCpu: v.TeOnCpu, VaeTiling: v.VaeTiling, DiffusionFa: v.DiffusionFa,
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
	realID, found := s.cfg.RealModelName(requested)
	if !found {
		shared.SendResponse(w, r, http.StatusNotFound, "model not found")
		return "", "", "", false
	}
	cmd = s.cfg.Models[realID].Cmd
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
	// Detect from the rendered command — the sd-server path always carries it.
	isImage := strings.Contains(cmd, "--diffusion-model")
	resp := modelConfigResp{Id: realID, Gguf: gguf, Cmd: strings.TrimSpace(cmd), IsImage: isImage, HasOverride: existing != nil}
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
	}
	// Read trained ctx + MTP capability from the gguf header (cheap; header only).
	// Non-fatal: a missing/unreadable gguf just leaves the slider ceiling at 0.
	if meta, err := autogen.ReadGgufMetadataCached(gguf); err == nil {
		resp.MaxCtx = int(meta.ContextLength)
		resp.BlockCount = int(meta.BlockCount)
		// MTP-capable via baked-in nextn layers OR a separate draft file paired in
		// the cmd (-md, e.g. Gemma-4). Either makes draft-mtp a valid spec choice.
		resp.IsMTP = meta.IsMTP || strings.Contains(cmd, "--spec-type draft-mtp") || strings.Contains(cmd, "-md ")
	}
	// Fleet-wide default variants (e.g. game) so the editor can surface + edit them.
	if gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir); err == nil {
		for _, v := range gf.Settings.DefaultVariants {
			resp.DefaultVariants = append(resp.DefaultVariants, variantToDTO(v))
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

func (s *Server) handleAPIModelEstimate(w http.ResponseWriter, r *http.Request) {
	_, gguf, cmd, ok := s.resolveModelGguf(w, r)
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
	// actual=true: seed from the loaded command so the status rail breaks down
	// the variant that's really running. Otherwise (config editor) start blank so
	// omitted fields fall back to the sizer's auto choices.
	var in autogen.EstimateInput
	if q.Get("actual") == "true" {
		in = estimateInputFromCmd(cmd)
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
	}
	if v := q.Get("cpuOffload"); v != "" {
		in.CpuOffload, _ = strconv.Atoi(v)
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
}

type settingsResp struct {
	TargetVramGB   float64          `json:"targetVramGB"`
	VramOverheadGB float64          `json:"vramOverheadGB"`
	MaxRamGB       float64          `json:"maxRamGB"`
	AutoVram       bool             `json:"autoVram"`
	Overridden     bool             `json:"overridden"` // a UI sidecar patch is active
	Defaults       settingsDefaults `json:"defaults"`   // values a reset reverts to
	ModelsRoot     string           `json:"modelsRoot"` // the shared/fallback scan folder
	// CategoryRoots is the effective per-category scan folder ("" => uses ModelsRoot).
	CategoryRoots map[string]string `json:"categoryRoots"`
}

type settingsPutDTO struct {
	TargetVramGB   float64 `json:"targetVramGB"`
	VramOverheadGB float64 `json:"vramOverheadGB"`
	MaxRamGB       float64 `json:"maxRamGB"`
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
	writeJSON(w, settingsResp{
		TargetVramGB:   gf.Settings.TargetVramGB,
		VramOverheadGB: gf.Settings.VramOverheadGB,
		MaxRamGB:       gf.Settings.MaxRamGB,
		AutoVram:       gf.Settings.AutoVram,
		Overridden:     patch != nil,
		Defaults: settingsDefaults{
			TargetVramGB:   base.TargetVramGB,
			VramOverheadGB: base.VramOverheadGB,
			MaxRamGB:       base.MaxRamGB,
		},
		ModelsRoot:    gf.Settings.ModelsRoot,
		CategoryRoots: gf.Settings.CategoryRoots,
	})
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
	autoOff := false
	patch := autogen.SettingsPatch{
		TargetVramGB:   &body.TargetVramGB,
		VramOverheadGB: &body.VramOverheadGB,
		MaxRamGB:       &body.MaxRamGB,
		AutoVram:       &autoOff,
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
	ov.Aliases = body.Aliases
	ov.Unlisted = body.Unlisted
	ov.Skip = body.Skip
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
	ov.VaePath = strings.TrimSpace(body.VaePath)
	ov.ClipLPath = strings.TrimSpace(body.ClipLPath)
	ov.ClipGPath = strings.TrimSpace(body.ClipGPath)
	ov.T5Path = strings.TrimSpace(body.T5Path)
	ov.TextEncoderPath = strings.TrimSpace(body.TextEncoderPath)
	ov.OffloadToCpu = body.OffloadToCpu
	ov.TeOnCpu = body.TeOnCpu
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
	meta, err := autogen.ReadGgufMetadataCached(gguf)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "reading gguf metadata failed: "+err.Error())
		return
	}
	ov := autogen.Override{Match: gguf}
	applyOverrideDTO(&ov, body)
	cmd, err := autogen.RenderSoloCmd(gf.Settings, meta, autogen.GgufRow{FullPath: gguf}, ov)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "rendering command failed: "+err.Error())
		return
	}
	writeJSON(w, map[string]string{"cmd": cmd})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// filepathSlash normalizes separators for case/sep-insensitive path compares.
func filepathSlash(p string) string { return strings.ReplaceAll(p, "\\", "/") }
