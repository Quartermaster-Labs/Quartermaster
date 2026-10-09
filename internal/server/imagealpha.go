package server

import (
	"errors"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Some image models can render a real alpha channel, and the way to ask for it
// is WORDING, not a flag: the model learned a caption pattern, so the subject
// has to be bracketed by the model card's own sentences, verbatim. A paraphrase
// ("isolated on a transparent background") comes back as an RGBA PNG whose
// alpha is 253-255 everywhere, i.e. opaque.
//
// This table is the one copy of that wording. The server publishes it on the
// model (apiModel.AlphaPrompt) so the playground can expand the prompt itself
// and store the expanded text with the turn, and expands it for API callers
// that send `"transparent": true` on /sdapi/v1/{txt2img,img2img}.
type alphaPrompt struct {
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
}

var alphaPrompts = []struct {
	match string
	alphaPrompt
}{
	// Qwen-Image 2.1 (base and turbo): RGBA VAE, wording from the model card.
	{"qwen-image-2.1", alphaPrompt{
		Prefix: "This is an RGBA image with transparency.",
		Suffix: "The image has alpha channel and the background is transparent.",
	}},
}

// alphaPromptFor returns the transparency wording for a model id, or nil when
// the model has no such mode. Ids are derived from filenames, so one model
// arrives spelled either way (qwen_image_2.1-q8_0 / qwen-image-2.1-Q8): the
// separator is normalised on both sides. Callers must also check the model runs
// sd-server, since the PE rewrite models share the qwen-image-2.1 prefix.
func alphaPromptFor(id string) *alphaPrompt {
	norm := func(s string) string { return strings.ReplaceAll(strings.ToLower(s), "_", "-") }
	l := norm(id)
	for _, a := range alphaPrompts {
		if strings.Contains(l, norm(a.match)) {
			ap := a.alphaPrompt
			return &ap
		}
	}
	return nil
}

// modelAlphaPrompt is alphaPromptFor restricted to sd-server models, for the
// catalog payload.
func modelAlphaPrompt(id, cmd string) *alphaPrompt {
	if !isSDServerCmd(cmd) {
		return nil
	}
	return alphaPromptFor(id)
}

// wrap brackets a subject in the wording. Already-wrapped text (the playground
// expands client-side, and a caller may do the same and ALSO set the flag) is
// returned as-is rather than wrapped twice.
func (a alphaPrompt) wrap(text string) string {
	text = strings.TrimSpace(text)
	if strings.Contains(text, a.Suffix) {
		return text
	}
	return strings.Join(strings.Fields(a.Prefix+" "+text+" "+a.Suffix), " ")
}

// applyImageTransparent consumes a top-level `"transparent"` field on an /sdapi
// body. true on a model with an alpha mode wraps the prompt; true on any other
// model is a 400-worthy request the caller should hear about (errAlphaUnsupported)
// rather than an opaque PNG they would have to inspect to discover. The field is
// removed either way so sd-server never sees an unknown key.
func applyImageTransparent(body []byte, modelID string) ([]byte, error) {
	t := gjson.GetBytes(body, "transparent")
	if !t.Exists() {
		return body, nil
	}
	body, err := sjson.DeleteBytes(body, "transparent")
	if err != nil {
		return nil, err
	}
	if !t.Bool() {
		return body, nil
	}
	a := alphaPromptFor(modelID)
	if a == nil {
		return nil, errAlphaUnsupported
	}
	prompt := gjson.GetBytes(body, "prompt")
	if prompt.Type != gjson.String {
		return body, nil
	}
	return sjson.SetBytes(body, "prompt", a.wrap(prompt.String()))
}

var errAlphaUnsupported = errors.New("transparent: this model has no transparent-background mode")
