package autogen

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// Diffusion component auto-discovery.
//
// A bare --diffusion-model GGUF carries no VAE and no text encoder, so sd-server
// needs those wired as separate files. They used to come exclusively from
// settings.encoders: a hand-written path per role, per machine, which is the
// opposite of this project's "no hand tuning" goal and silently broke whenever
// the models tree moved.
//
// Every one of those files announces what it is, structurally, in its header, so
// the pool is discovered instead of declared:
//
//   - a VAE has an encoder/decoder conv stack, and the thing that makes two VAEs
//     incompatible (latent channel count) is literally decoder.conv_in's input
//     width: 4 = SD/SDXL, 16 = flux.1 "ae", 32 = flux.2. Filenames cannot do
//     this job: three unrelated files on the dev box are all "ae.safetensors",
//     and two of those turned out to be byte-identical copies.
//   - a CLIP has text_model.embeddings.token_embedding, whose width separates
//     CLIP-L (768) from CLIP-G (1280).
//   - a text-encoder LLM reports its own hidden width (gguf embedding_length, or
//     safetensors model.embed_tokens), and the DiT that needs it states the width
//     it expects in its caption-projection tensor (Metadata.CondHidden). Matching
//     those two numbers picks the right encoder with no name table: verified
//     against LongCat and Qwen-Image-Edit (3584 = Qwen2.5-VL-7B), Z-Image and
//     Krea2 (2560 = Qwen3-4B / Qwen3-VL-4B), ERNIE (3072 = Ministral-3B) and
//     flux (4096 = T5-XXL).
//
// settings.encoders still wins wherever it is set, so an existing config keeps
// working and an odd pick stays overridable.

// ComponentRole is what a discovered component file is FOR. One file has exactly
// one role; the family/width fields below say which models can use it.
type ComponentRole string

const (
	RoleNone ComponentRole = ""
	RoleVae  ComponentRole = "vae"
	RoleClip ComponentRole = "clip"
	RoleT5   ComponentRole = "t5"
	RoleLlm  ComponentRole = "llm"
	// roleProj is a vision projector: paired to an llm, never picked alone.
	roleProj ComponentRole = "mmproj"
)

// VAE families, keyed by latent channel count (decoder.conv_in input width).
// The count IS the compatibility class: a 16-channel model cannot decode a
// 4-channel latent, so a family mismatch is a broken image, not a quality knob.
const (
	VaeFamilySD    = "sd"    // 4 ch: SD1/SD2/SDXL
	VaeFamilyFlux  = "flux"  // 16 ch: flux.1 "ae", and every model that reuses it
	VaeFamilyFlux2 = "flux2" // 32 ch: flux.2 / ERNIE (AutoencoderKLFlux2)
	VaeFamilyWan3D = "wan3d" // Wan 2.1-derived 3D causal VAE (conv1 is 5-dim)
)

// ComponentFile is one classified file on disk.
type ComponentFile struct {
	Path   string
	Role   ComponentRole
	Family string // VAE family, or the gguf arch for an llm/t5
	Width  int64  // hidden width: clip 768/1280, llm/t5 embedding length
	SizeGB float64
	Mmproj string // llm only: the vision projector paired to it (see pairProjectors)
	Vision bool   // llm only: carries (or is paired to) a vision tower
}

// EncoderPool is the discovered set of component files.
type EncoderPool struct {
	Files []ComponentFile
}

// safetensorsHeaderCap bounds the JSON header read. Real headers are tens of KB
// (a few hundred for a full checkpoint); anything past this is a corrupt or
// hostile length field, not a model.
const safetensorsHeaderCap = 64 << 20

type stTensor struct {
	DType string  `json:"dtype"`
	Shape []int64 `json:"shape"`
}

// readSafetensorsHeader parses the tensor table at the head of a .safetensors
// file: an 8-byte little-endian JSON length, then that many bytes of JSON. Only
// the header is read, so the cost is independent of the file size.
func readSafetensorsHeader(path string) (map[string]stTensor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var n uint64
	if err := binary.Read(f, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if n == 0 || n > safetensorsHeaderCap {
		return nil, io.ErrUnexpectedEOF
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(buf, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]stTensor, len(raw))
	for k, v := range raw {
		if k == "__metadata__" {
			continue
		}
		var t stTensor
		if json.Unmarshal(v, &t) == nil {
			out[k] = t
		}
	}
	return out, nil
}

// classifySafetensors identifies a .safetensors component from its tensor table.
// Returns RoleNone for anything that is not a component (a DiT, a LoRA, a full
// checkpoint) so the caller can ignore it.
func classifySafetensors(h map[string]stTensor, sizeGB float64, path string) ComponentFile {
	c := ComponentFile{Path: path, SizeGB: sizeGB}
	dim := func(name string, i int) int64 {
		t, ok := h[name]
		if !ok || i >= len(t.Shape) {
			return 0
		}
		return t.Shape[i]
	}
	switch {
	// 2D AE (SD / flux lineage). decoder.conv_in.weight is [out, latent, 3, 3],
	// so shape[1] is the latent channel count = the compatibility family.
	case dim("decoder.conv_in.weight", 1) > 0:
		c.Role = RoleVae
		switch dim("decoder.conv_in.weight", 1) {
		case 4:
			c.Family = VaeFamilySD
		case 16:
			c.Family = VaeFamilyFlux
		case 32:
			c.Family = VaeFamilyFlux2
		default:
			return ComponentFile{}
		}
	// Wan 2.1-derived 3D causal VAE: conv1.weight is 5-dim. Qwen-Image's VAE has
	// the identical shape table, so this family needs a path hint to pick between
	// two files (see EncoderPool.Vae).
	case len(h["conv1.weight"].Shape) == 5:
		c.Role = RoleVae
		c.Family = VaeFamilyWan3D
	// CLIP text tower. token_embedding is [vocab, width]; 768 = CLIP-L, 1280 = G.
	case dim("text_model.embeddings.token_embedding.weight", 1) > 0:
		c.Role = RoleClip
		c.Width = dim("text_model.embeddings.token_embedding.weight", 1)
	// A plain decoder LLM used as a text encoder (ERNIE takes Ministral-3 this
	// way). Hidden width comes from the embedding table; a vision_tower prefix
	// means it can also condition on a reference image.
	case dim("model.embed_tokens.weight", 1) > 0:
		c.Role = RoleLlm
		c.Width = dim("model.embed_tokens.weight", 1)
		for k := range h {
			if strings.HasPrefix(k, "vision_tower.") || strings.HasPrefix(k, "visual.") {
				c.Vision = true
				break
			}
		}
	// T5 encoder stack.
	case dim("encoder.block.0.layer.0.SelfAttention.q.weight", 0) > 0:
		c.Role = RoleT5
		c.Width = dim("encoder.block.0.layer.0.SelfAttention.q.weight", 0)
	default:
		return ComponentFile{}
	}
	return c
}

// ScanEncoderPool walks the model roots and classifies every diffusion component
// it finds. GGUF headers come from the shared metadata cache, so a component
// GGUF is not parsed twice when discovery has already read it.
func ScanEncoderPool(roots []string) *EncoderPool {
	p := &EncoderPool{}
	projByDir := map[string]ComponentFile{}
	seen := map[string]bool{}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			key := config.PathKey(path)
			if seen[key] {
				return nil
			}
			fi, e := d.Info()
			if e != nil {
				return nil
			}
			sizeGB := round(float64(fi.Size())/gib, 2)
			switch strings.ToLower(filepath.Ext(path)) {
			case ".safetensors":
				h, e := readSafetensorsHeader(path)
				if e != nil {
					return nil
				}
				if c := classifySafetensors(h, sizeGB, path); c.Role != RoleNone {
					seen[key] = true
					p.Files = append(p.Files, c)
				}
			case ".gguf":
				meta, e := ReadGgufMetadataCached(path)
				if e != nil {
					return nil
				}
				arch := strings.ToLower(strings.TrimSpace(meta.Architecture))
				switch {
				case arch == "clip":
					// A vision projector is never a standalone encoder: it is
					// paired to the LLM gguf sitting beside it.
					projByDir[config.PathKey(filepath.Dir(path))] = ComponentFile{
						Path: path, Role: roleProj, SizeGB: sizeGB,
					}
				case encoderArch[arch]:
					seen[key] = true
					p.Files = append(p.Files, ComponentFile{
						Path: path, Role: RoleT5, Family: arch,
						Width: meta.EmbeddingLength, SizeGB: sizeGB,
					})
				case isImageArch(effectiveImageArch(meta)) || meta.EmbeddingLength == 0:
					// A diffusion model, or something with no hidden width:
					// not usable as a text encoder.
				case isDraftSidecar(filepath.Base(path)):
					// A drafter is a reduced head, not an encoder. Its width
					// can coincide with a real encoder's (the Gemma-4 MTP
					// sidecars are the case), so leaving it in the pool risks
					// conditioning a diffusion model on a speculation head.
				case IsEmbeddingModel(meta):
					// A pooled embedder emits one vector for the whole prompt,
					// not the per-token sequence a DiT cross-attends to.
				default:
					seen[key] = true
					p.Files = append(p.Files, ComponentFile{
						Path: path, Role: RoleLlm, Family: arch,
						Width: meta.EmbeddingLength, SizeGB: sizeGB,
					})
				}
			}
			return nil
		})
	}
	p.pairProjectors(projByDir)
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	return p
}

// isDraftSidecar reports whether a gguf filename is one of the speculation
// sidecars discovery already refuses to serve on its own. The pool reuses the
// same patterns so a drafter never becomes a text encoder.
func isDraftSidecar(base string) bool {
	return mtpFileRe.MatchString(base) || fastMtpFileRe.MatchString(base) || dflashFileRe.MatchString(base)
}

// pairProjectors attaches each LLM gguf to the vision projector in its own
// directory, reusing the convention discovery already relies on for vision LLMs
// (publishers ship "mmproj-*.gguf" beside the model). This is what makes
// --llm_vision free: the projector for a chosen --llm is simply its neighbour.
func (p *EncoderPool) pairProjectors(projByDir map[string]ComponentFile) {
	for i := range p.Files {
		if p.Files[i].Role != RoleLlm {
			continue
		}
		if proj, ok := projByDir[config.PathKey(filepath.Dir(p.Files[i].Path))]; ok {
			p.Files[i].Mmproj = proj.Path
			p.Files[i].Vision = true
		}
	}
}

// Vae returns the discovered VAE of a family, or "" when none is on disk. hints
// are path substrings preferred when a family holds more than one file: the
// Wan-2.1 and Qwen-Image VAEs have the same shape table (same 194 tensors, every
// dimension equal), so nothing but the path distinguishes them. With several
// candidates and no hint match the pick is the first path in sorted order, which
// is at least stable across regens.
func (p *EncoderPool) Vae(family string, hints ...string) string {
	if p == nil {
		return ""
	}
	var cands []ComponentFile
	for _, f := range p.Files {
		if f.Role == RoleVae && f.Family == family {
			cands = append(cands, f)
		}
	}
	if len(cands) == 0 {
		return ""
	}
	for _, h := range hints {
		h = strings.ToLower(h)
		if h == "" {
			continue
		}
		for _, c := range cands {
			if strings.Contains(strings.ToLower(filepath.ToSlash(c.Path)), h) {
				return c.Path
			}
		}
	}
	return cands[0].Path
}

// Clip returns the discovered CLIP tower of a given width (768 = L, 1280 = G).
func (p *EncoderPool) Clip(width int64) string {
	if p == nil {
		return ""
	}
	for _, f := range p.Files {
		if f.Role == RoleClip && f.Width == width {
			return f.Path
		}
	}
	return ""
}

// T5 returns the discovered T5/UMT5 encoder, preferring the widest: T5-XXL is
// 4096, and a stray smaller T5 would condition a flux model into mush.
func (p *EncoderPool) T5() string {
	if p == nil {
		return ""
	}
	best := ComponentFile{}
	for _, f := range p.Files {
		if f.Role == RoleT5 && f.Width >= best.Width {
			best = f
		}
	}
	return best.Path
}

// encoderArchRank ranks a candidate text encoder by how likely its arch is to be
// the one a diffusion model was trained against. Hidden width alone is NOT an
// identity: on the dev box FIVE unrelated 2560-wide models are installed (Qwen3-4B,
// Qwen3.5-4B, Qwen3-VL-4B, Granite-4.2-3B, Gemma-4-E4B), and picking the wrong one
// produces confident garbage rather than an error, so a size tiebreak alone was
// wiring Z-Image to Gemma.
//
// The ranking is shipped knowledge, not per-machine tuning: essentially every
// LLM-conditioned DiT in circulation (Qwen-Image, Z-Image, LongCat, Krea,
// Flux.2-Klein) conditions on a Qwen, and the Mistral rung covers ERNIE-Image and
// Flux.2-dev. Lumina-Image-2.0 proper is the known exception (it conditions on
// Gemma-2), which is why Gemma is ranked rather than excluded, and why
// settings.encoders / textEncoderPath still win outright.
func encoderArchRank(arch string) int {
	a := strings.ToLower(arch)
	switch {
	case strings.HasPrefix(a, "qwen"):
		return 3
	case strings.HasPrefix(a, "mistral") || strings.HasPrefix(a, "ministral"):
		return 2
	case strings.HasPrefix(a, "gemma") || strings.HasPrefix(a, "llama"):
		return 1
	}
	return 0
}

// better orders two same-width candidates: known encoder arch first, then the
// larger file (the encoder runs once per generation, usually on the CPU, so a
// higher quant is nearly free), then the path so a regen is reproducible.
func better(f, best ComponentFile) bool {
	if r, br := encoderArchRank(f.Family), encoderArchRank(best.Family); r != br {
		return r > br
	}
	if f.SizeGB != best.SizeGB {
		return f.SizeGB > best.SizeGB
	}
	return f.Path < best.Path
}

// Llm returns the text-encoder LLM whose hidden width matches what the DiT's
// caption projection expects, plus its vision projector when one is paired.
// wantVision restricts the pick to encoders that have one: an edit model without
// its vision tower does not fail, it silently conditions on nothing and emits an
// unrelated image, which is worse than not starting at all.
//
// Ties break by encoderArchRank, then file size, then path (see better).
func (p *EncoderPool) Llm(hidden int64, wantVision bool) (path, mmproj string) {
	if p == nil {
		return "", ""
	}
	if hidden <= 0 {
		return "", ""
	}
	var best ComponentFile
	for _, f := range p.Files {
		if f.Role != RoleLlm || f.Width != hidden {
			continue
		}
		if wantVision && !f.Vision {
			continue
		}
		if best.Path == "" || better(f, best) {
			best = f
		}
	}
	return best.Path, best.Mmproj
}

// Pool scans are cached per root set: a regen happens on every settings save and
// on every models-watcher tick, and re-walking the tree (plus re-reading every
// safetensors header) each time would dominate. The TTL keeps a freshly
// downloaded encoder from needing a restart to be seen.
var (
	poolCacheMu sync.Mutex
	poolCache   = map[string]*cachedPool{}
)

const poolCacheTTL = 30 * time.Second

type cachedPool struct {
	pool *EncoderPool
	at   time.Time
}

func encoderPoolFor(roots []string) *EncoderPool {
	key := strings.Join(roots, "\x00")
	poolCacheMu.Lock()
	e, ok := poolCache[key]
	poolCacheMu.Unlock()
	if ok && time.Since(e.at) < poolCacheTTL {
		return e.pool
	}
	p := ScanEncoderPool(roots)
	poolCacheMu.Lock()
	poolCache[key] = &cachedPool{pool: p, at: time.Now()}
	poolCacheMu.Unlock()
	return p
}

// fillEncoderSet returns declared with every blank field filled from what the
// scan actually found on disk. Declared paths always win: an explicit setting is
// a deliberate statement about a machine, and silently second-guessing it would
// make a working config drift.
//
// The hints exist because a family can hold more than one file (see Vae).
func fillEncoderSet(declared EncoderSet, p *EncoderPool) EncoderSet {
	if p == nil {
		return declared
	}
	fill := func(dst *string, v string) {
		if strings.TrimSpace(*dst) == "" {
			*dst = v
		}
	}
	fill(&declared.FluxVae, p.Vae(VaeFamilyFlux, "flux", "/ae."))
	fill(&declared.Flux2Vae, p.Vae(VaeFamilyFlux2))
	fill(&declared.SdxlVae, p.Vae(VaeFamilySD, "sdxl"))
	// Z-Image ships "its own" ae.safetensors that is byte-for-byte the flux.1
	// AE (verified: same size, same hash), so the flux pick is the correct
	// fallback and one copy on disk serves both.
	fill(&declared.ZimageVae, p.Vae(VaeFamilyFlux, "z-image", "zimage"))
	fill(&declared.ZimageVae, declared.FluxVae)
	fill(&declared.ClipL, p.Clip(768))
	fill(&declared.ClipG, p.Clip(1280))
	fill(&declared.T5, p.T5())
	return declared
}

// projectorBeside returns the vision projector paired to a given text-encoder
// gguf, i.e. the mmproj the pool found in that file's own directory. Empty for a
// safetensors encoder (its vision tower, if any, is inside the same file) or
// when the encoder ships without one.
func projectorBeside(llmPath string, p *EncoderPool) string {
	if p == nil || strings.TrimSpace(llmPath) == "" {
		return ""
	}
	key := config.PathKey(llmPath)
	for _, f := range p.Files {
		if f.Role == RoleLlm && config.PathKey(f.Path) == key {
			return f.Mmproj
		}
	}
	return ""
}

// editModelRe marks a diffusion model whose pipeline conditions on a REFERENCE
// IMAGE and therefore needs the text encoder's vision tower (--llm_vision).
//
// This one genuinely cannot be read off the weights. The obvious structural
// tells do not hold: LongCat-Image-Edit and plain LongCat-Image have identical
// img_in/txt_in shapes and differ by two unrelated norm tensors, because the
// reference image enters as extra SEQUENCE tokens rather than extra input
// channels. So the model name is the signal, with an explicit llmVision
// override as the escape hatch when a publisher names something unhelpfully.
var editModelRe = regexp.MustCompile(`(?i)(^|[-_. ])(edit|rapid|kontext|instruct[-_]?pix2pix|inpaint|redux)([-_. ]|$)`)

// wantsVisionEncoder reports whether this model should get --llm_vision. Only
// llm-conditioned families are candidates: flux.1 edit models condition through
// T5, which has no vision tower at all.
func wantsVisionEncoder(arch, name string, ov *Override) bool {
	if ov != nil {
		switch ov.LlmVision {
		case "on":
			return true
		case "off":
			return false
		}
	}
	return editModelRe.MatchString(name)
}
