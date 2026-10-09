package server

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// sd-server's OpenAI route (/v1/images/generations) follows the OpenAI Images
// schema, which has no seed: a top-level "seed" is silently dropped, and every
// request samples with the launch -s value (default 42). Same prompt, same
// image, forever - the opposite of what an OpenAI client expects. The route's
// only side channel for generation params is a JSON block embedded in the
// prompt, which sd-server extracts and removes before encoding:
//
//	a red fox <sd_cpp_extra_args>{"seed": 1234}</sd_cpp_extra_args>
//
// applyImageSeed moves the request's seed into that block, and fills in -1
// (sd.cpp: "random seed for < 0") when the client sent none, so an unseeded
// request is random rather than pinned. A seed the caller already put in the
// block wins; it is the more deliberate of the two.
var sdExtraArgsRe = regexp.MustCompile(`(?s)<sd_cpp_extra_args>(.*?)</sd_cpp_extra_args>`)

// isSDServerCmd reports whether a launch command runs sd-server. The block is
// sd-server syntax: anywhere else it is just text appended to the prompt, so a
// peer (which may be a real OpenAI endpoint) or another image backend is left
// alone.
func isSDServerCmd(cmd string) bool {
	argv := config.ParseCmd(cmd).Argv
	if len(argv) == 0 {
		return false
	}
	exe := strings.ToLower(filepath.Base(filepath.ToSlash(argv[0])))
	return strings.HasPrefix(exe, "sd-server")
}

// applyImageSeed rewrites an /v1/images/generations body for sd-server (see
// above). A body it cannot reason about (no string prompt, an unparseable
// existing block) is returned unchanged, so sd-server reports the problem
// exactly as it would have without us.
func applyImageSeed(body []byte) ([]byte, error) {
	prompt := gjson.GetBytes(body, "prompt")
	if prompt.Type != gjson.String {
		return body, nil
	}

	seed := int64(-1)
	if s := gjson.GetBytes(body, "seed"); s.Type == gjson.Number {
		seed = s.Int()
	}

	text := prompt.String()
	var newPrompt string
	if m := sdExtraArgsRe.FindStringSubmatchIndex(text); m != nil {
		inner := text[m[2]:m[3]]
		if !gjson.Valid(inner) || !gjson.Parse(inner).IsObject() || gjson.Get(inner, "seed").Exists() {
			return body, nil
		}
		merged, err := sjson.Set(inner, "seed", seed)
		if err != nil {
			return nil, err
		}
		newPrompt = text[:m[2]] + merged + text[m[3]:]
	} else {
		block, err := sjson.Set("{}", "seed", seed)
		if err != nil {
			return nil, err
		}
		newPrompt = text + " <sd_cpp_extra_args>" + block + "</sd_cpp_extra_args>"
	}

	body, err := sjson.SetBytes(body, "prompt", newPrompt)
	if err != nil {
		return nil, err
	}
	// The top-level field is meaningless to sd-server; drop it so a capture of
	// the forwarded request shows the one seed that actually applies.
	return sjson.DeleteBytes(body, "seed")
}
