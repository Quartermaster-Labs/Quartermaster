package server

// The prompt-enhancer manager: the list of rewrite models an image model may
// hand its prompt to, each with the fixed system prompt it was trained under.
//
// Whole-list GET/PUT rather than the per-row upsert/delete pair the API-key
// manager uses, because an enhancer has no server-minted secret to preserve
// across an edit. The row IS its content, so sending the table back is both
// simpler and free of the "rename = delete + create, losing the key" problem
// that shaped the apikeys endpoints.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// promptEnhancerDTO is one row of the Settings page's enhancer table.
type promptEnhancerDTO struct {
	Model        string `json:"model"`
	Name         string `json:"name"`
	SystemPrompt string `json:"systemPrompt"`
	Vision       bool   `json:"vision"`
}

// handlePromptEnhancersGet lists the configured enhancers. Reads the EFFECTIVE
// list (sidecar if the UI has written one, else the generate file's), so the
// table shows what generation will actually use rather than only what the UI
// happens to own.
func (s *Server) handlePromptEnhancersGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]promptEnhancerDTO, 0, len(gf.Settings.PromptEnhancers))
	for _, e := range gf.Settings.PromptEnhancers {
		out = append(out, promptEnhancerDTO{
			Model:        e.Model,
			Name:         e.Name,
			SystemPrompt: e.SystemPrompt,
			Vision:       e.Vision,
		})
	}
	writeJSON(w, out)
}

// handlePromptEnhancersPut replaces the whole list, then regenerates + reloads
// so an edited system prompt reaches the playground on the next model listing.
//
// An empty array is a legitimate body (the user deleted the last row), which is
// why this is PUT-with-a-list rather than a per-row delete: there is no
// ambiguity between "clear the table" and "no change".
func (s *Server) handlePromptEnhancersPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	var body []promptEnhancerDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	list := make([]autogen.PromptEnhancer, 0, len(body))
	for _, e := range body {
		if strings.TrimSpace(e.Model) == "" {
			// A blank row is how the table renders "add one"; dropping it here
			// rather than rejecting the save means a half-filled row does not
			// block edits to the rows above it.
			continue
		}
		list = append(list, autogen.PromptEnhancer{
			Model:        e.Model,
			Name:         e.Name,
			SystemPrompt: e.SystemPrompt,
			Vision:       e.Vision,
		})
	}
	if err := autogen.UpsertSidecarPromptEnhancers(s.autogen.GeneratePath, list); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.proxylog.Infof("promptEnhancers: saved %d entr(ies)", len(list))
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, list)
}

// detectedEnhancerDTO is one NAME-DETECTED rewriter: a catalog model whose id
// says it is a prompt enhancer (see autogen.PromptEnhancerID).
type detectedEnhancerDTO struct {
	Model string `json:"model"`
	// Direction is "t2i", "i2i", or "" when the name says enhancer but not which
	// way. The model editor uses it to suggest the right id per field.
	Direction string `json:"direction"`
	// ImageModel is the family key of the image model the name was built from
	// ("qwen-image-2.1"), which is what pairs the two without asking anyone.
	ImageModel string `json:"imageModel"`
}

// handlePromptEnhancersDetected lists the enhancer candidates found by name.
//
// Read off the LOADED CONFIG rather than a fresh disk scan: every discovered
// gguf is already a model entry, and an enhancer has to be one to be callable at
// all (it is dispatched as the `model` of a chat request through the one
// scheduler). So the catalog is both the cheapest and the most accurate source -
// it can only suggest ids that actually resolve.
//
// Suggestions only. The model editor's enhancer fields stay free text, because
// a classifier that works on every name a publisher will ever choose does not
// exist, and being unable to type the id of a model you can see in the list
// would be the worse failure.
func (s *Server) handlePromptEnhancersDetected(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	out := make([]detectedEnhancerDTO, 0, 4)
	for id := range cfg.Models {
		dir, family, ok := autogen.PromptEnhancerID(id)
		if !ok {
			continue
		}
		out = append(out, detectedEnhancerDTO{Model: id, Direction: dir, ImageModel: family})
	}
	// Stable order: the map iteration is random and this feeds a picker list.
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	writeJSON(w, out)
}
