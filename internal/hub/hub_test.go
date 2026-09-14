package hub

import (
	"path/filepath"
	"testing"
)

func TestClassify_ShardGroup(t *testing.T) {
	cases := []struct {
		path          string
		shard, shards int
		group         string
	}{
		{"Qwen3-8B-Q4_K_M.gguf", 0, 0, "Qwen3-8B-Q4_K_M.gguf"},
		{"model-IQ3_XXS.gguf", 0, 0, "model-IQ3_XXS.gguf"},
		{"Llama-3-70B.BF16.gguf", 0, 0, "Llama-3-70B.BF16.gguf"},
		// Every shard of one file collapses onto a single group key, and that key
		// is what the picker shows as the row's name — the shared filename with
		// the part numbering removed.
		{"Q8_0/big-Q8_0-00001-of-00003.gguf", 1, 3, "Q8_0/big-Q8_0.gguf"},
		{"Q8_0/big-Q8_0-00003-of-00003.gguf", 3, 3, "Q8_0/big-Q8_0.gguf"},
	}
	for _, c := range cases {
		f := File{Path: c.path}
		classify(&f)
		if f.Shard != c.shard || f.Shards != c.shards || f.Group != c.group {
			t.Errorf("classify(%q) = shard %d/%d group %q; want %d/%d %q",
				c.path, f.Shard, f.Shards, f.Group, c.shard, c.shards, c.group)
		}
	}
}

func TestClassify_Projector(t *testing.T) {
	// A projector is a companion file, not a model: the flag is what sorts it
	// below the weights and replaces its fit verdict with "companion".
	for _, p := range []string{
		"mmproj-F16.gguf",
		"mmproj-model-f16.gguf",
		"Qwen2-VL-7B-Instruct.mmproj.gguf",
		"sub/Qwen2-VL-mmproj.gguf",
		"MMPROJ-BF16.GGUF",
		"llava-v1.6-mm_proj-Q8_0.gguf",
	} {
		f := File{Path: p}
		classify(&f)
		if !f.Projector {
			t.Errorf("classify(%q).Projector = false, want true", p)
		}
	}
	for _, p := range []string{"Qwen3-8B-Q4_K_M.gguf", "model-IQ3_XXS.gguf", "projection-tuned-Q8_0.gguf"} {
		f := File{Path: p}
		classify(&f)
		if f.Projector {
			t.Errorf("classify(%q).Projector = true, want false", p)
		}
	}
}

func TestIsModelFile(t *testing.T) {
	for _, p := range []string{"a.gguf", "sub/B.GGUF", "sam.ggml"} {
		if !IsModelFile(p) {
			t.Errorf("IsModelFile(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"README.md", "config.json", "model.safetensors", "preview.png"} {
		if IsModelFile(p) {
			t.Errorf("IsModelFile(%q) = true, want false", p)
		}
	}
}

// A repo that ships any GGUF stays a quant repo: its configs, tokenizer and
// README are noise beside the files a loader can open.
func TestSelectFiles_QuantRepo(t *testing.T) {
	files := []File{
		{Path: "Qwen3-8B-Q4_K_M.gguf"},
		{Path: "Qwen3-8B-Q5_K_M.gguf"},
		{Path: "mmproj-F16.gguf"},
		{Path: "config.json"},
		{Path: "tokenizer.json"},
		{Path: "model.safetensors"},
		{Path: "README.md"},
		{Path: ".gitattributes"},
	}
	got := SelectFiles("unsloth/Qwen3-8B-GGUF", files)
	want := []string{"Qwen3-8B-Q4_K_M.gguf", "Qwen3-8B-Q5_K_M.gguf", "mmproj-F16.gguf"}
	if len(got) != len(want) {
		t.Fatalf("got %d files, want %d: %+v", len(got), len(want), got)
	}
	for i, p := range want {
		if got[i].Path != p {
			t.Errorf("file[%d] = %q, want %q", i, got[i].Path, p)
		}
	}
}

// A TRELLIS.2 repo is a component set: the safetensors and the manifests that
// name them ARE the model, they land on disk as one folder, and every one of
// them shares a group key so the picker offers the set as a single row. The
// transformers scaffolding (tokenizer, preprocessor, chat template) stays out.
func TestSelectFiles_ComponentSet(t *testing.T) {
	files := []File{
		{Path: "pipeline.json"},
		{Path: "texturing_pipeline.json"},
		{Path: "ckpts/shape_slat_flow.safetensors"},
		{Path: "ckpts/shape_slat_flow.json"},
		{Path: "ckpts/texture_flow.safetensors"},
		{Path: "ckpts/texture_flow.json"},
		{Path: "config.json"},
		{Path: "tokenizer.json"},
		{Path: "preprocessor_config.json"},
		{Path: "chat_template.jinja"},
		{Path: "README.md"},
		{Path: "images/teaser.png"},
	}
	got := SelectFiles("microsoft/TRELLIS.2-4B", files)
	want := map[string]bool{
		"pipeline.json":                     true,
		"texturing_pipeline.json":           true,
		"ckpts/shape_slat_flow.safetensors": true,
		"ckpts/shape_slat_flow.json":        true,
		"ckpts/texture_flow.safetensors":    true,
		"ckpts/texture_flow.json":           true,
		"config.json":                       true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d files, want %d: %+v", len(got), len(want), got)
	}
	for _, f := range got {
		if !want[f.Path] {
			t.Errorf("unexpected file %q", f.Path)
		}
		if f.Group != "microsoft/TRELLIS.2-4B" {
			t.Errorf("%q: group = %q, want the repo id so the set is one picker row", f.Path, f.Group)
		}
	}
}

// Neither a quant file nor a component file: nothing can be loaded, so the
// picker must be empty rather than offering a README as a download.
func TestSelectFiles_UnloadableRepo(t *testing.T) {
	got := SelectFiles("someone/notes", []File{{Path: "README.md"}, {Path: "train.py"}, {Path: "tokenizer.json"}})
	if len(got) != 0 {
		t.Fatalf("want no files, got %+v", got)
	}
}

func TestHF_CheckURL(t *testing.T) {
	h := NewHF()
	ok := []string{
		"https://huggingface.co/o/r/resolve/main/a.gguf",
		"https://cdn-lfs-us-1.hf.co/repos/x/y",
		"https://cdn-lfs.huggingface.co/repos/x",
	}
	for _, u := range ok {
		if err := h.CheckURL(u); err != nil {
			t.Errorf("CheckURL(%q) = %v, want nil", u, err)
		}
	}
	// A redirect or a poisoned API response must not be able to walk the
	// download off the hub, and plain http must not be accepted either.
	bad := []string{
		"http://huggingface.co/o/r/resolve/main/a.gguf",
		"https://evil.com/a.gguf",
		"https://huggingface.co.evil.com/a.gguf",
		"https://nothf.co.uk/a.gguf",
	}
	for _, u := range bad {
		if err := h.CheckURL(u); err == nil {
			t.Errorf("CheckURL(%q) = nil, want refusal", u)
		}
	}
}

func TestValidRepoIDAndPath(t *testing.T) {
	for _, id := range []string{"unsloth/Qwen3-8B-GGUF", "a/b.c-d_e"} {
		if err := validRepoID(id); err != nil {
			t.Errorf("validRepoID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"noslash", "a/b/c", "/b", "a/", "../etc", "a/..", "o/r?x=1", "o/r#f"} {
		if err := validRepoID(id); err == nil {
			t.Errorf("validRepoID(%q) = nil, want refusal", id)
		}
	}
	for _, p := range []string{"a.gguf", "Q4_K_M/a-00001-of-00002.gguf"} {
		if err := validRepoPath(p); err != nil {
			t.Errorf("validRepoPath(%q) = %v, want nil", p, err)
		}
	}
	for _, p := range []string{"", "/abs.gguf", "../a.gguf", "a/../../b", "a\\b.gguf", "a//b"} {
		if err := validRepoPath(p); err == nil {
			t.Errorf("validRepoPath(%q) = nil, want refusal", p)
		}
	}
}

func TestFileURL_Escapes(t *testing.T) {
	h := NewHF()
	got, err := h.FileURL("unsloth/Qwen3-8B-GGUF", "Q4_K_M/model file.gguf")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://huggingface.co/unsloth/Qwen3-8B-GGUF/resolve/main/Q4_K_M/model%20file.gguf"
	if got != want {
		t.Errorf("FileURL = %q, want %q", got, want)
	}
	if _, err := h.FileURL("unsloth/Qwen3", "../../etc/passwd"); err == nil {
		t.Error("FileURL accepted a traversing path")
	}
}

func TestRepoDirName(t *testing.T) {
	// One folder per repo, nested under the publisher — the hub's own layout.
	cases := map[string]string{
		"unsloth/Qwen3-8B-GGUF": filepath.Join("unsloth", "Qwen3-8B-GGUF"),
		"a b/c:d":               filepath.Join("a-b", "c-d"),
		"noslash":               "noslash",
		// A traversing owner cannot climb out: every segment is sanitized before
		// it is joined, so ".." never survives as ".."
		"../..//etc": filepath.Join("--", "..--etc"),
	}
	for in, want := range cases {
		if got := RepoDirName(in); got != want {
			t.Errorf("RepoDirName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParamsB(t *testing.T) {
	cases := map[string]float64{
		"unsloth/Qwen3-235B-A22B-GGUF":      235, // MoE: total, never the active count
		"bartowski/Meta-Llama-3.1-70B-GGUF": 70,
		"unsloth/Qwen3.6-35B-A3B-Q4_K_M":    35,
		"TheBloke/Mixtral-8x7B-v0.1-GGUF":   56, // product; overstates, which is the safe way to be wrong
		"unsloth/Qwen3-4B-Instruct-GGUF":    4,
		"someone/tinyllama-1.1b-chat-gguf":  1.1,
		"ggml-org/gpt-oss-120b-GGUF":        120,
		"unsloth/Qwen3-8B-GGUF-Q4_K_M":      8, // a quant tag is not a size
		"mradermacher/some-model-i1-GGUF":   0, // no size stated -> unknown
		"openai/whisper-large-v3":           0,
	}
	for in, want := range cases {
		if got := ParamsB(in); got != want {
			t.Errorf("ParamsB(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestWithinParams(t *testing.T) {
	if !WithinParams("unsloth/Qwen3-235B-A22B-GGUF", 0) {
		t.Error("a zero cap must not filter anything")
	}
	if WithinParams("unsloth/Qwen3-235B-A22B-GGUF", 120) {
		t.Error("235B passed a 120B cap")
	}
	if !WithinParams("unsloth/Qwen3-70B-GGUF", 120) {
		t.Error("70B failed a 120B cap")
	}
	if !WithinParams("mradermacher/some-model-GGUF", 120) {
		t.Error("an unknown size must be kept, not hidden")
	}
	if WithinParams("ggml-org/gpt-oss-120b-GGUF", 120) {
		t.Error("the cap is exclusive: 120B must not pass a 120B cap")
	}
}

// The Video tab is the one category no single HF query describes: the exact
// pipeline tags are split across the catalog and HF ANDs its filters, so it
// asks twice and merges. `gguf` is deliberately absent — the useful video repos
// are LoRAs and safetensors component sets.
func TestSearchFilterSets_Video(t *testing.T) {
	got := searchFilterSets("video")
	if len(got) != 2 {
		t.Fatalf("searchFilterSets(video) = %v, want two sets", got)
	}
	for _, set := range got {
		if len(set) != 1 {
			t.Fatalf("set %v: want one filter per set, so neither hides the other", set)
		}
		if set[0] == "gguf" {
			t.Fatal("video must not require gguf: LoRA and safetensors repos are the point")
		}
	}
	if got[0][0] != "text-to-video" || got[1][0] != "image-to-video" {
		t.Fatalf("searchFilterSets(video) = %v", got)
	}
	// Every other category is one set, unchanged.
	if sets := searchFilterSets("image"); len(sets) != 1 || len(sets[0]) != 2 {
		t.Fatalf("searchFilterSets(image) = %v, want one gguf+pipeline set", sets)
	}
}

// A LoRA repo is a pile of ALTERNATIVES, not the parts of one model: grouping
// it under the repo id offered a single row that downloaded all seventeen.
func TestSelectFiles_LooseWeightsRepo(t *testing.T) {
	files := []File{
		{Path: "README.md"},
		{Path: "minimax_h3_fl2v_turbo_4step_v1.1_768p_bf16.safetensors"},
		{Path: "minimax_h3_fl2v_turbo_4step_v1.1_768p_fp8.safetensors"},
		{Path: "minimax_h3_ref2v_turbo_8step_v1.0_768p_bf16.safetensors"},
	}
	for i := range files {
		classify(&files[i])
	}
	got := SelectFiles("lightx2v/Minimax-h3-Turbo", files)
	if len(got) != 3 {
		t.Fatalf("kept %d files, want the 3 safetensors: %v", len(got), got)
	}
	groups := map[string]bool{}
	for _, f := range got {
		if f.Group == "lightx2v/Minimax-h3-Turbo" {
			t.Fatalf("%s: grouped as the whole repo, want its own row", f.Path)
		}
		if f.Group == "" {
			t.Fatalf("%s: no group key", f.Path)
		}
		groups[f.Group] = true
	}
	if len(groups) != 3 {
		t.Fatalf("3 alternatives collapsed onto %d rows", len(groups))
	}
}

// MarkSelection lists the WHOLE repo: the curated files keep SelectFiles'
// grouping, everything else comes back flagged and on its own row.
func TestMarkSelection_ListsEverything(t *testing.T) {
	files := []File{
		{Path: "README.md"},
		{Path: "Qwen3-8B-Q4_K_M.gguf"},
		{Path: "mmproj-F16.gguf"},
		{Path: "tokenizer.json"},
		{Path: "vae/diffusion_pytorch_model.safetensors"},
	}
	got := MarkSelection("unsloth/Qwen3-8B-GGUF", files)
	if len(got) != len(files) {
		t.Fatalf("listed %d files, want all %d", len(got), len(files))
	}
	aux := map[string]bool{}
	for _, f := range got {
		aux[f.Path] = f.Aux
		if f.Aux && f.Group != f.Path {
			t.Errorf("%s: aux file grouped as %q, want its own path", f.Path, f.Group)
		}
	}
	for p, want := range map[string]bool{
		"Qwen3-8B-Q4_K_M.gguf": false,
		"mmproj-F16.gguf":      false,
		"README.md":            true,
		"tokenizer.json":       true,
		"vae/diffusion_pytorch_model.safetensors": true, // a quant repo: the gguf is the model
	} {
		if aux[p] != want {
			t.Errorf("%s: aux=%v, want %v", p, aux[p], want)
		}
	}
}

// A repo with nothing loadable is still fully listed — that is the case the
// old picker answered with "this repo carries no GGUF files" and no recourse.
func TestMarkSelection_UnloadableRepo(t *testing.T) {
	got := MarkSelection("someone/notes", []File{{Path: "README.md"}, {Path: "train.py"}})
	if len(got) != 2 {
		t.Fatalf("listed %d files, want 2", len(got))
	}
	for _, f := range got {
		if !f.Aux {
			t.Errorf("%s: want aux", f.Path)
		}
	}
}
