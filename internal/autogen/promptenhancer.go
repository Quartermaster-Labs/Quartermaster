package autogen

import (
	"path/filepath"
	"strings"
)

// A prompt enhancer is architecturally invisible. Qwen's PE models are Qwen3.5-VL
// finetunes and their gguf header says so: general.architecture is "qwen3vl",
// identical to every other VL chat model on disk. So unlike the asr / tts /
// embedding classifiers in this package, which key on the arch (the gate that
// "matters", per family.go), this one has nothing but the NAME to go on.
//
// The name is unusually strong here, because a PE model is named after the IMAGE
// model it serves rather than after its own base: "Qwen-Image-2.1-PE-I2I" is a
// Qwen3.5-VL, and nothing in it says qwen3.5. That is what makes auto-pairing
// possible at all, and it is also why the marker must be narrow.
//
// Two things follow from a hit, and the second is why this file exists at all:
//
//  1. The id is offered as an enhancer, and pre-wired onto the image model whose
//     name it carries. Low risk: a rewrite never runs without a click and always
//     lands in the prompt box, so a wrong guess costs one visible bad rewrite and
//     an Undo, never a wrong picture.
//  2. The file is withheld from the text-encoder pool. This is the one that bites
//     today: a BF16 PE (17.9GB) and Qwen3VL-8B-Q8_0 (8.9GB) both rank qwen under
//     encoderArchRank, so `better` breaks the tie on FILE SIZE and hands
//     Qwen-Image 2.1 its own prompt rewriter as the text encoder it conditions
//     on. That fails the way every mis-picked encoder fails: not an error, just a
//     confidently unrelated image. Until now the only defence was a manual
//     textEncoderPath pin.

// PE direction markers. t2i composes a scene from nothing; i2i rewrites an
// instruction about a picture that already exists.
const (
	PEDirText = "t2i"
	PEDirEdit = "i2i"
)

// promptEnhancerName reports whether an id/filename names a prompt-rewrite model,
// which direction it serves, and the family key of the image model it belongs to.
//
// Deliberately narrow: a bare "pe" token is not enough, because tier 2 changes
// which file feeds a diffusion model's caption projection, and reclassifying
// somebody's "Pegasus-7B" there is a worse failure than missing an oddly named
// enhancer. Either the "pe" carries a direction beside it, or the name spells
// "prompt-enhancer" out.
//
// dir is "" when the name says enhancer but not which way, which is the honest
// answer for a single-model setup: the caller treats it as the txt2img slot,
// which is also the fallback for both directions.
func promptEnhancerName(id string) (dir, family string, ok bool) {
	s := strings.ToLower(strings.TrimSpace(id))
	if s == "" {
		return "", "", false
	}
	// Filenames arrive here too (the encoder pool holds paths, not ids).
	s = strings.TrimSuffix(filepath.Base(s), ".gguf")
	// One separator alphabet: publishers mix "-", "_" and "." freely
	// ("qwen_image_2.1_pe_t2i" is the same model as "Qwen-Image-2.1-PE-T2I").
	norm := strings.NewReplacer("_", "-", " ", "-").Replace(s)
	parts := strings.Split(norm, "-")

	// Spelled out: "prompt-enhancer", "prompt-enhance", "promptenhancer".
	spelled := strings.Contains(strings.ReplaceAll(norm, "-", ""), "promptenhanc")

	peAt := -1
	dirAt := -1
	for i, p := range parts {
		switch p {
		case "pe":
			if peAt < 0 {
				peAt = i
			}
		case PEDirText, PEDirEdit:
			if dirAt < 0 {
				dirAt = i
				dir = p
			}
		}
	}
	// A "pe" counts only next to its direction; a direction token counts on its
	// own only when the name also spells the role out. Neither alone is enough.
	adjacent := peAt >= 0 && dirAt == peAt+1
	if !adjacent && !spelled {
		return "", "", false
	}

	// Everything before the marker is the image model this enhancer serves. Cut
	// at whichever marker came first: "qwen-image-2.1-pe-i2i" -> "qwen-image-2.1".
	cut := len(parts)
	if peAt >= 0 && peAt < cut {
		cut = peAt
	}
	if dirAt >= 0 && dirAt < cut {
		cut = dirAt
	}
	if spelled {
		for i, p := range parts {
			if strings.Contains(p, "prompt") && i < cut {
				cut = i
			}
		}
	}
	if cut <= 0 {
		// The whole name is the marker, so it names no family. Still an enhancer,
		// just not one anything can be paired to automatically.
		return dir, "", true
	}
	family = enhancerFamilyKey(strings.Join(parts[:cut], "-"))
	return dir, family, true
}

// enhancerFamilyKey reduces a model id to the key both sides of the pairing are
// matched on. Separators are folded first: an id keeps whatever the publisher's
// filename used, and ModelBaseKey splits on "-" alone, so "qwen_image_2.1-q8_0"
// and "qwen-image-2.1-q8_0" would otherwise be two different families.
func enhancerFamilyKey(id string) string {
	norm := strings.NewReplacer("_", "-", " ", "-").Replace(strings.ToLower(strings.TrimSpace(id)))
	return ModelBaseKey(norm)
}

// IsPromptEnhancerFile reports whether a gguf path names a prompt-rewrite model.
// Used to withhold it from the text-encoder pool (see the file comment).
func IsPromptEnhancerFile(path string) bool {
	_, _, ok := promptEnhancerName(path)
	return ok
}

// autoEnhancers maps an image model's base key to the enhancer ids discovered
// beside it, one per direction.
type autoEnhancers map[string]*autoEnhancerPair

type autoEnhancerPair struct {
	Text string // txt2img, also the fallback when only one was found
	Edit string // img2img
}

// detectEnhancers builds the pairing table from the discovered catalog. Ids are
// row.ID, matching the name the emit loop gives a row unless two rows share an
// ID, where the loop appends a publisher tag to the SECOND one; first-wins here
// agrees with that.
//
// The exception is an i2i enhancer, which is auto-paired to the model's VISION
// TWIN instead. See enhancerServedID: on that one direction the base id is the
// wrong profile, every single time.
func detectEnhancers(rows []GgufRow, s Settings, overrides []Override) autoEnhancers {
	out := autoEnhancers{}
	for _, row := range rows {
		// An image/video/SAM row is never an enhancer, whatever it is called:
		// the rewriter is a chat model.
		if row.IsSam || row.IsTrellis {
			continue
		}
		dir, family, ok := promptEnhancerName(row.ID)
		if !ok || family == "" {
			continue
		}
		p := out[family]
		if p == nil {
			p = &autoEnhancerPair{}
			out[family] = p
		}
		switch dir {
		case PEDirEdit:
			if p.Edit == "" {
				p.Edit = enhancerServedID(row, s, overrides)
			}
		default:
			if p.Text == "" {
				p.Text = row.ID
			}
		}
	}
	return out
}

// enhancerServedID picks WHICH profile of an i2i enhancer to auto-pair.
//
// Every other profile of a vision model carries its projector in system RAM
// (--no-mmproj-offload), which costs no VRAM and changes neither the context
// window nor the layer placement, paid for with a one-off host-side encode on
// the requests that actually carry an image. That trade is right for a chat
// model, where most requests are text. It inverts completely for an i2i
// rewriter: EVERY call carries the image, by definition, so the base profile
// pays the CPU encode every time and never once banks the saving. Measured, it
// is the difference between seconds and minutes on a single reference image.
//
// So the twin is the correct pairing, and its extra VRAM is the price of the
// feature working at all. Falls back to the base id whenever the emit loop
// would not produce a twin, since pairing to an id that is never emitted hides
// the button behind a 404 instead.
func enhancerServedID(row GgufRow, s Settings, overrides []Override) string {
	ov := ResolveOverride(row, overrides)
	mmprojFile, modelPin := "", ""
	if ov != nil {
		mmprojFile = ov.MmprojFile
		modelPin = strings.ToLower(strings.TrimSpace(ov.Mmproj))
		// The reserved "vision" variant repins the TWIN's projector, and is the
		// one place a "none" drops the twin while leaving the base ids vision
		// capable. Mirrors the twin gate in generate.go.
		variants := append(append([]VariantSpec{}, ov.Variants...), s.DefaultVariants...)
		for i := range variants {
			if strings.EqualFold(strings.TrimSpace(variants[i].Name), "vision") {
				mmprojFile = inheritStr(variants[i].MmprojFile, mmprojFile)
				break
			}
		}
	}
	if strings.EqualFold(strings.TrimSpace(mmprojFile), NoneSentinel) || modelPin == "none" {
		return row.ID
	}
	if path, _ := mmprojFor(row, mmprojFile); path == "" {
		return row.ID
	}
	return row.ID + "-vision"
}

// For returns the enhancers discovered for an image model id, or nil. The lookup
// is on the model's base key, so every quant of an image model finds the same
// pair, and a finetune named after its base does too.
func (a autoEnhancers) For(imageID string) *autoEnhancerPair {
	if len(a) == 0 {
		return nil
	}
	return a[enhancerFamilyKey(imageID)]
}

// PEDisabled is the id that means "this model wants NO enhancer", as opposed to
// the empty string, which now means "nothing chosen, so use what was detected".
//
// The sentinel exists because auto-pairing gave the empty string a second
// meaning. Without it, clearing the field in the model modal would write "" and
// the next regeneration would helpfully put the enhancer straight back, and the
// user would have no way at all to say no. The UI writes this value when a field
// with a detected candidate is cleared.
const PEDisabled = "none"

// enhancerDisabled reports the sentinel. Case-insensitive: the id is typed by
// hand into a free-text field.
func enhancerDisabled(id string) bool {
	return strings.EqualFold(strings.TrimSpace(id), PEDisabled)
}

// resolveEnhancerIDs decides which enhancer ids an image model actually carries:
// what it was configured with, else what was detected beside it on disk.
//
// Auto-pairing is safe HERE in a way it would not be on the encoder path,
// because of where the result lands: a rewrite never runs without a click and
// always appears in the prompt box for the user to read, edit or undo. A wrong
// guess costs one visible bad rewrite; the same guess made about a text encoder
// costs a confidently unrelated picture with nothing to look at.
//
// A configured id always wins, including the PEDisabled sentinel, so a user who
// has said no is never overruled by a filename.
func resolveEnhancerIDs(s Settings, imageID, text, edit string) (string, string) {
	text, edit = strings.TrimSpace(text), strings.TrimSpace(edit)
	if text != "" && edit != "" {
		return text, edit
	}
	auto := s.autoEnhancers.For(imageID)
	if auto == nil {
		return text, edit
	}
	if text == "" {
		text = auto.Text
	}
	if edit == "" {
		edit = auto.Edit
	}
	return text, edit
}

// PromptEnhancerID is the exported classifier: whether a catalog model id names
// a prompt rewriter, which direction (PEDirText / PEDirEdit, or "" when the name
// does not say) and the family key of the image model it was named after.
//
// The server uses it twice: to suggest the ids it found as enhancer candidates,
// and to decide whether one with no settings row should be sent the reference
// image. An "-i2i" rewriter that cannot see the picture it is rewriting an
// instruction about is the one case where guessing is better than not.
func PromptEnhancerID(id string) (dir, family string, ok bool) {
	return promptEnhancerName(id)
}
