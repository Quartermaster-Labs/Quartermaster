package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// A quartermaster_configure call blocks the turn until the user accepts/denies
// the diff. If nobody answers (tab closed, walked away) the change is dropped so
// the turn — and the user's single active-turn slot — can't wedge forever.
const approvalTimeout = 5 * time.Minute

// qmDiffRow is one changed field, rendered as a before→after row in the approval
// card. Before/After are the raw JSON values (number/string/bool/null).
type qmDiffRow struct {
	Key    string `json:"key"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// pendingApproval is a config change awaiting the user's decision. Streamed to
// the client (kind:"approval") and re-sent on reconnect. The decide channel is
// unexported so it's skipped by json.Marshal.
type pendingApproval struct {
	ID     string      `json:"id"`     // the tool_call id — unique within the turn
	Target string      `json:"target"` // "settings" or a model id
	Diff   []qmDiffRow `json:"diff"`
	Status string      `json:"status"`           // pending | applied | denied | timeout | error
	Detail string      `json:"detail,omitempty"` // outcome text once resolved
	decide chan bool
}

// configPlan is a fully-resolved change, computed BEFORE the approval gate and
// applied only on accept.
type configPlan struct {
	target string
	path   string         // override/settings PUT path
	body   map[string]any // full merged body to PUT
	diff   []qmDiffRow
}

// The "quartermaster MCP": server-side dispatch for the quartermaster_inspect /
// quartermaster_configure playground chat tools (advertised client-side in
// ui-svelte/src/lib/qmTools.ts). Both work by calling quartermaster's OWN
// loopback API (tm.pg.SelfBase) with the same injected key the turn already uses,
// so they reuse every existing handler's validation + regen/reload instead of
// re-implementing config editing. Read is safe; configure goes through the same
// -generate editor endpoints (501 without -generate). No load/unload: swapping a
// model would evict the one answering the chat.

const (
	qmBodyLimit = 24 * 1024 // per-response cap fed back to the model
	// qmJSONLimit caps a response we PARSE ourselves before rendering it as text.
	// It must NOT be qmBodyLimit: that cap is about how much text the model can be
	// handed, and applying it to a JSON body cut /api/catalog mid-object on any
	// real fleet, so the decode failed with "unexpected end of JSON input" and the
	// tool reported the catalog as unreadable. The rendered text is still capped —
	// that happens after parsing, where a whole model can be dropped cleanly.
	qmJSONLimit = 4 << 20
)

// dispatchQM runs one quartermaster tool call, returning (displayLabel, resultText).
func (tm *turnManager) dispatchQM(ctx context.Context, at *activeTurn, tc toolCall) (string, string) {
	switch tc.Name {
	case "quartermaster_inspect":
		return tm.qmInspect(ctx, at, tc.Args)
	case "quartermaster_configure":
		return tm.qmConfigure(ctx, at, tc.ID, tc.Args)
	}
	return "quartermaster", "Unknown quartermaster tool."
}

// qmReq issues one loopback request and returns (status, body, err). The body is
// capped so a huge catalog can't blow the model's context.
func (tm *turnManager) qmReq(ctx context.Context, at *activeTurn, method, path string, body []byte) (int, string, error) {
	code, s, _, err := tm.qmDo(ctx, at, method, path, body, qmBodyLimit)
	return code, s, err
}

// qmDo is the shared loopback call. `limit` bounds the body read; `truncated`
// reports that the response hit it — harmless for text handed to the model,
// fatal for a body we are about to parse.
func (tm *turnManager) qmDo(ctx context.Context, at *activeTurn, method, path string, body []byte, limit int64) (int, string, bool, error) {
	u := strings.TrimRight(tm.pg.SelfBase, "/") + path
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return 0, "", false, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if at.authKey != "" {
		req.Header.Set("Authorization", "Bearer "+at.authKey) // /v1 is key-gated when keys are on
	}
	if at.user != "" {
		// The turn knows its user, so mint a properly signed session cookie for it
		// (same HMAC as a browser login) — that lets a loopback call reach per-user
		// endpoints like /api/prefs as that user.
		req.AddCookie(&http.Cookie{Name: pgCookie, Value: tm.pg.cookieValue(at.user)})
	}
	resp, err := tm.client.Do(req)
	if err != nil {
		return 0, "", false, err
	}
	defer resp.Body.Close()
	// Read one byte past the cap so hitting it is distinguishable from a body that
	// happens to be exactly that long.
	s, _ := readLimited(resp.Body, limit+1)
	truncated := int64(len(s)) > limit
	if truncated {
		s = s[:limit]
	}
	return resp.StatusCode, strings.TrimSpace(s), truncated, nil
}

// qmInspect reads live state and returns it as compact, human-readable TEXT (not
// raw JSON — that flooded the model's context). The `target` argument selects a
// slice so the model pulls only what it needs instead of one all-or-nothing dump:
//
//	""/"status"      → quick status: what's loaded + a one-line VRAM/RAM summary
//	"models"         → installed models with capabilities + context length
//	                   (variants folded in; `variants: true` expands them)
//	"loaded"         → models running now, with state + idle-TTL
//	"vram"           → live GPU/VRAM + system RAM
//	"settings"       → the global memory knobs
//	"logs"           → the last `tail` lines of the quartermaster (proxy) log
//	"backends"       → the backend registry: which exe/version each class runs
//	"estimate:<id>"  → a what-if load-plan sizing for that model (see qmEstimate)
//	<a model id>     → that model's effective config (ctx, KV, offload, variants…)
func (tm *turnManager) qmInspect(ctx context.Context, at *activeTurn, args string) (string, string) {
	var a struct {
		Target  string         `json:"target"`
		Tail    int            `json:"tail"`
		Source  string         `json:"source"`
		Options map[string]any `json:"options"`
	}
	json.Unmarshal([]byte(args), &a)
	target := strings.TrimSpace(a.Target)

	// "estimate:<model id>" — a what-if sizing run, so it carries a model id in the
	// target rather than a separate arg (one more top-level schema property sits in
	// every conversation's KV-stable prefix for a target used once in a hundred).
	if rest, ok := qmCutFold(target, "estimate"); ok {
		return "estimate", tm.qmEstimate(ctx, at, rest, a.Options)
	}

	switch strings.ToLower(target) {
	case "", "status", "overview":
		return "status", tm.qmStatus(ctx, at)
	case "models", "installed":
		return "models", tm.qmModels(ctx, at)
	case "loaded", "running":
		return "loaded", tm.qmLoaded(ctx, at)
	case "vram", "gpu", "memory":
		return "vram", tm.qmVram(ctx, at)
	case "settings":
		return "settings", tm.qmSettings(ctx, at)
	case "logs", "log":
		return "logs", tm.qmLogs(ctx, at, a.Tail, a.Source)
	case "backends", "backend":
		return "backends", tm.qmBackends(ctx, at)
	case "fields", "schema":
		// The full editable surface, pulled on demand — see turns_qm_fields.go for
		// why it isn't baked into the tool description.
		return "fields", qmFieldCatalog()
	default:
		return target, tm.qmModelConfig(ctx, at, target)
	}
}

const (
	qmLogsDefaultTail = 50
	qmLogsMaxTail     = 300
	// qmLogsReadLimit bounds the raw bytes pulled before tailing. The log monitor's
	// own history is capped (logmon.BufferSize = 100 KB), so this reads the whole
	// buffer — we then keep only the last N lines. Bigger than qmBodyLimit because
	// we tail from the END, not the front.
	qmLogsReadLimit = 128 * 1024
)

// qmLogs returns the last `tail` lines of a log so the model can diagnose a load
// failure/crash. `source` picks which: "proxy" (default) is quartermaster's OWN
// lifecycle log — loads, swaps, evictions, spawn/health errors — which is the
// useful diagnostic layer and, crucially, does NOT contain the answering model's
// own token-by-token decode spam; "upstream" is the raw backend (llama-server /
// sd-server) output for a crash reason (CUDA/Vulkan alloc errors etc., but noisy);
// "all" is both combined. Reads the full (bounded) history then tails — a
// front-truncated read would give the wrong end.
func (tm *turnManager) qmLogs(ctx context.Context, at *activeTurn, tail int, source string) string {
	if tail <= 0 {
		tail = qmLogsDefaultTail
	}
	if tail > qmLogsMaxTail {
		tail = qmLogsMaxTail
	}
	path, label := "/logs?source=proxy", "quartermaster"
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "upstream", "backend":
		path, label = "/logs?source=upstream", "upstream backend"
	case "all", "combined", "both":
		path, label = "/logs", "combined"
	}
	code, body, err := tm.qmGetRaw(ctx, at, path, qmLogsReadLimit)
	if err != nil {
		return "Couldn't read the log: " + err.Error()
	}
	if code != http.StatusOK {
		return fmt.Sprintf("Couldn't read the log (HTTP %d): %s", code, body)
	}
	if body == "" {
		return "The log is empty."
	}
	lines := strings.Split(body, "\n")
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return fmt.Sprintf("Last %d %s log line(s):\n", len(lines), label) + strings.Join(lines, "\n")
}

// qmGetRaw is qmReq's GET path with a caller-set read cap — used for /logs, whose
// tail we need in full (up to qmLogsReadLimit) rather than qmBodyLimit's front slice.
func (tm *turnManager) qmGetRaw(ctx context.Context, at *activeTurn, path string, limit int64) (int, string, error) {
	code, s, _, err := tm.qmDo(ctx, at, http.MethodGet, path, nil, limit)
	return code, s, err
}

// qmGetInto GETs a loopback JSON endpoint and decodes it into dst. It reads to
// qmJSONLimit, NOT qmBodyLimit — the model-facing cap belongs on rendered text,
// and applying it here truncated `/api/catalog` mid-object.
func (tm *turnManager) qmGetInto(ctx context.Context, at *activeTurn, path string, dst any) error {
	code, body, truncated, err := tm.qmDo(ctx, at, http.MethodGet, path, nil, qmJSONLimit)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", code, body)
	}
	// Say what actually happened. A truncated body fails to parse with a decoder
	// error that reads like the endpoint is broken.
	if truncated {
		return fmt.Errorf("response from %s is larger than %d bytes", path, qmJSONLimit)
	}
	return json.Unmarshal([]byte(body), dst)
}

// qmStatus is the cheap default: loaded models + a one-line VRAM/RAM summary,
// plus a hint at the other inspect targets.
func (tm *turnManager) qmStatus(ctx context.Context, at *activeTurn) string {
	var b strings.Builder
	b.WriteString(tm.qmLoaded(ctx, at))
	b.WriteString("\n\n")
	b.WriteString(tm.qmVramLine(ctx, at))
	b.WriteString("\n\nFor more detail, inspect: models · vram · settings · or a model id for its config.")
	return b.String()
}

// qmModels lists the installed catalog. It reads /api/catalog, NOT /v1/models:
// the latter is the OpenAI discovery route, so it is filtered by the model scope
// of whatever API key the turn authenticates with — an inspection tool that
// answers "what models do I have?" with "the one you're talking to" is worse
// than useless. /api/catalog is the dashboard's own view: every configured
// model, its live state, and the synthetic variants (`base@ctx32768`,
// `base@backend`) that are deliberately kept out of the OpenAI catalog.
//
// Variants are folded into their base and counted, never listed: a fleet with
// 16 ctx tiers per model would bury the catalog, and the interesting question
// about a variant ("what does it change?") is answered by inspecting the model
// id itself, which diffs each variant's launch command against the base.
func (tm *turnManager) qmModels(ctx context.Context, at *activeTurn) string {
	var resp struct {
		Models []apiModel `json:"models"`
	}
	if err := tm.qmGetInto(ctx, at, "/api/catalog", &resp); err != nil {
		return "Couldn't read the model list: " + err.Error()
	}
	if len(resp.Models) == 0 {
		return "No models are installed."
	}

	line := func(m apiModel, indent string) string {
		var s strings.Builder
		s.WriteString(indent + "• " + m.Id)
		if m.PeerID != "" {
			s.WriteString("  (peer " + m.PeerID + ")")
		}
		if caps := capLabels(m.Capabilities); len(caps) > 0 {
			s.WriteString("  [" + strings.Join(caps, ", ") + "]")
		}
		if m.Ctx > 0 {
			s.WriteString("  ctx " + humanCtx(m.Ctx))
		}
		if m.State != "" && m.State != "stopped" {
			s.WriteString("  - " + m.State)
		}
		if m.Unlisted && m.PeerID == "" {
			s.WriteString("  (unlisted)")
		}
		return s.String()
	}

	// Synthetic variants are named "<base id>@<something>"; group them under the
	// base when it exists, otherwise treat them as top-level rows.
	byID := make(map[string]bool, len(resp.Models))
	for _, m := range resp.Models {
		byID[m.Id] = true
	}
	variants := map[string][]apiModel{}
	var bases []apiModel
	for _, m := range resp.Models {
		if i := strings.Index(m.Id, "@"); i > 0 && byID[m.Id[:i]] {
			variants[m.Id[:i]] = append(variants[m.Id[:i]], m)
			continue
		}
		bases = append(bases, m)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d model(s) installed", len(bases))
	if n := len(resp.Models) - len(bases); n > 0 {
		fmt.Fprintf(&b, " (+%d variant(s))", n)
	}
	b.WriteString(":\n")
	for _, m := range bases {
		b.WriteString(line(m, ""))
		if n := len(variants[m.Id]); n > 0 {
			fmt.Fprintf(&b, "  (+%d variant(s))", n)
		}
		b.WriteString("\n")
	}
	if len(resp.Models) > len(bases) {
		b.WriteString("\nInspect a model by id to see its variants and what each one changes.")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (tm *turnManager) qmLoaded(ctx context.Context, at *activeTurn) string {
	var resp struct {
		Running []runningModel `json:"running"`
	}
	if err := tm.qmGetInto(ctx, at, "/running", &resp); err != nil {
		return "Couldn't read running models: " + err.Error()
	}
	if len(resp.Running) == 0 {
		return "Nothing is loaded right now."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d model(s) loaded:\n", len(resp.Running))
	for _, m := range resp.Running {
		line := "• " + m.Model + "  (" + m.State + ")"
		if m.TTL > 0 {
			fmt.Fprintf(&b, "%s, idle-unload %ds\n", line, m.TTL)
		} else {
			b.WriteString(line + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// perfSnapshot decodes the fields of /api/performance qmVram/qmStatus need.
type perfSnapshot struct {
	Gpu []struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		MemUsedMB  int    `json:"mem_used_mb"`
		MemTotalMB int    `json:"mem_total_mb"`
	} `json:"gpu_stats"`
	Sys []struct {
		MemUsedMB  int `json:"mem_used_mb"`
		MemTotalMB int `json:"mem_total_mb"`
	} `json:"sys_stats"`
	SystemMB int64 `json:"system_mb"`
	Foreign  struct {
		MB int `json:"mb"`
	} `json:"foreign"`
}

// latestGpus collapses the buffered per-tick history to the most recent sample
// per GPU id (later entries overwrite earlier), returned sorted by id.
func (p perfSnapshot) latestGpus() []struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	MemUsedMB  int    `json:"mem_used_mb"`
	MemTotalMB int    `json:"mem_total_mb"`
} {
	last := map[int]int{} // id -> index of newest sample
	for i, g := range p.Gpu {
		last[g.ID] = i
	}
	ids := make([]int, 0, len(last))
	for id := range last {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		MemUsedMB  int    `json:"mem_used_mb"`
		MemTotalMB int    `json:"mem_total_mb"`
	}, 0, len(ids))
	for _, id := range ids {
		out = append(out, p.Gpu[last[id]])
	}
	return out
}

func (tm *turnManager) qmVram(ctx context.Context, at *activeTurn) string {
	var p perfSnapshot
	if err := tm.qmGetInto(ctx, at, "/api/performance", &p); err != nil {
		return "Couldn't read GPU/VRAM stats: " + err.Error()
	}
	var b strings.Builder
	gpus := p.latestGpus()
	if len(gpus) == 0 {
		b.WriteString("No GPU stats available.\n")
	}
	for _, g := range gpus {
		free := g.MemTotalMB - g.MemUsedMB
		fmt.Fprintf(&b, "GPU %d (%s): %.1f / %.1f GB used, %.1f GB free\n",
			g.ID, g.Name, gb(g.MemUsedMB), gb(g.MemTotalMB), gb(free))
	}
	if len(p.Sys) > 0 {
		s := p.Sys[len(p.Sys)-1]
		fmt.Fprintf(&b, "System RAM: %.1f / %.1f GB used\n", gb(s.MemUsedMB), gb(s.MemTotalMB))
	}
	if p.SystemMB > 0 {
		fmt.Fprintf(&b, "Idle VRAM floor (reserved for the OS/desktop): %.1f GB\n", gb(int(p.SystemMB)))
	}
	if p.Foreign.MB > 0 {
		fmt.Fprintf(&b, "Foreign inference VRAM (not managed by this instance): %.1f GB\n", gb(p.Foreign.MB))
	}
	return strings.TrimRight(b.String(), "\n")
}

// qmVramLine is a one-liner VRAM summary for the default status view.
func (tm *turnManager) qmVramLine(ctx context.Context, at *activeTurn) string {
	var p perfSnapshot
	if err := tm.qmGetInto(ctx, at, "/api/performance", &p); err != nil {
		return "VRAM: unavailable"
	}
	parts := make([]string, 0, 2)
	for _, g := range p.latestGpus() {
		parts = append(parts, fmt.Sprintf("GPU %d %.1f/%.1f GB", g.ID, gb(g.MemUsedMB), gb(g.MemTotalMB)))
	}
	if len(parts) == 0 {
		return "VRAM: unavailable"
	}
	return "VRAM - " + strings.Join(parts, ", ")
}

func (tm *turnManager) qmSettings(ctx context.Context, at *activeTurn) string {
	var s struct {
		TargetVramGB   float64 `json:"targetVramGB"`
		VramOverheadGB float64 `json:"vramOverheadGB"`
		MaxRamGB       float64 `json:"maxRamGB"`
		TtlSec         int     `json:"ttlSec"`
	}
	if err := tm.qmGetInto(ctx, at, "/api/settings", &s); err != nil {
		return "Couldn't read settings: " + err.Error()
	}
	ttl := "never (models stay loaded until evicted for space)"
	if s.TtlSec > 0 {
		ttl = fmt.Sprintf("%ds idle", s.TtlSec)
	}
	return fmt.Sprintf(
		"Global memory settings:\n"+
			"• targetVramGB: %g - VRAM budget the sizer aims to fill per model\n"+
			"• vramOverheadGB: %g - headroom kept free on top of that\n"+
			"• maxRamGB: %g - cap on system RAM for CPU-offloaded weights/KV\n"+
			"• ttlSec: %d - idle auto-unload: %s",
		s.TargetVramGB, s.VramOverheadGB, s.MaxRamGB, s.TtlSec, ttl)
}

// qmCutFold splits "<prefix>:<rest>" (prefix matched case-insensitively, ':' or
// '=' or whitespace as the separator), so "estimate:qwen3" and "Estimate qwen3"
// both land. A bare prefix with no rest still matches, with rest == "".
func qmCutFold(target, prefix string) (string, bool) {
	if len(target) < len(prefix) || !strings.EqualFold(target[:len(prefix)], prefix) {
		return "", false
	}
	rest := target[len(prefix):]
	if rest == "" {
		return "", true
	}
	if rest[0] != ':' && rest[0] != '=' && rest[0] != ' ' {
		return "", false // "estimates", "estimate-foo" — a different target
	}
	return strings.TrimSpace(rest[1:]), true
}

// qmEstimateParams whitelists the sizer knobs the estimate target may forward,
// keyed by a lowercased alias → the real query param. A whitelist, not a
// pass-through of the model's `options` object: the values land in a URL, and an
// unknown key silently ignored by the handler would make the model report an
// estimate for a tuning it never asked for.
var qmEstimateParams = map[string]string{
	"ctx":               "ctx",
	"kvk":               "kvK",
	"kvv":               "kvV",
	"spec":              "spec",
	"kvinram":           "kvInRam",
	"ctxcheckpoints":    "ctxCheckpoints",
	"checkpointminstep": "checkpointMinStep",
	"vram":              "vram",
	"vramtargetgb":      "vram",
	"cpuoffload":        "cpuOffload",
	"actual":            "actual",
}

// qmEstimate runs the load-plan sizer for a model WITHOUT changing anything:
// the same `/api/models/{id}/estimate` preview the cogwheel draws. It answers
// the question a configure call otherwise has to guess at — "does ctx 128k with
// q8_0 KV actually fit in this box?" — so the model can check a tuning before
// proposing it, instead of proposing one that spills to RAM or refuses to spawn.
//
// Options are what-if overrides; anything not passed is re-derived by the sizer
// (NOT inherited from the model's current config), so an estimate with no
// options is the auto plan, not the configured one — pass actual:true for the
// running placement.
func (tm *turnManager) qmEstimate(ctx context.Context, at *activeTurn, id string, opts map[string]any) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "Missing the model id: use target='estimate:<model id>' (inspect target='models' for the ids)."
	}
	q := url.Values{}
	var unknown, asked []string
	for k, v := range opts {
		param, ok := qmEstimateParams[strings.ToLower(strings.TrimSpace(k))]
		if !ok {
			unknown = append(unknown, k)
			continue
		}
		s := qmScalarString(v)
		if s == "" {
			continue
		}
		q.Set(param, s)
		asked = append(asked, param+"="+s)
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return "Unknown estimate option(s): " + strings.Join(unknown, ", ") +
			". Valid options: ctx, kvK, kvV, spec, kvInRam, ctxCheckpoints, checkpointMinStep, vram, cpuOffload, actual."
	}

	path := "/api/models/" + url.PathEscape(id) + "/estimate"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var e struct {
		Ctx          int     `json:"ctx"`
		Ngl          int     `json:"ngl"`
		NCpuMoe      int     `json:"nCpuMoe"`
		EstVramGB    float64 `json:"estVramGB"`
		EstRamGB     float64 `json:"estRamGB"`
		TargetVramGB float64 `json:"targetVramGB"`
		MaxRamGB     float64 `json:"maxRamGB"`
		KvReserveGB  float64 `json:"kvReserveGB"`
		CheckpointGB float64 `json:"checkpointGB"`
		DraftGB      float64 `json:"draftGB"`
		ComputeBufGB float64 `json:"computeBufGB"`
		MmprojGB     float64 `json:"mmprojGB"`
		OverheadGB   float64 `json:"overheadGB"`
		RamExceeded  bool    `json:"ramExceeded"`
		IsMoE        bool    `json:"isMoE"`
	}
	if err := tm.qmGetInto(ctx, at, path, &e); err != nil {
		return "Couldn't estimate " + id + ": " + err.Error()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Load-plan estimate for %s", id)
	if len(asked) > 0 {
		sort.Strings(asked)
		fmt.Fprintf(&b, " (what-if: %s)", strings.Join(asked, ", "))
	}
	b.WriteString(" - nothing was changed:\n")
	fmt.Fprintf(&b, "• context it would launch with: %s\n", humanCtx(e.Ctx))
	fmt.Fprintf(&b, "• layer placement: -ngl %d", e.Ngl)
	if e.NCpuMoe > 0 {
		fmt.Fprintf(&b, ", --n-cpu-moe %d (MoE expert layers on CPU)", e.NCpuMoe)
	}
	b.WriteString("\n")
	fit := "fits"
	if e.TargetVramGB > 0 && e.EstVramGB > e.TargetVramGB {
		fit = "OVER BUDGET"
	}
	fmt.Fprintf(&b, "• estimated VRAM: %.1f GB of a %.1f GB budget - %s\n", e.EstVramGB, e.TargetVramGB, fit)
	ram := "within the cap"
	if e.RamExceeded {
		ram = "EXCEEDS the cap - this tuning would not be safe to load"
	}
	fmt.Fprintf(&b, "• estimated system RAM: %.1f GB of %.1f GB - %s\n", e.EstRamGB, e.MaxRamGB, ram)
	fmt.Fprintf(&b, "• of the VRAM: %.1f GB KV cache", e.KvReserveGB)
	for _, part := range []struct {
		label string
		v     float64
	}{
		{"checkpoints", e.CheckpointGB}, {"draft/MTP", e.DraftGB},
		{"compute buffer", e.ComputeBufGB}, {"vision projector", e.MmprojGB},
		{"safety headroom", e.OverheadGB},
	} {
		if part.v > 0 {
			fmt.Fprintf(&b, ", %.1f GB %s", part.v, part.label)
		}
	}
	b.WriteString(" (the rest is weights)\n")
	if e.IsMoE {
		b.WriteString("• this is a MoE model - cpuOffload moves whole layers, --n-cpu-moe only their experts\n")
	}
	b.WriteString("This is a prediction from the same sizer that bakes the config, not a load test.")
	return b.String()
}

// qmScalarString renders one JSON option value as a query-param string. Numbers
// come back as float64, so an integral one is printed without the ".000000" a
// %v would give — the handler parses "32768", not "32768.000000".
func qmScalarString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// qmBackends lists the backend registry: which executable (and, for a managed
// install, which upstream build) each class runs, plus the ★ class default a
// model with no explicit `backend` resolves to. The model needs this to answer
// "why is this model slow / why did it fail to spawn" — a missing exe or an old
// build is a whole class of problem invisible from the model config alone.
func (tm *turnManager) qmBackends(ctx context.Context, at *activeTurn) string {
	var list []struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Path      string `json:"path"`
		Default   bool   `json:"default"`
		Exists    bool   `json:"exists"`
		Managed   bool   `json:"managed"`
		Component string `json:"component"`
		Version   string `json:"version"`
		Variant   string `json:"variant"`
	}
	if err := tm.qmGetInto(ctx, at, "/api/backends", &list); err != nil {
		return "Couldn't read the backend registry: " + err.Error()
	}
	if len(list) == 0 {
		return "No backends are registered."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d backend(s) registered (a model's `backend` field picks one by id; ★ is the auto-pick for its class):\n", len(list))
	missing := 0
	for _, e := range list {
		kind := e.Kind
		if kind == "" {
			kind = "tool"
		}
		fmt.Fprintf(&b, "• %s [%s]", e.ID, kind)
		if e.Default {
			b.WriteString(" ★")
		}
		if e.Name != "" && e.Name != e.ID {
			b.WriteString("  " + e.Name)
		}
		if e.Managed {
			ver := e.Version
			if ver == "" {
				ver = "unknown version"
			}
			fmt.Fprintf(&b, "  - managed build %s", ver)
			if e.Variant != "" {
				b.WriteString(" (" + e.Variant + ")")
			}
		}
		if !e.Exists {
			b.WriteString("  - MISSING ON DISK")
			missing++
		}
		b.WriteString("\n")
		if e.Path != "" {
			fmt.Fprintf(&b, "  %s\n", e.Path)
		}
	}
	if missing > 0 {
		fmt.Fprintf(&b, "\n%d backend(s) point at an executable that isn't there - any model resolving to one of those cannot start.", missing)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (tm *turnManager) qmModelConfig(ctx context.Context, at *activeTurn, id string) string {
	var c modelConfigResp
	if err := tm.qmGetInto(ctx, at, "/api/models/"+url.PathEscape(id)+"/config", &c); err != nil {
		return "Couldn't read config for " + id + ": " + err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Config for %s:\n", c.Id)
	kind := "text"
	switch {
	case c.IsImage:
		kind = "image (diffusion / sd-server)"
	case c.IsAudio:
		kind = "audio / TTS (tts-server)"
	}
	add := func(label string, cond bool, val string) {
		if cond {
			fmt.Fprintf(&b, "• %s: %s\n", label, val)
		}
	}
	add("kind", true, kind)
	if c.MaxCtx > 0 {
		fmt.Fprintf(&b, "• trained context: %s (max ctx you can set)\n", humanCtx(c.MaxCtx))
	}
	if c.BlockCount > 0 {
		fmt.Fprintf(&b, "• layers: %d\n", c.BlockCount)
	}
	if drafts := draftLabels(c); drafts != "" {
		add("draft/speculative available", true, drafts)
	}

	o := c.Override
	if o == nil {
		b.WriteString("• per-model override: none - running on auto-derived defaults\n")
	} else {
		// Reflection over the DTO, not a hand-listed subset: a knob added to the
		// cogwheel shows up here (and in the configure tool) with no edit, and the
		// model never sees a config that silently omits a field that IS set.
		b.WriteString("• per-model override set (fields below differ from / pin auto defaults):\n")
		if qmRenderNonZero(&b, *o, "  ") == 0 {
			b.WriteString("  (all fields at their auto/inherit value)\n")
		}
	}

	// Variants come from two sources: the model's own override.Variants and the
	// fleet-wide settings.defaultVariants shared by every model. Show both, with
	// their salient settings, not just names.
	if o != nil && len(o.Variants) > 0 {
		b.WriteString("• per-model variants:\n")
		for _, v := range o.Variants {
			fmt.Fprintf(&b, "  - %s\n", describeVariant(v))
		}
	}
	if len(c.DefaultVariants) > 0 {
		b.WriteString("• fleet-wide variants (settings.defaultVariants, apply to every model):\n")
		for _, v := range c.DefaultVariants {
			fmt.Fprintf(&b, "  - %s\n", describeVariant(v))
		}
	}

	if c.Cmd != "" {
		fmt.Fprintf(&b, "• effective launch command (base/default variant):\n  %s\n", c.Cmd)
	}

	b.WriteString(tm.qmModelVariants(ctx, at, c.Id, c.Cmd))
	return strings.TrimRight(b.String(), "\n")
}

// qmMaxDiffedVariants caps how many sibling variants get a full flag diff. Each
// one costs a loopback /config call, and a model with the ctx-variant limit
// (16) would otherwise turn one inspect into 16 round trips and a wall of argv.
const qmMaxDiffedVariants = 8

// qmModelVariants renders the synthetic variants of a model — the separate
// (unlisted) config entries minted for a per-request ctx ("id@ctx32768") or an
// alternate backend ("id@vulkan"), NOT the named presets in override.Variants
// above. For each it diffs the variant's launch command against the base's, so
// the model reports what actually deviates instead of just naming ids.
//
// Also handles being called ON a variant: then the diff runs the other way, and
// the section names the base so the model can go read it.
func (tm *turnManager) qmModelVariants(ctx context.Context, at *activeTurn, id, baseCmd string) string {
	var cat struct {
		Models []apiModel `json:"models"`
	}
	if err := tm.qmGetInto(ctx, at, "/api/catalog", &cat); err != nil {
		return "" // the config above is still worth returning
	}

	state := map[string]string{}
	for _, m := range cat.Models {
		state[m.Id] = m.State
	}

	// Called on a variant: describe it relative to its base.
	if i := strings.Index(id, "@"); i > 0 {
		base := id[:i]
		if _, ok := state[base]; ok {
			var bc modelConfigResp
			if err := tm.qmGetInto(ctx, at, "/api/models/"+url.PathEscape(base)+"/config", &bc); err != nil {
				return fmt.Sprintf("• this is a variant of %s\n", base)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "• this is a variant of %s; it differs from the base by:\n", base)
			b.WriteString(qmDiffLines(bc.Cmd, baseCmd, "  "))
			return b.String()
		}
	}

	var ids []string
	for _, m := range cat.Models {
		if strings.HasPrefix(m.Id, id+"@") {
			ids = append(ids, m.Id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)

	var b strings.Builder
	fmt.Fprintf(&b, "• %d variant(s) of this model (separate routable ids, same swap group - deviations from the base command):\n", len(ids))
	shown := ids
	if len(shown) > qmMaxDiffedVariants {
		shown = shown[:qmMaxDiffedVariants]
	}
	for _, vid := range shown {
		fmt.Fprintf(&b, "  - %s", vid)
		if st := state[vid]; st != "" && st != "stopped" {
			b.WriteString(" - " + st)
		}
		b.WriteString("\n")
		var vc modelConfigResp
		if err := tm.qmGetInto(ctx, at, "/api/models/"+url.PathEscape(vid)+"/config", &vc); err != nil {
			b.WriteString("    (couldn't read its config: " + err.Error() + ")\n")
			continue
		}
		b.WriteString(qmDiffLines(baseCmd, vc.Cmd, "    "))
	}
	if len(ids) > len(shown) {
		fmt.Fprintf(&b, "  (+%d more variant(s) not diffed - inspect one by its id)\n", len(ids)-len(shown))
	}
	return b.String()
}

// qmDiffLines renders qmCmdDiff as indented bullet lines, or a "no difference"
// note — an empty section would read as "couldn't tell".
func qmDiffLines(base, variant, indent string) string {
	diffs := qmCmdDiff(base, variant)
	if len(diffs) == 0 {
		return indent + "(launch command identical to the base)\n"
	}
	var b strings.Builder
	for _, d := range diffs {
		b.WriteString(indent + "· " + d + "\n")
	}
	return b.String()
}

// qmCmdDiff compares two launch commands flag by flag and returns one line per
// deviation: value changed, flag added, flag dropped, or a different executable
// (what a backend variant is). Order follows the variant's own argv so the
// output reads in command order rather than alphabetically.
func qmCmdDiff(base, variant string) []string {
	bf, _ := qmCmdFlags(base)
	vf, vOrder := qmCmdFlags(variant)

	var out []string
	if b, v := qmExeName(base), qmExeName(variant); b != v && v != "" {
		out = append(out, fmt.Sprintf("backend exe: %s (base: %s)", v, b))
	}
	for _, f := range vOrder {
		bv, had := bf[f]
		vv := vf[f]
		switch {
		case !had:
			out = append(out, strings.TrimSpace(f+" "+vv)+" (base: not set)")
		case bv != vv:
			out = append(out, strings.TrimSpace(f+" "+vv)+" (base: "+qmOrNone(bv)+")")
		}
	}
	for _, f := range qmSortedKeys(bf) {
		if _, still := vf[f]; !still {
			out = append(out, f+" dropped (base: "+qmOrNone(bf[f])+")")
		}
	}
	return out
}

// qmCmdFlags maps each flag in a launch command to its value ("" for a boolean
// flag; repeated flags join their values), plus first-seen order. A token is a
// value, not a flag, when it doesn't start with '-' or is a negative number
// ("-ngl -1"), which is why this isn't a plain strings.HasPrefix check.
func qmCmdFlags(cmd string) (map[string]string, []string) {
	argv := config.ParseCmd(cmd).Argv
	flags := map[string]string{}
	var order []string
	isFlag := func(tok string) bool {
		if len(tok) < 2 || tok[0] != '-' {
			return false
		}
		return !(tok[1] >= '0' && tok[1] <= '9') && tok[1] != '.'
	}
	for i := 1; i < len(argv); i++ { // skip argv[0], the exe
		tok := argv[i]
		if !isFlag(tok) {
			continue
		}
		val := ""
		if i+1 < len(argv) && !isFlag(argv[i+1]) {
			val = argv[i+1]
			i++
		}
		if prev, dup := flags[tok]; dup {
			flags[tok] = strings.TrimSpace(prev + " " + val)
			continue
		}
		flags[tok] = val
		order = append(order, tok)
	}
	return flags, order
}

func qmExeName(cmd string) string {
	argv := config.ParseCmd(cmd).Argv
	if len(argv) == 0 {
		return ""
	}
	return filepath.Base(argv[0])
}

func qmOrNone(v string) string {
	if v == "" {
		return "set, no value"
	}
	return v
}

func qmSortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// draftLabels lists which speculative-decode drafts the model can use.
func draftLabels(c modelConfigResp) string {
	var d []string
	if c.IsMTP {
		d = append(d, "mtp")
	}
	if c.IsDflash {
		d = append(d, "dflash")
	}
	return strings.Join(d, ", ")
}

// describeVariant renders a variant name plus the settings it overrides, so the
// model sees what each variant actually changes (ctx tier, KV, spec, …) not just
// a bare label.
// Same reflection pass as the override dump, so a variant's every set field is
// visible (name is dropped — it's the label) instead of a curated subset.
func describeVariant(v variantDTO) string {
	name := v.Name
	v.Name = ""
	var inner strings.Builder
	qmRenderNonZero(&inner, v, "")
	parts := strings.Split(strings.TrimRight(inner.String(), "\n"), "\n")
	var kv []string
	for _, p := range parts {
		if p = strings.TrimPrefix(strings.TrimSpace(p), "• "); p != "" {
			kv = append(kv, strings.Replace(p, ": ", " ", 1))
		}
	}
	if len(kv) == 0 {
		return name
	}
	return name + " (" + strings.Join(kv, ", ") + ")"
}

// capLabels lists a model's true-valued capability keys, sorted, with a couple
// renamed to shorter words for the model to read.
func capLabels(caps map[string]any) []string {
	rename := map[string]string{"function_calling": "tools", "image_generation": "image-gen", "image_to_image": "img2img", "audio_transcriptions": "transcribe", "audio_speech": "tts"}
	var out []string
	for k, v := range caps {
		if b, ok := v.(bool); ok && b {
			if r, ok := rename[k]; ok {
				k = r
			}
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// humanCtx renders a token count as e.g. "32K" when it's a clean multiple of
// 1024, else the raw number.
func humanCtx(n int) string {
	if n >= 1024 && n%1024 == 0 {
		return fmt.Sprintf("%dK", n/1024)
	}
	return fmt.Sprintf("%d", n)
}

// gb converts MiB to GiB for display.
func gb(mb int) float64 { return float64(mb) / 1024 }

func (tm *turnManager) qmConfigure(ctx context.Context, at *activeTurn, callID, args string) (string, string) {
	plan, errMsg := tm.buildConfigPlan(ctx, at, args)
	if plan == nil {
		label := "configure"
		return label, errMsg
	}
	label := "configure " + plan.target

	// Human-in-the-loop gate: surface the diff and BLOCK the turn until the user
	// accepts or denies (or it times out / the turn is cancelled). Nothing is
	// applied without an explicit accept.
	pa := &pendingApproval{ID: callID, Target: plan.target, Diff: plan.diff, Status: "pending", decide: make(chan bool, 1)}
	at.requestApproval(pa)

	select {
	case accept := <-pa.decide:
		if !accept {
			at.resolveApproval(pa, "denied", "")
			return label + " - denied", "The user DENIED this change; nothing was applied. Do not retry it - acknowledge and move on."
		}
	case <-ctx.Done():
		at.resolveApproval(pa, "denied", "cancelled")
		return label + " - cancelled", "The turn was cancelled before the change was approved; nothing was applied."
	case <-time.After(approvalTimeout):
		at.resolveApproval(pa, "timeout", "")
		return label + " - timed out", "The approval request timed out; nothing was applied."
	}

	ok, text := tm.applyPlan(ctx, at, plan)
	if ok {
		at.resolveApproval(pa, "applied", diffSummary(plan.diff))
		// The outcome rides on the tool step's own label (and its result text
		// carries the diff), so the accepted change stays visible in the
		// reasoning trail after the transient approval card is gone — and
		// survives the post-turn resync, which drops the card's state.
		return label + " - accepted", text
	}
	at.resolveApproval(pa, "error", text)
	return label + " - failed", text
}

// buildConfigPlan resolves a configure request into a full merged body + a diff,
// reading current state but changing nothing. Returns (nil, errText) on any
// problem (bad args, unknown model, read failure, or a no-op change).
func (tm *turnManager) buildConfigPlan(ctx context.Context, at *activeTurn, args string) (*configPlan, string) {
	var a struct {
		Target  string         `json:"target"`
		Changes map[string]any `json:"changes"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return nil, "Bad arguments: " + err.Error()
	}
	target := strings.TrimSpace(a.Target)
	if target == "" {
		return nil, "Missing 'target' (use 'settings', 'playground', or a model id)."
	}
	if len(a.Changes) == 0 {
		return nil, "No 'changes' given - nothing to do."
	}

	// Playground settings are the logged-in user's own per-user prefs (a flat
	// key→value blob), not global config — so no hot-reload, and the field names
	// the model uses map to internal pref keys via a validated whitelist.
	if strings.EqualFold(target, "playground") {
		return tm.buildPlaygroundPlan(ctx, at, a.Changes)
	}

	// "<model id>#<variant name>" edits ONE named variant, mirroring the cogwheel's
	// variant tabs. Split before the model lookup — a '#' is not legal in a model id.
	modelID, variantName := target, ""
	if i := strings.LastIndex(target, "#"); i >= 0 {
		modelID, variantName = strings.TrimSpace(target[:i]), strings.TrimSpace(target[i+1:])
		if modelID == "" || variantName == "" {
			return nil, "Bad target: use '<model id>#<variant name>' to edit a variant."
		}
	}

	var path string
	var current map[string]any
	if strings.EqualFold(target, "settings") {
		target = "settings"
		path = "/api/settings"
		if msg := validateQmChanges(qmSettingsFieldSpecs(), a.Changes); msg != "" {
			return nil, msg
		}
		code, cur, err := tm.qmReq(ctx, at, http.MethodGet, "/api/settings", nil)
		if err != nil {
			return nil, "Reading current settings failed: " + err.Error()
		}
		if code != http.StatusOK {
			return nil, fmt.Sprintf("Reading current settings failed (HTTP %d): %s", code, cur)
		}
		var m map[string]any
		json.Unmarshal([]byte(cur), &m)
		// The settings PUT validates all four fields, so the body must carry them all.
		current = map[string]any{
			"targetVramGB":   m["targetVramGB"],
			"vramOverheadGB": m["vramOverheadGB"],
			"maxRamGB":       m["maxRamGB"],
			"ttlSec":         m["ttlSec"],
		}
	} else {
		specs := qmModelFieldSpecs()
		if variantName != "" {
			specs = qmVariantFieldSpecs()
		}
		if msg := validateQmChanges(specs, a.Changes); msg != "" {
			return nil, msg
		}
		code, cur, err := tm.qmReq(ctx, at, http.MethodGet, "/api/models/"+url.PathEscape(modelID)+"/config", nil)
		if err != nil {
			return nil, "Reading current config failed: " + err.Error()
		}
		if code != http.StatusOK {
			return nil, fmt.Sprintf("Reading current config failed (HTTP %d): %s", code, cur)
		}
		// Seed from the model's effective override (full field set + variants) so a
		// partial change preserves the rest.
		var cfg struct {
			Override map[string]any `json:"override"`
		}
		json.Unmarshal([]byte(cur), &cfg)
		current = cfg.Override
		if current == nil {
			current = map[string]any{}
		}
		if variantName != "" {
			// The variant PUT replaces the whole variant by name, so the body must be
			// that variant's CURRENT values with the changes layered on — not a sparse
			// patch (which would blank every other field).
			path = "/api/models/" + url.PathEscape(modelID) + "/variant"
			v, ok := findVariantMap(current["variants"], variantName)
			if !ok {
				return nil, fmt.Sprintf("Model %s has no variant named %q. quartermaster_inspect the model to see its variants; this tool edits existing variants only.", modelID, variantName)
			}
			current = v
			// name identifies the variant; changing it here would fork a new one.
			delete(a.Changes, "name")
			if len(a.Changes) == 0 {
				return nil, "No editable fields left in 'changes' (a variant's name can't be changed here)."
			}
		} else {
			path = "/api/models/" + url.PathEscape(modelID) + "/override"
		}
	}

	body, diff := mergeAndDiff(current, a.Changes)
	if len(diff) == 0 {
		return nil, "Those values are already set - no change needed."
	}
	return &configPlan{target: target, path: path, body: body, diff: diff}, ""
}

// findVariantMap picks one variant (case-insensitive by name) out of the decoded
// override.variants array, so a variant edit starts from its real current values.
func findVariantMap(variants any, name string) (map[string]any, bool) {
	arr, ok := variants.([]any)
	if !ok {
		return nil, false
	}
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := m["name"].(string); strings.EqualFold(n, name) {
			return m, true
		}
	}
	return nil, false
}

// prefField maps a model-facing playground setting name to its internal pref key
// plus a validator that type-checks and range-checks the incoming value.
type prefField struct {
	key      string
	validate func(any) (any, error)
}

// playgroundPrefFields is the whitelist of per-user playground settings the chat
// model may change. Keys mirror ui-svelte/src/stores/playground.ts. Anything not
// listed (system presets, selected model, rewrite state, theme) is off-limits.
var playgroundPrefFields = map[string]prefField{
	"temperature":      {"playground-temperature", numRange(0, 2)},
	"maxTokens":        {"playground-max-tokens", numMin(1)},
	"reasoningBudget":  {"playground-reasoning-budget", numMin(0)},
	"reasoning":        {"playground-reasoning", boolVal},
	"webSearch":        {"playground-websearch", boolVal},
	"qmTools":          {"playground-qmtools", boolVal},
	"searxngUrl":       {"playground-searxng-url", strVal},
	"searchMaxPerTurn": {"playground-search-max-per-turn", numMin(1)},
	"searchThrottleMs": {"playground-search-throttle-ms", numMin(0)},
	"searchDedupe":     {"playground-search-dedupe", boolVal},
}

func playgroundFieldList() string {
	names := make([]string, 0, len(playgroundPrefFields))
	for n := range playgroundPrefFields {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// buildPlaygroundPlan resolves a change to the user's playground prefs: reads the
// current blob, validates+maps each field, and returns a plan whose body is the
// full merged blob (PUT overwrites it wholesale) with a friendly-named diff.
func (tm *turnManager) buildPlaygroundPlan(ctx context.Context, at *activeTurn, changes map[string]any) (*configPlan, string) {
	code, cur, err := tm.qmReq(ctx, at, http.MethodGet, "/api/prefs", nil)
	if err != nil {
		return nil, "Reading your playground settings failed: " + err.Error()
	}
	if code != http.StatusOK {
		return nil, fmt.Sprintf("Reading your playground settings failed (HTTP %d): %s", code, cur)
	}
	prefs := map[string]any{}
	json.Unmarshal([]byte(cur), &prefs)

	body := map[string]any{}
	for k, v := range prefs {
		body[k] = v
	}
	var diff []qmDiffRow
	var unknown []string
	for name, raw := range changes {
		f, ok := playgroundPrefFields[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		val, verr := f.validate(raw)
		if verr != nil {
			return nil, fmt.Sprintf("Invalid value for %s: %s.", name, verr)
		}
		if !jsonEqual(prefs[f.key], val) {
			diff = append(diff, qmDiffRow{Key: name, Before: prefs[f.key], After: val})
		}
		body[f.key] = val
	}
	if len(unknown) > 0 {
		return nil, "Unknown playground setting(s): " + strings.Join(unknown, ", ") + ". Valid settings: " + playgroundFieldList() + "."
	}
	if len(diff) == 0 {
		return nil, "Those values are already set - no change needed."
	}
	return &configPlan{target: "playground", path: "/api/prefs", body: body, diff: diff}, ""
}

func asNum(v any) (float64, error) {
	f, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("must be a number")
	}
	return f, nil
}

func numMin(min float64) func(any) (any, error) {
	return func(v any) (any, error) {
		f, err := asNum(v)
		if err != nil {
			return nil, err
		}
		if f < min {
			return nil, fmt.Errorf("must be >= %g", min)
		}
		return f, nil
	}
}

func numRange(lo, hi float64) func(any) (any, error) {
	return func(v any) (any, error) {
		f, err := asNum(v)
		if err != nil {
			return nil, err
		}
		if f < lo || f > hi {
			return nil, fmt.Errorf("must be between %g and %g", lo, hi)
		}
		return f, nil
	}
}

func boolVal(v any) (any, error) {
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("must be true or false")
	}
	return b, nil
}

func strVal(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("must be a string")
	}
	return s, nil
}

// mergeAndDiff overlays changes onto current, returning the full merged body and
// a diff of only the fields whose value actually changes (no-ops are skipped so
// the approval card and tool result stay honest).
func mergeAndDiff(current, changes map[string]any) (map[string]any, []qmDiffRow) {
	body := map[string]any{}
	for k, v := range current {
		body[k] = v
	}
	var diff []qmDiffRow
	for k, v := range changes {
		if !jsonEqual(current[k], v) {
			diff = append(diff, qmDiffRow{Key: k, Before: current[k], After: v})
		}
		body[k] = v
	}
	return body, diff
}

// applyPlan PUTs the merged body (regenerates config + hot-reloads) and returns
// (ok, resultText) for the model.
func (tm *turnManager) applyPlan(ctx context.Context, at *activeTurn, plan *configPlan) (bool, string) {
	buf, err := json.Marshal(plan.body)
	if err != nil {
		return false, "Encoding change failed: " + err.Error()
	}
	code, resp, err := tm.qmReq(ctx, at, http.MethodPut, plan.path, buf)
	if err != nil {
		return false, "Applying change failed: " + err.Error()
	}
	if code != http.StatusOK {
		return false, fmt.Sprintf("Rejected (HTTP %d): %s", code, resp)
	}
	if plan.target == "playground" {
		return true, "Saved to the user's playground settings; it takes effect on their next page reload. " + diffSummary(plan.diff)
	}
	return true, "Applied and hot-reloaded (no models evicted). " + diffSummary(plan.diff)
}

// diffSummary renders the change compactly for the tool result / card detail.
func diffSummary(diff []qmDiffRow) string {
	parts := make([]string, len(diff))
	for i, d := range diff {
		parts[i] = fmt.Sprintf("%s %v→%v", d.Key, d.Before, d.After)
	}
	return strings.Join(parts, ", ")
}

// jsonEqual compares two decoded-JSON values by their marshaled form (numbers,
// strings, bools, null) — enough to tell a real change from a no-op.
func jsonEqual(a, b any) bool {
	ba, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ba) == string(bb)
}

// --- approval plumbing on activeTurn --------------------------------------

// requestApproval registers the pending change and streams it to viewers.
func (at *activeTurn) requestApproval(pa *pendingApproval) {
	at.mu.Lock()
	defer at.mu.Unlock()
	at.pending = pa
	payload, _ := json.Marshal(pa)
	at.fan(turnDelta{Kind: "approval", Data: payload})
}

// resolveApproval marks the pending change resolved and streams the final state
// (so the card drops its buttons and shows the outcome).
func (at *activeTurn) resolveApproval(pa *pendingApproval, status, detail string) {
	at.mu.Lock()
	defer at.mu.Unlock()
	pa.Status = status
	pa.Detail = detail
	payload, _ := json.Marshal(pa)
	at.fan(turnDelta{Kind: "approval", Data: payload})
	if at.pending == pa {
		at.pending = nil
	}
}

// deliverDecision routes a client accept/deny to the blocked turn. Returns false
// when there's no matching pending approval (already resolved / wrong id).
func (at *activeTurn) deliverDecision(id string, accept bool) bool {
	at.mu.Lock()
	defer at.mu.Unlock()
	if at.pending == nil || at.pending.ID != id {
		return false
	}
	select {
	case at.pending.decide <- accept:
	default: // already has a decision buffered; ignore the dup
	}
	return true
}

// handleTurnApprove — POST /api/chats/turn/approve {chatId, id, accept}. Delivers
// the user's decision to the blocked quartermaster_configure call.
func (s *Server) handleTurnApprove(w http.ResponseWriter, r *http.Request) {
	tm, user := s.turnAuth(w, r)
	if tm == nil {
		return
	}
	var body struct {
		ChatID string `json:"chatId"`
		ID     string `json:"id"`
		Accept bool   `json:"accept"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tm.mu.Lock()
	at := tm.active[user]
	tm.mu.Unlock()
	if at == nil || at.chatID != body.ChatID {
		http.Error(w, "no active turn for this chat", http.StatusNotFound)
		return
	}
	if !at.deliverDecision(body.ID, body.Accept) {
		http.Error(w, "no matching pending approval", http.StatusConflict)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}
