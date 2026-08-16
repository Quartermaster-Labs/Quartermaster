package server

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Field catalog for the quartermaster_configure / quartermaster_inspect chat
// tools. Everything here is DERIVED FROM THE DTOs by reflection, so the tool's
// editable surface is exactly the cogwheel's surface: add a knob to overrideDTO
// / variantDTO and the model can set it, see it in an inspect, and get a
// validation error when it misspells it — with no hand-maintained list to drift.
// (It drifted before: the tool advertised ~10 fields, so a model asked for a
// chat template wrote `--chat-template-file` into extraArgs instead.)

// qmFieldSpec is one editable field as advertised to the model.
type qmFieldSpec struct {
	Name string
	Type string // json-ish type label ("int", "number", "string", "bool", "bool|null", ...)
	Doc  string // one-line hint; "" when the name speaks for itself
}

// qmSpecsOf reflects a DTO struct into its json field list, in declaration order
// (the order a human reads the cogwheel in). Nested structs/slices of structs
// are labelled, not descended — variants are edited through their own target.
func qmSpecsOf(t reflect.Type) []qmFieldSpec {
	specs := make([]qmFieldSpec, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		specs = append(specs, qmFieldSpec{Name: tag, Type: qmTypeLabel(f.Type), Doc: qmFieldDocs[tag]})
	}
	return specs
}

// qmTypeLabel renders a Go type as the JSON shape the model must send. Pointers
// are the tri-state knobs (null = "inherit / auto"), so they say so.
func qmTypeLabel(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Ptr:
		return qmTypeLabel(t.Elem()) + "|null"
	case reflect.Slice:
		return qmTypeLabel(t.Elem()) + "[]"
	case reflect.Bool:
		return "bool"
	case reflect.String:
		return "string"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "int"
	case reflect.Struct:
		return "object"
	default:
		return t.Kind().String()
	}
}

// qmFieldDocs annotates the fields whose name isn't self-explanatory. Sparse on
// purpose: a missing entry just prints name+type, which is still enough for the
// model to set it correctly — unlike a curated list, nothing is *hidden* by an
// omission here. Keyed by json tag, shared by overrideDTO and variantDTO.
var qmFieldDocs = map[string]string{
	// Sizing / memory
	"ctx":            "context length in tokens (<= the model's trained max); 0 = auto-size",
	"vramTargetGB":   "VRAM budget for this model; 0 = the global target",
	"cpuOffload":     "layers pinned to CPU (--n-cpu-moe); 0 = auto placement",
	"kvK":            "KV key quant: f32, f16, bf16, q8_0, q5_1, q5_0, q4_1, q4_0 (must match kvV)",
	"kvV":            "KV value quant, same values as kvK",
	"kvInRam":        "keep the KV cache in system RAM instead of VRAM",
	"ub":             "micro-batch size (-ub); prefill throughput knob",
	"ctxVariants":    "extra context tiers to publish as separate model ids (e.g. [32768, 65536])",
	"ctxCheckpoints": "--ctx-checkpoints count; null = auto, 0 = disabled",
	// Behaviour
	"reasoningFmt":     "reasoning format; 'off' disables reasoning output",
	"reasoningBudget":  "max reasoning tokens per turn; 0 = unlimited",
	"preserveThinking": "keep prior-turn <think> blocks in the chat history",
	"spec":             "speculative decoding spec (e.g. draft-mtp, draft-dflash, ngram-mod)",
	"chatTemplateFile": "path to a .jinja chat template replacing the gguf's baked-in one (--chat-template-file). Use THIS field, never extraArgs",
	"extraArgs":        "extra backend flags appended verbatim. Only for flags with no field of their own",
	"backend":          "backend registry id to launch with; '' = the class default",
	"slotCache":        "opt into on-disk slot-KV persistence; null = default (on)",
	"unlisted":         "hide from the model catalog (still loadable by id)",
	"skip":             "do not emit this model at all",
	"flashAttn":        "'on' / 'off' / '' = auto",
	"mmap":             "'on' / 'off' / '' = the placement default",
	"threads":          "generation threads (-t); 0 = auto",
	"parallel":         "parallel slots (--parallel); 0 = auto",
	// Sampler
	"dry":              "DRY repetition sampler; null = on with defaults, false = disabled",
	"dryMultiplier":    "DRY penalty multiplier; 0 = default",
	"dryBase":          "DRY base; 0 = default",
	"dryAllowedLength": "DRY allowed repeat length; 0 = default",
	"temp":             "server-side default temperature; null = llama default (0.8). Most clients send their own, overriding this",
	"topK":             "server-side default top-k; null = the arch baseline (20 on Qwen3) or llama default (40). No OpenAI field, so clients cannot override",
	"topP":             "server-side default top-p; null = llama default (0.95)",
	"minP":             "server-side default min-p; null = the arch baseline (0 on Qwen3) or llama default (0.05). No OpenAI field, so clients cannot override",
	"presencePenalty":  "server-side default presence penalty; null = llama default (0)",
	// Advanced
	"threadsBatch":         "batch/prefill threads (-tb)",
	"prio":                 "process priority (--prio)",
	"cacheReuse":           "--cache-reuse token window",
	"swaFull":              "--swa-full (keep the full sliding-window KV)",
	"contextShift":         "'on' / 'off' / '' = default (--context-shift)",
	"ropeScaling":          "rope scaling type (linear, yarn, none)",
	"splitMode":            "multi-GPU split mode (layer, row, none)",
	"tensorSplit":          "per-GPU split ratios, e.g. '0.6,0.4'",
	"overrideTensor":       "-ot tensor placement expression",
	"slotPromptSimilarity": "slot reuse similarity threshold (0-1)",
	// Image (sd-server)
	"vaePath":        "external VAE file",
	"offloadToCpu":   "sd-server component offload spec",
	"diffusionFa":    "'on' / 'off' - diffusion flash-attention",
	"defaultSteps":   "default sampling steps for this image model",
	"defaultCfg":     "default CFG scale",
	"defaultSampler": "default sampler name",
	// Variant-only
	"name": "the variant's name (identifies it; not editable through changes)",
	// Global settings
	"targetVramGB":   "VRAM budget the sizer plans against",
	"vramOverheadGB": "headroom held back from that budget",
	"maxRamGB":       "system-RAM cap for CPU-offloaded weights",
	"ttlSec":         "idle seconds before a model is unloaded; 0 = never",
}

// qmModelFieldSpecs / qmVariantFieldSpecs / qmSettingsFieldSpecs are the three
// editable surfaces, each mirroring exactly what its PUT handler accepts.
func qmModelFieldSpecs() []qmFieldSpec   { return qmSpecsOf(reflect.TypeOf(overrideDTO{})) }
func qmVariantFieldSpecs() []qmFieldSpec { return qmSpecsOf(reflect.TypeOf(variantDTO{})) }
func qmSettingsFieldSpecs() []qmFieldSpec {
	return qmSpecsOf(reflect.TypeOf(settingsPutDTO{}))
}

// qmFieldCatalog renders every editable field for inspect target='fields'. The
// model pulls this on demand instead of it riding in the tool description on
// every request (it is long, and a stable system prompt is worth KV cache).
func qmFieldCatalog() string {
	var b strings.Builder
	b.WriteString("Editable configuration fields (quartermaster_configure `changes`).\n")
	b.WriteString("Send only the fields you want to change; everything else is preserved.\n")
	b.WriteString("An unknown or misspelled field is rejected, never silently ignored.\n\n")

	b.WriteString("target='settings' (global):\n")
	writeQmSpecs(&b, qmSettingsFieldSpecs())

	b.WriteString("\ntarget=<model id> (per-model override - the cogwheel's fields):\n")
	writeQmSpecs(&b, qmModelFieldSpecs())

	b.WriteString("\ntarget='<model id>#<variant name>' (one named variant of that model;\n")
	b.WriteString("zero/empty means 'inherit the model-wide value', and the variant must exist):\n")
	writeQmSpecs(&b, qmVariantFieldSpecs())

	b.WriteString("\ntarget='playground' has its own field list - see the tool description.\n")
	return b.String()
}

func writeQmSpecs(b *strings.Builder, specs []qmFieldSpec) {
	for _, s := range specs {
		if s.Doc != "" {
			fmt.Fprintf(b, "• %s (%s) - %s\n", s.Name, s.Type, s.Doc)
		} else {
			fmt.Fprintf(b, "• %s (%s)\n", s.Name, s.Type)
		}
	}
}

// validateQmChanges rejects unknown fields and wrong-typed values BEFORE the
// approval card is built, so the model gets a fixable error instead of a change
// that looks accepted but is dropped by the PUT handler's JSON decode.
func validateQmChanges(specs []qmFieldSpec, changes map[string]any) string {
	byName := make(map[string]qmFieldSpec, len(specs))
	for _, s := range specs {
		byName[s.Name] = s
	}
	// Deterministic error order: map iteration would shuffle it run to run.
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var problems []string
	for _, k := range keys {
		spec, ok := byName[k]
		if !ok {
			msg := "unknown field '" + k + "'"
			if near := qmNearestField(k, specs); near != "" {
				msg += " (did you mean '" + near + "'?)"
			}
			problems = append(problems, msg)
			continue
		}
		if err := qmCheckType(spec, changes[k]); err != "" {
			problems = append(problems, "field '"+k+"' "+err)
		}
	}
	if len(problems) == 0 {
		return ""
	}
	return strings.Join(problems, "; ") +
		". Call quartermaster_inspect with target='fields' for the exact field names and types."
}

// qmCheckType type-checks one decoded-JSON value against its field spec.
// Returns "" when fine, else the tail of an error sentence.
func qmCheckType(spec qmFieldSpec, v any) string {
	base := strings.TrimSuffix(spec.Type, "|null")
	nullable := base != spec.Type
	if v == nil {
		if nullable {
			return ""
		}
		return "cannot be null (type " + spec.Type + ")"
	}
	switch base {
	case "bool":
		if _, ok := v.(bool); !ok {
			return "must be true or false"
		}
	case "string":
		if _, ok := v.(string); !ok {
			return "must be a string"
		}
	case "int":
		f, ok := v.(float64)
		if !ok {
			return "must be an integer"
		}
		if f != float64(int64(f)) {
			return "must be a whole number"
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return "must be a number"
		}
	case "int[]":
		arr, ok := v.([]any)
		if !ok {
			return "must be an array of integers"
		}
		for _, e := range arr {
			f, ok := e.(float64)
			if !ok || f != float64(int64(f)) {
				return "must be an array of integers"
			}
		}
	case "object[]":
		return "cannot be set here - edit one variant at a time with target '<model id>#<variant name>'"
	}
	return ""
}

// qmNearestField finds the closest field name for a typo, so the model can
// self-correct in one retry. Case-insensitive match first, then a cheap edit
// distance (a rename like extraArgs→extra_args or a dropped letter).
func qmNearestField(key string, specs []qmFieldSpec) string {
	lower := strings.ToLower(key)
	squash := func(s string) string {
		return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(s))
	}
	target := squash(key)
	best, bestDist := "", 3 // distance 3+ is not a typo, it's a different field
	// A truncation ("chatTemplate" for chatTemplateFile) is the most common
	// near-miss and is FAR in edit distance, so prefix hits get their own ranking
	// (by length gap) and win over a merely-similar name.
	prefix, prefixGap := "", 1<<30
	for _, s := range specs {
		name := squash(s.Name)
		if strings.ToLower(s.Name) == lower || name == target {
			return s.Name
		}
		if len(target) >= 4 && (strings.HasPrefix(name, target) || strings.HasPrefix(target, name)) {
			if gap := abs(len(name) - len(target)); gap < prefixGap {
				prefix, prefixGap = s.Name, gap
			}
		}
		if d := qmEditDistance(target, name); d < bestDist {
			best, bestDist = s.Name, d
		}
	}
	if prefix != "" {
		return prefix
	}
	return best
}

// qmEditDistance is Levenshtein over two short field names (two-row DP).
func qmEditDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = minInt(minInt(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// qmRenderNonZero prints a DTO's non-zero fields as "• name: value" lines —
// reflection-driven so an inspect always shows every knob that is actually set,
// including ones added after this code was written. Nested variant structs are
// summarized by the caller.
func qmRenderNonZero(b *strings.Builder, dto any, indent string) int {
	v := reflect.ValueOf(dto)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return 0
		}
		v = v.Elem()
	}
	t := v.Type()
	shown := 0
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		fv := v.Field(i)
		// Slices of structs (variants) are rendered by the caller, which knows how
		// to summarize them; everything else prints only when it deviates from zero
		// (zero == "auto / inherit" everywhere in these DTOs).
		if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.Struct {
			continue
		}
		if fv.IsZero() {
			continue
		}
		val := fv
		if val.Kind() == reflect.Ptr {
			val = val.Elem()
		}
		fmt.Fprintf(b, "%s• %s: %v\n", indent, tag, val.Interface())
		shown++
	}
	return shown
}
