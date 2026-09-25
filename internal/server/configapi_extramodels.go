package server

// The "Add model manually" table: image models the scan cannot classify
// (single-file safetensors DiTs with a separate VAE / text encoder), declared
// from explicit paths and persisted in the sidecar so a regen keeps them.
//
// Whole-list GET/PUT like the prompt-enhancer manager, with one twist: rows
// from the generate file's settings.extraImageModels (source "file") are shown
// read-only but CAN be deleted. The generate file is the user's hand-edited
// source and is never rewritten, so deleting a file row records its name in
// the sidecar's removed list instead. The PUT body is the table as shown:
// its "ui" rows are stored, its "file" rows are only a presence check. Tuning (cfg,
// steps, FA, offload...) is not in this table at all: an extra model is a normal
// catalog entry, so the model config editor edits it through an override keyed
// by its model path, same as a discovered one.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// extraModelDTO is one row of the Settings page's manual-model table.
type extraModelDTO struct {
	Name      string `json:"name"`
	ModelPath string `json:"modelPath"`
	// ModelFlag is "-m" (all-in-one checkpoint, the default) or
	// "--diffusion-model" (a bare DiT wired to separate encoders).
	ModelFlag string `json:"modelFlag"`
	VaePath   string `json:"vaePath"`
	LlmPath   string `json:"llmPath"`
	ClipLPath string `json:"clipLPath"`
	ClipGPath string `json:"clipGPath"`
	T5Path    string `json:"t5Path"`
	// Source is "ui" (sidecar, editable) or "file" (the generate file,
	// read-only here). On PUT a "file" row is kept as-is and never copied.
	Source string `json:"source"`
}

func sideNames(side []autogen.ExtraImageModel) []string {
	out := make([]string, 0, len(side))
	for _, m := range side {
		out = append(out, m.Name)
	}
	return out
}

func extraModelToDTO(m autogen.ExtraImageModel, source string) extraModelDTO {
	return extraModelDTO{
		Name: m.Name, ModelPath: m.ModelPath, ModelFlag: m.ModelFlag,
		VaePath: m.VaePath, LlmPath: m.LlmPath, ClipLPath: m.ClipLPath,
		ClipGPath: m.ClipGPath, T5Path: m.T5Path, Source: source,
	}
}

// handleExtraModelsGet lists the EFFECTIVE extra image models (file + UI),
// each tagged with who owns it.
func (s *Server) handleExtraModelsGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	side, err := autogen.LoadSidecarExtraImageModels(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	ui := map[string]bool{}
	for _, m := range side {
		ui[strings.ToLower(m.Name)] = true
	}
	out := make([]extraModelDTO, 0, len(gf.Settings.ExtraImageModels))
	for _, m := range gf.Settings.ExtraImageModels {
		src := "file"
		if ui[strings.ToLower(strings.TrimSpace(m.Name))] {
			src = "ui"
		}
		out = append(out, extraModelToDTO(m, src))
	}
	writeJSON(w, out)
}

// handleExtraModelsPut stores the table as shown, then regenerates + reloads.
// A file row missing from the body is deleted (added to the removed list); a
// name that comes back as a "ui" row is no longer removed.
//
// Validation happens HERE rather than only in the emitter, because the emitter's
// answer to a bad row is a "# SKIPPED" comment in a file the user never opens:
// the save would succeed and the model would just not appear. So a missing
// file, an unknown model flag, or a name that already belongs to a discovered
// model is a 4xx on the save itself.
func (s *Server) handleExtraModelsPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	var body []extraModelDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	side, err := autogen.LoadSidecarExtraImageModels(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	removed, err := autogen.LoadSidecarRemovedExtraImageModels(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	fileNames, err := autogen.FileExtraImageModelNames(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	// Names that are extras today, or were until deleted here: re-saving one is
	// not a clash with "the model already served under that id", since that
	// model IS this row.
	current := map[string]bool{}
	for _, n := range append(append(slices.Clone(removed), fileNames...), sideNames(side)...) {
		current[strings.ToLower(n)] = true
	}
	// A file row absent from the body was deleted in the table. That includes
	// one a UI row was shadowing: trashing the row the user sees must not bring
	// the file's version back. Upsert drops any name the body still keeps.
	inBody := map[string]bool{}
	for _, e := range body {
		inBody[strings.ToLower(strings.TrimSpace(e.Name))] = true
	}
	for _, n := range fileNames {
		if !inBody[strings.ToLower(n)] {
			removed = append(removed, n)
		}
	}
	served := s.config().Models

	list := make([]autogen.ExtraImageModel, 0, len(body))
	for _, e := range body {
		if e.Source == "file" {
			continue // presence only: never copied into the sidecar
		}
		name := strings.TrimSpace(e.Name)
		path := strings.TrimSpace(e.ModelPath)
		if name == "" && path == "" {
			continue // the table's blank "add one" row
		}
		if name == "" || path == "" {
			shared.SendResponse(w, r, http.StatusBadRequest, fmt.Sprintf("model %q: name and model file are both required", name))
			return
		}
		flag := strings.TrimSpace(e.ModelFlag)
		if flag != "" && flag != "-m" && flag != "--diffusion-model" {
			shared.SendResponse(w, r, http.StatusBadRequest, fmt.Sprintf("model %q: model flag must be -m or --diffusion-model", name))
			return
		}
		if _, taken := served[name]; taken && !current[strings.ToLower(name)] {
			shared.SendResponse(w, r, http.StatusConflict, fmt.Sprintf("%q is already the id of a discovered model; pick another name", name))
			return
		}
		m := autogen.ExtraImageModel{
			Name: name, ModelPath: path, ModelFlag: flag,
			VaePath: strings.TrimSpace(e.VaePath), LlmPath: strings.TrimSpace(e.LlmPath),
			ClipLPath: strings.TrimSpace(e.ClipLPath), ClipGPath: strings.TrimSpace(e.ClipGPath),
			T5Path: strings.TrimSpace(e.T5Path),
		}
		for _, f := range [][2]string{
			{"model file", m.ModelPath}, {"VAE", m.VaePath}, {"LLM encoder", m.LlmPath},
			{"CLIP-L", m.ClipLPath}, {"CLIP-G", m.ClipGPath}, {"T5", m.T5Path},
		} {
			if f[1] == "" {
				continue
			}
			if fi, err := os.Stat(f[1]); err != nil || fi.IsDir() {
				shared.SendResponse(w, r, http.StatusBadRequest, fmt.Sprintf("model %q: %s %q is not a file", name, f[0], f[1]))
				return
			}
		}
		list = append(list, m)
	}
	if err := autogen.UpsertSidecarExtraImageModels(s.autogen.GeneratePath, list, removed); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.proxylog.Infof("extraImageModels: saved %d UI entr(ies)", len(list))
	if !s.regenAndReload(w, r) {
		return
	}
	out := make([]extraModelDTO, 0, len(list))
	for _, m := range list {
		out = append(out, extraModelToDTO(m, "ui"))
	}
	writeJSON(w, out)
}
