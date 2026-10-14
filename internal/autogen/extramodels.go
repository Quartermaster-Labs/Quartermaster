package autogen

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"gopkg.in/yaml.v3"
)

// emitExtraModels writes each settings.extraModels entry into the models
// section as written: the escape hatch for a server autogen has no backend
// kind for (Strata, or any other OpenAI-compatible engine). Nothing is sized
// or inferred, so the block's own estVramGB/estRamGB is the scheduler's only
// view of it, which is why one of them is required.
//
// Every entry is decoded strictly into config.ModelConfig first and skipped
// with an in-band reason when it fails, rather than spliced in raw: one
// mis-indented or misspelled block would otherwise fail the whole config
// load, and with it startup. A valid entry is re-marshalled from its node, so
// key order and block scalars (cmd: >) come out the way the user wrote them.
//
// Emitted names join `emitted`, which is what puts them in the exclusive swap
// group (or a settings.groups glob): a block outside every group would load
// alongside the GPU model and the two would collide. A name already served
// keeps its owner, same rule as emitExtraImageModels.
func emitExtraModels(b *strings.Builder, s Settings, seen map[string]bool, emitted *[]string) {
	n := s.ExtraModels
	if n.Kind == 0 || (n.Kind == yaml.ScalarNode && n.Tag == "!!null") {
		return
	}
	if n.Kind != yaml.MappingNode {
		b.WriteString("\n  # SKIPPED settings.extraModels: expected a map of model name -> model block\n")
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		name := strings.TrimSpace(n.Content[i].Value)
		val := n.Content[i+1]
		if reason := extraModelErr(name, val); reason != "" {
			fmt.Fprintf(b, "\n  # SKIPPED extra model %q: %s\n", name, reason)
			continue
		}
		if seen[name] || slices.Contains(*emitted, name) {
			fmt.Fprintf(b, "\n  # SKIPPED extra model %q: that name is already served by another model\n", name)
			continue
		}
		out, err := yaml.Marshal(val)
		if err != nil {
			fmt.Fprintf(b, "\n  # SKIPPED extra model %q: %s\n", name, oneLine(err))
			continue
		}
		seen[name] = true
		b.WriteString("\n  # extra model - hand-declared in settings.extraModels, emitted as written\n")
		fmt.Fprintf(b, "  %q:\n", name)
		b.WriteString(indentYAML(string(out), "    "))
		*emitted = append(*emitted, name)
	}
}

// extraModelErr returns why an extraModels entry cannot be emitted, or "".
func extraModelErr(name string, val *yaml.Node) string {
	if name == "" {
		return "the model name is empty"
	}
	if val.Kind != yaml.MappingNode {
		return "expected a model block (cmd, proxy, checkEndpoint, ...)"
	}
	raw, err := yaml.Marshal(val)
	if err != nil {
		return oneLine(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var mc config.ModelConfig
	if err := dec.Decode(&mc); err != nil {
		return oneLine(err)
	}
	if strings.TrimSpace(mc.Cmd) == "" {
		return "cmd is required"
	}
	if !mappingHasKey(val, "estVramGB") && !mappingHasKey(val, "estRamGB") {
		return "set estVramGB (or estRamGB for a CPU-only server) so the scheduler can account for it"
	}
	return ""
}

func mappingHasKey(n *yaml.Node, key string) bool {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return true
		}
	}
	return false
}

// oneLine keeps an error inside a single YAML comment line.
func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}
