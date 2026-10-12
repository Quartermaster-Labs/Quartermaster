package process

import (
	"fmt"
	"strings"
)

// componentLabels turns autogen's role keys into what a user would search the
// model browser for. An unknown key is shown as-is.
var componentLabels = map[string]string{
	"vae":       "VAE",
	"audio_vae": "audio VAE",
	"llm":       "text encoder (LLM)",
	"t5xxl":     "T5-XXL text encoder",
	"clip_l":    "CLIP-L text encoder",
	"clip_g":    "CLIP-G text encoder",
}

// missingComponentsNote is the one-line explanation for a diffusion model
// launched without components autogen could not find, or "" when nothing is
// missing.
func missingComponentsNote(roles []string) string {
	if len(roles) == 0 {
		return ""
	}
	labels := make([]string, 0, len(roles))
	for _, r := range roles {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if l, ok := componentLabels[r]; ok {
			labels = append(labels, l)
		} else {
			labels = append(labels, r)
		}
	}
	if len(labels) == 0 {
		return ""
	}
	return fmt.Sprintf("missing required component(s): %s. Download them into the models folder (or set the paths in the model's settings); the backend cannot load without them",
		strings.Join(labels, ", "))
}
