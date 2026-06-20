package server

import (
	"encoding/json"
	"net/http"
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
	// Engine knobs (variant carries the full launch shape; zero/empty => inherit).
	KvInRam    bool   `json:"kvInRam"`
	CpuOffload int    `json:"cpuOffload"`
	FlashAttn  string `json:"flashAttn"`
	Mmap       string `json:"mmap"`
	Mlock      bool   `json:"mlock"`
	Threads    int    `json:"threads"`
	Parallel   int    `json:"parallel"`
	ExtraArgs  string `json:"extraArgs"`
}

// overrideDTO is the curated JSON shape of a per-model override (the cogwheel
// fields) plus its named variants.
type overrideDTO struct {
	Ctx          int          `json:"ctx"`
	KvK          string       `json:"kvK"`
	KvV          string       `json:"kvV"`
	KvInRam      bool         `json:"kvInRam"`
	VramTargetGB float64      `json:"vramTargetGB"`
	CpuOffload   int          `json:"cpuOffload"`
	Spec         string       `json:"spec"`
	ReasoningFmt string       `json:"reasoningFmt"`
	FlashAttn    string       `json:"flashAttn"`
	Mmap         string       `json:"mmap"`
	Mlock        bool         `json:"mlock"`
	Threads      int          `json:"threads"`
	Parallel     int          `json:"parallel"`
	Ub           int          `json:"ub"`
	ExtraArgs    string       `json:"extraArgs"`
	Aliases      []string     `json:"aliases"`
	Unlisted     bool         `json:"unlisted"`
	Skip         bool         `json:"skip"`
	Variants     []variantDTO `json:"variants"`
}

type modelConfigResp struct {
	Id          string       `json:"id"`
	Gguf        string       `json:"gguf"`
	Cmd         string       `json:"cmd"`
	MaxCtx      int          `json:"maxCtx"`     // trained context length (slider ceiling); 0 if unknown
	BlockCount  int          `json:"blockCount"` // transformer layers (denominator for -ngl); 0 if unknown
	IsMTP       bool         `json:"isMTP"`      // model has nextn/MTP layers => draft-mtp usable
	HasOverride bool         `json:"hasOverride"`
	Override    *overrideDTO `json:"override"`
}

func toOverrideDTO(o autogen.Override) *overrideDTO {
	dto := &overrideDTO{
		Ctx: o.Ctx, KvK: o.KvK, KvV: o.KvV, KvInRam: o.KvInRam,
		VramTargetGB: o.VramTargetGB, CpuOffload: o.CpuOffload,
		Spec: o.Spec, ReasoningFmt: o.ReasoningFmt,
		FlashAttn: o.FlashAttn, Mmap: o.Mmap, Mlock: o.Mlock,
		Threads: o.Threads, Parallel: o.Parallel, Ub: o.Ub,
		ExtraArgs: o.ExtraArgs,
		Aliases:   o.Aliases, Unlisted: o.Unlisted, Skip: o.Skip,
	}
	for _, v := range o.Variants {
		dto.Variants = append(dto.Variants, variantDTO{
			Name: v.Name, Ctx: v.Ctx, VramTargetGB: v.VramTargetGB,
			KvK: v.KvK, KvV: v.KvV, Spec: v.Spec, ReasoningFmt: v.ReasoningFmt,
			Ub: v.Ub, Dry: v.Dry, CtxCheckpoints: v.CtxCheckpoints,
			Unlisted: v.Unlisted, Aliases: v.Aliases,
			KvInRam: v.KvInRam, CpuOffload: v.CpuOffload,
			FlashAttn: v.FlashAttn, Mmap: v.Mmap, Mlock: v.Mlock,
			Threads: v.Threads, Parallel: v.Parallel, ExtraArgs: v.ExtraArgs,
		})
	}
	return dto
}

func toVariantSpec(v variantDTO) autogen.VariantSpec {
	return autogen.VariantSpec{
		Name: v.Name, Ctx: v.Ctx, VramTargetGB: v.VramTargetGB,
		KvK: v.KvK, KvV: v.KvV, Spec: v.Spec, ReasoningFmt: v.ReasoningFmt,
		Ub: v.Ub, Dry: v.Dry, CtxCheckpoints: v.CtxCheckpoints,
		Unlisted: v.Unlisted, Aliases: v.Aliases,
		KvInRam: v.KvInRam, CpuOffload: v.CpuOffload,
		FlashAttn: v.FlashAttn, Mmap: v.Mmap, Mlock: v.Mlock,
		Threads: v.Threads, Parallel: v.Parallel, ExtraArgs: v.ExtraArgs,
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
	resp := modelConfigResp{Id: realID, Gguf: gguf, Cmd: strings.TrimSpace(cmd), HasOverride: existing != nil}
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
		resp.IsMTP = meta.IsMTP
	}
	writeJSON(w, resp)
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
			if v, ok := next(); ok {
				in.Spec = v
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
	})
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
