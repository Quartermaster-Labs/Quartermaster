package server

// Ad-hoc launch commands: render (and optionally inject into the live router)
// a one-off VRAM-sized command for a model with sparse flag overrides, with
// nothing persisted to disk. Meant for bench scripts that want a properly
// sized arg combo without adding a permanent catalog entry.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

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
	v, _ := config.ParseCmd(cmd).Value("--port", "-port")
	return v
}
