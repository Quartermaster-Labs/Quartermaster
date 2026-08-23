// Package autogen discovers local GGUF models, reads their headers, computes a
// llama-server load plan (-ngl / --n-cpu-moe / context) from a VRAM budget, and
// emits a quartermaster config. It is a Go port of the domina-llm-eval PowerShell
// planner (Read-GgufMetadata / Get-LlamaLoadPlan / Pick-LocalGguf) plus the
// Generate-Config orchestration. Fork-specific; kept separable for upstreaming.
package autogen

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
)

// GGUF metadata value type tags (spec v2/v3).
const (
	ggufU8 = iota
	ggufI8
	ggufU16
	ggufI16
	ggufU32
	ggufI32
	ggufF32
	ggufBool
	ggufString
	ggufArray
	ggufU64
	ggufI64
	ggufF64
)

// isIntType reports whether a value type tag is an integer scalar (the set the
// PowerShell reader accepted for count/dim keys).
func isIntType(t uint32) bool {
	switch t {
	case ggufU8, ggufI8, ggufU16, ggufI16, ggufU32, ggufI32, ggufU64, ggufI64:
		return true
	}
	return false
}

// Metadata is the parsed subset of a GGUF header needed for load planning. A
// zero value for an optional numeric field means "absent in the header"; every
// downstream consumer guards on > 0, matching the PowerShell $null checks.
type Metadata struct {
	Path       string
	FileSizeGB float64

	Architecture string
	BlockCount   int64

	// GeneralType is the gguf "general.type" KV ("diffusion" for an image model,
	// absent/"model" for a normal LLM). Some diffusion GGUFs (e.g. HiDream-O1,
	// whose transformer is Qwen-based) report a non-image general.architecture
	// ("qwen") but mark themselves here — the authoritative diffusion signal when
	// the arch tag doesn't self-identify. See effectiveImageArch.
	GeneralType string

	// DiffusionKind is a tensor-name-sniffed diffusion arch ("sdxl"/"sd1") for a
	// UNet gguf that carries no general.architecture — stable-diffusion.cpp's
	// `convert` strips the metadata KVs, so a converted SDXL UNet reports
	// Architecture="" but still has input_blocks/label_emb tensors. Empty when the
	// tensor section shows no UNet markers (i.e. a normal LLM/embedder).
	DiffusionKind string

	// HasBakedEncoders is true when a diffusion gguf carries its text encoder(s)
	// in-file (tensor names under conditioner.embedders/cond_stage_model) — i.e.
	// it's a full checkpoint, not a bare UNet. SD1/SD2/SDXL can only be
	// version-detected (and served) as full checkpoints via -m; a UNet-only
	// export of those has no working sd-server load path.
	HasBakedEncoders bool
	ExpertCount      int64
	ExpertUsed       int64

	ContextLength   int64
	EmbeddingLength int64
	HeadCount       int64
	HeadCountKv     int64   // representative per-attn-layer KV heads
	HeadCountKvSum  int64   // KV heads summed over all layers (hybrid-aware)
	HeadCountKvArr  []int64 // per-layer KV heads (nil = uniform); 0 = no-KV layer
	AttnLayerCount  int64   // layers with KV (0 = uniform/all layers)
	KeyLength       int64
	ValueLength     int64

	RopeScalingType   string // "none"|"linear"|"yarn"|"" (absent)
	RopeScalingFactor float64
	RopeOrigCtxLen    int64
	RopeFreqBase      float64

	SlidingWindow     int64
	SlidingWinPattern int64
	KeyLengthSwa      int64
	ValueLengthSwa    int64

	FullAttnInterval int64 // hybrid SSM: full-attn layer every Nth; 0 = not hybrid
	SsmInnerSize     int64
	SsmConvKernel    int64
	SsmStateSize     int64

	// VocabSize is the token vocabulary size, derived from the token_embd.weight
	// (or output.weight) tensor shape / embedding_length. 0 when unsizable. Used
	// to size the logits/output compute buffer in the VRAM estimate.
	VocabSize int64

	// PoolingType is the gguf "<arch>.pooling_type" (0/absent=none, 1=mean,
	// 2=cls, 3=last). A value > 0 marks an embedding model (it pools token
	// states into one sentence vector); generative models leave it unset.
	PoolingType int64

	IsMoE bool

	// IsMTP is true when the model carries multi-token-prediction / nextn
	// layers (gguf key "<arch>.num_nextn_predict_layers" or "<arch>.nextn_predict_layers" > 0).
	// Converters disagree on the spelling; accept both. Only such models
	// can use --spec-type draft-mtp.
	IsMTP bool

	// ExpertWeightShare is the fraction of on-disk weight bytes in expert
	// tensors (*_exps.weight), derived from the tensor section. 0 when not MoE
	// or the tensor section could not be sized. Replaces the per-arch heuristic.
	ExpertWeightShare float64

	// Vision (CLIP mmproj) hparams — nonzero only in an mmproj projector gguf
	// (general.architecture = "clip"); all 0 in a normal LLM gguf. Drive
	// clipComputeBufferGB so a "-vision" twin's compute-buffer reserve scales
	// per projector instead of a flat pad.
	VisionImageSize int64
	VisionPatchSize int64
	VisionEmbd      int64
	VisionFFN       int64
	VisionBlocks    int64
	VisionHeads     int64
	VisionMerge     int64

	// ChatTemplatePreservesThinking is true when the baked-in
	// "tokenizer.chat_template" keeps prior-turn <think> blocks in rendered
	// history *by default* (Qwen 3.8+ wording: "preserve_thinking is undefined
	// or preserve_thinking is true"). False for the 3.5/3.6 wording
	// ("preserve_thinking is defined and ..."), which strips them and so
	// re-renders history differently every turn, and false for any template
	// with no preserve_thinking logic at all. See needsQwenFixedChatTemplate.
	ChatTemplatePreservesThinking bool

	// ChatTemplateEffortLevels are the reasoning-effort values the baked-in chat
	// template accepts, read out of its own validation guard (Qwen 3.8:
	// "xhigh", "medium", "low", injected as a system-prompt instruction). Nil
	// when the template has no reasoning_effort support, or when it has some but
	// declares no value set we can read — in both cases nothing may be
	// normalized against it, because the template raises on an unexpected value
	// and that surfaces as a 500. Only meaningful when the model actually runs
	// its own template rather than a --chat-template-file override.
	ChatTemplateEffortLevels []string
}

// effortLevelsRe pulls the value tuple out of a template's own guard, e.g.
//
//	{%- if resolved_reasoning_effort not in ('xhigh', 'medium', 'low') %}
//	    {{- raise_exception('Unexpected reasoning effort ...') }}
//
// Matched against whitespace-collapsed source, so the guard may be wrapped.
var effortLevelsRe = regexp.MustCompile(`reasoning_effort\s+not\s+in\s*\(([^)]*)\)`)

// effortValueRe extracts one quoted value from that tuple.
var effortValueRe = regexp.MustCompile(`['"]([A-Za-z0-9_-]+)['"]`)

// effortAssignRe is the fallback for TOLERANT templates — ones that read
// reasoning_effort but never validate it, so they declare no value tuple to
// read. They instead normalize the request onto their own canonical rungs:
//
//	{%- elif _effort_raw == 'high' or _effort_raw == 'xhigh' %}
//	    {%- set _initial_effort = 'xhigh' %}
//
// so the ladder is the set of literals ASSIGNED to an effort-named variable,
// not the set compared against it (which is padded with OpenAI-ladder aliases
// the template folds away). Only consulted when the guard regex finds nothing
// and the template validates nothing — with no raise in play a level we get
// wrong degrades to the template's own default instead of a 500.
var effortAssignRe = regexp.MustCompile(`set\s+[A-Za-z0-9_]*[Ee]ffort[A-Za-z0-9_]*\s*=\s*['"]([A-Za-z0-9_-]+)['"]`)

// effortRaiseRe asks the narrow question "does this template raise ABOUT
// effort", not "does it raise at all": every one of these templates raises on
// malformed content ("Unexpected item type in content"), so a blanket
// raise_exception check would call every real template strict and suppress the
// fallback. A raise that belongs to an effort guard is the body of the test
// that names effort:
//
//	{%- if resolved_reasoning_effort not in (...) %} {{- raise_exception(...) }}
//
// so it is reachable from the word "effort" without crossing a `{%` — the
// start of the next statement tag, which is where an unrelated raise lives.
var effortRaiseRe = regexp.MustCompile(`(?is)effort(?:[^{]|\{[^%])*raise_exception`)

// scanChatTemplate derives the chat-template feature flags stored on Metadata
// from the raw jinja source. Matching is done on whitespace-collapsed text so
// re-indentation by a converter doesn't defeat it; both markers are literal
// jinja conditions copied from the upstream Qwen templates, which quant
// repackagers reproduce verbatim.
func scanChatTemplate(tmpl string) (preservesThinking bool, effortLevels []string) {
	if tmpl == "" {
		return false, nil
	}
	flat := strings.Join(strings.Fields(tmpl), " ")
	preservesThinking = strings.Contains(flat, "preserve_thinking is undefined or preserve_thinking is true")
	if m := effortLevelsRe.FindStringSubmatch(flat); m != nil {
		for _, v := range effortValueRe.FindAllStringSubmatch(m[1], -1) {
			effortLevels = append(effortLevels, strings.ToLower(v[1]))
		}
		return preservesThinking, effortLevels
	}
	if strings.Contains(flat, "reasoning_effort") && !effortRaiseRe.MatchString(flat) {
		seen := map[string]bool{}
		for _, v := range effortAssignRe.FindAllStringSubmatch(flat, -1) {
			lvl := strings.ToLower(v[1])
			// "none"/"off" is the enable_thinking switch, not a rung; the UI
			// and the proxy both carry it separately.
			if lvl == "none" || lvl == "off" || seen[lvl] {
				continue
			}
			seen[lvl] = true
			effortLevels = append(effortLevels, lvl)
		}
	}
	return preservesThinking, effortLevels
}

// ggmlTypeSize maps a ggml tensor type to (block size in elements, bytes per
// block). bytes(tensor) = n_elements / blockElems * blockBytes.
//
// The numbers are ggml's own: the enum in ggml/include/ggml.h fixes the type
// ids, and each block struct in ggml/src/ggml-common.h fixes its pair (QK_* is
// the block size, sizeof(block_*) the byte count). An id we don't list only
// costs the BYTE accounting - the tensor walk keeps going and returns
// unknownType, which suppresses the MoE expert share rather than failing the
// file (see readExpertShare). Removed types (4/5, 31-33, 36-38) are absent on
// purpose: no gguf still carries them.
var ggmlTypeSize = map[uint32][2]int64{
	0:  {1, 4},     // F32
	1:  {1, 2},     // F16
	2:  {32, 18},   // Q4_0
	3:  {32, 20},   // Q4_1
	6:  {32, 22},   // Q5_0
	7:  {32, 24},   // Q5_1
	8:  {32, 34},   // Q8_0
	9:  {32, 36},   // Q8_1
	10: {256, 84},  // Q2_K
	11: {256, 110}, // Q3_K
	12: {256, 144}, // Q4_K
	13: {256, 176}, // Q5_K
	14: {256, 210}, // Q6_K
	15: {256, 292}, // Q8_K
	16: {256, 66},  // IQ2_XXS
	17: {256, 74},  // IQ2_XS
	18: {256, 98},  // IQ3_XXS
	19: {256, 50},  // IQ1_S
	20: {32, 18},   // IQ4_NL
	21: {256, 110}, // IQ3_S
	22: {256, 82},  // IQ2_S
	23: {256, 136}, // IQ4_XS
	24: {1, 1},     // I8
	25: {1, 2},     // I16
	26: {1, 4},     // I32
	27: {1, 8},     // I64
	28: {1, 8},     // F64
	29: {256, 56},  // IQ1_M
	30: {1, 2},     // BF16
	34: {256, 54},  // TQ1_0
	35: {256, 66},  // TQ2_0
	39: {32, 17},   // MXFP4 (1-byte E8M0 scale + 32 4-bit elements)
	40: {64, 36},   // NVFP4 (4x 1-byte E4M3 sub-scales + 64 4-bit elements)
	41: {128, 18},  // Q1_0  (1.125 bpw: fp16 delta + 128 1-bit elements)
}

// ggufReader reads little-endian GGUF primitives from a seekable source. It is
// an io.ReadSeeker rather than an *os.File so the same parser can run over a
// header pulled from a hub with a Range request (see ReadGgufMetadataFrom) —
// the metadata + tensor-info sections are the first few MB of the file, which
// is the whole point of being able to size a model before downloading it.
type ggufReader struct {
	f   io.ReadSeeker
	br  *bufio.Reader
	pos int64
}

func (r *ggufReader) read(n int64) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r.br, buf); err != nil {
		return nil, err
	}
	r.pos += n
	return buf, nil
}

// discard advances past n bytes without allocating, keeping the read buffer warm.
// Used to skip string/array payloads (e.g. the tokenizer vocab) we don't decode.
func (r *ggufReader) discard(n int64) error {
	d, err := io.CopyN(io.Discard, r.br, n)
	r.pos += d
	return err
}

func (r *ggufReader) u32() (uint32, error) {
	b, err := r.read(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (r *ggufReader) u64() (uint64, error) {
	b, err := r.read(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

func (r *ggufReader) seek(delta int64) error {
	// br has buffered ahead, so the file offset is past r.pos. Seek to the absolute
	// logical target and reset the buffer rather than seeking the file relatively.
	np, err := r.f.Seek(r.pos+delta, io.SeekStart)
	if err != nil {
		return err
	}
	r.pos = np
	r.br.Reset(r.f)
	return nil
}

// str reads a GGUF string (uint64 length + UTF-8 bytes).
func (r *ggufReader) str() (string, error) {
	n, err := r.u64()
	if err != nil {
		return "", err
	}
	if n > 1<<20 {
		return "", fmt.Errorf("gguf string length absurd (%d) at offset %d", n, r.pos-8)
	}
	b, err := r.read(int64(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// skipStr advances past a GGUF string (uint64 length + bytes) without decoding it.
func (r *ggufReader) skipStr() error {
	n, err := r.u64()
	if err != nil {
		return err
	}
	return r.discard(int64(n))
}

// scalarWidth returns the byte width of a fixed-width scalar type, or 0 for
// variable-width (string) / unknown types.
func scalarWidth(t uint32) int64 {
	switch t {
	case ggufU8, ggufI8, ggufBool:
		return 1
	case ggufU16, ggufI16:
		return 2
	case ggufU32, ggufI32, ggufF32:
		return 4
	case ggufU64, ggufI64, ggufF64:
		return 8
	}
	return 0
}

// readScalar reads a value of type t as int64 (integers/bool) or float64
// (f32/f64) or string. Returns an error for array/unknown types.
func (r *ggufReader) readScalar(t uint32) (i int64, f float64, s string, err error) {
	switch t {
	case ggufU8:
		b, e := r.read(1)
		return int64(b[0]), 0, "", e
	case ggufI8:
		b, e := r.read(1)
		return int64(int8(b[0])), 0, "", e
	case ggufU16:
		b, e := r.read(2)
		if e != nil {
			return 0, 0, "", e
		}
		return int64(binary.LittleEndian.Uint16(b)), 0, "", nil
	case ggufI16:
		b, e := r.read(2)
		if e != nil {
			return 0, 0, "", e
		}
		return int64(int16(binary.LittleEndian.Uint16(b))), 0, "", nil
	case ggufU32:
		v, e := r.u32()
		return int64(v), 0, "", e
	case ggufI32:
		v, e := r.u32()
		return int64(int32(v)), 0, "", e
	case ggufF32:
		b, e := r.read(4)
		if e != nil {
			return 0, 0, "", e
		}
		return 0, float64(math.Float32frombits(binary.LittleEndian.Uint32(b))), "", nil
	case ggufBool:
		b, e := r.read(1)
		if e != nil {
			return 0, 0, "", e
		}
		if b[0] != 0 {
			return 1, 0, "", nil
		}
		return 0, 0, "", nil
	case ggufString:
		s, e := r.str()
		return 0, 0, s, e
	case ggufU64:
		v, e := r.u64()
		return int64(v), 0, "", e
	case ggufI64:
		v, e := r.u64()
		return int64(v), 0, "", e
	case ggufF64:
		b, e := r.read(8)
		if e != nil {
			return 0, 0, "", e
		}
		return 0, math.Float64frombits(binary.LittleEndian.Uint64(b)), "", nil
	}
	return 0, 0, "", fmt.Errorf("scalar requested but type=%d (array or unknown)", t)
}

// skipValue advances past a value of type t without decoding it. Arrays of
// fixed-width elements seek past the whole block in one move.
func (r *ggufReader) skipValue(t uint32) error {
	if w := scalarWidth(t); w > 0 {
		return r.discard(w)
	}
	switch t {
	case ggufString:
		return r.skipStr()
	case ggufArray:
		elemType, err := r.u32()
		if err != nil {
			return err
		}
		count, err := r.u64()
		if err != nil {
			return err
		}
		if w := scalarWidth(elemType); w > 0 {
			return r.seek(int64(count) * w)
		}
		if elemType == ggufString {
			// Skip each string's bytes via the buffer (no alloc, no per-string seek);
			// large vocab arrays would otherwise cost ~2 syscalls per entry.
			for i := uint64(0); i < count; i++ {
				if err := r.skipStr(); err != nil {
					return err
				}
			}
			return nil
		}
		for i := uint64(0); i < count; i++ {
			if err := r.skipValue(elemType); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("unknown gguf value type=%d at offset %d", t, r.pos-4)
}

// readIntArray reads a type-9 array of numeric elements into an int64 slice.
func (r *ggufReader) readIntArray() ([]int64, error) {
	elemType, err := r.u32()
	if err != nil {
		return nil, err
	}
	count, err := r.u64()
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, count)
	for i := uint64(0); i < count; i++ {
		v, _, _, err := r.readScalar(elemType)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// ReadGgufMetadata parses the metadata KV section of a GGUF file. Tensor
// descriptors are not read, so the work is bounded to the header.
func ReadGgufMetadata(path string) (Metadata, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("gguf not found: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()
	return ReadGgufMetadataFrom(f, path, fi.Size())
}

// ReadGgufMetadataFrom is ReadGgufMetadata over an already-open source. Callers
// that do not have the file on disk (the model browser, which Range-fetches the
// first few MB from the hub) pass a bytes.Reader over the header and the file's
// FULL size — sizeBytes is the weights figure every sizing path charges, so
// handing it the length of the prefix that was fetched would report a model
// that fits in any budget. A truncated source surfaces as io.ErrUnexpectedEOF,
// which is the caller's signal to re-fetch a longer prefix.
func ReadGgufMetadataFrom(rs io.ReadSeeker, path string, sizeBytes int64) (Metadata, error) {
	r := &ggufReader{f: rs, br: bufio.NewReaderSize(rs, 1<<20)}
	magic, err := r.read(4)
	if err != nil {
		return Metadata{}, err
	}
	if string(magic) != "GGUF" {
		return Metadata{}, fmt.Errorf("not a gguf file (magic=%q): %s", string(magic), path)
	}
	// version (v2/v3 share this layout); newer minor revisions stay compatible.
	if _, err := r.u32(); err != nil {
		return Metadata{}, err
	}
	tensorCount, err := r.u64()
	if err != nil {
		return Metadata{}, err
	}
	kvCount, err := r.u64()
	if err != nil {
		return Metadata{}, err
	}

	// Optional fields tracked via pointers so absence is distinguishable from a
	// real zero, exactly like the PowerShell $null checks.
	var arch string
	var genType string
	var chatTmpl string
	var blockCount, expertCount, expertUsed *int64
	var contextLength, embeddingLength, headCount, headCountKv *int64
	var headCountKvArr []int64
	var keyLength, valueLength *int64
	var ropeScalingType *string
	var ropeScalingFactor *float64
	var ropeOrigCtxLen *int64
	var ropeFreqBase *float64
	var slidingWindow, slidingWinPattern, keyLengthSwa, valueLengthSwa *int64
	var fullAttnInterval, ssmInnerSize, ssmConvKernel, ssmStateSize *int64
	var nextnLayers, poolingType *int64
	var visImageSize, visPatchSize, visEmbd, visFFN, visBlocks, visHeads, visMerge *int64

	pi := func(v int64) *int64 { return &v }
	pf := func(v float64) *float64 { return &v }
	ps := func(v string) *string { return &v }

	for i := uint64(0); i < kvCount; i++ {
		key, err := r.str()
		if err != nil {
			return Metadata{}, err
		}
		t, err := r.u32()
		if err != nil {
			return Metadata{}, err
		}
		matched := false

		if key == "general.architecture" && t == ggufString {
			_, _, s, err := r.readScalar(t)
			if err != nil {
				return Metadata{}, err
			}
			arch = s
			matched = true
		} else if key == "general.type" && t == ggufString {
			_, _, s, err := r.readScalar(t)
			if err != nil {
				return Metadata{}, err
			}
			genType = s
			matched = true
		} else if key == "tokenizer.chat_template" && t == ggufString {
			// Decoded (not skipped) so the baked template's own feature flags can
			// gate the arch-derived --chat-template-file override. Templates run
			// ~10-100 KB — well under ggufReader.str's 1 MB sanity cap — and only
			// the derived booleans are kept, not the source.
			_, _, s, err := r.readScalar(t)
			if err != nil {
				return Metadata{}, err
			}
			chatTmpl = s
			matched = true
		} else if arch != "" {
			pfx := arch + "."
			readInt := func() (int64, error) { v, _, _, err := r.readScalar(t); return v, err }
			readFloat := func() (float64, error) { _, v, _, err := r.readScalar(t); return v, err }
			readStr := func() (string, error) { _, _, v, err := r.readScalar(t); return v, err }

			switch {
			case key == pfx+"block_count" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				blockCount = pi(v)
				matched = true
			case key == pfx+"expert_count" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				expertCount = pi(v)
				matched = true
			case key == pfx+"expert_used_count" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				expertUsed = pi(v)
				matched = true
			case key == pfx+"context_length" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				contextLength = pi(v)
				matched = true
			case key == pfx+"embedding_length" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				embeddingLength = pi(v)
				matched = true
			case key == pfx+"attention.head_count" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				headCount = pi(v)
				matched = true
			case key == pfx+"attention.head_count_kv" && t == ggufArray:
				arr, err := r.readIntArray()
				if err != nil {
					return Metadata{}, err
				}
				headCountKvArr = arr
				matched = true
			case key == pfx+"attention.head_count_kv" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				headCountKv = pi(v)
				matched = true
			case key == pfx+"attention.key_length" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				keyLength = pi(v)
				matched = true
			case key == pfx+"attention.value_length" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				valueLength = pi(v)
				matched = true
			case key == pfx+"rope.scaling.type" && t == ggufString:
				v, err := readStr()
				if err != nil {
					return Metadata{}, err
				}
				ropeScalingType = ps(v)
				matched = true
			case key == pfx+"rope.scaling.factor" && (t == ggufF32 || t == ggufF64):
				v, err := readFloat()
				if err != nil {
					return Metadata{}, err
				}
				ropeScalingFactor = pf(v)
				matched = true
			case key == pfx+"rope.scaling.original_context_length" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				ropeOrigCtxLen = pi(v)
				matched = true
			case key == pfx+"rope.freq_base" && (t == ggufF32 || t == ggufF64):
				v, err := readFloat()
				if err != nil {
					return Metadata{}, err
				}
				ropeFreqBase = pf(v)
				matched = true
			case key == pfx+"attention.sliding_window" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				slidingWindow = pi(v)
				matched = true
			case key == pfx+"attention.sliding_window_pattern" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				slidingWinPattern = pi(v)
				matched = true
			case key == pfx+"attention.key_length_swa" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				keyLengthSwa = pi(v)
				matched = true
			case key == pfx+"attention.value_length_swa" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				valueLengthSwa = pi(v)
				matched = true
			case key == pfx+"full_attention_interval" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				fullAttnInterval = pi(v)
				matched = true
			case key == pfx+"ssm.inner_size" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				ssmInnerSize = pi(v)
				matched = true
			case key == pfx+"ssm.conv_kernel" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				ssmConvKernel = pi(v)
				matched = true
			case key == pfx+"ssm.state_size" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				ssmStateSize = pi(v)
				matched = true
			case (key == pfx+"num_nextn_predict_layers" || key == pfx+"nextn_predict_layers") && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				nextnLayers = pi(v)
				matched = true
			case key == pfx+"pooling_type" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				poolingType = pi(v)
				matched = true
			// Vision (CLIP mmproj) hparams — present only when arch == "clip"
			// (the projector gguf). Drive the per-projector CLIP compute-buffer
			// model in clipComputeBufferGB.
			case key == pfx+"vision.image_size" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				visImageSize = pi(v)
				matched = true
			case key == pfx+"vision.patch_size" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				visPatchSize = pi(v)
				matched = true
			case key == pfx+"vision.embedding_length" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				visEmbd = pi(v)
				matched = true
			case key == pfx+"vision.feed_forward_length" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				visFFN = pi(v)
				matched = true
			case key == pfx+"vision.block_count" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				visBlocks = pi(v)
				matched = true
			case key == pfx+"vision.attention.head_count" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				visHeads = pi(v)
				matched = true
			case key == pfx+"vision.spatial_merge_size" && isIntType(t):
				v, err := readInt()
				if err != nil {
					return Metadata{}, err
				}
				visMerge = pi(v)
				matched = true
			}
		}

		if !matched {
			if err := r.skipValue(t); err != nil {
				return Metadata{}, err
			}
		}
	}

	// Tensor section: sum on-disk weight bytes of expert tensors vs all tensors
	// to derive the expert-weight share exactly (replaces the per-arch heuristic).
	// The reader is now positioned at the first tensor info. Failures leave the
	// share at 0 so the caller falls back to the arch table.
	expertShare, vocabElems, diffKind, bakedEnc, _ := readExpertShare(r, tensorCount)
	var vocab int64
	if vocabElems > 0 && embeddingLength != nil && *embeddingLength > 0 {
		vocab = vocabElems / *embeddingLength
	}

	// Derive per-head dim when key/value_length absent: head_dim =
	// embedding_length / head_count. K and V share it unless stated.
	if (keyLength == nil || valueLength == nil) && embeddingLength != nil && headCount != nil && *headCount > 0 {
		derived := int64(math.Floor(float64(*embeddingLength) / float64(*headCount)))
		if keyLength == nil {
			keyLength = pi(derived)
		}
		if valueLength == nil {
			valueLength = pi(derived)
		}
	}

	// Hybrid conv+attn archs give head_count_kv as a per-layer array: conv
	// layers report 0. Sum across layers = total KV heads; non-zero = attn layers.
	var kvHeadSum *int64
	var attnLayers *int64
	if len(headCountKvArr) > 0 {
		var sum int64
		var nz int64
		for _, h := range headCountKvArr {
			sum += h
			if h > 0 {
				nz++
			}
		}
		kvHeadSum = pi(sum)
		attnLayers = pi(nz)
		if headCountKv == nil {
			for _, h := range headCountKvArr {
				if h > 0 {
					headCountKv = pi(h)
					break
				}
			}
		}
	}
	// GQA: KV heads default to attention heads when head_count_kv is absent (MHA).
	if headCountKv == nil {
		headCountKv = headCount
	}
	// Uniform model: layer-summed KV heads = blocks * per-layer KV heads.
	if kvHeadSum == nil && blockCount != nil && headCountKv != nil {
		kvHeadSum = pi(*blockCount * *headCountKv)
	}

	deref := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	derefF := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	derefS := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}

	m := Metadata{
		Path:              path,
		FileSizeGB:        round(float64(sizeBytes)/gib, 3),
		Architecture:      arch,
		GeneralType:       genType,
		BlockCount:        deref(blockCount),
		ExpertCount:       deref(expertCount),
		ExpertUsed:        deref(expertUsed),
		ContextLength:     deref(contextLength),
		EmbeddingLength:   deref(embeddingLength),
		HeadCount:         deref(headCount),
		HeadCountKv:       deref(headCountKv),
		HeadCountKvSum:    deref(kvHeadSum),
		HeadCountKvArr:    headCountKvArr,
		AttnLayerCount:    deref(attnLayers),
		KeyLength:         deref(keyLength),
		ValueLength:       deref(valueLength),
		RopeScalingType:   derefS(ropeScalingType),
		RopeScalingFactor: derefF(ropeScalingFactor),
		RopeOrigCtxLen:    deref(ropeOrigCtxLen),
		RopeFreqBase:      derefF(ropeFreqBase),
		SlidingWindow:     deref(slidingWindow),
		SlidingWinPattern: deref(slidingWinPattern),
		KeyLengthSwa:      deref(keyLengthSwa),
		ValueLengthSwa:    deref(valueLengthSwa),
		FullAttnInterval:  deref(fullAttnInterval),
		SsmInnerSize:      deref(ssmInnerSize),
		SsmConvKernel:     deref(ssmConvKernel),
		SsmStateSize:      deref(ssmStateSize),
		VocabSize:         vocab,
		DiffusionKind:     diffKind,
		HasBakedEncoders:  bakedEnc,
		PoolingType:       deref(poolingType),
		IsMoE:             expertCount != nil && *expertCount > 0,
		IsMTP:             nextnLayers != nil && *nextnLayers > 0,
		ExpertWeightShare: expertShare,
		VisionImageSize:   deref(visImageSize),
		VisionPatchSize:   deref(visPatchSize),
		VisionEmbd:        deref(visEmbd),
		VisionFFN:         deref(visFFN),
		VisionBlocks:      deref(visBlocks),
		VisionHeads:       deref(visHeads),
		VisionMerge:       deref(visMerge),
	}
	m.ChatTemplatePreservesThinking, m.ChatTemplateEffortLevels = scanChatTemplate(chatTmpl)
	return m, nil
}

// readExpertShare parses the GGUF tensor info section (the reader must be
// positioned at the first tensor info) and returns the fraction of on-disk
// weight bytes held by expert tensors (name contains "_exps"), plus the element
// count of the token_embd.weight (or output.weight) tensor (= n_embd*n_vocab,
// for vocab sizing). Share is 0 when no expert tensors are found or any tensor
// uses an unknown ggml type; vocabElems is 0 when neither tensor is present.
// diffKind is "sdxl"/"sd1" when the tensor names mark a diffusion UNet (a
// metadata-stripped converted model), else "". bakedEnc is true when the file
// also carries its text encoder(s) (conditioner.embedders/cond_stage_model),
// i.e. it's a full checkpoint rather than a bare UNet.
func readExpertShare(r *ggufReader, tensorCount uint64) (share float64, vocabElems int64, diffKind string, bakedEnc bool, err error) {
	var expertBytes, totalBytes int64
	var sawExpert, sawInputBlocks, sawLabelEmb, unknownType bool
	for i := uint64(0); i < tensorCount; i++ {
		name, err := r.str()
		if err != nil {
			return 0, 0, "", false, err
		}
		nDims, err := r.u32()
		if err != nil {
			return 0, 0, "", false, err
		}
		elems := int64(1)
		for d := uint32(0); d < nDims; d++ {
			dim, err := r.u64()
			if err != nil {
				return 0, 0, "", false, err
			}
			elems *= int64(dim)
		}
		typ, err := r.u32()
		if err != nil {
			return 0, 0, "", false, err
		}
		if _, err := r.u64(); err != nil { // offset (unused)
			return 0, 0, "", false, err
		}
		// An unknown ggml type only costs us the BYTE accounting - the walk
		// itself is fixed-width, and name/elems are already in hand. Keep going
		// rather than failing the file: bailing here used to zero VocabSize for
		// every gguf in a quant we had not tabulated yet (MXFP4), which silently
		// refused it a family sidecar and mis-sized its logits buffer.
		ts, ok := ggmlTypeSize[typ]
		if ok {
			bytes := elems / ts[0] * ts[1]
			totalBytes += bytes
			if strings.Contains(name, "_exps") {
				expertBytes += bytes
			}
		} else {
			unknownType = true
		}
		if strings.Contains(name, "_exps") {
			sawExpert = true
		}
		// token_embd.weight is the canonical vocab×embd tensor; output.weight is
		// the tied/untied fallback (some models omit token_embd in the count).
		if name == "token_embd.weight" || (vocabElems == 0 && name == "output.weight") {
			vocabElems = elems
		}
		// SD/SDXL UNet markers, for a converted diffusion gguf that lost its
		// general.architecture. input_blocks = a UNet; label_emb (the size/crop
		// conditioning) is SDXL-only, absent in SD1.
		if strings.Contains(name, "input_blocks.") {
			sawInputBlocks = true
		}
		if strings.Contains(name, "label_emb.") {
			sawLabelEmb = true
		}
		// Baked text encoder = full checkpoint. SDXL bakes both encoders under
		// conditioner.embedders.{0,1}; older SD1 checkpoints use cond_stage_model.
		if strings.Contains(name, "conditioner.embedders.") || strings.Contains(name, "cond_stage_model.") {
			bakedEnc = true
		}
	}
	if sawInputBlocks {
		if sawLabelEmb {
			diffKind = "sdxl"
		} else {
			diffKind = "sd1"
		}
	}
	// An untabulated type makes the ratio meaningless (the missing tensors are
	// exactly the ones whose share we would be reporting), so the share alone
	// degrades to "unknown". Everything else here is type-independent.
	if !sawExpert || totalBytes <= 0 || unknownType {
		return 0, vocabElems, diffKind, bakedEnc, nil
	}
	return float64(expertBytes) / float64(totalBytes), vocabElems, diffKind, bakedEnc, nil
}

const gib = 1024 * 1024 * 1024

// round mirrors PowerShell [math]::Round (banker's rounding is not required for
// these display/size values; standard half-away rounding matches in practice).
func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
