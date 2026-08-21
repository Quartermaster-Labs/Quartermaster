package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// chatTemplateErr validates a --chat-template-file path at SAVE time, returning
// a message when it can't be used. llama-server refuses to start on a missing
// template, so an unchecked path turns into a dead model at its next load — far
// from the edit that caused it. It also stops a chat model driving
// quartermaster_configure from persisting an invented-but-plausible path
// (a real one: it guessed a repo-style `chat_template.jinja` next to the gguf).
// Relative paths resolve against the server's cwd, like the built-in template.
func chatTemplateErr(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	st, err := os.Stat(p)
	if err != nil {
		return "chat template file not found: " + p
	}
	if st.IsDir() {
		return "chat template path is a directory, not a file: " + p
	}
	return ""
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

// modelsRoots resolves the folders discovery scans (main root + category roots),
// or nil when settings can't be loaded. Sidecar lookups that reach beyond a
// model's own dir (an inherited drafter, autogen/family.go) need them.
func (s *Server) modelsRoots() []string {
	if s.autogen == nil {
		return nil
	}
	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		return nil
	}
	return gf.Settings.RootList()
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
		if config.PathEqual(rows[i].Match, gguf) {
			return &rows[i], rows[i], nil
		}
	}
	return nil, autogen.Override{Match: gguf}, nil
}

// regenAndReload writes the config from the (now-updated) sidecar + generate
// file and hot-reloads. Slow (reads gguf metadata) but it's a settings save.
func (s *Server) regenAndReload(w http.ResponseWriter, r *http.Request) bool {
	a := s.autogen
	if _, err := autogen.EnsureConfig(a.GeneratePath, a.ConfigPath, a.ModelsDir, noticeLogger(s.proxylog)); err != nil {
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
	mc, hasMC := s.config().Models[realID]
	info := config.ParseCmd(cmd)
	isImage := info.Has("--diffusion-model")
	if hasMC && slices.Contains(mc.Capabilities.Out, "image") {
		isImage = true
	}
	// Speech models get the audio form: no KV/ctx/spec/estimate. The declared
	// capability is the discriminator — out:[audio] is TTS, in:[audio] is ASR.
	// This used to sniff `--model` off the rendered command, which broke twice: it
	// swept in ASR/SAM (they use --model too, SAM only escaping via the explicit
	// guard above), and it MISSED TTS.cpp, whose flag is --model-path — a Kokoro
	// model then rendered the whole llama.cpp KV/offload form and offered LLM
	// backends. The --codec fallback keeps a hand-written qwentts config with no
	// capabilities block on the audio form.
	isTTS := info.Has("--codec") || (hasMC && slices.Contains(mc.Capabilities.Out, "audio"))
	isASR := hasMC && slices.Contains(mc.Capabilities.In, "audio")
	isAudio := isTTS || isASR
	if isSam {
		isImage, isAudio, isTTS, isASR = false, false, false, false
	}
	// Class is what the UI filters the backend picker by, so it must name the
	// engine class autogen resolves against (kindClass), not just the form shape:
	// TTS and ASR share the audio form but not their backends.
	class := "llm"
	switch {
	case isSam:
		class = "segment"
	case isImage:
		class = "image"
	case isTTS:
		class = "tts"
	case isASR:
		class = "asr"
	}
	resp := modelConfigResp{Id: realID, Gguf: gguf, Cmd: strings.TrimSpace(cmd), IsImage: isImage, IsAudio: isAudio, IsSam: isSam, Class: class, HasOverride: existing != nil}
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
		// "Paired" includes a drafter inherited from a family sibling, which is
		// what the generator would launch this model with (autogen/family.go).
		roots := s.modelsRoots()
		draftPath, draftKind, _, inherited := autogen.DraftSidecarFor(roots, gguf)
		// --spec-type is repeatable and the emitter may put it on its own line, so
		// match it as a token/value pair rather than as a raw substring.
		resp.IsMTP = meta.IsMTP || draftKind == "mtp" || info.HasValue("draft-mtp", "--spec-type")
		resp.IsDflash = draftKind == "dflash" || info.HasValue("draft-dflash", "--spec-type")
		resp.DraftPath = draftPath
		resp.DraftInherited = inherited
		// The projector the "-vision" twin loads, and whether it came from this
		// model's folder. Same roots, so the index behind it is already warm.
		resp.MmprojPath, _, resp.MmprojInherited = autogen.MmprojSidecarFor(roots, gguf)
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
	if msg := chatTemplateErr(body.ChatTemplateFile); msg != "" {
		shared.SendResponse(w, r, http.StatusBadRequest, msg)
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
	// An override rewrites the model's launch args on its next load, which is
	// the usual answer to "why is it slower / why did it stop fitting today".
	// The access log shows that a PUT happened; this says which model it hit.
	s.proxylog.Infof("config: saved overrides for %s", filepath.Base(gguf))
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
	s.proxylog.Infof("config: reset overrides for %s to the autogen defaults", filepath.Base(gguf))
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
	if msg := chatTemplateErr(v.ChatTemplateFile); msg != "" {
		shared.SendResponse(w, r, http.StatusBadRequest, msg)
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
