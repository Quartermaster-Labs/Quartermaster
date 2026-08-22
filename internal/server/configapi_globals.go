package server

// The dashboard's remaining global-settings sections: the OOM guard, GPU-usage
// admission, and the advanced sizer knobs. Same shape as the memory editor in
// configapi_settings.go — read the effective value, write a sidecar patch,
// regenerate, hot-reload — but split out because each section owns a DISJOINT
// slice of SettingsPatch and PUTs only its own fields.
//
// That disjointness is the whole reason autogen.MergeSettingsPatch exists: a
// guard save must not revert the advanced knobs, and vice versa. Do not "help"
// by filling in a neighbouring section's fields here.

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// guardsDTO is the OOM guard + GPU-usage sections. Every field is a real value
// (not a pointer) because both forms always submit all of their own fields, and
// because 0/negative are MEANINGFUL here rather than "unset": a zero reserve
// admits into the raw leftovers and a zero/negative gpu fraction disables the
// admission floor. See the Settings field docs in autogen/overrides.go.
type guardsDTO struct {
	OomGuardEvict     bool    `json:"oomGuardEvict"`
	OomGuardReserveGB float64 `json:"oomGuardReserveGB"`
	OomGuardGraceSec  int     `json:"oomGuardGraceSec"`
	MinGpuFraction    float64 `json:"minGpuFraction"`
	MultiResident     bool    `json:"multiResident"`
}

// advancedDTO is the sizer knobs behind the "advanced" warning. Pointers, and
// the distinction matters: applyDefaults runs BEFORE the sidecar patch is
// overlaid (autogen.LoadGenerateFile), so a stored 0 is never re-defaulted —
// it reaches the sizer AS zero. A zero computeBufFactor models a zero-byte
// compute buffer and a zero threads count emits `-t 0`. nil (JSON null, or a
// blank field in the UI) is therefore the only correct way to say "default",
// and it is exactly what the section's reset writes.
type advancedDTO struct {
	ComputeBufFactor   *float64 `json:"computeBufFactor"`
	VisionOverheadGB   *float64 `json:"visionOverheadGB"`
	VisionCtx          *int     `json:"visionCtx"`
	MoeCtxTarget       *int     `json:"moeCtxTarget"`
	DenseMinCtx        *int     `json:"denseMinCtx"`
	DenseCtxLadder     *[]int   `json:"denseCtxLadder"`
	Threads            *int     `json:"threads"`
	HealthCheckTimeout *int     `json:"healthCheckTimeout"`
	KvQuant            *string  `json:"kvQuant"`
	LoraDir            *string  `json:"loraDir"`
}

// kvQuantValues is the accepted -ctk/-ctv set, plus "" for auto. Whitelisted
// rather than passed through: the value lands verbatim on a launch command line,
// and an unknown type fails the spawn rather than the save, which surfaces as
// "the model just won't load" long after the setting was changed.
var kvQuantValues = map[string]bool{
	"": true, "f32": true, "f16": true, "bf16": true,
	"q8_0": true, "q5_1": true, "q5_0": true, "q4_1": true, "q4_0": true,
}

// guardsFromSettings reads the effective guard values. The two tri-state
// pointers on Settings default to ON when unset, matching their field docs.
func guardsFromSettings(s autogen.Settings) guardsDTO {
	evict := s.OomGuardEvict == nil || *s.OomGuardEvict
	multi := s.MultiResident == nil || *s.MultiResident
	return guardsDTO{
		OomGuardEvict:     evict,
		OomGuardReserveGB: s.OomGuardReserveGB,
		OomGuardGraceSec:  s.OomGuardGraceSec,
		MinGpuFraction:    s.MinGpuFraction,
		MultiResident:     multi,
	}
}

// advancedFromSettings reads the effective sizer knobs. Values, not nils: the
// UI shows what the sizer is ACTUALLY using, defaults included, so the fields
// are never blank and "what is it doing right now" needs no second lookup.
// Whether a value is user-set is reported separately (advancedOverridden).
func advancedFromSettings(s autogen.Settings) advancedDTO {
	ladder := append([]int(nil), s.DenseCtxLadder...)
	return advancedDTO{
		ComputeBufFactor:   &s.ComputeBufFactor,
		VisionOverheadGB:   &s.VisionOverheadGB,
		VisionCtx:          &s.VisionCtx,
		MoeCtxTarget:       &s.MoeCtxTarget,
		DenseMinCtx:        &s.DenseMinCtx,
		DenseCtxLadder:     &ladder,
		Threads:            &s.Threads,
		HealthCheckTimeout: &s.HealthCheckTimeout,
		KvQuant:            &s.KvQuant,
		LoraDir:            &s.LoraDir,
	}
}

// advancedOverridden reports whether the sidecar patch pins any advanced knob —
// what lights the section's "custom" badge and enables its reset button.
func advancedOverridden(p *autogen.SettingsPatch) bool {
	if p == nil {
		return false
	}
	return p.ComputeBufFactor != nil || p.VisionOverheadGB != nil || p.VisionCtx != nil ||
		p.MoeCtxTarget != nil || p.DenseMinCtx != nil || p.DenseCtxLadder != nil ||
		p.Threads != nil || p.HealthCheckTimeout != nil || p.KvQuant != nil || p.LoraDir != nil
}

// guardsOverridden reports the same for the guard / GPU-usage sections.
func guardsOverridden(p *autogen.SettingsPatch) bool {
	if p == nil {
		return false
	}
	return p.OomGuardEvict != nil || p.OomGuardReserveGB != nil || p.OomGuardGraceSec != nil ||
		p.MinGpuFraction != nil || p.MultiResident != nil
}

// handleAPIGuardsPut writes the OOM-guard + GPU-usage settings.
//
// The watchdog reads these from the snapshot taken when the guard was built, so
// the regen+reload is what actually moves them: see newVramGuard in vramguard.go.
func (s *Server) handleAPIGuardsPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	var body guardsDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// A zero grace would shed a model on the first spiky sample — a shader
	// compile or a video decode — which is the exact thrash the grace exists to
	// prevent. Refuse rather than silently clamp, so the number in the box is
	// always the number in force.
	if body.OomGuardGraceSec < 1 {
		shared.SendResponse(w, r, http.StatusBadRequest, "oomGuardGraceSec must be >= 1 (it is the anti-thrash delay; disable shedding with oomGuardEvict instead)")
		return
	}
	if body.MinGpuFraction > 1 {
		shared.SendResponse(w, r, http.StatusBadRequest, "minGpuFraction is a fraction: it must be <= 1 (0 or negative disables the floor)")
		return
	}
	patch := autogen.SettingsPatch{
		OomGuardEvict:     &body.OomGuardEvict,
		OomGuardReserveGB: &body.OomGuardReserveGB,
		OomGuardGraceSec:  &body.OomGuardGraceSec,
		MinGpuFraction:    &body.MinGpuFraction,
		MultiResident:     &body.MultiResident,
	}
	if err := autogen.UpsertSidecarSettings(s.autogen.GeneratePath, patch); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.proxylog.Infof("guards: oomGuardEvict=%v reserve=%.1fGB grace=%ds minGpuFraction=%.2f multiResident=%v",
		body.OomGuardEvict, body.OomGuardReserveGB, body.OomGuardGraceSec, body.MinGpuFraction, body.MultiResident)
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleAPIAdvancedPut writes the advanced sizer knobs. A null (or zero) field
// CLEARS that knob back to the computed default rather than pinning a zero —
// see advancedDTO for why zero is never a legal stored value here.
func (s *Server) handleAPIAdvancedPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	var body advancedDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	for _, f := range []struct {
		name string
		v    *float64
	}{
		{"computeBufFactor", body.ComputeBufFactor},
		{"visionOverheadGB", body.VisionOverheadGB},
	} {
		if f.v != nil && *f.v < 0 {
			shared.SendResponse(w, r, http.StatusBadRequest, f.name+" must be >= 0")
			return
		}
	}
	for _, f := range []struct {
		name string
		v    *int
	}{
		{"visionCtx", body.VisionCtx},
		{"moeCtxTarget", body.MoeCtxTarget},
		{"denseMinCtx", body.DenseMinCtx},
		{"threads", body.Threads},
		{"healthCheckTimeout", body.HealthCheckTimeout},
	} {
		if f.v != nil && *f.v < 0 {
			shared.SendResponse(w, r, http.StatusBadRequest, f.name+" must be >= 0")
			return
		}
	}
	if body.KvQuant != nil && !kvQuantValues[strings.TrimSpace(*body.KvQuant)] {
		shared.SendResponse(w, r, http.StatusBadRequest, "unknown kvQuant (want one of: f32 f16 bf16 q8_0 q5_1 q5_0 q4_1 q4_0, or empty for auto)")
		return
	}
	if body.DenseCtxLadder != nil {
		for _, v := range *body.DenseCtxLadder {
			if v <= 0 {
				shared.SendResponse(w, r, http.StatusBadRequest, "denseCtxLadder entries must be > 0")
				return
			}
		}
	}
	patch := autogen.SettingsPatch{
		ComputeBufFactor:   nilIfZeroF(body.ComputeBufFactor),
		VisionOverheadGB:   nilIfZeroF(body.VisionOverheadGB),
		VisionCtx:          nilIfZeroI(body.VisionCtx),
		MoeCtxTarget:       nilIfZeroI(body.MoeCtxTarget),
		DenseMinCtx:        nilIfZeroI(body.DenseMinCtx),
		Threads:            nilIfZeroI(body.Threads),
		HealthCheckTimeout: nilIfZeroI(body.HealthCheckTimeout),
		KvQuant:            body.KvQuant,
		LoraDir:            body.LoraDir,
	}
	if body.DenseCtxLadder != nil && len(*body.DenseCtxLadder) > 0 {
		patch.DenseCtxLadder = body.DenseCtxLadder
	}
	// A blank kvQuant/loraDir means "auto" / "the model's own folder", which is
	// the default — store nothing rather than an empty pin.
	if patch.KvQuant != nil && strings.TrimSpace(*patch.KvQuant) == "" {
		patch.KvQuant = nil
	}
	if patch.LoraDir != nil && strings.TrimSpace(*patch.LoraDir) == "" {
		patch.LoraDir = nil
	}
	// Every field of this section is being rewritten, so clear the stored ones
	// first: a plain merge would keep a knob the user just blanked. Only THIS
	// section's fields are cleared — the guard and memory fields are untouched.
	if err := s.clearAdvancedPatch(); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if err := autogen.UpsertSidecarSettings(s.autogen.GeneratePath, patch); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.proxylog.Info("advanced sizer settings updated: " + describeAdvanced(patch))
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleAPIAdvancedDelete restores the advanced knobs to their computed
// defaults, leaving every other section's patch fields in place.
func (s *Server) handleAPIAdvancedDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	if err := s.clearAdvancedPatch(); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.proxylog.Info("advanced sizer settings reset to defaults")
	if !s.regenAndReload(w, r) {
		return
	}
	writeJSON(w, map[string]string{"status": "reset"})
}

// clearAdvancedPatch nils exactly the advanced fields of the stored patch and
// writes it back. Read-modify-write rather than a merge, because a merge can
// only ever SET fields — clearing one is the operation MergeSettingsPatch
// deliberately cannot express.
func (s *Server) clearAdvancedPatch() error {
	cur, err := autogen.LoadSidecarSettings(s.autogen.GeneratePath)
	if err != nil {
		return err
	}
	if cur == nil {
		return nil
	}
	next := *cur
	next.ComputeBufFactor = nil
	next.VisionOverheadGB = nil
	next.VisionCtx = nil
	next.MoeCtxTarget = nil
	next.DenseMinCtx = nil
	next.DenseCtxLadder = nil
	next.Threads = nil
	next.HealthCheckTimeout = nil
	next.KvQuant = nil
	next.LoraDir = nil
	return autogen.ReplaceSidecarSettings(s.autogen.GeneratePath, next)
}

// nilIfZeroF / nilIfZeroI map "0" onto "unset". The UI sends 0 for a blank
// number field, and for every advanced knob zero is a nonsense value the sizer
// would nonetheless honour — see advancedDTO.
func nilIfZeroF(v *float64) *float64 {
	if v == nil || *v == 0 {
		return nil
	}
	return v
}

func nilIfZeroI(v *int) *int {
	if v == nil || *v == 0 {
		return nil
	}
	return v
}

// describeAdvanced renders the pinned knobs for the log line, so a config that
// starts sizing models differently can be traced to the save that did it.
func describeAdvanced(p autogen.SettingsPatch) string {
	var parts []string
	if p.ComputeBufFactor != nil {
		parts = append(parts, "computeBufFactor="+strconv.FormatFloat(*p.ComputeBufFactor, 'g', -1, 64))
	}
	if p.VisionOverheadGB != nil {
		parts = append(parts, "visionOverheadGB="+strconv.FormatFloat(*p.VisionOverheadGB, 'g', -1, 64))
	}
	if p.VisionCtx != nil {
		parts = append(parts, "visionCtx="+strconv.Itoa(*p.VisionCtx))
	}
	if p.MoeCtxTarget != nil {
		parts = append(parts, "moeCtxTarget="+strconv.Itoa(*p.MoeCtxTarget))
	}
	if p.DenseMinCtx != nil {
		parts = append(parts, "denseMinCtx="+strconv.Itoa(*p.DenseMinCtx))
	}
	if p.DenseCtxLadder != nil {
		var l []string
		for _, v := range *p.DenseCtxLadder {
			l = append(l, strconv.Itoa(v))
		}
		parts = append(parts, "denseCtxLadder="+strings.Join(l, "/"))
	}
	if p.Threads != nil {
		parts = append(parts, "threads="+strconv.Itoa(*p.Threads))
	}
	if p.HealthCheckTimeout != nil {
		parts = append(parts, "healthCheckTimeout="+strconv.Itoa(*p.HealthCheckTimeout))
	}
	if p.KvQuant != nil {
		parts = append(parts, "kvQuant="+*p.KvQuant)
	}
	if p.LoraDir != nil {
		parts = append(parts, "loraDir="+*p.LoraDir)
	}
	if len(parts) == 0 {
		return "all defaults"
	}
	return strings.Join(parts, " ")
}

// --- Process-level settings: ports, remote access, updates, HF token --------
//
// These never reach the generated config: main() consumes them before a Server
// exists (see internal/autogen/appsettings.go). So unlike every other section
// in this file, a save here does NOT regenerate and hot-reload — it writes the
// sidecar and reports that a restart is needed, which is the honest answer: a
// bound socket cannot be moved under a live server.

// RunningApp is what this process actually started with, so the dashboard can
// tell "saved" apart from "in force" and mark exactly which fields still need a
// restart. Set once by main() before serving, so it needs no locking.
type RunningApp struct {
	Listen                 string `json:"listen"`
	PlaygroundListen       string `json:"playgroundListen"`
	AdminAllow             string `json:"adminAllow"`
	AdminOpen              bool   `json:"adminOpen"`
	WatchModels            bool   `json:"watchModels"`
	WatchModelsIntervalSec int    `json:"watchModelsIntervalSec"`
	UpdateCheck            bool   `json:"updateCheck"`
}

// SetRunningApp records the in-force process settings.
func (s *Server) SetRunningApp(r RunningApp) { s.runningApp = r }

// appSettingsDTO is the editable block. Booleans are plain values because the
// form always submits all of them.
//
// The token is WRITE-ONLY: a GET reports only whether one is stored. The admin
// surface has no password, and echoing a credential back into a page is how it
// ends up in a screenshot or a support paste.
type appSettingsDTO struct {
	Listen                 string `json:"listen"`
	PlaygroundListen       string `json:"playgroundListen"`
	AdminAllow             string `json:"adminAllow"`
	AdminOpen              bool   `json:"adminOpen"`
	WatchModels            bool   `json:"watchModels"`
	WatchModelsIntervalSec int    `json:"watchModelsIntervalSec"`
	UpdateCheck            bool   `json:"updateCheck"`
	// HfToken empty on a PUT means "keep the stored one", so the form can be
	// saved without re-typing it. Clearing is explicit, via HfTokenClear.
	HfToken      string `json:"hfToken,omitempty"`
	HfTokenClear bool   `json:"hfTokenClear,omitempty"`
	HfTokenSet   bool   `json:"hfTokenSet"`
}

type appSettingsResp struct {
	Settings   appSettingsDTO `json:"settings"`
	Running    RunningApp     `json:"running"`
	Overridden bool           `json:"overridden"` // a dashboard-owned block is stored
	// EnvToken reports that HF_TOKEN (or a sibling) is set in the environment,
	// which WINS over the stored one — otherwise a user whose shell exports a
	// token sees the field they filled in have no effect, with no explanation.
	EnvToken bool `json:"envToken"`
}

func (s *Server) handleAPIAppSettingsGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	app, err := autogen.LoadAppSettings(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	stored, err := autogen.LoadSidecarApp(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, appSettingsResp{
		Settings: appSettingsDTO{
			Listen:                 app.Listen,
			PlaygroundListen:       app.PlaygroundListen,
			AdminAllow:             app.AdminAllow,
			AdminOpen:              app.AdminOpen != nil && *app.AdminOpen,
			WatchModels:            app.WatchModels == nil || *app.WatchModels,
			WatchModelsIntervalSec: app.WatchModelsIntervalSec,
			UpdateCheck:            app.UpdateCheck == nil || *app.UpdateCheck,
			HfTokenSet:             strings.TrimSpace(app.HfToken) != "",
		},
		Running:    s.runningApp,
		Overridden: stored != nil,
		EnvToken:   hfTokenInEnv(),
	})
}

// hfTokenInEnv mirrors the lookup order the hub client uses (hubapi.go).
func hfTokenInEnv() bool {
	for _, k := range []string{"HF_TOKEN", "HUGGING_FACE_HUB_TOKEN", "HUGGINGFACE_TOKEN"} {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			return true
		}
	}
	return false
}

func (s *Server) handleAPIAppSettingsPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutogen(w, r) {
		return
	}
	var body appSettingsDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Validate the addresses HERE rather than at the next startup. A listen
	// address that cannot be parsed is the one setting that can leave the app
	// unreachable — with the dashboard that would fix it behind the very socket
	// that failed to bind.
	for _, f := range []struct{ name, v string }{
		{"listen", body.Listen},
		{"playgroundListen", body.PlaygroundListen},
	} {
		v := strings.TrimSpace(f.v)
		if v == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(v); err != nil {
			shared.SendResponse(w, r, http.StatusBadRequest, f.name+" must be host:port (e.g. 0.0.0.0:1250 or :8081)")
			return
		}
	}
	if _, err := ParseAdminAllow(body.AdminAllow); err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "adminAllow: "+err.Error())
		return
	}
	if body.WatchModelsIntervalSec < 0 {
		shared.SendResponse(w, r, http.StatusBadRequest, "watchModelsIntervalSec must be >= 0")
		return
	}

	prev, err := autogen.LoadAppSettings(s.autogen.GeneratePath)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	adminOpen, watchModels, updateCheck := body.AdminOpen, body.WatchModels, body.UpdateCheck
	next := autogen.AppSettings{
		Listen:                 strings.TrimSpace(body.Listen),
		PlaygroundListen:       strings.TrimSpace(body.PlaygroundListen),
		AdminAllow:             strings.TrimSpace(body.AdminAllow),
		AdminOpen:              &adminOpen,
		WatchModels:            &watchModels,
		WatchModelsIntervalSec: body.WatchModelsIntervalSec,
		UpdateCheck:            &updateCheck,
		HfToken:                prev.HfToken,
	}
	switch {
	case body.HfTokenClear:
		next.HfToken = ""
	case strings.TrimSpace(body.HfToken) != "":
		next.HfToken = strings.TrimSpace(body.HfToken)
	}
	if err := autogen.UpsertSidecarApp(s.autogen.GeneratePath, next); err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	// No token in the log line, and no regen: nothing here feeds the config.
	s.proxylog.Infof("config: app settings saved (listen=%q playground=%q adminAllow=%q adminOpen=%v watchModels=%v/%ds updateCheck=%v) - restart to apply",
		next.Listen, next.PlaygroundListen, next.AdminAllow, adminOpen, watchModels, next.WatchModelsIntervalSec, updateCheck)
	writeJSON(w, map[string]string{"status": "ok", "note": "restart to apply"})
}
