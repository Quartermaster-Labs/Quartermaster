package autogen

import (
	"reflect"
	"testing"
)

// The help block mixes short/long pairs, value placeholders, prose that names
// other flags and non-flag hyphenated words; the parser must keep the first
// spelling of each flag and drop everything else.
func TestParseBackendHelp(t *testing.T) {
	help := `usage: llama-server [options]

options:
  -h, --help                      show this help message and exit
  -m, --model FNAME               model path (default: models/7B/ggml-model-f16.gguf)
  -c, --ctx-size N                size of the prompt context (default: 4096)
  --no-mmap                       do not memory-map model (may result in slower load)
  --flash-attn [on|off|auto]      set Flash Attention use (deprecated: use -fa)
  -fa, --flash-attn               same as --flash-attn
  --cache-ram N                   max. size of the cache in MiB (default: 8192)
  --temp N                        temperature (default: 0.80). A value of -1 is special.
`
	got := parseBackendHelp(help)
	want := []string{
		"-h", "--help",
		"-m", "--model",
		"-c", "--ctx-size",
		"--no-mmap",
		"--flash-attn",
		"-fa",
		"--cache-ram",
		"--temp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseBackendHelp() = %v, want %v", got, want)
	}
}

func TestParseBackendHelp_NoFlags(t *testing.T) {
	if got := parseBackendHelp("failed to load model: file not found"); len(got) != 0 {
		t.Errorf("parseBackendHelp() = %v, want none", got)
	}
}
