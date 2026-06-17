package server

import (
	"encoding/json"
	"net/http"
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

// variantDTO is the JSON shape of one named custom variant.
type variantDTO struct {
	Name         string   `json:"name"`
	Ctx          int      `json:"ctx"`
	VramTargetGB float64  `json:"vramTargetGB"`
	KvK          string   `json:"kvK"`
	KvV          string   `json:"kvV"`
	Spec         string   `json:"spec"`
	ReasoningFmt string   `json:"reasoningFmt"`
	Unlisted     bool     `json:"unlisted"`
	Aliases      []string `json:"aliases"`
}

// overrideDTO is the curated JSON shape of a per-model override (the cogwheel
// fields) plus its named variants.
type overrideDTO struct {
	Ctx          int          `json:"ctx"`
	KvK          string       `json:"kvK"`
	KvV          string       `json:"kvV"`
	KvInRam      bool         `json:"kvInRam"`
	Spec         string       `json:"spec"`
	ReasoningFmt string       `json:"reasoningFmt"`
	Aliases      []string     `json:"aliases"`
	Unlisted     bool         `json:"unlisted"`
	Skip         bool         `json:"skip"`
	Variants     []variantDTO `json:"variants"`
}

type modelConfigResp struct {
	Id          string       `json:"id"`
	Gguf        string       `json:"gguf"`
	Cmd         string       `json:"cmd"`
	MaxCtx      int          `json:"maxCtx"` // trained context length (slider ceiling); 0 if unknown
	IsMTP       bool         `json:"isMTP"`  // model has nextn/MTP layers => draft-mtp usable
	HasOverride bool         `json:"hasOverride"`
	Override    *overrideDTO `json:"override"`
}

func toOverrideDTO(o autogen.Override) *overrideDTO {
	dto := &overrideDTO{
		Ctx: o.Ctx, KvK: o.KvK, KvV: o.KvV, KvInRam: o.KvInRam,
		Spec: o.Spec, ReasoningFmt: o.ReasoningFmt,
		Aliases: o.Aliases, Unlisted: o.Unlisted, Skip: o.Skip,
	}
	for _, v := range o.Variants {
		dto.Variants = append(dto.Variants, variantDTO{
			Name: v.Name, Ctx: v.Ctx, VramTargetGB: v.VramTargetGB,
			KvK: v.KvK, KvV: v.KvV, Spec: v.Spec, ReasoningFmt: v.ReasoningFmt,
			Unlisted: v.Unlisted, Aliases: v.Aliases,
		})
	}
	return dto
}

func toVariantSpec(v variantDTO) autogen.VariantSpec {
	return autogen.VariantSpec{
		Name: v.Name, Ctx: v.Ctx, VramTargetGB: v.VramTargetGB,
		KvK: v.KvK, KvV: v.KvV, Spec: v.Spec, ReasoningFmt: v.ReasoningFmt,
		Unlisted: v.Unlisted, Aliases: v.Aliases,
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
	if existing != nil {
		resp.Override = toOverrideDTO(*existing)
	}
	// Read trained ctx + MTP capability from the gguf header (cheap; header only).
	// Non-fatal: a missing/unreadable gguf just leaves the slider ceiling at 0.
	if meta, err := autogen.ReadGgufMetadata(gguf); err == nil {
		resp.MaxCtx = int(meta.ContextLength)
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
	_, ov, err := s.findSidecarOverride(gguf)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	// Replace the whole override from the body: the editor owns the complete
	// picture (it loaded the current state via GET), so curated fields and the
	// variants list are both authoritative here. The incremental variant
	// endpoint remains for API callers that only want to add one.
	ov.Match = gguf
	ov.Ctx = body.Ctx
	ov.KvK = body.KvK
	ov.KvV = body.KvV
	ov.KvInRam = body.KvInRam
	ov.Spec = body.Spec
	ov.ReasoningFmt = body.ReasoningFmt
	ov.Aliases = body.Aliases
	ov.Unlisted = body.Unlisted
	ov.Skip = body.Skip
	ov.Variants = ov.Variants[:0]
	for _, v := range body.Variants {
		ov.Variants = append(ov.Variants, toVariantSpec(v))
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
	_, ov, err := s.findSidecarOverride(gguf)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// filepathSlash normalizes separators for case/sep-insensitive path compares.
func filepathSlash(p string) string { return strings.ReplaceAll(p, "\\", "/") }
