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
	// RoleAudioVae decodes an AUDIO latent, not a picture. Its own role rather
	// than a VAE family because it is never a substitute for one: a video model
	// that emits a soundtrack loads BOTH (--vae and --audio-vae).
	RoleAudioVae ComponentRole = "audio-vae"
	RoleClip     ComponentRole = "clip"
	RoleT5       ComponentRole = "t5"
	RoleLlm      ComponentRole = "llm"
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
	// VaeFamilyVideo3D is a transformer 3D video autoencoder (MiniMax-H3): the
	// encoder patchifies RGB with a conv3d but the DECODER is a transformer, so
	// it has neither decoder.conv_in nor Wan's bare conv1.
	VaeFamilyVideo3D = "video3d"
	// VaeFamilyLtx is the LTX-2.x pair of autoencoders. Both halves carry this
	// family: they ship as two files (video + audio) that are only ever loaded
	// TOGETHER, and giving them one family is what keeps an LTX model from being
	// handed MiniMax-H3's soundtrack decoder, which is a different role-mate
	// entirely. The video half is a 128-channel 3D conv VAE whose convolutions
	// are wrapped one level deeper than anyone else's (decoder.conv_in.CONV.
	// weight), which is also why neither arm above sees it.
	VaeFamilyLtx = "ltx"
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
	// Wan 2.1-derived 3D causal VAE: conv1.weight is 5-dim. Wan 2.1 and the
	// Qwen-Image 20B line have the identical shape table, so those two need a
	// path hint to pick between them (see EncoderPool.Vae).
	//
	// Width is the LATENT channel count, read off the decoder's input projection
	// ([out, latent, t, h, w]). It is 16 for Wan 2.1 and Qwen-Image 20B, and 64
	// for Qwen-Image 2.1's RGBA autoencoder, which is the first member of this
	// family whose latent is a different size. A 64-channel VAE handed to a
	// 16-channel DiT decodes to noise rather than failing, so the width is what
	// keeps the filename sort in pickByHint from deciding it.
	case len(h["conv1.weight"].Shape) == 5:
		c.Role = RoleVae
		c.Family = VaeFamilyWan3D
		c.Width = dim("decoder.conv1.weight", 1)
	// Transformer 3D video VAE (MiniMax-H3). Neither arm above sees it: the
	// decoder is a transformer stack (decoder.x_embedder), so there is no
	// decoder.conv_in and no bare conv1. What remains is the encoder's conv3d
	// stem, encoder.conv_in.weight = [out, 3, t, h, w]. Deliberately placed
	// AFTER the wan3d arm so a Wan VAE that also carries this tensor keeps its
	// own family.
	case len(h["encoder.conv_in.weight"].Shape) == 5:
		c.Role = RoleVae
		c.Family = VaeFamilyVideo3D
	// LTX-2.x video VAE. Its conv modules are wrapped (LTXVideoCausalConv3d),
	// so the tensor is decoder.conv_in.CONV.weight = [out, latent, t, h, w] and
	// neither the 2D arm nor the two 3D arms above match it. shape[1] is the
	// latent channel count (128 for LTX-2.5), kept as the width so a future
	// second latent size is a family mismatch rather than a silent bad decode.
	case len(h["decoder.conv_in.conv.weight"].Shape) == 5:
		c.Role = RoleVae
		c.Family = VaeFamilyLtx
		c.Width = dim("decoder.conv_in.conv.weight", 1)
	// LTX-2.x audio VAE. One file, two stacks: the VAE proper under an
	// audio_vae. prefix plus the vocoder that turns its mel output into samples.
	// The decoder input projection is a 2D conv over the mel spectrogram
	// ([width, latent, 3, 3]), NOT the 1D stack H3 uses, so it needs its own arm
	// and carries the LTX family so the two audio decoders never cross-wire.
	case len(h["audio_vae.decoder.conv_in.conv.weight"].Shape) == 4:
		c.Role = RoleAudioVae
		c.Family = VaeFamilyLtx
		c.Width = dim("audio_vae.decoder.conv_in.conv.weight", 1)
	// Audio VAE (MiniMax-H3): a 1D conv stack, so the decoder input projection
	// is [width, latent, 1]. Width here is the LATENT channel count, which is
	// what has to match the DiT's audio_patch_proj.
	case len(h["dec_in_proj.weight"].Shape) == 3:
		c.Role = RoleAudioVae
		c.Family = VaeFamilyVideo3D
		c.Width = dim("dec_in_proj.weight", 1)
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
				// Same exclusion as the gguf ladder below: a PE published as
				// safetensors is still not an encoder.
				if c := classifySafetensors(h, sizeGB, path); c.Role != RoleNone && !(c.Role == RoleLlm && IsPromptEnhancerFile(path)) {
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
				case IsPromptEnhancerFile(path):
					// A prompt rewriter is a chat finetune NAMED AFTER the image
					// model it serves, so it lands in that model's own width class
					// while being the one file there that was never trained as its
					// conditioner. Worse, it is usually the largest: better() breaks
					// a same-arch tie on file size, so a BF16 PE outranks the real
					// Q8_0 encoder and the model renders confident nonsense.
				case isDraftSidecar(filepath.Base(path)):
					// A drafter is a reduced head, not an encoder. Its width
					// can coincide with a real encoder's (the Gemma-4 MTP
					// sidecars are the case), so leaving it in the pool risks
					// conditioning a diffusion model on a speculation head.
				case IsEmbeddingModel(meta):
					// A pooled embedder emits one vector for the whole prompt,
					// not the per-token sequence a DiT cross-attends to.
				case isRecurrentLlm(meta):
					// sd.cpp's conditioner is a plain transformer stack, so a
					// hybrid cannot load at all. Left in the pool it still
					// wins its width class: MiMo-V2.6-Distill-Qwen-9B (a
					// Qwen3.5 hybrid) is 4096 wide, arch qwen*, has an mmproj
					// and outweighs Qwen3-VL-8B, so it took Qwen-Image 2.1
					// and sd-server died on "model metadata validation failed".
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

// isRecurrentLlm reports whether an LLM gguf carries recurrent (linear/SSM)
// layers: a GatedDeltaNet hybrid like Qwen3.5/3.6 states full_attention_interval,
// and a Mamba-style model or hybrid states its ssm.* sizes. No diffusion model
// conditions on one, and the same keys already drive the recurrent KV and
// checkpoint sizing, so this is no new identity table.
func isRecurrentLlm(meta Metadata) bool {
	return meta.FullAttnInterval > 0 || meta.SsmStateSize > 0 || meta.SsmInnerSize > 0
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
	return p.VaeOfWidth(family, 0, hints...)
}

// VaeOfWidth is Vae narrowed to a latent channel count. want 0 means "no
// opinion" and behaves exactly like Vae, which is what every family with a
// single latent size passes.
//
// A want that matches nothing falls back to the full candidate list rather than
// returning "": a family whose files predate the width being recorded (Width 0)
// would otherwise turn a working config into a missing-vae warning, and an
// unfiltered pick is no worse than what this function did before.
func (p *EncoderPool) VaeOfWidth(family string, want int64, hints ...string) string {
	if p == nil {
		return ""
	}
	var cands, sized []ComponentFile
	for _, f := range p.Files {
		if f.Role != RoleVae || f.Family != family {
			continue
		}
		cands = append(cands, f)
		if want > 0 && f.Width == want {
			sized = append(sized, f)
		}
	}
	if len(sized) > 0 {
		return pickByHint(sized, hints)
	}
	return pickByHint(cands, hints)
}

// pickByHint returns the first candidate whose path contains one of the hints
// (hints in priority order), else the first candidate, else "".
func pickByHint(cands []ComponentFile, hints []string) string {
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

// AudioVae returns the discovered audio VAE of a family, or "" when none is on
// disk. An empty family matches any, which is what a box with a single audio
// model wants; naming one is what keeps a machine holding BOTH an LTX and a
// MiniMax-H3 checkpoint from handing either the other's soundtrack decoder (the
// two are unrelated networks over unrelated latents, so a cross-wire is a load
// failure at best). hints work exactly as in Vae.
func (p *EncoderPool) AudioVae(family string, hints ...string) string {
	if p == nil {
		return ""
	}
	var cands []ComponentFile
	for _, f := range p.Files {
		if f.Role == RoleAudioVae && (family == "" || f.Family == family) {
			cands = append(cands, f)
		}
	}
	return pickByHint(cands, hints)
}

// LlmHinted returns the text-encoder LLM whose PATH matches one of the hints,
// or "" when none does. Unlike Llm it never falls back to an arbitrary
// candidate: it exists for a family whose encoder is a bespoke, republished file
// (LTX ships a Gemma-4-12B with the caption projection grafted on), where the
// width alone would also match every stock Gemma-3-12B on the disk. A miss here
// is meant to fall through to the ordinary width-matched pick, not to guess.
func (p *EncoderPool) LlmHinted(hints ...string) string {
	if p == nil {
		return ""
	}
	for _, h := range hints {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		for _, f := range p.Files {
			if f.Role != RoleLlm {
				continue
			}
			if strings.Contains(strings.ToLower(filepath.ToSlash(f.Path)), h) {
				return f.Path
			}
		}
	}
	return ""
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
// "Matches" is captionFactor, not equality: a DiT that concatenates several
// encoder hidden layers states a caption width that is a MULTIPLE of the encoder
// it wants (flux.2 klein 9B: txt_in 12288 against a 4096-wide Qwen3-8B). An
// exact match always outranks a concatenated one.
//
// prefer is the declared settings.encoders.qwenLlm (empty when none declared).
// A declared file that clears the same width and vision gates wins outright:
// width only narrows the field, it does not name the file a publisher trained
// against. Qwen3-4B-Instruct-2507 and Qwen3-VL-4B-Instruct are both 2560 wide
// and round to the same size, so the tiebreak below will hand one of them to
// either kind of DiT; a user who pinned the right one has answered the question.
// A prefer that fails the gates (wrong width, no vision tower for a vision
// model, a file the scan never saw) is ignored and the scan decides.
//
// Ties break by encoderArchRank, then file size, then path (see better).
func (p *EncoderPool) Llm(hidden int64, wantVision bool, prefer string) (path, mmproj string) {
	if p == nil {
		return "", ""
	}
	if hidden <= 0 {
		return "", ""
	}
	pin := config.PathKey(strings.TrimSpace(prefer))
	var best, pinned ComponentFile
	var bestFactor, pinnedFactor int64
	for _, f := range p.Files {
		if f.Role != RoleLlm {
			continue
		}
		factor := captionFactor(hidden, f.Width)
		if factor == 0 {
			continue
		}
		if wantVision && !f.Vision {
			continue
		}
		if pin != "" && config.PathKey(f.Path) == pin {
			if pinnedFactor == 0 || factor < pinnedFactor {
				pinned, pinnedFactor = f, factor
			}
			continue
		}
		if bestFactor == 0 || factor < bestFactor || (factor == bestFactor && better(f, best)) {
			best, bestFactor = f, factor
		}
	}
	// The pin wins inside its tier, not across tiers: an exact-width scan hit
	// beats a pin that only fits by concatenation, because k>1 is an inference
	// about the model's layout and k==1 is not.
	if pinnedFactor != 0 && (bestFactor == 0 || pinnedFactor <= bestFactor) {
		return pinned.Path, pinned.Mmproj
	}
	return best.Path, best.Mmproj
}

// maxCaptionConcat caps how many encoder hidden layers a DiT may be assumed to
// concatenate. 3 is the only value seen in the wild (flux.2); the spare rung is
// slack for the next one. Keeping the cap low is what stops the divisibility
// test below from degrading into "any sufficiently narrow encoder will do".
const maxCaptionConcat = 4

// captionFactor reports how many encoder hidden states of the given width this
// DiT's caption projection consumes, or 0 when that encoder cannot be the one.
//
// Nearly every DiT projects a SINGLE encoder hidden state, so txt_in is exactly
// the encoder width and the answer is 1. FLUX.2 is the exception: it conditions
// on several hidden LAYERS of its LLM concatenated, so klein 9B states txt_in
// 12288 against a 4096-wide Qwen3-8B. Matched on equality that finds nothing,
// and the caller's pin fallback then substitutes an encoder of the wrong width
// with no warning at all.
//
// Testing divisibility instead keeps this structural rather than a per-model
// table: a future DiT that concatenates a different number of layers resolves
// here with no new case. Exactness is still preferred by the caller.
func captionFactor(hidden, width int64) int64 {
	if hidden <= 0 || width <= 0 || hidden%width != 0 {
		return 0
	}
	if k := hidden / width; k <= maxCaptionConcat {
		return k
	}
	return 0
}

// knowsLlm reports whether the scan classified this exact path as a text-encoder
// LLM, at ANY width. That is a different question from whether Llm would PICK it
// for a given DiT: a pin the scan measured and rejected on width is a mismatch
// worth reporting, while one the scan never saw (kept outside the models root,
// say) is still the user's explicit choice and should be honoured.
func (p *EncoderPool) knowsLlm(path string) bool {
	key := config.PathKey(strings.TrimSpace(path))
	if p == nil || key == "" {
		return false
	}
	for _, f := range p.Files {
		if f.Role == RoleLlm && config.PathKey(f.Path) == key {
			return true
		}
	}
	return false
}

// llmCandidate reports whether a declared pin would actually be HONOURED by Llm
// at this width, which is a different question from whether one was declared.
// settings.encoders.qwenLlm is a single global field shared by every diffusion
// model on the box, and Llm applies it only to candidates that clear the same
// width and vision gates, so a pin aimed at an image model is silently dropped
// for a video one. A family that carries its own path hint has to test for the
// pin APPLYING, or a populated encoders block (every real install has one)
// suppresses the hint without ever using the pin.
//
// The width test is captionFactor, not equality, for the same reason Llm's is:
// a pin this mirror rejected on a width Llm would have accepted by concatenation
// would fire the family's path hint over a pin that was about to be honoured.
func (p *EncoderPool) llmCandidate(path string, hidden int64, wantVision bool) bool {
	key := config.PathKey(strings.TrimSpace(path))
	if p == nil || key == "" || hidden <= 0 {
		return false
	}
	for _, f := range p.Files {
		if f.Role != RoleLlm || captionFactor(hidden, f.Width) == 0 || config.PathKey(f.Path) != key {
			continue
		}
		return !wantVision || f.Vision
	}
	return false
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
	// Video components. Both fields describe the MiniMax-H3 pair: the 3D
	// transformer VAE has no image-model consumer, so an unhinted pick within
	// the family is safe. LTX's two VAEs are deliberately NOT filled here -
	// there is one slot per role and the two families' files are not
	// interchangeable, so LTX resolves its own pair structurally in
	// videoComponents and leaves these pins meaning what they have always meant.
	fill(&declared.VideoVae, p.Vae(VaeFamilyVideo3D))
	fill(&declared.AudioVae, p.AudioVae(VaeFamilyVideo3D))
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

// unifiedEditRe matches models where ONE checkpoint does both text-to-image and
// reference editing, so the name carries no edit token to detect and the two
// regexes above both miss. Qwen-Image 2.1 is the first of these: upstream
// documents `-r ref.png` against the same weights used for plain generation,
// and every 2.1 checkpoint is edit-capable, so this is a property of the model
// version rather than of the individual file.
//
// Still matched by name, for the reason given above: the reference enters as
// extra sequence tokens, so an edit-capable and a text-only checkpoint have the
// same tensor shapes. The version number is the reliable part of the name here,
// since both upstream releases (leejet's gguf, Comfy-Org's safetensors) spell
// it in the filename, whereas neither says "edit" anywhere.
//
// Getting this wrong is a silent downgrade, not an error: the model would
// declare `in: [text]`, the playground would fall through to img2img, and that
// route scales the step count by the denoise strength and redraws the whole
// frame instead of editing against a reference.
var unifiedEditRe = regexp.MustCompile(`(?i)(^|[-_. ])qwen[-_. ]?image[-_. ]?2\.1([-_. ]|$)`)

// wantsVisionEncoder reports whether this model should get --llm_vision. Only
// llm-conditioned families are candidates: flux.1 edit models condition through
// T5, which has no vision tower at all.
// refEditRe matches models that take their source image as a REFERENCE (extra
// sequence tokens) rather than as an img2img denoise base. Deliberately
// narrower than editModelRe: inpaint/redux models are edits too, but they want
// the masked img2img route, and routing them through reference images would
// redraw the whole frame instead of honouring the mask.
var refEditRe = regexp.MustCompile(`(?i)(^|[-_. ])(edit|rapid|kontext)([-_. ]|$)`)

// IsReferenceEditModel reports whether a model conditions on a reference image.
// It is what the emitted `in: [text, image]` capability is keyed on, so the
// client can route an edit to the reference path instead of guessing from the
// model id. Like the projector pairing this is name detection, because the
// distinction is not structural: the reference enters as extra sequence tokens,
// not as extra input channels, so a base and an edit checkpoint have identical
// tensor shapes. An explicit llmVision pin doubles as the escape hatch.
func IsReferenceEditModel(name string, ov *Override) bool {
	if ov != nil {
		// refEdit is the field that means exactly this, so it wins outright.
		switch ov.RefEdit {
		case "on":
			return true
		case "off":
			return false
		}
		// An llmVision pin is the older, narrower spelling: a user who pinned a
		// vision projector on has said this model consumes a reference image.
		switch ov.LlmVision {
		case "on":
			return true
		case "off":
			return false
		}
	}
	return refEditRe.MatchString(name) || unifiedEditRe.MatchString(name)
}

func wantsVisionEncoder(arch, name string, ov *Override) bool {
	if ov != nil {
		switch ov.LlmVision {
		case "on":
			return true
		case "off":
			return false
		}
		// refEdit on is the same statement from the other side: the user has
		// said this model consumes a reference image, and for an llm-conditioned
		// family the reference is READ by the encoder's vision tower. Pinning
		// one without the other is the trap this closes: sd.cpp refuses the job
		// outright ("Qwen Image 2.1 editing requires Qwen3-VL vision weights;
		// provide --llm_vision or a combined encoder"), so a user who turns on
		// reference editing and nothing else gets a model that cannot generate.
		//
		// Only the "on" direction implies anything. refEdit off does NOT mean no
		// vision: an inpaint model is an edit that wants the masked img2img
		// route, and it still needs the projector.
		if ov.RefEdit == "on" {
			return true
		}
	}
	return editModelRe.MatchString(name) || unifiedEditRe.MatchString(name)
}
