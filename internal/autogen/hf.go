package autogen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Hugging Face model folders: the layout `huggingface-cli download` (and the
// in-app hub, `<models root>/<owner>/<repo>`) produces for an unconverted model:
// config.json beside one or more *.safetensors weight files. llama.cpp cannot
// load these; vllm can, from the folder itself. So an HF folder is a row only
// for the vllm path, and its "metadata" comes from config.json instead of a gguf
// header.
//
// The detection is deliberately narrow, because a models tree is full of folders
// that LOOK like this and are not chat models: diffusion components (text
// encoders, VAEs, CLIP), classifier heads (ModernBERT), vision backbones (the
// DINOv3 encoder TRELLIS.2 pairs with), and the source folder a gguf was
// converted from. A folder qualifies only when all of these hold:
//
//  1. config.json names a generative architecture (hfGenerativeArch);
//  2. at least one *.safetensors sits directly in the folder;
//  3. no *.gguf sits directly in the folder. A conversion kept beside its
//     source is the same model twice, and the gguf is the one every backend
//     here can run, so it wins and the folder is left to the gguf scan.

const hfConfigFile = "config.json"

// hfConfig is the subset of a transformers config.json the sizer reads. Every
// numeric field is optional (0 = absent); several are null in real configs,
// which json decodes to 0 as well.
type hfConfig struct {
	Architectures         []string `json:"architectures"`
	ModelType             string   `json:"model_type"`
	NumHiddenLayers       int64    `json:"num_hidden_layers"`
	NumAttentionHeads     int64    `json:"num_attention_heads"`
	NumKeyValueHeads      int64    `json:"num_key_value_heads"`
	HeadDim               int64    `json:"head_dim"`
	HiddenSize            int64    `json:"hidden_size"`
	MaxPositionEmbeddings int64    `json:"max_position_embeddings"`
	// LayerTypes is the per-layer attention kind newer configs spell out
	// ("full_attention", "sliding_attention", "linear_attention"), and the only
	// place a hybrid (Qwen3.5/3.6 GatedDeltaNet, Qwen3-Next) says which layers
	// hold no KV at all.
	LayerTypes            []string `json:"layer_types"`
	FullAttentionInterval int64    `json:"full_attention_interval"`
	// TorchDtype / Dtype: the weights' storage type. transformers renamed the
	// key, so both spellings are in circulation.
	TorchDtype         string          `json:"torch_dtype"`
	Dtype              string          `json:"dtype"`
	QuantizationConfig *hfQuantization `json:"quantization_config"`
	// Multimodal wrappers (Gemma 3, Llama 4, Mistral 3, Qwen3.5-VL...) nest the
	// language model's dimensions here and leave the top level nearly empty.
	TextConfig   *hfConfig       `json:"text_config"`
	VisionConfig json.RawMessage `json:"vision_config"`
}

type hfQuantization struct {
	QuantMethod string `json:"quant_method"`
}

// text returns the config holding the language model's dimensions: the nested
// text_config for a multimodal wrapper, else the top level. The wrapper's own
// architectures list and any top-level trained window are kept, since those
// describe the served model rather than its text tower.
func (c hfConfig) text() hfConfig {
	if c.TextConfig == nil || c.NumHiddenLayers > 0 {
		return c
	}
	t := *c.TextConfig
	t.Architectures = c.Architectures
	if t.MaxPositionEmbeddings == 0 {
		t.MaxPositionEmbeddings = c.MaxPositionEmbeddings
	}
	return t
}

// hfGenerativeArch reports whether config.json describes a model vllm serves as
// a chat/completions LLM. *ForCausalLM is a decoder by definition.
// *ForConditionalGeneration is ALSO what encoder-decoder models call themselves
// (T5, BART, Whisper), which are not chat models; the multimodal chat wrappers
// that share the suffix (Llava, Gemma 3, Qwen2-VL, Mistral 3) are told apart by
// carrying a text_config or vision_config, which the seq2seq ones never do.
func hfGenerativeArch(c hfConfig) bool {
	for _, a := range c.Architectures {
		switch {
		case strings.HasSuffix(a, "ForCausalLM"):
			return true
		case strings.HasSuffix(a, "ForConditionalGeneration"):
			if c.TextConfig != nil || len(c.VisionConfig) > 0 {
				return true
			}
		}
	}
	return false
}

// readHFConfig parses dir/config.json. ok is false when the folder has none.
func readHFConfig(dir string) (hfConfig, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, hfConfigFile))
	if err != nil {
		if os.IsNotExist(err) {
			return hfConfig{}, false, nil
		}
		return hfConfig{}, false, err
	}
	var c hfConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return hfConfig{}, true, fmt.Errorf("%s: %w", filepath.Join(dir, hfConfigFile), err)
	}
	return c, true, nil
}

// hfWeights scans dir's own entries (not subfolders) and returns the summed
// size of its *.safetensors files and whether any *.gguf sits beside them.
func hfWeights(dir string) (bytes int64, hasGguf bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".safetensors":
			// os.Stat, not e.Info(): an HF cache snapshot holds symlinks into its
			// blobs/ folder, and the entry's own info is the link's.
			if fi, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				bytes += fi.Size()
			}
		case ".gguf":
			hasGguf = true
		}
	}
	return bytes, hasGguf
}

// IsHFModelDir reports whether dir is a Hugging Face LLM folder autogen serves
// through vllm (see the file comment for the three rules).
func IsHFModelDir(dir string) bool {
	_, ok := hfModelDir(dir)
	return ok
}

// hfModelDir applies the three rules and returns the parsed config on success.
// A folder whose config.json fails to parse is simply not a model: the same
// "skip what cannot be read" rule the gguf walk follows.
func hfModelDir(dir string) (hfConfig, bool) {
	c, found, err := readHFConfig(dir)
	if !found || err != nil || !hfGenerativeArch(c) {
		return hfConfig{}, false
	}
	bytes, hasGguf := hfWeights(dir)
	if bytes == 0 || hasGguf {
		return hfConfig{}, false
	}
	return c, true
}

// hfQuant names how the folder's weights are stored, for the row's Quant and
// id: the quantization method when the config declares one (AWQ, GPTQ, FP8),
// else the storage dtype (BF16, F16, F32). "" when the config says neither.
func hfQuant(c hfConfig) string {
	if q := c.QuantizationConfig; q != nil && strings.TrimSpace(q.QuantMethod) != "" {
		return strings.ToUpper(strings.TrimSpace(q.QuantMethod))
	}
	dt := c.TorchDtype
	if dt == "" {
		dt = c.Dtype
	}
	switch strings.ToLower(dt) {
	case "bfloat16":
		return "BF16"
	case "float16":
		return "F16"
	case "float32":
		return "F32"
	}
	return ""
}

// hfRow builds the discovery row for an HF folder. FullPath is the folder, which
// is what `vllm serve` takes, and the id follows the gguf convention (lowercased
// name, quant appended unless the name already spells it) so a folder and a
// gguf of the same model sort and read alike in the catalog.
func hfRow(dir string, c hfConfig) GgufRow {
	base := filepath.Base(dir)
	publisher := filepath.Base(filepath.Dir(dir))
	if owner, repo, ok := hfCacheRepo(dir); ok {
		publisher, base = owner, repo
	}
	quant := hfQuant(c)
	id := strings.ToLower(base)
	baseID := id
	if quant != "" && !hasQuantPart(id, quant) {
		id += "-" + strings.ToLower(quant)
	}
	bytes, _ := hfWeights(dir)
	return GgufRow{
		ID:        id,
		BaseID:    baseID,
		FullPath:  dir,
		FileName:  base,
		Quant:     quant,
		SizeGB:    round(float64(bytes)/gib, 2),
		Publisher: publisher,
		Repo:      base,
		IsHF:      true,
	}
}

// hfCacheRepo names the repo of a folder inside the Hugging Face CACHE layout,
// `models--<owner>--<repo>/snapshots/<commit>/`, where the folder's own name is
// a commit hash and would make a meaningless id. ok is false for any other
// layout. A repo name may itself contain "--", so only the first one splits.
func hfCacheRepo(dir string) (owner, repo string, ok bool) {
	snapshots := filepath.Dir(dir)
	if !strings.EqualFold(filepath.Base(snapshots), "snapshots") {
		return "", "", false
	}
	name, found := strings.CutPrefix(filepath.Base(filepath.Dir(snapshots)), "models--")
	if !found {
		return "", "", false
	}
	owner, repo, ok = strings.Cut(name, "--")
	if !ok || owner == "" || repo == "" {
		return "", "", false
	}
	return owner, repo, true
}

// ReadHFMetadata maps an HF folder's config.json onto the Metadata fields the
// vllm sizer reads, so an HF model runs through the same --max-model-len math a
// gguf does. It is deliberately CONSERVATIVE where config.json is ambiguous:
// sliding-window layers are charged as full-attention layers (a larger KV slope,
// so a smaller window, never a window vllm then refuses to allocate), and the
// constant recurrent state of a hybrid's linear layers is not modelled (it is
// small and ctx-independent; vllmOverheadGB covers it).
func ReadHFMetadata(dir string) (Metadata, error) {
	c, found, err := readHFConfig(dir)
	if err != nil {
		return Metadata{}, err
	}
	if !found {
		return Metadata{}, fmt.Errorf("%s: no %s", dir, hfConfigFile)
	}
	bytes, _ := hfWeights(dir)
	t := c.text()

	arch := c.ModelType
	if arch == "" {
		arch = t.ModelType
	}
	m := Metadata{
		Path:            dir,
		FileSizeGB:      round(float64(bytes)/gib, 2),
		Architecture:    arch,
		BlockCount:      t.NumHiddenLayers,
		ContextLength:   t.MaxPositionEmbeddings,
		EmbeddingLength: t.HiddenSize,
		HeadCount:       t.NumAttentionHeads,
		HeadCountKv:     t.NumKeyValueHeads,
		KeyLength:       t.HeadDim,
		QuantLabel:      hfQuant(c),
	}
	if m.HeadCountKv == 0 {
		m.HeadCountKv = m.HeadCount // MHA configs omit the KV head count
	}
	if m.KeyLength == 0 && m.HeadCount > 0 {
		m.KeyLength = t.HiddenSize / m.HeadCount
	}
	m.ValueLength = m.KeyLength

	// Which layers carry KV. layer_types is exact per layer, so it wins over the
	// interval form; a list whose length disagrees with the layer count is not
	// trusted (every layer is then charged, the conservative reading).
	if n := len(t.LayerTypes); n > 0 && int64(n) == m.BlockCount {
		arr := make([]int64, n)
		attn := int64(0)
		for i, lt := range t.LayerTypes {
			if hfLayerHasKV(lt) {
				arr[i] = m.HeadCountKv
				attn++
			}
		}
		m.HeadCountKvArr = arr
		m.AttnLayerCount = attn
	} else if t.FullAttentionInterval > 0 {
		m.FullAttnInterval = t.FullAttentionInterval
	}
	return m, nil
}

// ReadModelMetadata is ReadGgufMetadataCached for any path a served model can
// name: an HF folder reads its config.json, everything else its gguf header.
// For the server's per-model lookups, which hold a path and not a GgufRow.
func ReadModelMetadata(path string) (Metadata, error) {
	if IsHFModelDir(path) {
		return ReadHFMetadata(path)
	}
	return ReadGgufMetadataCached(path)
}

// HFRowFor is the discovery row for dir when it is an HF model folder. A
// caller rendering a command from a bare path needs the row's weight size, not
// just IsHF: vllmMaxModelLen charges it against the budget.
func HFRowFor(dir string) (GgufRow, bool) {
	c, ok := hfModelDir(dir)
	if !ok {
		return GgufRow{}, false
	}
	return hfRow(dir, c), true
}

// HFWeightBytes is the summed size of an HF folder's safetensors, 0 for
// anything else. A folder's own stat size says nothing about its weights.
func HFWeightBytes(dir string) int64 {
	bytes, _ := hfWeights(dir)
	return bytes
}

// emitHFModel serves an HF folder through vllm. The backend pick PREFERS vllm
// over the class default, since llama.cpp (the usual ★ default for "llm") cannot
// read safetensors at all; a per-model backend pin still wins, and one naming a
// non-vllm engine is an error rather than a command that cannot load.
func emitHFModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name string, emitted *[]string) error {
	be := resolveBackendPreferring(s, ov, "llm", "vllm")
	if !strings.EqualFold(be.Kind, "vllm") {
		return fmt.Errorf("safetensors model folder needs a vllm backend; llama.cpp loads gguf only. %s", hfSkipHint(runtime.GOOS))
	}
	meta, err := ReadHFMetadata(row.FullPath)
	if err != nil {
		return err
	}
	emitVllmModel(b, s, row, ov, name, be, meta, emitted)
	return nil
}

// hfLayerHasKV reports whether a layer_types entry keeps a per-token KV cache.
// Linear-attention and state-space layers hold a fixed recurrent state instead.
func hfLayerHasKV(layerType string) bool {
	lt := strings.ToLower(layerType)
	return !strings.Contains(lt, "linear") && !strings.Contains(lt, "mamba") && !strings.Contains(lt, "ssm")
}

// hfSkipHint is the next step a skipped HF folder's comment points at. vllm is
// Linux-only, so on Windows "add a vllm backend" is advice nobody can follow:
// the one real way to run the model there is a gguf conversion of it.
func hfSkipHint(goos string) string {
	if goos == "windows" {
		return "vllm does not run on Windows: download a GGUF build of this model instead"
	}
	return "Add one under Settings > Backends"
}
