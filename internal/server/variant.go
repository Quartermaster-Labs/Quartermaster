package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// maxCtxVariants caps how many synthetic per-request-ctx models a single base
// model can accumulate. Each one is a config entry that survives until the next
// file reload, so an unbounded client sweeping `?ctx=` values would otherwise
// grow the live config without limit.
const maxCtxVariants = 16

// ensureCtxVariant returns a model id that serves realID at the requested
// context size, minting it on first use. The synthetic model is a copy of the
// base with a freshly VRAM-sized launch command (the same sizer the cogwheel
// editor and adhoc-cmd use, so -ngl / --n-cpu-moe / KV types are re-derived for
// the new ctx rather than kept at the base model's values).
//
// The variant reuses the base model's already-allocated ${PORT} and proxy, and
// joins every group the base belongs to. Both are safe for the same reason
// ensureBackendVariant relies on: base and variant sit in one exclusive swap
// group, so only one of them ever runs at a time.
func (s *Server) ensureCtxVariant(realID string, ctx int) (string, error) {
	if s.autogen == nil {
		return "", fmt.Errorf("per-request ctx requires -generate")
	}
	syntheticID := ctxVariantID(realID, ctx)
	if s.local.Handles(syntheticID) {
		return syntheticID, nil
	}

	// Serialize minting so two concurrent first-requests can't each ApplyConfig
	// from their own stale snapshot and drop the other's variant.
	s.variantMu.Lock()
	defer s.variantMu.Unlock()
	if s.local.Handles(syntheticID) {
		return syntheticID, nil
	}

	cfg := s.config()
	base, ok := cfg.Models[realID]
	if !ok {
		return "", fmt.Errorf("model %q not found", realID)
	}
	baseInfo := config.ParseCmd(base.Cmd)
	if !baseInfo.Has("-c", "--ctx-size") {
		return "", fmt.Errorf("model %q takes no context-size flag", realID)
	}
	if n := countCtxVariants(cfg.Models, realID); n >= maxCtxVariants {
		return "", fmt.Errorf("model %q already has %d context variants (limit %d)", realID, n, maxCtxVariants)
	}
	gguf := baseInfo.ModelPath
	if gguf == "" {
		return "", fmt.Errorf("model %q has no gguf path to re-size", realID)
	}
	port, _ := baseInfo.Value("--port", "-port")
	if port == "" {
		return "", fmt.Errorf("could not determine port for model %q", realID)
	}

	cmd, err := s.renderAdhocCmd(gguf, variantDTO{Ctx: ctx})
	if err != nil {
		return "", err
	}
	// The sizer emits a ${PORT} placeholder; splice in the base model's concrete
	// port so the configured proxy target still matches.
	cmd = strings.ReplaceAll(cmd, "${PORT}", port)

	variant := base // struct copy
	variant.Cmd = cmd
	variant.Unlisted = true // routable + dashboard-visible, out of the /v1/models catalog
	if variant.Name == "" {
		variant.Name = realID
	}
	variant.Name += " @ ctx " + strconv.Itoa(ctx)

	addVariantToConfig(&cfg, realID, syntheticID, variant)
	if err := s.ApplyConfig(cfg); err != nil {
		return "", fmt.Errorf("registering ctx variant: %w", err)
	}
	return syntheticID, nil
}

// requestedCtx extracts a per-request context size from the requested model
// name ("qwen?ctx=32768") or an X-QM-Ctx header, header last so an explicit
// suffix in the payload wins. Out-of-range or non-numeric values report ok
// false and are ignored — the request then runs at the model's configured ctx
// rather than at some silently clamped size.
func requestedCtx(r *http.Request, requestedModel string) (int, bool) {
	if _, ctx, ok := config.SplitCtxRequest(requestedModel); ok {
		return ctx, true
	}
	h := strings.TrimSpace(r.Header.Get("X-QM-Ctx"))
	if h == "" {
		return 0, false
	}
	n, err := strconv.Atoi(h)
	if err != nil || n < config.MinRequestCtx || n > config.MaxRequestCtx {
		return 0, false
	}
	return n, true
}

func ctxVariantID(realID string, ctx int) string {
	return realID + "@ctx" + strconv.Itoa(ctx)
}

func countCtxVariants(models map[string]config.ModelConfig, realID string) int {
	prefix := realID + "@ctx"
	n := 0
	for id := range models {
		if strings.HasPrefix(id, prefix) {
			n++
		}
	}
	return n
}

// addVariantToConfig registers a synthetic model derived from realID into cfg,
// copy-on-write: fresh Models and Groups maps are built so the live config the
// rest of the server is reading is never mutated in place. The variant joins
// every group realID belongs to, which is what keeps it under the same
// exclusive-VRAM accounting as its base.
func addVariantToConfig(cfg *config.Config, realID, syntheticID string, variant config.ModelConfig) {
	newModels := make(map[string]config.ModelConfig, len(cfg.Models)+1)
	for k, v := range cfg.Models {
		newModels[k] = v
	}
	newModels[syntheticID] = variant
	cfg.Models = newModels

	oldGroups := cfg.Routing.Router.Settings.Groups
	newGroups := make(map[string]config.GroupConfig, len(oldGroups))
	for gid, g := range oldGroups {
		if containsStr(g.Members, realID) {
			m := make([]string, len(g.Members), len(g.Members)+1)
			copy(m, g.Members)
			g.Members = append(m, syntheticID)
		}
		newGroups[gid] = g
	}
	cfg.Routing.Router.Settings.Groups = newGroups
}

// ensureBackendVariant makes model realID routable on an ALTERNATE backend
// (registry entry backendID) without touching its configured backend. It clones
// the model's live config with the backend's exe swapped into argv[0], registers
// it under a synthetic id "<realID>@<backendID>" in the SAME swap group (so it
// evicts/loads against the same VRAM and shows on the dashboard), and returns
// that id to route to. Idempotent: a no-op once the variant is already live.
// Requires -generate — the backend registry lives in the autogen sidecar.
//
// ponytail: the synthetic model reuses the base model's already-allocated
// ${PORT} and proxy; safe because both share one exclusive group, so only one
// ever runs at a time. Minting is serialized on s.variantMu so concurrent
// first-requests can't each ApplyConfig from their own stale snapshot.
func (s *Server) ensureBackendVariant(realID, backendID string) (string, error) {
	if s.autogen == nil {
		return "", fmt.Errorf("backend override requires -generate")
	}
	syntheticID := realID + "@" + backendID
	if s.local.Handles(syntheticID) {
		return syntheticID, nil
	}
	// Serialize minting so two concurrent first-requests can't each ApplyConfig
	// from their own stale snapshot and drop the other's variant.
	s.variantMu.Lock()
	defer s.variantMu.Unlock()
	if s.local.Handles(syntheticID) {
		return syntheticID, nil
	}
	cfg := s.config()
	base, ok := cfg.Models[realID]
	if !ok {
		return "", fmt.Errorf("model %q not found", realID)
	}

	gf, err := autogen.LoadGenerateFile(s.autogen.GeneratePath, s.autogen.ModelsDir)
	if err != nil {
		return "", fmt.Errorf("loading backend registry: %w", err)
	}
	var exe, name string
	for _, e := range gf.Settings.Backends {
		if e.ID == backendID {
			exe = strings.TrimSpace(e.Path)
			name = e.Name
			break
		}
	}
	if exe == "" {
		return "", fmt.Errorf("backend %q not in registry (or has no path)", backendID)
	}
	newCmd, err := swapCmdExe(base.Cmd, exe)
	if err != nil {
		return "", err
	}

	variant := base // struct copy
	variant.Cmd = newCmd
	variant.Unlisted = true // routable + dashboard-visible, but out of the /v1/models catalog
	if name == "" {
		name = backendID
	}
	if variant.Name == "" {
		variant.Name = realID
	}
	variant.Name += " @ " + name

	addVariantToConfig(&cfg, realID, syntheticID, variant)
	if err := s.ApplyConfig(cfg); err != nil {
		return "", fmt.Errorf("registering backend variant: %w", err)
	}
	return syntheticID, nil
}

// swapCmdExe replaces the executable (argv[0]) of a model cmd with newExe,
// preserving the rest of the command byte-for-byte (flags, newlines, comments).
func swapCmdExe(cmd, newExe string) (string, error) {
	argv, err := config.SanitizeCommand(cmd)
	if err != nil || len(argv) == 0 {
		return "", fmt.Errorf("cannot parse model cmd")
	}
	oldExe := argv[0]
	i := strings.Index(cmd, oldExe)
	if i < 0 {
		// ponytail: only trips when the exe token is quoted/space-containing;
		// every generated cmd emits an unquoted exe path, so this is a hand-edit
		// edge case, not the normal path.
		return "", fmt.Errorf("cannot locate exe token in cmd")
	}
	return cmd[:i] + newExe + cmd[i+len(oldExe):], nil
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
