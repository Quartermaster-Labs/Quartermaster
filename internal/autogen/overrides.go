package autogen

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"gopkg.in/yaml.v3"
)

// GenerateFile is the external autogen control surface: tuning knobs (Settings)
// plus the per-model Overrides table. It replaces the PowerShell
// Generate-Config.ps1 parameters + $Overrides array so models can be tuned
// without recompiling.
type GenerateFile struct {
	Settings  Settings   `yaml:"settings"`
	Overrides []Override `yaml:"overrides"`
}

// Settings are the global generation knobs. Defaults mirror the PowerShell
// Generate-Config.ps1 parameter defaults; applyDefaults fills any zero value.
type Settings struct {
	ModelsRoot string `yaml:"modelsRoot"`
	// CategoryRoots are optional per-UI-category extra scan folders
	// ("llm"|"image"|"tts"|"transcribe" -> path). Discovery scans the union of
	// ModelsRoot + these; capability detection still decides each model's
	// category/engine, so a root is just additional scan scope (organizational),
	// not a hard tag. Empty/absent => only ModelsRoot is scanned.
	CategoryRoots map[string]string `yaml:"categoryRoots"`
	ServerExe     string            `yaml:"serverExe"`
	// SdServerExe runs all-in-one diffusion GGUFs (stable-diffusion.cpp's
	// sd-server). Empty => a sibling "sd-server" of ServerExe, else bare on PATH.
	SdServerExe string `yaml:"sdServerExe"`
	// TtsServerExe runs Qwen3-TTS talker GGUFs (qwentts.cpp's tts-server). Empty =>
	// a sibling "tts-server" of ServerExe, else bare on PATH.
	TtsServerExe string `yaml:"ttsServerExe"`
	// AsrServerExe runs Parakeet-family speech-to-text GGUFs (parakeet.cpp's
	// parakeet-server). Empty => a sibling "parakeet-server" of ServerExe, else
	// bare on PATH.
	AsrServerExe string `yaml:"asrServerExe"`
	// Backends is the dashboard's full backend registry (llama / vllm / sd / tts /
	// asr / custom entries). Populated from the UI sidecar; a model resolves its
	// backend against this via resolveBackend. The legacy ServerExe/SdServerExe/
	// TtsServerExe/AsrServerExe above stay as the fallback when no registry entry
	// matches a model's class.
	Backends []BackendEntry `yaml:"backends"`
	// KvQuant pins the fleet-wide default KV cache type (-ctk/-ctv) for every
	// llama-backed LLM: f32 | f16 | bf16 | q8_0 | q5_1 | q5_0 | q4_1 | q4_0.
	// Empty (default) => auto, which prefers f16 and steps down to q8_0 only when
	// f16 can't reach denseMinCtx inside the VRAM budget (see defaultKvQuant). A
	// per-model Override.KvK/KvV still wins over either. An unknown value is
	// ignored (falls back to auto).
	KvQuant      string  `yaml:"kvQuant"`
	TargetVramGB float64 `yaml:"targetVramGB"`
	// MultiResident emits the `vramBudgetGB` config block and a per-model
	// `estVramGB`, which switch the router from "every group is exclusive, any
	// load evicts everything else" to VRAM-budget admission: models stay
	// co-resident as long as their summed estimates fit TargetVramGB. nil =>
	// default ON. Set false to keep the legacy one-model-at-a-time behaviour
	// (the per-model estimate is still emitted; only the budget is withheld).
	MultiResident *bool `yaml:"multiResident"`
	// MinGpuFraction is the admission floor for the spawn-time sizer: the least
	// of a model's weight that must land on the GPU before a load is REFUSED
	// instead of silently degraded to CPU. It exists because multi-resident
	// loading otherwise always "succeeds" — the sizer just offloads the new
	// model until it fits the leftovers, and every model ends up crawling. Only
	// applies when the model's own baked plan was above the floor, so a model
	// deliberately configured to run mostly on CPU still loads. 0 => default
	// 0.5; negative disables the floor (pure best-effort degradation).
	MinGpuFraction float64 `yaml:"minGpuFraction"`
	// OomGuardReserveGB is extra VRAM held back from the resident model set for
	// the OTHER GPU clients' growth. The live ceiling the router admits against
	// is (vramBudgetGB - foreign usage ABOVE the idle baseline - this), so the
	// reserve is charged only while a foreign client is actually growing: a game
	// that has just claimed 8 GB is still allocating, and admitting a model into
	// the exact leftovers means the next allocation of either side lands in
	// shared memory. An untroubled box is charged nothing. 0 => default 1.0;
	// negative disables the reserve (admit into the raw leftovers).
	OomGuardReserveGB float64 `yaml:"oomGuardReserveGB"`
	// OomGuardEvict enables the post-load watchdog: when foreign VRAM grows into
	// the resident set's footprint and stays there for OomGuardGraceSec, IDLE
	// models are unloaded until the set fits again. Without it the only guard is
	// at spawn, and a model already resident when a game starts is silently
	// demoted into shared memory by the driver - no error, just collapsed
	// throughput. nil => default ON. Set false to leave resident models alone and
	// accept the degradation.
	OomGuardEvict *bool `yaml:"oomGuardEvict"`
	// OomGuardGraceSec is how long the resident set must be over the live ceiling
	// before the watchdog sheds anything. VRAM pressure is spiky - a shader
	// compile, a video decode, a browser painting - and unloading a model on a
	// transient spike costs a full reload for nothing. 0 => default 30.
	OomGuardGraceSec int `yaml:"oomGuardGraceSec"`
	// AutoVram measures free VRAM at gen time and caps TargetVramGB at it (the raw
	// reading — VramOverheadGB is charged inside the plan, not deducted here). Only
	// ever tightens: a static TargetVramGB below the reading stays a user ceiling.
	// The reading used is the idle high-water mark, not the instantaneous sample,
	// so re-resolving while a model is resident can't budget into its leftovers.
	AutoVram       bool    `yaml:"autoVram"`
	VramOverheadGB float64 `yaml:"vramOverheadGB"`
	// ComputeBufFactor scales the modeled compute buffer (logits + activations).
	// 1.0 = the analytic estimate; tune against the "compute buffer size" llama
	// prints at load if your build/arch differs. 0 => default 1.0. Measure PEAK
	// (mid-generation dedicated + SHARED usage), never an idle process — see the
	// warning above computeBufferGB before lowering this on Vulkan/ROCm.
	ComputeBufFactor float64 `yaml:"computeBufFactor"`
	// VisionOverheadGB is the VRAM reserved for a "-vision" twin's CLIP/vision
	// compute buffer (image-token activations + patch-embed work), on TOP of the
	// projector's own weights (its gguf size). llama.cpp allocates this when an
	// image is processed and it is NOT captured by the text compute-buffer model.
	// Tune against the "CLIP ... compute buffer size" llama prints for a real
	// image. 0 => default 1.0. ponytail: flat reserve, not resolution-scaled —
	// one image at a time here; make it image-size-aware if batched vision lands.
	VisionOverheadGB float64 `yaml:"visionOverheadGB"`
	// VisionCtx is the default context window (tokens) for the auto-generated
	// "-vision" twin. Image chats need a small text window — one image is a few
	// hundred-to-thousand tokens plus a short prompt — so the twin doesn't inherit
	// the solo model's maxed 32k ctx (that KV is ~2.5 GB on an 8B and buys nothing
	// for vision). A per-model/variant Ctx override still wins. 0 => default 8192.
	VisionCtx int `yaml:"visionCtx"`
	// Groups optionally split the emitted models across named groups bound to
	// separate listen addresses (use-case agnostic: membership is by model-name
	// glob, first match wins). Empty => one group, one port (upstream default).
	Groups       []GroupSpec `yaml:"groups"`
	MaxRamGB     float64     `yaml:"maxRamGB"`
	MoeCtxTarget int         `yaml:"moeCtxTarget"`
	// DefaultVariants are named custom variants emitted for EVERY non-skip model,
	// in addition to any per-override variants (use-case agnostic). Lets a config
	// apply a fleet-wide spawn shape (e.g. a low-VRAM coexistence variant) without
	// repeating it on each row. Same VariantSpec semantics as Override.Variants.
	DefaultVariants []VariantSpec `yaml:"defaultVariants"`
	// DisplayNames renames a model's advertised id WITHOUT touching the real
	// served id (the config key that keys cmd/gguf/slotcache/groups). Keyed by
	// BASE served id (row.ID); the new name cascades to every derived variant id
	// via a prefix swap (see resolvePublicName), so "foo"->"bar" also renames
	// "foo-mtp" to "bar-mtp". Each renamed id emits config name: + a routable
	// alias; the real id still resolves. UI-owned (sidecar), regen-safe.
	DisplayNames       map[string]string `yaml:"displayNames"`
	DenseCtxLadder     []int             `yaml:"denseCtxLadder"`
	DenseMinCtx        int               `yaml:"denseMinCtx"`
	Threads            int               `yaml:"threads"`
	TtlSec             int               `yaml:"ttlSec"`
	HealthCheckTimeout int               `yaml:"healthCheckTimeout"`
	// DryDefault is the fleet-wide DRY-sampler default. nil => off; set true to
	// make DRY on by default (a model still flips it via Override.Dry).
	DryDefault *bool `yaml:"dryDefault"`
	// APIKeys are the UI-managed API keys emitted into the generated config's
	// apiKeys / apiKeyModels blocks. Owned by the web UI (sidecar), not the
	// hand-authored generate file. Empty Models on an entry => full access.
	APIKeys []APIKeyEntry `yaml:"apiKeys"`
	// SlotCache enables on-disk slot KV persistence: adds --slot-save-path to each
	// llama-server cmd and emits the matching slotCache config block the server
	// reads. Off unless Enable.
	SlotCache SlotCacheSettings `yaml:"slotCache"`
	// Encoders declares the shared diffusion component files (VAE / CLIP / T5 /
	// text-encoder) ONCE, so image models get them auto-attached by architecture
	// instead of a per-model override each. A bare `--diffusion-model` GGUF carries
	// none of these; archComponents (image.go) maps each family to the fields it
	// needs. A per-model Override component path still wins over the pool.
	Encoders EncoderSet `yaml:"encoders"`
	// LoraDir is the fleet-wide `--lora-model-dir` for image models: the folder
	// sd-server scans for LoRA .safetensors, which is what `/sdapi/v1/loras`
	// lists and what a request's `lora: [{path,multiplier}]` refs resolve against.
	// Empty => each image model defaults to its OWN gguf's directory, so dropping
	// a LoRA next to the checkpoint it was trained for is zero-config. A per-model
	// Override.LoraDir still wins over both.
	LoraDir string `yaml:"loraDir"`
	// ExtraImageModels are sd-server image models that autogen's gguf scan can't
	// discover or header-parse — chiefly single-file .safetensors DiTs whose weights
	// exceed the gguf 4-dim tensor cap (HiDream-O1's 5-D vision patch-embed can't be
	// stored in gguf at all, only safetensors). Each entry emits a verbatim sd-server
	// block from explicit paths; no VRAM planner or arch detection runs. Persisted
	// here (not a runtime hack) so a regen keeps them.
	ExtraImageModels []ExtraImageModel `yaml:"extraImageModels"`
}

// ExtraImageModel is one hand-declared sd-server image model (see
// Settings.ExtraImageModels). Only ModelPath is required. ModelFlag defaults to
// "-m" (all-in-one checkpoint, the sd.cpp version-detect entry). The component
// paths are wired verbatim — nothing is arch-inferred — so a self-contained DiT
// that still needs an external VAE (HiDream-O1 bakes its text model but no VAE)
// just sets VaePath. Placement tri-states mirror Override ("" => on).
type ExtraImageModel struct {
	Name           string  `yaml:"name"`
	ModelPath      string  `yaml:"modelPath"`
	ModelFlag      string  `yaml:"modelFlag"` // "" => "-m"; or "--diffusion-model"
	VaePath        string  `yaml:"vaePath"`   // --vae
	LlmPath        string  `yaml:"llmPath"`   // --llm
	ClipLPath      string  `yaml:"clipLPath"` // --clip_l
	ClipGPath      string  `yaml:"clipGPath"` // --clip_g
	T5Path         string  `yaml:"t5Path"`    // --t5xxl
	LoraDir        string  `yaml:"loraDir"`   // --lora-model-dir ("" => settings.loraDir, else the model's own dir)
	VramTargetGB   float64 `yaml:"vramTargetGB"`
	DefaultCfg     float64 `yaml:"defaultCfg"`
	DefaultSteps   int     `yaml:"defaultSteps"`
	DefaultSampler string  `yaml:"defaultSampler"` // --sampling-method
	DefaultWidth   int     `yaml:"defaultWidth"`
	DefaultHeight  int     `yaml:"defaultHeight"`
	DiffusionFa    string  `yaml:"diffusionFa"`  // "" => on, "off" => off
	VaeTiling      string  `yaml:"vaeTiling"`    // "" => on, "off" => off
	TeOnCpu        string  `yaml:"teOnCpu"`      // "" => on (te=cpu), "off" => keep on GPU
	VaeOnCpu       string  `yaml:"vaeOnCpu"`     // "on" => add vae=cpu to --backend; "" => GPU
	OffloadToCpu   string  `yaml:"offloadToCpu"` // "on" => --offload-to-cpu (+ --vae-on-cpu)
	Threads        int     `yaml:"threads"`
	ExtraArgs      string  `yaml:"extraArgs"`
	Unlisted       bool    `yaml:"unlisted"`
}

// EncoderSet is the pool of shared diffusion component files, each field one
// physical file on disk. Models draw from it by architecture (archComponents),
// so adding another model of an already-declared family is zero-config. An empty
// field means "not on disk"; a model whose arch requires it emits a WARNING in
// the generated YAML rather than a silently broken command.
type EncoderSet struct {
	FluxVae   string `yaml:"fluxVae"`   // --vae for flux.1 / chroma (the flux "ae.safetensors")
	ClipL     string `yaml:"clipL"`     // --clip_l (flux, sdxl, sd3)
	ClipG     string `yaml:"clipG"`     // --clip_g (sdxl, sd3)
	T5        string `yaml:"t5"`        // --t5xxl (flux, sd3)
	SdxlVae   string `yaml:"sdxlVae"`   // --vae for sdxl (optional; full checkpoints bake it)
	ZimageVae string `yaml:"zimageVae"` // --vae for z-image / lumina
	QwenLlm   string `yaml:"qwenLlm"`   // --llm text encoder (z-image, qwen-image, flux.2 klein)
	Flux2Vae  string `yaml:"flux2Vae"`  // --vae for flux.2 (32-ch latent, NOT flux.1's fluxVae)
}

// SlotCacheSettings mirrors config.SlotCacheConfig; zero values fall back to the
// server's defaults (30k tokens / 10 GB / 20 sessions).
type SlotCacheSettings struct {
	Enable        bool   `yaml:"enable"`
	Path          string `yaml:"path"`
	MinSaveTokens int    `yaml:"minSaveTokens"`
	// RecurrentSeeds mirrors config.SlotCacheConfig.RecurrentSeeds: also run the
	// partial-prefix seed paths on hybrid/recurrent archs. Measurement knob.
	RecurrentSeeds bool    `yaml:"recurrentSeeds"`
	MaxDiskGB      float64 `yaml:"maxDiskGB"`
	MaxSessions    int     `yaml:"maxSessions"`
	// PreambleCaches mirrors config.SlotCacheConfig.PreambleCaches: the fleet-wide
	// switch for the preamble (system+tools seed) half of the cache. nil => on.
	PreambleCaches *bool `yaml:"preambleCaches"`
}

// APIKeyEntry is one named API key plus the model IDs it may reach. Name is a
// UI label only; Key is the secret; empty Models => unrestricted (full access,
// and admin rights over the management endpoints).
type APIKeyEntry struct {
	Name   string   `yaml:"name"`
	Key    string   `yaml:"key"`
	Models []string `yaml:"models"`
}

// CategoryOrder is the canonical UI-category order; RootList walks CategoryRoots
// in this order for deterministic scanning + hashing.
var CategoryOrder = []string{"llm", "image", "tts", "transcribe"}

// RootList returns the ordered, de-duplicated set of folders to scan: ModelsRoot
// first, then each CategoryRoots value in CategoryOrder. Blank entries are
// dropped; duplicates collapse to the first, comparing separator-insensitively
// always and case-insensitively only where the filesystem is (config.PathKey).
func (s Settings) RootList() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		key := config.PathKey(p)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, p)
	}
	add(s.ModelsRoot)
	for _, c := range CategoryOrder {
		add(s.CategoryRoots[c])
	}
	return out
}

// SettingsPatch is a partial Settings override written by the dashboard's
// "GPU memory" editor and stored in the UI sidecar. Only non-nil fields apply,
// so the hand-authored generate file keeps owning everything not touched in the
// UI. Setting a manual TargetVramGB pairs with AutoVram=false so the live-VRAM
// sampler doesn't clobber the user's choice.
// EVERY field here must be a pointer, and the dashboard PUTs only the fields
// its own section owns — see MergeSettingsPatch, which carries the rest forward.
// TestSettingsPatch_AllFieldsArePointers enforces the first half of that rule.
type SettingsPatch struct {
	TargetVramGB   *float64 `yaml:"targetVramGB,omitempty"`
	VramOverheadGB *float64 `yaml:"vramOverheadGB,omitempty"`
	MaxRamGB       *float64 `yaml:"maxRamGB,omitempty"`
	AutoVram       *bool    `yaml:"autoVram,omitempty"`
	DryDefault     *bool    `yaml:"dryDefault,omitempty"`
	// TtlSec is the idle-eviction timeout (seconds) baked into every model's
	// `ttl`. 0 => never auto-unload. nil => inherit the generate file / default.
	TtlSec *int `yaml:"ttlSec,omitempty"`

	// --- OOM guard (Settings.OomGuard*, consumed by internal/server/vramguard.go)
	OomGuardEvict     *bool    `yaml:"oomGuardEvict,omitempty"`
	OomGuardReserveGB *float64 `yaml:"oomGuardReserveGB,omitempty"`
	OomGuardGraceSec  *int     `yaml:"oomGuardGraceSec,omitempty"`

	// --- GPU usage / admission
	MinGpuFraction *float64 `yaml:"minGpuFraction,omitempty"`
	MultiResident  *bool    `yaml:"multiResident,omitempty"`

	// --- Advanced sizer knobs. Wrong values here mis-size every model, which is
	// why the UI hides them behind a warning and a per-section reset.
	ComputeBufFactor   *float64 `yaml:"computeBufFactor,omitempty"`
	VisionOverheadGB   *float64 `yaml:"visionOverheadGB,omitempty"`
	VisionCtx          *int     `yaml:"visionCtx,omitempty"`
	MoeCtxTarget       *int     `yaml:"moeCtxTarget,omitempty"`
	DenseMinCtx        *int     `yaml:"denseMinCtx,omitempty"`
	DenseCtxLadder     *[]int   `yaml:"denseCtxLadder,omitempty"`
	Threads            *int     `yaml:"threads,omitempty"`
	HealthCheckTimeout *int     `yaml:"healthCheckTimeout,omitempty"`

	// --- Fleet-wide model knobs
	KvQuant *string `yaml:"kvQuant,omitempty"`
	LoraDir *string `yaml:"loraDir,omitempty"`
}

// MergeSettingsPatch overlays next onto prev field-wise: a nil field in next
// keeps prev's value.
//
// Reflection rather than 20 hand-written nil checks, because the hand-written
// version is what this replaces and it had already lost the argument. The
// dashboard writes the patch in SECTIONS — the memory form PUTs four fields, the
// guard form three, the advanced form eight — and a whole-struct replace means
// every save wipes every field the saving section doesn't model. That bug was
// live for DryDefault and TtlSec, each fixed by its own bespoke carry-forward
// line; adding fifteen more fields to that pattern is fifteen more chances to
// forget one, and the symptom (a setting silently reverting a week later) is
// close to undebuggable. Here, a new pointer field is carried forward by
// construction.
func MergeSettingsPatch(prev, next SettingsPatch) SettingsPatch {
	out := next
	pv, ov := reflect.ValueOf(prev), reflect.ValueOf(&out).Elem()
	for i := 0; i < ov.NumField(); i++ {
		if ov.Field(i).Kind() == reflect.Ptr && ov.Field(i).IsNil() {
			ov.Field(i).Set(pv.Field(i))
		}
	}
	return out
}

// apply overlays the patch's set fields onto s. Mirrors MergeSettingsPatch's
// field-wise contract, but writes into the flat Settings (values, not pointers),
// so it stays explicit — the target field names differ from the source ones only
// by the dereference, and a reflect-based version here would silently do the
// wrong thing for the two fields Settings itself stores as pointers.
func (p *SettingsPatch) apply(s *Settings) {
	if p == nil {
		return
	}
	if p.TargetVramGB != nil {
		s.TargetVramGB = *p.TargetVramGB
	}
	if p.VramOverheadGB != nil {
		s.VramOverheadGB = *p.VramOverheadGB
	}
	if p.MaxRamGB != nil {
		s.MaxRamGB = *p.MaxRamGB
	}
	if p.AutoVram != nil {
		s.AutoVram = *p.AutoVram
	}
	if p.DryDefault != nil {
		s.DryDefault = p.DryDefault
	}
	if p.TtlSec != nil {
		s.TtlSec = *p.TtlSec
	}
	if p.OomGuardEvict != nil {
		s.OomGuardEvict = p.OomGuardEvict
	}
	if p.OomGuardReserveGB != nil {
		s.OomGuardReserveGB = *p.OomGuardReserveGB
	}
	if p.OomGuardGraceSec != nil {
		s.OomGuardGraceSec = *p.OomGuardGraceSec
	}
	if p.MinGpuFraction != nil {
		s.MinGpuFraction = *p.MinGpuFraction
	}
	if p.MultiResident != nil {
		s.MultiResident = p.MultiResident
	}
	if p.ComputeBufFactor != nil {
		s.ComputeBufFactor = *p.ComputeBufFactor
	}
	if p.VisionOverheadGB != nil {
		s.VisionOverheadGB = *p.VisionOverheadGB
	}
	if p.VisionCtx != nil {
		s.VisionCtx = *p.VisionCtx
	}
	if p.MoeCtxTarget != nil {
		s.MoeCtxTarget = *p.MoeCtxTarget
	}
	if p.DenseMinCtx != nil {
		s.DenseMinCtx = *p.DenseMinCtx
	}
	if p.DenseCtxLadder != nil {
		s.DenseCtxLadder = *p.DenseCtxLadder
	}
	if p.Threads != nil {
		s.Threads = *p.Threads
	}
	if p.HealthCheckTimeout != nil {
		s.HealthCheckTimeout = *p.HealthCheckTimeout
	}
	if p.KvQuant != nil {
		s.KvQuant = *p.KvQuant
	}
	if p.LoraDir != nil {
		s.LoraDir = *p.LoraDir
	}
}

// GroupSpec defines one output group and (optionally) the listen address that
// exposes it. Match is a list of model-name globs (PowerShell -like); a model
// joins the first group it matches. Listen empty => the group binds no
// dedicated port but still groups its members for eviction.
//
// Coexist turns the group into a coexistence group (emitted swap:false): its own
// members stay loaded alongside each other instead of evicting one another, while
// the group as a whole still evicts the other (exclusive) groups. That is what an
// eval harness wants on a card with room for several small models at once —
// several candidates resident, all other catalogs pushed out. Nothing here budgets
// VRAM, so the caller decides how many members it loads concurrently; oversubscribe
// and the extra llama-server just fails to allocate.
type GroupSpec struct {
	Name    string   `yaml:"name"`
	Listen  string   `yaml:"listen"`
	Match   []string `yaml:"match"`
	Coexist bool     `yaml:"coexist"`
}

// Override supplies what gguf metadata can't, matched by a path glob against the
// gguf's full path (first match wins; optional Quant scopes the match to one
// quant of a multi-quant repo). Mirrors the PowerShell $Overrides rows.
type Override struct {
	Match string `yaml:"match"`
	Quant string `yaml:"quant"`
	// Backend is the registry entry id (settings.backends) this model launches
	// with. Empty => auto-pick the class-default backend (see resolveBackend). The
	// entry's kind selects the emitter: "llama" => the llama-server path below,
	// "vllm" => vllm.go (a different arg set — the llama knobs are ignored, only
	// Ctx/Vllm* apply). Config is thus keyed to the backend: switching kind reads
	// a different subset of these fields; the dormant ones are kept, not wiped.
	Backend string `yaml:"backend"`
	// --- vllm knobs (kind "vllm"; ignored by the llama path) ---
	// VllmGpuUtil => --gpu-memory-utilization (0 => derived from the VRAM budget
	// and the card's size). VllmTensorParallel => --tensor-parallel-size when >1.
	// --max-model-len comes from Ctx when pinned, else it is sized against the
	// same budget.
	VllmGpuUtil        float64 `yaml:"vllmGpuUtil"`
	VllmTensorParallel int     `yaml:"vllmTensorParallel"`
	// VllmTokenizer => --tokenizer. Upstream recommends pointing vllm at the base
	// model's tokenizer rather than the one converted out of the gguf ("the
	// tokenizer conversion from GGUF is time-consuming and unstable"). It is not
	// derived automatically: the discovered row only knows the model's local
	// folder name, not a verified Hugging Face repo id, so a guess here would bake
	// a wrong remote reference into a launch command. A repo id or a local path.
	VllmTokenizer string `yaml:"vllmTokenizer"`
	Spec          string `yaml:"spec"`         // "draft-mtp" | "draft-dflash" | "" (=> ngram-mod); chainable with "+"
	ReasoningFmt  string `yaml:"reasoningFmt"` // "auto" | "off" | "" (=> auto)
	// ReasoningBudget caps thinking tokens (--reasoning-budget N). 0 => omit (no
	// cap). Inherited by ctx-tier variants; named variants are standalone.
	ReasoningBudget int    `yaml:"reasoningBudget"`
	CtxVariants     []int  `yaml:"ctxVariants"`
	Ctx             int    `yaml:"ctx"`
	KvK             string `yaml:"kvK"`
	KvV             string `yaml:"kvV"`
	KvInRam         bool   `yaml:"kvInRam"`
	// VramTargetGB caps how much VRAM this model is sized against. 0 => inherit
	// settings.TargetVramGB (the global budget). Lets one model run leaner than
	// the fleet default without a separate variant.
	VramTargetGB float64 `yaml:"vramTargetGB"`
	// CpuOffload pins how many layers are pushed to CPU, overriding the auto
	// sizer. 0 => auto. MoE models offload expert layers (--n-cpu-moe N); dense
	// models drop GPU layers (-ngl = blocks-N).
	CpuOffload int `yaml:"cpuOffload"`
	// Mmproj places the CLIP image projector behind the auto-generated "-vision"
	// twin. Only meaningful for a model that has a projector (own or inherited):
	//
	//	""     auto - sized both ways, GPU while it costs neither layer placement
	//	       nor a quarter of the context window (see cpuMmprojWins)
	//	"gpu"  pin the projector in VRAM: fastest image encode, and the twin pays
	//	       for it in ctx/offload
	//	"ram"  pin it in RAM (--no-mmproj-offload): free VRAM, slow image encode
	//	"none" emit no vision twin at all
	//
	// "none" is not the same as marking the twin unlisted: unlisted still builds
	// and can still be loaded by id, this removes the profile.
	Mmproj string `yaml:"mmproj"`
	// Engine knobs surfaced from llama-server. Zero/empty => the generator's
	// default (shown in parentheses):
	//   FlashAttn: "" (on) | "on" | "off" | "auto"  (-fa; required for quantized KV)
	//   Mmap:      "" | "on" | "off"                 ("" => placement-gated: mmap
	//              only where weights sit on the CPU, --load-mode none otherwise;
	//              see buildCmdLines. "on"/"off" force it either way)
	//   Mlock:     false => no --mlock; true => --mlock (lock weights in RAM)
	//   Threads:   0 => settings.Threads             (-t)
	//   Parallel:  0 => 1, capped at MaxParallelSlots (--parallel, concurrent
	//              slots). Sizing treats the computed ctx as PER SLOT and charges
	//              N x the KV cost; emit multiplies it back into -c, which
	//              --kv-unified makes the shared pool. So raising this shrinks the
	//              per-conversation window rather than over-committing VRAM.
	//   Ub:        0 => auto (1024, 512 for >=64k ctx) (-ub/-b physical batch)
	FlashAttn string `yaml:"flashAttn"`
	Mmap      string `yaml:"mmap"`
	Mlock     bool   `yaml:"mlock"`
	Threads   int    `yaml:"threads"`
	Parallel  int    `yaml:"parallel"`
	Ub        int    `yaml:"ub"`
	// CtxCheckpoints, when non-nil, emits --ctx-checkpoints N. 0 disables the KV
	// prompt-prefix checkpoint cache (llama-server default is 32, each a full KV
	// snapshot — costly in VRAM and rarely reused for short/divergent prompts).
	// nil => omit the flag (llama default). Variants inherit this unless they set
	// their own.
	CtxCheckpoints *int `yaml:"ctxCheckpoints"`
	// PreserveThinking emits --reasoning-preserve so the chat template keeps
	// prior-turn <think> blocks in history instead of stripping them (Qwen3.6+).
	// No-op when reasoning is off, and when the template has no preserve support
	// (llama-server warns and carries on). Requires the client to send
	// reasoning_content back on assistant messages.
	//
	// nil => ON. Reasoning amnesia across turns is the wrong default for an
	// agentic loop: the model re-derives what it already worked out last turn,
	// and on a template that preserves by default (3.8) omitting the flag was
	// never "off" anyway - it was "whatever the template does". So the generator
	// states it, and *false is the only way to get stripping.
	//
	// Still a POINTER, not a plain bool: as a bool, false could not be told from
	// "never set", so every save through the config editor stamped the default
	// onto the model and no default could ever be changed again.
	PreserveThinking *bool `yaml:"preserveThinking"`
	// Dry sampler (repetition penalty). Dry==nil => the fleet default (off);
	// *Dry==true emits the flags, *Dry==false omits them. DryMultiplier/DryBase/
	// DryAllowedLength override the defaults (0.8 / 1.75 / 3); 0 => default.
	// Mirrored on VariantSpec.
	Dry              *bool   `yaml:"dry"`
	DryMultiplier    float64 `yaml:"dryMultiplier"`
	DryBase          float64 `yaml:"dryBase"`
	DryAllowedLength int     `yaml:"dryAllowedLength"`
	// --- Sampler defaults baked into the launch command ---
	// llama-server applies each of these only when a request omits that field, so
	// they are the server-side default for every client. nil => omit the flag and
	// take llama's own default (temp 0.80 / top-k 40 / top-p 0.95 / min-p 0.05 /
	// presence 0.0).
	//
	// Pointers, not zero-gated floats like the knobs around them: 0 is a
	// MEANINGFUL, non-default value for every one of these (min-p 0 disables
	// truncation, temp 0 is greedy, top-k 0 disables top-k), so a plain float64
	// could not express the exact settings model cards recommend.
	//
	// TopK and MinP are the two that actually bite: neither has an OpenAI-API
	// field, so almost no client ever sends them and the launch flag is the only
	// thing that ever sets them. Temp/TopP are overwritten by nearly every
	// request and are mostly a floor for clients that omit them. Mirrored on
	// VariantSpec. See defaultSamplerFor for the arch-derived TopK/MinP baseline.
	Temp            *float64 `yaml:"temp"`
	TopK            *int     `yaml:"topK"`
	TopP            *float64 `yaml:"topP"`
	MinP            *float64 `yaml:"minP"`
	PresencePenalty *float64 `yaml:"presencePenalty"`
	// Speculative-decode sub-knobs, emitted only for the matching Spec backend;
	// 0/false => omit (llama-server default). SpecDraftNMax defaults to 2 for
	// draft-mtp, 6 for draft-dflash (a diffusion block tolerates a longer draft
	// chain than single-token MTP; 6 measured optimal, higher over-drafts); the
	// SpecNgram* knobs apply to the
	// ngram-map-k4v backend.
	SpecDraftNMax    int  `yaml:"specDraftNMax"`
	SpecDefault      bool `yaml:"specDefault"`
	SpecNgramSizeN   int  `yaml:"specNgramSizeN"`
	SpecNgramSizeM   int  `yaml:"specNgramSizeM"`
	SpecNgramMinHits int  `yaml:"specNgramMinHits"`
	// --- Advanced / power-user llama-server knobs ---
	// All default zero/empty => the flag is omitted, so existing configs emit
	// unchanged. Variants inherit these from the model-wide override and can
	// override each (see the effOv merge in generate.go). Grouped under the UI's
	// "Advanced" collapsible.
	//   ThreadsBatch: 0 => same as -t           (-tb, prompt/batch threads)
	//   Prio:         0 => normal                (--prio 0..3)
	//   DirectIo:     -dio (faster cold load)
	//   NoOpOffload:  --no-op-offload
	//   NoRepack:     --no-repack
	//   KvKDraft/KvVDraft: "" => llama f16       (-ctkd/-ctvd, draft KV quant; draft models only)
	//   CacheReuse:   0 => off                   (--cache-reuse N, prefix KV-shift reuse)
	//   CacheRamMB:   0 => llama default (8192)  (-cram, prompt-cache size MiB)
	//   CacheIdleSlots: "" | "on" | "off"        (--cache-idle-slots / --no-)
	//   SwaFull:      --swa-full (full SWA cache)
	//   CheckpointMinStep: 0 => llama default    (-cms, ctx-checkpoint spacing)
	//   ContextShift: "" | "on" | "off"          (--context-shift / --no-)
	//   SpecDraftNMin: 0 => llama default        (--spec-draft-n-min)
	//   SlotPromptSimilarity: 0 => omit          (-sps, slot reuse threshold)
	//   RopeScaling:  "" | none | linear | yarn  (--rope-scaling)
	//   RopeScale:    0 => omit                  (--rope-scale)
	//   RopeFreqBase: 0 => omit                  (--rope-freq-base)
	//   YarnOrigCtx:  0 => omit                  (--yarn-orig-ctx)
	//   SplitMode:    "" | none|layer|row|tensor (-sm, multi-GPU)
	//   TensorSplit:  "" => omit                 (-ts, e.g. "3,1")
	//   MainGpu:      0 => GPU 0                  (-mg)
	//   OverrideTensor: "" => omit               (-ot, manual tensor placement)
	ThreadsBatch         int     `yaml:"threadsBatch"`
	Prio                 int     `yaml:"prio"`
	DirectIo             bool    `yaml:"directIo"`
	NoOpOffload          bool    `yaml:"noOpOffload"`
	NoRepack             bool    `yaml:"noRepack"`
	KvKDraft             string  `yaml:"kvKDraft"`
	KvVDraft             string  `yaml:"kvVDraft"`
	CacheReuse           int     `yaml:"cacheReuse"`
	CacheRamMB           int     `yaml:"cacheRamMB"`
	CacheIdleSlots       string  `yaml:"cacheIdleSlots"`
	SwaFull              bool    `yaml:"swaFull"`
	CheckpointMinStep    int     `yaml:"checkpointMinStep"`
	ContextShift         string  `yaml:"contextShift"`
	SpecDraftNMin        int     `yaml:"specDraftNMin"`
	SlotPromptSimilarity float64 `yaml:"slotPromptSimilarity"`
	RopeScaling          string  `yaml:"ropeScaling"`
	RopeScale            float64 `yaml:"ropeScale"`
	RopeFreqBase         float64 `yaml:"ropeFreqBase"`
	YarnOrigCtx          int     `yaml:"yarnOrigCtx"`
	SplitMode            string  `yaml:"splitMode"`
	TensorSplit          string  `yaml:"tensorSplit"`
	MainGpu              int     `yaml:"mainGpu"`
	OverrideTensor       string  `yaml:"overrideTensor"`
	// ExtraArgs are additional llama-server flags appended verbatim to the emitted
	// command, for knobs autogen doesn't model (e.g. --rope-freq-scale,
	// --override-kv). The structured fields above still own the computed flags;
	// these are pure passthrough. The UI captures anything it can't map from the
	// editable launch-parameters box into here.
	ExtraArgs string `yaml:"extraArgs"`
	// ChatTemplateFile is a path to a .jinja chat template that replaces the
	// gguf's baked-in one (--chat-template-file). Empty => the baked-in template,
	// except for archs autogen ships a known-good fix for (Qwen 3.5/3.6); a
	// non-empty value always wins over that built-in fix.
	ChatTemplateFile string `yaml:"chatTemplateFile"`
	// --- Image (diffusion / sd-server) knobs ---
	// Only consumed for image-arch models (emitImageModel / imageCmdLines); ignored
	// by the llama-server path. The component paths are the external VAE + text
	// encoder(s) a diffusion-only GGUF needs (it loads via --diffusion-model, not an
	// all-in-one checkpoint). Empty => omit the flag.
	VaePath         string `yaml:"vaePath"`         // --vae
	ClipLPath       string `yaml:"clipLPath"`       // --clip_l
	ClipGPath       string `yaml:"clipGPath"`       // --clip_g
	T5Path          string `yaml:"t5Path"`          // --t5xxl
	TextEncoderPath string `yaml:"textEncoderPath"` // --llm (Z-Image / Lumina text encoder)
	// LoraDir is this model's `--lora-model-dir`. Empty => settings.loraDir, and
	// if that is empty too, the directory the model gguf itself lives in.
	LoraDir string `yaml:"loraDir"`
	// Placement tri-states: "" => the generator default (shown), "on"/"off" pin it.
	//   OffloadToCpu: "" => auto (sizer offloads when weights+compute don't fit)
	//   TeOnCpu:      "" => on  (--backend te=cpu); "off" keeps the encoder on GPU
	//   VaeTiling:    "" => on  (--vae-tiling, caps the VAE decode VRAM spike)
	//   DiffusionFa:  "" => on  (--diffusion-fa)
	//   VaeOnCpu:     "" => off (VAE decodes on GPU); "on" adds vae=cpu to --backend
	//                 (bf16 VAE whitens on some GPU backends; CPU is the safe fallback)
	OffloadToCpu string `yaml:"offloadToCpu"`
	TeOnCpu      string `yaml:"teOnCpu"`
	VaeOnCpu     string `yaml:"vaeOnCpu"`
	VaeTiling    string `yaml:"vaeTiling"`
	DiffusionFa  string `yaml:"diffusionFa"`
	// Generation defaults baked into the sd-server command (applied when a request
	// omits them). 0/empty => omit (sd-server's own default). DefaultCfg matters:
	// Z-Image-Turbo blurs unless cfg-scale is pinned to 1.0.
	DefaultSteps   int     `yaml:"defaultSteps"`   // --steps
	DefaultCfg     float64 `yaml:"defaultCfg"`     // --cfg-scale
	DefaultSampler string  `yaml:"defaultSampler"` // --sampling-method
	DefaultWidth   int     `yaml:"defaultWidth"`   // --width
	DefaultHeight  int     `yaml:"defaultHeight"`  // --height
	Unlisted       bool    `yaml:"unlisted"`
	Skip           bool    `yaml:"skip"`
	// SlotCache opts this model into on-disk slot KV persistence: emits
	// --slot-save-path so the server's slotCache can save/restore its conversation
	// KV. OPT-IN: nil/absent => off, so enabling the dashboard master switch does
	// not start littering snapshots and preamble caches for every model in the
	// fleet; set true on the models whose prefills are actually worth keeping.
	// No-op unless settings.slotCache.enable is also on (the master switch).
	SlotCache *bool `yaml:"slotCache"`
	// SlotCachePreamble opts this model out of the PREAMBLE half only (the shared
	// system+tools seed minted per agent), keeping conversation snapshots. nil =>
	// on for a model that set SlotCache. Emitted as the model's
	// slotCachePreamble config key, which the server reads per request.
	SlotCachePreamble *bool `yaml:"slotCachePreamble"`
	// Variants are named custom profiles emitted in addition to the solo model:
	// each becomes "<model>-<name>" with its own ctx/VRAM/kv/spec. Use-case
	// agnostic — the UI's "create variant" flow writes these.
	Variants []VariantSpec `yaml:"variants"`
}

// VariantSpec is one named custom variant of a model (UI-created). Zero/empty
// fields inherit: VramTargetGB => settings.TargetVramGB, KvK/KvV/Spec/
// ReasoningFmt => the model-wide override. Name is the suffix appended after
// the model id ("<model>-<name>").
//
// Ub and Dry are use-case-agnostic serving knobs that let a variant reproduce
// any spawn shape (e.g. a low-VRAM "game" coexistence variant, or a short-ctx
// "judge" variant with greedy sampling) without the engine knowing those names:
//   - Ub: physical batch size (-ub/-b). 0 => default (1024, or 512 for >=64k ctx).
//   - Dry: nil => the fleet default (off); *Dry==true emits the flags, false omits.
//
// ReasoningFmt: "off" emits "--reasoning off" (plus --reasoning-format none).
type VariantSpec struct {
	Name         string  `yaml:"name"`
	Ctx          int     `yaml:"ctx"`
	VramTargetGB float64 `yaml:"vramTargetGB"`
	KvK          string  `yaml:"kvK"`
	KvV          string  `yaml:"kvV"`
	Spec         string  `yaml:"spec"`
	ReasoningFmt string  `yaml:"reasoningFmt"`
	Ub           int     `yaml:"ub"`
	Dry          *bool   `yaml:"dry"`
	// CtxCheckpoints, when non-nil, emits --ctx-checkpoints N (0 disables). nil =>
	// inherit the model-wide Override.CtxCheckpoints.
	CtxCheckpoints *int `yaml:"ctxCheckpoints"`
	Unlisted       bool `yaml:"unlisted"`
	// PreserveThinking is *bool so a standalone variant defaults preserve-on
	// (nil => emit, matching the Qwen3.6 reasoning default); false disables it.
	PreserveThinking *bool `yaml:"preserveThinking"`
	// SlotCache opts this variant into on-disk slot KV persistence. nil => inherit
	// the model-wide Override.SlotCache; true/false sets it explicitly. Gated by
	// settings.slotCache.enable like the model-wide flag.
	SlotCache *bool `yaml:"slotCache"`
	// Engine knobs mirroring Override, so a variant can carry the full launch
	// shape (the UI's "full settings page" for a variant). Named variants are
	// STANDALONE: zero/empty => the generator default, NOT the model-wide Override
	// (the Default tab and a variant are independent profiles).
	KvInRam    bool   `yaml:"kvInRam"`
	CpuOffload int    `yaml:"cpuOffload"`
	FlashAttn  string `yaml:"flashAttn"`
	Mmap       string `yaml:"mmap"`
	Mlock      bool   `yaml:"mlock"`
	Threads    int    `yaml:"threads"`
	Parallel   int    `yaml:"parallel"`
	ExtraArgs  string `yaml:"extraArgs"`
	// ChatTemplateFile mirrors Override; empty => inherit the model-wide value.
	ChatTemplateFile string `yaml:"chatTemplateFile"`
	// Mmproj pins the image projector's placement, but ONLY on the reserved
	// "vision" variant - it is the one variant that tunes a profile carrying an
	// mmproj at all. Same vocabulary as Override.Mmproj ("gpu"/"ram"/"none");
	// empty inherits the model-wide value, so an untouched variant changes
	// nothing. Ignored on every other variant, whose profile loads no projector.
	Mmproj string `yaml:"mmproj"`
	// Sampler / speculative sub-knobs mirroring Override; 0/empty => inherit the
	// model-wide value. (Dry on/off is the *bool field above.)
	DryMultiplier    float64 `yaml:"dryMultiplier"`
	DryBase          float64 `yaml:"dryBase"`
	DryAllowedLength int     `yaml:"dryAllowedLength"`
	// Sampler defaults mirroring Override; nil => inherit the model-wide value
	// (and, failing that, the arch baseline / llama's default). Non-nil wins at
	// merge, so a variant can pin greedy sampling (temp 0) on a model whose
	// default is hot.
	Temp             *float64 `yaml:"temp"`
	TopK             *int     `yaml:"topK"`
	TopP             *float64 `yaml:"topP"`
	MinP             *float64 `yaml:"minP"`
	PresencePenalty  *float64 `yaml:"presencePenalty"`
	SpecDraftNMax    int      `yaml:"specDraftNMax"`
	SpecDefault      bool     `yaml:"specDefault"`
	SpecNgramSizeN   int      `yaml:"specNgramSizeN"`
	SpecNgramSizeM   int      `yaml:"specNgramSizeM"`
	SpecNgramMinHits int      `yaml:"specNgramMinHits"`
	// Advanced / power-user knobs mirroring Override; zero/empty => inherit the
	// model-wide value (a variant's own non-zero/non-empty value wins at merge).
	ThreadsBatch         int     `yaml:"threadsBatch"`
	Prio                 int     `yaml:"prio"`
	DirectIo             bool    `yaml:"directIo"`
	NoOpOffload          bool    `yaml:"noOpOffload"`
	NoRepack             bool    `yaml:"noRepack"`
	KvKDraft             string  `yaml:"kvKDraft"`
	KvVDraft             string  `yaml:"kvVDraft"`
	CacheReuse           int     `yaml:"cacheReuse"`
	CacheRamMB           int     `yaml:"cacheRamMB"`
	CacheIdleSlots       string  `yaml:"cacheIdleSlots"`
	SwaFull              bool    `yaml:"swaFull"`
	CheckpointMinStep    int     `yaml:"checkpointMinStep"`
	ContextShift         string  `yaml:"contextShift"`
	SpecDraftNMin        int     `yaml:"specDraftNMin"`
	SlotPromptSimilarity float64 `yaml:"slotPromptSimilarity"`
	RopeScaling          string  `yaml:"ropeScaling"`
	RopeScale            float64 `yaml:"ropeScale"`
	RopeFreqBase         float64 `yaml:"ropeFreqBase"`
	YarnOrigCtx          int     `yaml:"yarnOrigCtx"`
	SplitMode            string  `yaml:"splitMode"`
	TensorSplit          string  `yaml:"tensorSplit"`
	MainGpu              int     `yaml:"mainGpu"`
	OverrideTensor       string  `yaml:"overrideTensor"`
	// Image (sd-server) knobs. Unlike the llama knobs above, an image variant
	// INHERITS the model-wide Override for anything it leaves empty (component
	// paths + placement) and overrides only what it sets — the natural use is a
	// generation preset (e.g. "fast" 8-step cfg 1 vs "quality" 30-step) on the
	// same model + encoders. See mergeImageVariant.
	VaePath         string  `yaml:"vaePath"`
	ClipLPath       string  `yaml:"clipLPath"`
	ClipGPath       string  `yaml:"clipGPath"`
	T5Path          string  `yaml:"t5Path"`
	TextEncoderPath string  `yaml:"textEncoderPath"`
	LoraDir         string  `yaml:"loraDir"`
	OffloadToCpu    string  `yaml:"offloadToCpu"`
	TeOnCpu         string  `yaml:"teOnCpu"`
	VaeOnCpu        string  `yaml:"vaeOnCpu"`
	VaeTiling       string  `yaml:"vaeTiling"`
	DiffusionFa     string  `yaml:"diffusionFa"`
	DefaultSteps    int     `yaml:"defaultSteps"`
	DefaultCfg      float64 `yaml:"defaultCfg"`
	DefaultSampler  string  `yaml:"defaultSampler"`
	DefaultWidth    int     `yaml:"defaultWidth"`
	DefaultHeight   int     `yaml:"defaultHeight"`
}

// applyDefaults fills zero-valued settings with the PowerShell defaults.
func (s *Settings) applyDefaults() {
	if s.ServerExe == "" {
		s.ServerExe = "llama-server"
	}
	if s.SdServerExe == "" {
		if strings.ContainsAny(s.ServerExe, `/\`) {
			s.SdServerExe = filepath.Join(filepath.Dir(s.ServerExe), "sd-server")
		} else {
			s.SdServerExe = "sd-server"
		}
	}
	if s.TtsServerExe == "" {
		if strings.ContainsAny(s.ServerExe, `/\`) {
			s.TtsServerExe = filepath.Join(filepath.Dir(s.ServerExe), "tts-server")
		} else {
			s.TtsServerExe = "tts-server"
		}
	}
	if s.AsrServerExe == "" {
		if strings.ContainsAny(s.ServerExe, `/\`) {
			s.AsrServerExe = filepath.Join(filepath.Dir(s.ServerExe), "parakeet-server")
		} else {
			s.AsrServerExe = "parakeet-server"
		}
	}
	// The budget placeholders. LoadGenerateFile replaces both with a live
	// hardware reading when the file/sidecar left them unset (seedHardwareBudgets),
	// so these only survive on a box with no GPU telemetry and no OS memory
	// reading — deliberately small, because the cost of guessing low is a model
	// sized conservatively and the cost of guessing high is a load that OOMs.
	if s.TargetVramGB == 0 {
		s.TargetVramGB = 7
	}
	// vramOverheadGB is pure allocator slack: targetVramGB is already the free
	// VRAM, so the desktop's own usage is outside the budget rather than
	// something this has to reserve for. 1.0 was double-counting that, and on a
	// 12 GB card it cost a dense model a whole layer for nothing. 0.5 also
	// matches the example file and longCtxHeadroomGB, which makes the long-context
	// top-up in longCtxTarget an exact no-op instead of a partial one.
	if s.VramOverheadGB == 0 {
		s.VramOverheadGB = 0.5
	}
	if s.MinGpuFraction == 0 {
		s.MinGpuFraction = 0.5
	} else if s.MinGpuFraction < 0 {
		s.MinGpuFraction = 0 // explicit opt-out: no floor, degrade instead of refuse
	}
	if s.OomGuardReserveGB == 0 {
		s.OomGuardReserveGB = 1.0
	} else if s.OomGuardReserveGB < 0 {
		s.OomGuardReserveGB = 0 // explicit opt-out: admit into the raw leftovers
	}
	if s.OomGuardGraceSec == 0 {
		s.OomGuardGraceSec = 30
	}
	if s.ComputeBufFactor == 0 {
		s.ComputeBufFactor = 1.0
	}
	if s.VisionOverheadGB == 0 {
		s.VisionOverheadGB = 1.0
	}
	if s.VisionCtx == 0 {
		s.VisionCtx = 8192
	}
	if s.MaxRamGB == 0 {
		s.MaxRamGB = 24 // placeholder; see TargetVramGB above
	}
	if s.MoeCtxTarget == 0 {
		s.MoeCtxTarget = 65536
	}
	if len(s.DenseCtxLadder) == 0 {
		s.DenseCtxLadder = []int{131072, 65536, 32768}
	}
	if s.DenseMinCtx == 0 {
		s.DenseMinCtx = 32768
	}
	if s.Threads == 0 {
		s.Threads = 7
	}
	if s.TtlSec == 0 {
		s.TtlSec = 600
	}
	if s.HealthCheckTimeout == 0 {
		s.HealthCheckTimeout = 300
	}
	// Slot KV-cache persistence is OFF by default (the master switch); the
	// server-default knobs below are still pre-filled so the dashboard shows real
	// numbers, not 0. Per-model SlotCache is OPT-IN (nil => off), so the master
	// switch arms the feature and each model is enrolled from its config editor -
	// otherwise every model in the fleet starts leaving snapshots and preamble
	// caches behind on its first big prefill. Enable's zero value (false) is
	// the default; turn it on via the dashboard (writes enable:true to the
	// sidecar, which overlays this).
	if s.SlotCache.MinSaveTokens == 0 {
		s.SlotCache.MinSaveTokens = 30000
	}
	if s.SlotCache.MaxDiskGB == 0 {
		s.SlotCache.MaxDiskGB = 10
	}
	if s.SlotCache.MaxSessions == 0 {
		s.SlotCache.MaxSessions = 20
	}
}

// LoadGenerateFile reads and validates an autogen control file. Settings
// defaults are applied. modelsDirOverride (from --models-dir) wins over the
// file's modelsRoot when non-empty.
func LoadGenerateFile(path, modelsDirOverride string) (GenerateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return GenerateFile{}, err
	}
	var gf GenerateFile
	if err := yaml.Unmarshal(data, &gf); err != nil {
		return GenerateFile{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	// Capture "the file said nothing about the memory budgets" BEFORE
	// applyDefaults writes its placeholders over the zero values, so
	// seedHardwareBudgets below can tell an unset knob from a deliberate one.
	vramUnset, ramUnset := gf.Settings.TargetVramGB == 0, gf.Settings.MaxRamGB == 0
	// Overlay UI-owned backend-exe paths BEFORE applyDefaults so an empty
	// sd/tts-server derives as a sibling of the (possibly UI-set) llama exe.
	be, err := LoadSidecarBackends(path)
	if err != nil {
		return GenerateFile{}, err
	}
	if be != nil {
		if be.ServerExe != "" {
			gf.Settings.ServerExe = be.ServerExe
		}
		if be.SdServerExe != "" {
			gf.Settings.SdServerExe = be.SdServerExe
		}
		if be.TtsServerExe != "" {
			gf.Settings.TtsServerExe = be.TtsServerExe
		}
		if be.AsrServerExe != "" {
			gf.Settings.AsrServerExe = be.AsrServerExe
		}
	}
	// The full backend registry (multi-entry) that per-model resolveBackend reads.
	// Sidecar wins over any generate-file backends list.
	if list, err := LoadSidecarBackendList(path); err != nil {
		return GenerateFile{}, err
	} else if len(list) > 0 {
		gf.Settings.Backends = list
	}
	gf.Settings.applyDefaults()
	// Overlay the UI-owned settings patch (dashboard VRAM/headroom edits) ahead of
	// modelsDir resolution. Absent => no-op.
	patch, err := LoadSidecarSettings(path)
	if err != nil {
		return GenerateFile{}, err
	}
	patch.apply(&gf.Settings)
	if patch != nil {
		vramUnset = vramUnset && patch.TargetVramGB == nil
		ramUnset = ramUnset && patch.MaxRamGB == nil
	}
	// Measure the box for anything neither the file nor the dashboard pinned.
	// Done here rather than in applyDefaults so the ONE cached probe covers every
	// consumer of the merged settings — startup, hot reload, and the editor's
	// preview all size against the same budget instead of the preview quietly
	// using the placeholder the emitter replaced.
	seedHardwareBudgets(&gf.Settings, vramUnset, ramUnset)
	// UI-owned fleet-wide default variants replace the file's list wholesale.
	sideDV, err := LoadSidecarDefaultVariants(path)
	if err != nil {
		return GenerateFile{}, err
	}
	if sideDV != nil {
		gf.Settings.DefaultVariants = sideDV
	}
	// UI-owned display-name renames overlay the file's (per-key, like categoryRoots).
	sideDN, err := LoadSidecarDisplayNames(path)
	if err != nil {
		return GenerateFile{}, err
	}
	if len(sideDN) > 0 {
		if gf.Settings.DisplayNames == nil {
			gf.Settings.DisplayNames = map[string]string{}
		}
		for k, v := range sideDN {
			gf.Settings.DisplayNames[k] = v
		}
	}
	// UI-owned per-category scan folders overlay the file's (the folder picker
	// writes these). Stored at the sidecar top level so a VRAM reset can't wipe them.
	sideRoots, err := LoadSidecarCategoryRoots(path)
	if err != nil {
		return GenerateFile{}, err
	}
	if len(sideRoots) > 0 {
		if gf.Settings.CategoryRoots == nil {
			gf.Settings.CategoryRoots = map[string]string{}
		}
		for k, v := range sideRoots {
			gf.Settings.CategoryRoots[k] = v
		}
	}
	if modelsDirOverride != "" {
		gf.Settings.ModelsRoot = modelsDirOverride
	}

	// Merge the UI-owned sidecar overrides ahead of the hand-authored ones so
	// per-model edits from the web editor win (override resolution is
	// first-match). The sidecar is fully owned by the UI and may be absent.
	sideRows, err := LoadSidecarOverrides(path)
	if err != nil {
		return GenerateFile{}, err
	}
	if len(sideRows) > 0 {
		gf.Overrides = append(sidecarExactFirst(sideRows), gf.Overrides...)
	}
	// UI-owned API keys replace the file's list wholesale (the UI sends the full
	// list). nil => inherit the generate file's apiKeys.
	sideKeys, err := LoadSidecarAPIKeys(path)
	if err != nil {
		return GenerateFile{}, err
	}
	if sideKeys != nil {
		gf.Settings.APIKeys = sideKeys
	}
	// UI-owned slot-KV block overlays the generate file's settings.slotCache.
	sideSlot, err := LoadSidecarSlotCache(path)
	if err != nil {
		return GenerateFile{}, err
	}
	if sideSlot != nil {
		gf.Settings.SlotCache = *sideSlot
	}
	// A blank modelsRoot is not an error: the server boots with an empty catalog
	// so a setup UI can point it at a models folder later. Discovery/hashing
	// short-circuit on empty (no cwd scan).
	for i, o := range gf.Overrides {
		if strings.TrimSpace(o.Match) == "" {
			return GenerateFile{}, fmt.Errorf("overrides[%d]: match is required", i)
		}
	}
	return gf, nil
}

// LoadBaseSettings returns the generate file's settings with defaults applied
// but WITHOUT the UI sidecar patch - i.e. the values a dashboard "reset to
// default" reverts to.
func LoadBaseSettings(path, modelsDirOverride string) (Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, err
	}
	var gf GenerateFile
	if err := yaml.Unmarshal(data, &gf); err != nil {
		return Settings{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	gf.Settings.applyDefaults()
	if modelsDirOverride != "" {
		gf.Settings.ModelsRoot = modelsDirOverride
	}
	return gf.Settings, nil
}

// sidecarExactFirst stably partitions sidecar overrides so exact-path rows (the
// ones the UI writes) precede glob rows. Override resolution is first-match, so
// without this a hand-authored glob (e.g. "*35B-MTP-GGUF*") earlier in the file
// shadows the UI's exact-path row for the same gguf and silently drops the
// variants the editor still shows (it reads the exact-path row). Returns a fresh
// slice; the input is not mutated.
func sidecarExactFirst(rows []Override) []Override {
	exact := make([]Override, 0, len(rows))
	var globs []Override
	for _, r := range rows {
		if strings.ContainsAny(r.Match, "*?") {
			globs = append(globs, r)
		} else {
			exact = append(exact, r)
		}
	}
	return append(exact, globs...)
}

// ResolveFileOverride returns the hand-authored generate FILE override (the UI
// sidecar is EXCLUDED) that the generator resolves for ggufPath, matched by path
// glob + detected quant. The config editor uses this as the base for a UI save so
// file-only fields the editor doesn't model (ctxVariants, quant, file-defined
// variants) are carried into the sidecar row instead of being silently lost when
// the sidecar shadows the file row. ok=false (zero Override) when nothing matches.
func ResolveFileOverride(generatePath, ggufPath string) (Override, bool, error) {
	data, err := os.ReadFile(generatePath)
	if err != nil {
		return Override{}, false, err
	}
	var gf GenerateFile
	if err := yaml.Unmarshal(data, &gf); err != nil {
		return Override{}, false, fmt.Errorf("parsing %s: %w", generatePath, err)
	}
	row := GgufRow{FullPath: ggufPath, Quant: quantFromName(filepath.Base(ggufPath))}
	if o := ResolveOverride(row, gf.Overrides); o != nil {
		return *o, true, nil
	}
	return Override{}, false, nil
}

// ResolveOverride returns the first override whose Match globs the gguf path and
// whose optional Quant equals the row's quant. nil when none match.
func ResolveOverride(row GgufRow, overrides []Override) *Override {
	for i := range overrides {
		o := &overrides[i]
		if !globLike(o.Match, row.FullPath) {
			continue
		}
		if o.Quant != "" && !strings.EqualFold(o.Quant, row.Quant) {
			continue
		}
		return o
	}
	return nil
}

var globCache = map[string]*regexp.Regexp{}

// globLike emulates PowerShell's -like: case-insensitive, '*' matches any run of
// characters (including path separators), '?' matches one. Both pattern and
// subject are normalized to forward slashes so a pattern written either way
// matches a Windows path.
func globLike(pattern, s string) bool {
	re, ok := globCache[pattern]
	if !ok {
		var b strings.Builder
		b.WriteString("(?i)^")
		for _, r := range strings.ReplaceAll(pattern, "\\", "/") {
			switch r {
			case '*':
				b.WriteString(".*")
			case '?':
				b.WriteString(".")
			default:
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		b.WriteString("$")
		re = regexp.MustCompile(b.String())
		globCache[pattern] = re
	}
	return re.MatchString(strings.ReplaceAll(s, "\\", "/"))
}
