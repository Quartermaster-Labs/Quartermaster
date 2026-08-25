package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/event"
	"github.com/quartermaster-labs/quartermaster/internal/perf"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// apiModel is one entry in the /api/events modelStatus payload.
type apiModel struct {
	Id           string         `json:"id"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	State        string         `json:"state"`
	Unlisted     bool           `json:"unlisted"`
	PeerID       string         `json:"peerID"`
	Aliases      []string       `json:"aliases,omitempty"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
	// Family is the gguf path shared by a model's variants (ctx tiers, game,
	// judge); the UI groups rows by it. Empty when the cmd has no model path.
	Family string `json:"family,omitempty"`
	// Group is the model's swap group; Listeners are the listen addresses that
	// expose that group (its catalog/port). The UI sections the catalog by these
	// so each port shows only its own models. Empty when ungrouped/unrestricted.
	Group     string   `json:"group,omitempty"`
	Listeners []string `json:"listeners,omitempty"`
	// Ctx is the context window ONE conversation gets, 0 when the command takes
	// no such flag (image/audio backends). On a multi-slot model that is the
	// launched -c divided by Slots: --kv-unified makes -c the shared pool across
	// all slots, so reporting it raw would promise 4x the context any single chat
	// can hold.
	Ctx int `json:"ctx,omitempty"`
	// Slots is the number of concurrent conversation slots the model serves
	// (--parallel N). Omitted for the single-slot default.
	Slots int `json:"slots,omitempty"`
	// ModelKey / FamilyKey are the Models table's grouping keys, derived from the
	// gguf HEADER where it carries an identity and from the model id otherwise
	// (see modelKeys). Rows sharing a ModelKey are one model listed once with a
	// pill per quant; rows sharing a FamilyKey are finetunes of one base. The UI
	// must not re-derive these from the id - that guessing game is what made one
	// model show up as several rows.
	ModelKey  string `json:"modelKey,omitempty"`
	FamilyKey string `json:"familyKey,omitempty"`
	// Quant is the weight type named by the gguf FILENAME ("Q4_K_M"), and SizeGB
	// its on-disk size. Both drive the Models table's spreadsheet columns; "" / 0
	// when the command has no model path or the file is unreadable.
	//
	// QuantLabel is the same fact read off the TENSORS ("Q4_K", "IQ4_XS mix"),
	// sent only when the filename named nothing, and is display-only: the table
	// merges two folders' copies of a model on Quant, and a computed label two
	// unrelated builds happen to share is not that agreement. See modelKeys.
	Quant      string  `json:"quant,omitempty"`
	QuantLabel string  `json:"quantLabel,omitempty"`
	SizeGB     float64 `json:"sizeGB,omitempty"`
	// EstVramGB / EstRamGB are the autogen sizer's predicted footprint for this
	// model, carried in the generated config. EstVramGB is also the router's
	// admission input; EstRamGB is non-zero only when weights are CPU-offloaded.
	EstVramGB float64 `json:"estVramGB,omitempty"`
	EstRamGB  float64 `json:"estRamGB,omitempty"`
	// RunningCmd is the actual argv the process spawned with, set only while the
	// model is running. It differs from the config command after a live config
	// edit (new args apply on next load) or a spawn-time offload rewrite, so the
	// UI shows what the model is REALLY loaded with, not the pending config.
	RunningCmd string `json:"runningCmd,omitempty"`
}

// groupIndex maps each model ID to its group name (first group listing it as a
// member), and each group to the sorted listen addresses that expose it. Used
// to tag the modelStatus payload so the UI can section the catalog by port.
func (s *Server) groupIndex() (modelGroup map[string]string, groupListeners map[string][]string) {
	cfg := s.config()
	modelGroup = make(map[string]string)
	for gid, gc := range cfg.Groups {
		for _, mid := range gc.Members {
			if _, exists := modelGroup[mid]; !exists {
				modelGroup[mid] = gid
			}
		}
	}
	groupListeners = make(map[string][]string)
	for addr, lc := range cfg.Listeners {
		for _, gid := range lc.Groups {
			groupListeners[gid] = append(groupListeners[gid], addr)
		}
	}
	for gid := range groupListeners {
		sort.Strings(groupListeners[gid])
	}
	return modelGroup, groupListeners
}

// modelStatus returns every configured model joined with its current process
// state (defaulting to "stopped"), followed by peer models.
func (s *Server) modelStatus() []apiModel {
	running := s.local.RunningModels()
	cfg := s.config()

	ids := make([]string, 0, len(cfg.Models))
	for id := range cfg.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	modelGroup, groupListeners := s.groupIndex()

	models := make([]apiModel, 0, len(ids))
	for _, id := range ids {
		mc := cfg.Models[id]
		state := "stopped"
		var runningCmd string
		if st, ok := running[id]; ok {
			state = string(st)
			// Actual spawned argv — what the model is really serving under, which
			// differs from mc.Cmd after a live edit or offload rewrite. "" until ready.
			runningCmd, _ = s.local.LaunchedCmd(id)
		}
		_, capsMap, _, _ := renderCapabilities(mc.Capabilities)
		gid := modelGroup[id]
		info := config.ParseCmd(mc.Cmd)
		ctxSize := 0
		if v, ok := info.Value("-c", "--ctx-size"); ok {
			ctxSize, _ = strconv.Atoi(strings.TrimSpace(v))
		}
		slots := 0
		if v, ok := info.Value("-np", "--parallel"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 1 {
				slots = n
				ctxSize /= n // -c is the shared pool; report the per-conversation share
			}
		}
		family := modelFamily(mc.Cmd)
		quantName, quantLabel, modelKey, familyKey := modelKeys(family, id)
		models = append(models, apiModel{
			Id:           id,
			Name:         mc.Name,
			Description:  mc.Description,
			State:        state,
			Unlisted:     mc.Unlisted,
			Aliases:      mc.Aliases,
			Capabilities: capsMap,
			Family:       family,
			Group:        gid,
			Listeners:    groupListeners[gid],
			Ctx:          ctxSize,
			Slots:        slots,
			ModelKey:     modelKey,
			FamilyKey:    familyKey,
			Quant:        quantName,
			QuantLabel:   quantLabel,
			SizeGB:       fileSizeGB(family),
			EstVramGB:    mc.EstVramGB,
			EstRamGB:     mc.EstRamGB,
			RunningCmd:   runningCmd,
		})
	}

	for peerID, peer := range cfg.Peers {
		for _, modelID := range peer.Models {
			models = append(models, apiModel{Id: modelID, PeerID: peerID})
		}
	}

	return models
}

// handleAPICatalog serves the FULL local catalog as JSON — the same payload the
// dashboard gets pushed over /api/events, but pullable. Unlike /v1/models it is
// not the OpenAI discovery route: it is not filtered by an API key's model scope
// and it keeps unlisted entries (synthetic ctx/backend variants), so an
// inspection client (the quartermaster_inspect chat tool) sees every model that
// exists rather than the slice one key is allowed to call.
func (s *Server) handleAPICatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"models": s.modelStatus()})
}

// handleAPIUnloadAll stops every running local process.
func (s *Server) handleAPIUnloadAll(w http.ResponseWriter, r *http.Request) {
	s.local.Unload(apiUnloadTimeout)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"msg": "ok"})
}

// handleAPIUnloadModel stops a single named local process.
func (s *Server) handleAPIUnloadModel(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimPrefix(r.PathValue("model"), "/")
	cfg := s.config()
	realName, found := cfg.RealModelName(requested)
	if !found {
		shared.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	if !s.local.Handles(realName) {
		shared.SendResponse(w, r, http.StatusNotFound, "no local server found for requested model")
		return
	}
	s.local.Unload(apiUnloadTimeout, realName)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleAPIMetrics serves the activity log as a JSON array.
func (s *Server) handleAPIMetrics(w http.ResponseWriter, r *http.Request) {
	data, err := s.metrics.getMetricsJSON()
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "failed to get metrics")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// handleAPIBackendMetrics serves the latest scraped backend /metrics + /props
// snapshot (per running llama-server: KV-cache fill, slots, throughput totals).
func (s *Server) handleAPIBackendMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.backendMetrics.snapshot())
}

// handleAPIPerformance serves the buffered system/GPU stats, optionally
// filtered to samples after the ?after=<RFC3339> timestamp.
func (s *Server) handleAPIPerformance(w http.ResponseWriter, r *http.Request) {
	if s.perf == nil {
		shared.SendResponse(w, r, http.StatusServiceUnavailable, "performance monitor not available")
		return
	}

	sysStats, gpuStats := s.perf.Current()

	if afterStr := r.URL.Query().Get("after"); afterStr != "" {
		after, err := time.Parse(time.RFC3339, afterStr)
		if err != nil {
			shared.SendResponse(w, r, http.StatusBadRequest, "invalid 'after' timestamp, use RFC3339 format")
			return
		}
		filteredSys := make([]perf.SysStat, 0, len(sysStats))
		for _, st := range sysStats {
			if st.Timestamp.After(after) {
				filteredSys = append(filteredSys, st)
			}
		}
		sysStats = filteredSys

		filteredGpu := make([]perf.GpuStat, 0, len(gpuStats))
		for _, g := range gpuStats {
			if g.Timestamp.After(after) {
				filteredGpu = append(filteredGpu, g)
			}
		}
		gpuStats = filteredGpu
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"sys_stats": sysStats,
		"gpu_stats": gpuStats,
		"foreign":   s.foreignGPU(r.Context()),
		// Idle system-VRAM floor (MiB) sampled server-side; 0 = not observed yet.
		"system_mb": s.systemVramMB.Load(),
		// OOM guard: VRAM (MiB) held by everything that is NOT one of our
		// children, and the card total it is measured against. Unlike "foreign"
		// above — which counts only stray llama-server/sd-server instances by name
		// — this is the whole rest of the desktop (a game, a browser, the
		// compositor), because that is what actually shrinks the budget the router
		// admits models into. Omitted when there is no trustworthy reading.
		"guard": s.vramGuardStats(),
	})
}

// foreignVram is the current-snapshot tally of GPU memory held by llama-server /
// sd-server processes that this instance did NOT spawn — i.e. a stray llama.cpp
// left running elsewhere. The UI shows MB as a red gauge segment.
type foreignVram struct {
	MB    int            `json:"mb"`
	Procs []perf.GpuProc `json:"procs,omitempty"`
}

// isInferenceProc reports whether a GPU process name looks like a llama.cpp /
// stable-diffusion server. ponytail: substring heuristic; widen if other
// backends with distinct binary names show up.
func isInferenceProc(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "llama-server") || strings.Contains(n, "sd-server")
}

// foreignGPU lists inference processes using GPU memory whose pid is not one of
// our managed children. Uses nvidia-smi on NVIDIA and a vendor-neutral PDH
// per-process VRAM source on non-NVIDIA Windows (AMD/Intel); zero tally when no
// source is available.
func (s *Server) foreignGPU(ctx context.Context) foreignVram {
	procs := perf.QueryComputeApps(ctx)
	if len(procs) == 0 {
		return foreignVram{}
	}
	ours := make(map[int]bool)
	for _, pid := range s.local.RunningPIDs() {
		ours[pid] = true
	}
	out := foreignVram{}
	for _, p := range procs {
		if ours[p.PID] || !isInferenceProc(p.Name) {
			continue
		}
		out.MB += p.MemMB
		out.Procs = append(out.Procs, p)
	}
	return out
}

// vramGuardStats reports the OOM guard's current view of the card for the
// dashboard: foreign VRAM, the card total, and the ceiling the router admits
// against. nil when the guard isn't wired (hand-written config, no perf
// monitor) or has no trustworthy reading yet — the UI must then show nothing
// rather than a zero, which would read as "nothing else is using the GPU".
func (s *Server) vramGuardStats() map[string]any {
	if s.vramGuard == nil {
		return nil
	}
	foreignMB, floorMB, totalMB, ok := s.vramGuard.snapshot()
	if !ok {
		return nil
	}
	// floor_mb is the desktop's idle cost. Only foreign_mb ABOVE it is pressure —
	// showing the raw foreign figure alone would make an idle box look loaded.
	out := map[string]any{"foreign_mb": foreignMB, "floor_mb": floorMB, "total_mb": totalMB}
	if ceilingGB, ok := s.vramGuard.ceilingGB(); ok {
		out["ceiling_gb"] = math.Round(ceilingGB*10) / 10
	}
	return out
}

// handleAPIVersion serves the build metadata.
func (s *Server) handleAPIVersion(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"version":    s.build.Version,
		"commit":     s.build.Commit,
		"build_date": s.build.Date,
	}
	// Surface the update status so the UI can show an "update available" banner.
	// blocked/restart come along so the banner can say what will actually happen
	// (restart itself vs "restart the service to apply") instead of promising a
	// seamless swap the environment won't allow.
	if s.updater != nil && s.updater.Enabled() {
		st := s.updater.Status()
		out["update_available"] = st.Available
		out["latest_version"] = st.Latest
		out["release_url"] = st.ReleaseURL
		out["update_blocked"] = st.Blocked
		out["update_restart"] = st.Restart
		out["update_phase"] = st.Phase
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleAPICapture returns the stored request/response capture for a metric ID.
func (s *Server) handleAPICapture(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "invalid capture ID")
		return
	}

	capture := s.metrics.getCaptureByID(id)
	if capture == nil {
		shared.SendResponse(w, r, http.StatusNotFound, "capture not found")
		return
	}

	jsonBytes, err := json.Marshal(capture)
	if err != nil {
		shared.SendResponse(w, r, http.StatusInternalServerError, "failed to marshal capture")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonBytes)
}

type messageType string

const (
	msgTypeModelStatus    messageType = "modelStatus"
	msgTypeLogData        messageType = "logData"
	msgTypeMetrics        messageType = "metrics"
	msgTypeInFlight       messageType = "inflight"
	msgTypeLiveTokens     messageType = "liveTokens"
	msgTypeBackendMetrics messageType = "backendMetrics"
)

type messageEnvelope struct {
	Type messageType `json:"type"`
	Data string      `json:"data"`
}

// handleAPIEvents streams server events (model status, log data, metrics,
// in-flight counts) to the client as Server-Sent Events.
func (s *Server) handleAPIEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// prevent nginx from buffering SSE
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		shared.SendResponse(w, r, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// internal/event already has a 50K event buffer
	// a 1K message buffer should be enough, watch the logs for the warning that the sendBuffer is full
	sendBuffer := make(chan messageEnvelope, 1024)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	send := func(msg messageEnvelope) {
		select {
		case sendBuffer <- msg:
		case <-ctx.Done():
			s.proxylog.Warn("handleAPIEvents send suppressed due to context done")
		default:
			s.proxylog.Warn("handleAPIEvents sendBuffer full, dropped message")
		}
	}
	sendModels := func() {
		if data, err := json.Marshal(s.modelStatus()); err == nil {
			send(messageEnvelope{Type: msgTypeModelStatus, Data: string(data)})
		}
	}
	sendLogData := func(source string, data []byte) {
		if j, err := json.Marshal(map[string]string{"source": source, "data": string(data)}); err == nil {
			send(messageEnvelope{Type: msgTypeLogData, Data: string(j)})
		}
	}
	sendMetrics := func(metrics []ActivityLogEntry) {
		if j, err := json.Marshal(metrics); err == nil {
			send(messageEnvelope{Type: msgTypeMetrics, Data: string(j)})
		}
	}
	sendInFlight := func(total int) {
		if j, err := json.Marshal(map[string]int{"total": total}); err == nil {
			send(messageEnvelope{Type: msgTypeInFlight, Data: string(j)})
		}
	}
	sendBackendMetrics := func(metrics []BackendMetrics) {
		if j, err := json.Marshal(metrics); err == nil {
			send(messageEnvelope{Type: msgTypeBackendMetrics, Data: string(j)})
		}
	}
	sendLiveTokens := func(e shared.LiveTokensEvent) {
		if j, err := json.Marshal(map[string]any{
			"model":          e.Model,
			"output_tokens":  e.OutputTokens,
			"elapsed_ms":     e.ElapsedMs,
			"first_token_ms": e.FirstTokenMs,
		}); err == nil {
			send(messageEnvelope{Type: msgTypeLiveTokens, Data: string(j)})
		}
	}

	defer event.On(func(e shared.ProcessStateChangeEvent) { sendModels() })()
	defer event.On(func(e shared.ConfigFileChangedEvent) { sendModels() })()
	defer event.On(func(e shared.LiveTokensEvent) { sendLiveTokens(e) })()

	// A playground caller subscribes to model status only. It reaches this route
	// (pgChain) purely to populate its model picker, and it may be a stranger on
	// the LAN, so the server's log stream, request history and backend telemetry
	// are not part of the deal. An admin caller — the dashboard — gets the lot.
	fullFeed := s.adminAllowed(r)
	if fullFeed {
		defer s.proxylog.OnLogData(func(data []byte) { sendLogData("proxy", data) })()
		defer s.upstreamlog.OnLogData(func(data []byte) { sendLogData("upstream", data) })()
		defer event.On(func(e ActivityLogEvent) { sendMetrics([]ActivityLogEntry{e.Metrics}) })()
		defer event.On(func(e shared.InFlightRequestsEvent) { sendInFlight(e.Total) })()
		defer event.On(func(e BackendMetricsEvent) { sendBackendMetrics(e.Metrics) })()
	}

	// initial payload
	if fullFeed {
		sendLogData("proxy", s.proxylog.GetHistory())
		sendLogData("upstream", s.upstreamlog.GetHistory())
	}
	sendModels()
	if fullFeed {
		sendMetrics(s.metrics.getMetrics())
		sendInFlight(int(s.inflight.Current()))
		sendBackendMetrics(s.backendMetrics.snapshot())
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.shutdownCtx.Done():
			return
		case msg := <-sendBuffer:
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event:message\ndata:%s\n\n", data)
			flusher.Flush()
		}
	}
}
