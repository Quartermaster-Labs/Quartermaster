package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/radu0120/llama-quartermaster/internal/autogen"
	"github.com/radu0120/llama-quartermaster/internal/shared"
)

// apiKeyDTO is the JSON shape of one managed API key. Models empty => the key
// has full access (and admin rights over the management endpoints). Builtin
// marks the auto-managed Playground key the UI hides from its key list.
type apiKeyDTO struct {
	Name    string   `json:"name"`
	Key     string   `json:"key"`
	Models  []string `json:"models"`
	Builtin bool     `json:"builtin,omitempty"`
}

// generateAPIKey mints a random secret of the form "qm-<48 hex chars>".
func generateAPIKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "qm-" + hex.EncodeToString(b), nil
}

// handleAPIKeysGet lists the managed API keys (secrets included; the UI hides
// them behind a visibility toggle).
func (s *Server) handleAPIKeysGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	keys, changed, err := autogen.EnsureSidecarPlaygroundKey(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	// A reconciled key set only takes effect once the config is rebuilt so auth
	// accepts (or stops accepting) the Playground key.
	if changed && !s.regenAndReload(w, r) {
		return
	}
	out := make([]apiKeyDTO, 0, len(keys))
	for _, k := range keys {
		out = append(out, apiKeyDTO{
			Name:    k.Name,
			Key:     k.Key,
			Models:  k.Models,
			Builtin: strings.EqualFold(k.Name, autogen.BuiltinPlaygroundKeyName),
		})
	}
	writeJSON(w, out)
}

// handleAPIKeyUpsert creates or updates (by name) one API key. A new name mints
// a fresh secret; an existing name keeps its secret and just updates the model
// scope. Body: {name, models}. Regenerates + reloads so the new key takes
// effect immediately.
func (s *Server) handleAPIKeyUpsert(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	var body apiKeyDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		shared.SendResponse(w, r, http.StatusBadRequest, "key name is required")
		return
	}
	if strings.EqualFold(body.Name, autogen.BuiltinPlaygroundKeyName) {
		shared.SendResponse(w, r, http.StatusBadRequest, "that key name is reserved")
		return
	}

	// Reuse the existing secret when editing a key by name; mint one otherwise.
	existing, err := autogen.LoadSidecarAPIKeys(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	secret := ""
	for _, k := range existing {
		if strings.EqualFold(k.Name, body.Name) {
			secret = k.Key
			break
		}
	}
	if secret == "" {
		if secret, err = generateAPIKey(); err != nil {
			shared.SendResponse(w, r, http.StatusInternalServerError, "generating key failed: "+err.Error())
			return
		}
	}

	entry := autogen.APIKeyEntry{Name: body.Name, Key: secret, Models: sanitizeModelList(body.Models)}
	if _, err := autogen.UpsertSidecarAPIKey(s.autogen.GeneratePath, entry); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	// Add/drop the built-in Playground key as needed (e.g. first scoped key added,
	// or a full-access key now makes it redundant) before the single reload.
	if _, _, err := autogen.EnsureSidecarPlaygroundKey(s.autogen.GeneratePath); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, apiKeyDTO{Name: entry.Name, Key: entry.Key, Models: entry.Models})
}

// handleAPIKeyDelete removes the named API key, then regenerates + reloads.
func (s *Server) handleAPIKeyDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		shared.SendResponse(w, r, http.StatusBadRequest, "key name is required")
		return
	}
	if strings.EqualFold(name, autogen.BuiltinPlaygroundKeyName) {
		shared.SendResponse(w, r, http.StatusBadRequest, "that key is managed automatically")
		return
	}
	removed, err := autogen.DeleteSidecarAPIKey(s.autogen.GeneratePath, name)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !removed {
		shared.SendResponse(w, r, http.StatusNotFound, "key not found")
		return
	}
	// Drop the built-in Playground key once the last user key is gone (so auth
	// turns off), or reconcile it otherwise, before the single reload.
	if _, _, err := autogen.EnsureSidecarPlaygroundKey(s.autogen.GeneratePath); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

// sanitizeModelList trims, drops blanks, and de-duplicates a model-scope list.
// A nil/empty result means full access.
func sanitizeModelList(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}
