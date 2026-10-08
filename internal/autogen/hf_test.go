package autogen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHFDir lays down an HF model folder: config.json with the given body and
// one stub file per weight name. Discovery reads config.json and stats the
// weights, and never opens a safetensors, so the stubs need no real header.
func writeHFDir(t *testing.T, dir, config string, files ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, hfConfigFile), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), make([]byte, 2048), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const (
	hfLlamaConfig = `{"architectures":["LlamaForCausalLM"],"model_type":"llama",
		"num_hidden_layers":32,"num_attention_heads":32,"num_key_value_heads":8,
		"hidden_size":4096,"max_position_embeddings":131072,"torch_dtype":"bfloat16"}`
	hfGemmaConfig = `{"architectures":["Gemma3ForConditionalGeneration"],"model_type":"gemma3",
		"text_config":{"model_type":"gemma3_text","num_hidden_layers":34,"num_attention_heads":8,
		"num_key_value_heads":4,"head_dim":256,"hidden_size":2560,"max_position_embeddings":131072},
		"vision_config":{"model_type":"siglip_vision_model"},"torch_dtype":"bfloat16"}`
)

func hfRowsByPath(rows []GgufRow) map[string]GgufRow {
	m := map[string]GgufRow{}
	for _, r := range rows {
		m[filepath.Clean(r.FullPath)] = r
	}
	return m
}

// The folders a real models tree holds that LOOK like an HF model: only the
// generative ones with their own weights, and no conversion beside them, may
// become rows.
func TestDiscover_HFFolders(t *testing.T) {
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

	sharded := writeHFDir(t, at("meta/Llama-3-8B-Instruct"), hfLlamaConfig,
		"model-00001-of-00002.safetensors", "model-00002-of-00002.safetensors", "model.safetensors.index.json")
	multimodal := writeHFDir(t, at("google/gemma-3-4b-it"), hfGemmaConfig, "model.safetensors")
	awq := writeHFDir(t, at("q/Qwen2.5-7B-Instruct"),
		`{"architectures":["Qwen2ForCausalLM"],"num_hidden_layers":28,"quantization_config":{"quant_method":"awq"}}`,
		"model.safetensors")

	negatives := map[string]string{
		// A classifier head and a vision backbone: safetensors + config.json, not chat.
		"answerdotai/ModernBERT": `{"architectures":["ModernBertForMaskedLM"]}`,
		"facebook/dinov3":        `{"architectures":["DINOv3ViTModel"]}`,
		// Seq2seq models call themselves *ForConditionalGeneration too.
		"google/t5-base":       `{"architectures":["T5ForConditionalGeneration"]}`,
		"openai/whisper-small": `{"architectures":["WhisperForConditionalGeneration"]}`,
		"broken/json":          `{"architectures":[`,
		"x/no-config":          ``,
	}
	for rel, cfg := range negatives {
		writeHFDir(t, at(rel), cfg, "model.safetensors")
	}
	noWeights := writeHFDir(t, at("x/config-only"), hfLlamaConfig)

	// The source folder a gguf was converted from: the gguf wins, the folder is
	// not a second copy of the model.
	converted := writeHFDir(t, at("conv/Llama-3-8B"), hfLlamaConfig, "model.safetensors")
	convGguf := seededGguf(t, root, "conv/Llama-3-8B/Llama-3-8B-Q4_K_M.gguf")

	// A projector in the PARENT folder of an HF model belongs to whatever gguf
	// sits there; the dir-local pairing must not hand it to the folder.
	// Pairing reads the header arch, so the stub is seeded as a clip projector.
	proj := at("meta/mmproj-F16.gguf")
	if err := os.WriteFile(proj, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	seedMetaCache(t, proj, Metadata{Architecture: "clip"})

	rows, err := DiscoverGgufModels(root)
	if err != nil {
		t.Fatal(err)
	}
	byPath := hfRowsByPath(rows)

	for _, want := range []struct {
		dir, id, quant, publisher string
	}{
		{sharded, "llama-3-8b-instruct-bf16", "BF16", "meta"},
		{multimodal, "gemma-3-4b-it-bf16", "BF16", "google"},
		{awq, "qwen2.5-7b-instruct-awq", "AWQ", "q"},
	} {
		r, ok := byPath[filepath.Clean(want.dir)]
		if !ok {
			t.Errorf("%s: no row", want.dir)
			continue
		}
		if !r.IsHF || r.ID != want.id || r.Quant != want.quant || r.Publisher != want.publisher {
			t.Errorf("%s: row = {IsHF:%v ID:%q Quant:%q Publisher:%q}, want {true %q %q %q}",
				want.dir, r.IsHF, r.ID, r.Quant, r.Publisher, want.id, want.quant, want.publisher)
		}
		if r.MmprojPath != "" || r.DraftPath != "" {
			t.Errorf("%s: HF row picked up a sidecar (mmproj %q, draft %q)", want.dir, r.MmprojPath, r.DraftPath)
		}
	}
	if r := byPath[filepath.Clean(sharded)]; r.SizeGB != round(float64(2*2048)/gib, 2) {
		t.Errorf("sharded size = %g, want both shards summed", r.SizeGB)
	}

	for rel := range negatives {
		if _, ok := byPath[filepath.Clean(at(rel))]; ok {
			t.Errorf("%s: became a row", rel)
		}
	}
	for _, dir := range []string{noWeights, converted} {
		if _, ok := byPath[filepath.Clean(dir)]; ok {
			t.Errorf("%s: became a row", dir)
		}
	}
	if r, ok := byPath[filepath.Clean(convGguf)]; !ok || r.IsHF {
		t.Errorf("the conversion's gguf should still be an ordinary row, got %+v (found %v)", r, ok)
	}
}

// The HF cache layout names the model only in a grandparent folder; the folder
// holding the weights is a commit hash, which would make a meaningless id.
func TestHFRow_CacheLayout(t *testing.T) {
	root := t.TempDir()
	dir := writeHFDir(t, filepath.Join(root, "models--Qwen--Qwen3-4B--Instruct", "snapshots", "0123abcd"),
		hfLlamaConfig, "model.safetensors")
	r, ok := HFRowFor(dir)
	if !ok {
		t.Fatal("snapshot folder not recognized")
	}
	if r.Publisher != "Qwen" || r.Repo != "Qwen3-4B--Instruct" || r.ID != "qwen3-4b--instruct-bf16" {
		t.Errorf("cache row = {Publisher:%q Repo:%q ID:%q}", r.Publisher, r.Repo, r.ID)
	}
}

func TestReadHFMetadata(t *testing.T) {
	root := t.TempDir()

	t.Run("dense", func(t *testing.T) {
		m, err := ReadHFMetadata(writeHFDir(t, filepath.Join(root, "llama"), hfLlamaConfig, "model.safetensors"))
		if err != nil {
			t.Fatal(err)
		}
		// head_dim absent: hidden/heads, the transformers default.
		if m.Architecture != "llama" || m.BlockCount != 32 || m.HeadCount != 32 || m.HeadCountKv != 8 ||
			m.KeyLength != 128 || m.ValueLength != 128 || m.ContextLength != 131072 || m.QuantLabel != "BF16" {
			t.Errorf("dense meta = %+v", m)
		}
	})

	t.Run("multimodal text_config", func(t *testing.T) {
		m, err := ReadHFMetadata(writeHFDir(t, filepath.Join(root, "gemma"), hfGemmaConfig, "model.safetensors"))
		if err != nil {
			t.Fatal(err)
		}
		if m.BlockCount != 34 || m.HeadCountKv != 4 || m.KeyLength != 256 || m.ContextLength != 131072 {
			t.Errorf("text_config not read: %+v", m)
		}
	})

	t.Run("MHA omits kv heads", func(t *testing.T) {
		m, err := ReadHFMetadata(writeHFDir(t, filepath.Join(root, "mha"),
			`{"architectures":["GPT2LMHeadModel","XForCausalLM"],"num_hidden_layers":4,"num_attention_heads":16,"hidden_size":1024}`,
			"model.safetensors"))
		if err != nil {
			t.Fatal(err)
		}
		if m.HeadCountKv != 16 || m.KeyLength != 64 {
			t.Errorf("MHA meta = %+v, want kv heads = heads and hidden/heads head dim", m)
		}
	})

	t.Run("hybrid layer_types", func(t *testing.T) {
		m, err := ReadHFMetadata(writeHFDir(t, filepath.Join(root, "hybrid"),
			`{"architectures":["Qwen3NextForCausalLM"],"num_hidden_layers":4,"num_attention_heads":16,
			"num_key_value_heads":2,"head_dim":256,"hidden_size":2048,
			"layer_types":["linear_attention","linear_attention","linear_attention","full_attention"]}`,
			"model.safetensors"))
		if err != nil {
			t.Fatal(err)
		}
		if m.AttnLayerCount != 1 || len(m.HeadCountKvArr) != 4 || m.HeadCountKvArr[3] != 2 || m.HeadCountKvArr[0] != 0 {
			t.Errorf("hybrid meta = AttnLayerCount %d, HeadCountKvArr %v", m.AttnLayerCount, m.HeadCountKvArr)
		}
	})

	t.Run("layer_types length mismatch is not trusted", func(t *testing.T) {
		m, err := ReadHFMetadata(writeHFDir(t, filepath.Join(root, "mismatch"),
			`{"architectures":["XForCausalLM"],"num_hidden_layers":4,"num_attention_heads":8,"hidden_size":512,
			"layer_types":["linear_attention","full_attention"]}`,
			"model.safetensors"))
		if err != nil {
			t.Fatal(err)
		}
		if m.HeadCountKvArr != nil || m.AttnLayerCount != 0 {
			t.Errorf("mismatched layer_types was used: %+v", m)
		}
	})
}

// The same model described by config.json and by its gguf header must size to
// the same vllm window: the HF path reuses the gguf KV model, so any gap here is
// a mapping bug in ReadHFMetadata.
func TestVllmMaxModelLen_HFParityWithGguf(t *testing.T) {
	dir := writeHFDir(t, filepath.Join(t.TempDir(), "llama"), hfLlamaConfig, "model.safetensors")
	hfMeta, err := ReadHFMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	ggufMeta := Metadata{
		Architecture: "llama", BlockCount: 32, ContextLength: 131072, EmbeddingLength: 4096,
		HeadCount: 32, HeadCountKv: 8, KeyLength: 128, ValueLength: 128,
	}
	s := Settings{TargetVramGB: 24}
	row := GgufRow{SizeGB: 15}
	hfCtx, _ := vllmMaxModelLen(s, nil, row, hfMeta)
	ggCtx, _ := vllmMaxModelLen(s, nil, row, ggufMeta)
	if hfCtx != ggCtx || hfCtx <= 0 {
		t.Errorf("config.json window %d != gguf window %d", hfCtx, ggCtx)
	}
}

// servesBlocks is blocksFor for vllm, which names its model positionally.
func servesBlocks(out, dir string) []string {
	var got []string
	for _, cmd := range modelBlocks(out) {
		if strings.Contains(cmd, "serve "+dir+"\n") {
			got = append(got, cmd)
		}
	}
	return got
}

func TestGenerate_HFFolderServedByVllm(t *testing.T) {
	withCard(t, 24, true)
	root := t.TempDir()
	dir := writeHFDir(t, filepath.Join(root, "meta", "Llama-3-8B-Instruct"), hfLlamaConfig, "model.safetensors")
	slashed := filepath.ToSlash(dir)

	t.Run("vllm registered", func(t *testing.T) {
		// llama is the class default; the HF folder must still go to vllm,
		// because llama.cpp cannot read it at all.
		s := Settings{ModelsRoot: root, TargetVramGB: 16, Backends: []BackendEntry{
			{ID: "llama", Kind: "llama", Path: "llama-server", Default: true},
			{ID: "v", Kind: "vllm", Path: "vllm"},
		}}
		out, err := Generate(GenerateFile{Settings: s}, "test")
		if err != nil {
			t.Fatal(err)
		}
		blocks := servesBlocks(out, slashed)
		if len(blocks) != 1 {
			t.Fatalf("want one block serving %s, got %d in:\n%s", slashed, len(blocks), out)
		}
		for _, want := range []string{"serve " + slashed, "--served-model-name llama-3-8b-instruct-bf16", "--max-model-len"} {
			if !strings.Contains(blocks[0], want) {
				t.Errorf("block missing %q:\n%s", want, blocks[0])
			}
		}
		if strings.Contains(blocks[0], "--quantization gguf") {
			t.Errorf("HF folder emitted the gguf quantization flag:\n%s", blocks[0])
		}
		if !strings.Contains(out, "(vllm, safetensors,") {
			t.Errorf("comment should name the safetensors format:\n%s", out)
		}
	})

	t.Run("no vllm backend", func(t *testing.T) {
		s := Settings{ModelsRoot: root, TargetVramGB: 16}
		out, err := Generate(GenerateFile{Settings: s}, "test")
		if err != nil {
			t.Fatal(err)
		}
		if len(servesBlocks(out, slashed)) != 0 {
			t.Errorf("HF folder emitted without vllm:\n%s", out)
		}
		if !strings.Contains(out, `# SKIPPED "llama-3-8b-instruct-bf16": safetensors model folder needs a vllm backend`) {
			t.Errorf("want an in-band skip reason:\n%s", out)
		}
	})
}

func TestRenderSoloCmdLayers_HFFolder(t *testing.T) {
	withCard(t, 24, true)
	dir := writeHFDir(t, filepath.Join(t.TempDir(), "meta", "Llama-3-8B"), hfLlamaConfig, "model.safetensors")
	row, ok := HFRowFor(dir)
	if !ok {
		t.Fatal("not an HF folder")
	}
	meta, err := ReadHFMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}

	s := Settings{TargetVramGB: 16, Backends: []BackendEntry{{ID: "v", Kind: "vllm", Path: "vllm"}}}
	cmd, err := RenderSoloCmdLayers(s, meta, row, Override{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd.Effective, "serve "+filepath.ToSlash(dir)) || strings.Contains(cmd.Effective, "--quantization gguf") {
		t.Errorf("preview = %s", cmd.Effective)
	}

	if _, err := RenderSoloCmdLayers(Settings{TargetVramGB: 16}, meta, row, Override{}); err == nil {
		t.Error("preview with no vllm backend should refuse, not render a llama command")
	}
}

// A folder dropped into the models tree must change the inputs hash, or the
// config is never regenerated to serve it.
func TestInputsHash_SeesHFFolders(t *testing.T) {
	root := t.TempDir()
	before, err := InputsHash(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeHFDir(t, filepath.Join(root, "meta", "Llama"), hfLlamaConfig, "model.safetensors")
	after, err := InputsHash(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("adding an HF folder left the inputs hash unchanged")
	}
}

func TestHFSkipHint_WindowsPointsAtGguf(t *testing.T) {
	if h := hfSkipHint("windows"); !strings.Contains(h, "GGUF") || strings.Contains(h, "Settings > Backends") {
		t.Errorf("windows hint must point at a gguf, not an uninstallable backend: %q", h)
	}
	if h := hfSkipHint("linux"); !strings.Contains(h, "Settings > Backends") {
		t.Errorf("linux hint must point at the backends page: %q", h)
	}
}
