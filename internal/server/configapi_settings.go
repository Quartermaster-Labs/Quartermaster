package server

// Global settings editor: the dashboard's GPU-memory card, backend registry,
// slot-cache knobs, and the native folder/file pickers behind them. Everything
// here is -generate-only (see requireAutogen) and writes the autogen sidecar.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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

// isDefaultSlotCachePath reports whether p names the same directory as the
// exe-relative default. Compared as PATHS, not as strings: slotKvPath emits
// forward slashes into the generated YAML while DefaultSlotCachePath returns
// OS-native separators, so a raw `==` misses on Windows and freezes an absolute
// install-relative path into the sidecar — the exact staleness the caller is
// trying to avoid. Case-insensitive because the platform that has the separator
// mismatch also has case-insensitive paths.
func isDefaultSlotCachePath(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(p), filepath.Clean(config.DefaultSlotCachePath()))
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

	// Guards is the OOM-guard + GPU-usage section, Advanced the sizer knobs.
	// Both carry EFFECTIVE values (defaults included, never blanks) and each has
	// its own -Overridden flag, because the two sections reset independently —
	// see configapi_globals.go. AdvancedDefaults is what its reset reverts to.
	Guards             guardsDTO   `json:"guards"`
	GuardsOverridden   bool        `json:"guardsOverridden"`
	Advanced           advancedDTO `json:"advanced"`
	AdvancedDefaults   advancedDTO `json:"advancedDefaults"`
	AdvancedOverridden bool        `json:"advancedOverridden"`
}

// backendsDTO mirrors the effective backend executable paths (llama-server /
// sd-server / tts-server) — the legacy 3-slot view, GET-only. Writes go through
// backendEntryDTO (the registry list); these three are derived from it.
type backendsDTO struct {
	ServerExe    string `json:"serverExe"`
	SdServerExe  string `json:"sdServerExe"`
	TtsServerExe string `json:"ttsServerExe"`
	AsrServerExe string `json:"asrServerExe"`
}

// backendEntryDTO mirrors autogen.BackendEntry — one row of the dashboard's
// backend registry.
type backendEntryDTO struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Default bool   `json:"default"` // the auto-pick for this backend's model class
	// Managed + Component identify a row the in-app installer owns (see
	// backendsapi.go). Read-only for the UI: the path is whatever build is
	// currently activated, and a PUT restores these from the stored row so an
	// unrelated edit elsewhere in the list can't strip a row's provenance.
	Managed   bool   `json:"managed,omitempty"`
	Component string `json:"component,omitempty"`
	Version   string `json:"version,omitempty"`
	Variant   string `json:"variant,omitempty"`
}

// slotCacheDTO mirrors autogen.SlotCacheSettings for the dashboard slot-KV
// section. Zero values fall back to the server's defaults (30k / 10 GB / 20).
type slotCacheDTO struct {
	Enable        bool    `json:"enable"`
	Path          string  `json:"path"`
	MinSaveTokens int     `json:"minSaveTokens"`
	MaxDiskGB     float64 `json:"maxDiskGB"`
	MaxSessions   int     `json:"maxSessions"`
	// PreambleCaches is the preamble (shared system+tools seed) half of the
	// feature. Plain bool, not a tri-state: the dashboard always sends a value,
	// and true is what an absent config key means anyway.
	PreambleCaches bool `json:"preambleCaches"`
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
			backendList = append(backendList, backendEntryDTO{
				ID: e.ID, Kind: e.Kind, Name: e.Name, Path: e.Path, Default: e.Default,
				Managed: e.Managed, Component: e.Component, Version: e.Version, Variant: e.Variant,
			})
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
			// nil (never saved / hand-authored file) => on.
			PreambleCaches: gf.Settings.SlotCache.PreambleCaches == nil || *gf.Settings.SlotCache.PreambleCaches,
		},
		Backends: backendsDTO{
			ServerExe:    gf.Settings.ServerExe,
			SdServerExe:  gf.Settings.SdServerExe,
			TtsServerExe: gf.Settings.TtsServerExe,
			AsrServerExe: gf.Settings.AsrServerExe,
		},
		BackendList:        backendList,
		Guards:             guardsFromSettings(gf.Settings),
		GuardsOverridden:   guardsOverridden(patch),
		Advanced:           advancedFromSettings(gf.Settings),
		AdvancedDefaults:   advancedFromSettings(base),
		AdvancedOverridden: advancedOverridden(patch),
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
	// Managed provenance is server-owned: re-read it from the stored row rather
	// than trusting (or requiring) the client to echo it back, so a save from the
	// manual editor can't turn an installer-owned row into an orphaned path.
	stored, err := autogen.LoadSidecarBackendList(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	prov := make(map[string]autogen.BackendEntry, len(stored))
	for _, e := range stored {
		if e.Managed {
			prov[e.ID] = e
		}
	}
	list := make([]autogen.BackendEntry, 0, len(body))
	for _, e := range body {
		row := autogen.BackendEntry{ID: e.ID, Kind: e.Kind, Name: e.Name, Path: e.Path, Default: e.Default}
		if p, ok := prov[e.ID]; ok {
			row.Managed, row.Component, row.Version, row.Variant = true, p.Component, p.Version, p.Variant
			row.Path = p.Path // the active build's exe, not an edited copy
		}
		list = append(list, row)
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
	if isDefaultSlotCachePath(path) {
		path = ""
	}
	// RecurrentSeeds is deliberately NOT in slotCacheDTO — it is a backend
	// regression-test knob (see slotcache.md), not a setting, and turning it on
	// makes hybrid models slower. But the sidecar block REPLACES the generate
	// file's settings.slotCache wholesale, so writing a DTO-shaped struct here
	// would silently drop a hand-authored recurrentSeeds: true and leave no way
	// to get it back short of editing YAML. Carry it from the MERGED value
	// (file + sidecar), which covers both the hand-authored and previously-saved
	// cases.
	recurrentSeeds := false
	if merged, mErr := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir); mErr == nil {
		recurrentSeeds = merged.Settings.SlotCache.RecurrentSeeds
	}
	err := autogen.UpsertSidecarSlotCache(s.autogen.GeneratePath, autogen.SlotCacheSettings{
		Enable:         body.Enable,
		Path:           path,
		MinSaveTokens:  body.MinSaveTokens,
		MaxDiskGB:      body.MaxDiskGB,
		MaxSessions:    body.MaxSessions,
		RecurrentSeeds: recurrentSeeds,
		PreambleCaches: &body.PreambleCaches,
	})
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.proxylog.Infof("slot cache: enabled=%v minSaveTokens=%d maxDisk=%gGB maxSessions=%d preambleCaches=%v",
		body.Enable, body.MinSaveTokens, body.MaxDiskGB, body.MaxSessions, body.PreambleCaches)
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
	path, err := pickFile(pickSpecs["backend"])
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

// handleAPIPickFile opens the host's native open-file dialog for a named kind
// (?kind=template) and returns the chosen path without persisting anything —
// the caller drops it into a form field. The kind is looked up in the
// server-side pickSpecs whitelist; the dialog config is never taken from the
// request (it is interpolated into a shell command line). 204 on cancel, 501
// when the platform has no native picker (UI keeps the text field).
func (s *Server) handleAPIPickFile(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	spec, ok := pickSpecs[kind]
	if !ok {
		shared.SendResponse(w, r, http.StatusBadRequest, "unknown file picker kind: "+kind)
		return
	}
	path, err := pickFile(spec)
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
	// These four numbers decide every model's context, offload and TTL, so a
	// load that behaves differently than it did yesterday usually traces back
	// to this line.
	s.proxylog.Infof("settings: targetVram %.1fGB, overhead %.1fGB, maxRam %.1fGB, ttl %ds",
		body.TargetVramGB, body.VramOverheadGB, body.MaxRamGB, body.TtlSec)
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
	s.proxylog.Info("settings: reset to the generate file's defaults")
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "reset"})
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
		// Provenance of a managed row (downloaded by the in-app backend manager):
		// which upstream component/build this exe is. Empty on a hand-entered row.
		Managed   bool   `json:"managed,omitempty"`
		Component string `json:"component,omitempty"`
		Version   string `json:"version,omitempty"`
		Variant   string `json:"variant,omitempty"`
	}
	out := make([]backendListEntry, 0, len(gf.Settings.Backends))
	for _, e := range gf.Settings.Backends {
		exists := false
		if p := strings.TrimSpace(e.Path); p != "" {
			if _, statErr := os.Stat(p); statErr == nil {
				exists = true
			}
		}
		out = append(out, backendListEntry{
			ID: e.ID, Kind: e.Kind, Name: e.Name, Path: e.Path, Default: e.Default, Exists: exists,
			Managed: e.Managed, Component: e.Component, Version: e.Version, Variant: e.Variant,
		})
	}
	writeJSON(w, out)
}
