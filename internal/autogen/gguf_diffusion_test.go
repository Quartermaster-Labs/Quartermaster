package autogen

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

type ggufTensor struct {
	name string
	dims []uint64
}

// writeGgufTensors lays down a gguf with the given string KVs and a tensor table
// (F32, offsets all 0, no payload). The tensor walk reads names and shapes only,
// so this exercises the same path as a multi-GB DiT.
func writeGgufTensors(t *testing.T, path string, kvs [][2]string, tensors []ggufTensor) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	le := binary.LittleEndian
	str := func(s string) {
		binary.Write(f, le, uint64(len(s)))
		f.WriteString(s)
	}
	f.WriteString("GGUF")
	binary.Write(f, le, uint32(3))
	binary.Write(f, le, uint64(len(tensors)))
	binary.Write(f, le, uint64(len(kvs)))
	for _, kv := range kvs {
		str(kv[0])
		binary.Write(f, le, uint32(8))
		str(kv[1])
	}
	for _, tn := range tensors {
		str(tn.name)
		binary.Write(f, le, uint32(len(tn.dims)))
		for _, d := range tn.dims {
			binary.Write(f, le, d)
		}
		binary.Write(f, le, uint32(0)) // F32
		binary.Write(f, le, uint64(0))
	}
}

// qwenImage21Tensors is the non-block tensor set of a real Qwen-Image 2.1 Turbo
// gguf (caption projection 4096 wide), with every name under prefix.
func qwenImage21Tensors(prefix string) []ggufTensor {
	names := []ggufTensor{
		{"img_in.weight", []uint64{64, 4096}},
		{"txt_in.in_layer.weight", []uint64{4096, 4096}},
		{"txt_in.out_layer.weight", []uint64{4096, 4096}},
		{"transformer_blocks.0.attn.to_q.weight", []uint64{4096, 4096}},
		{"proj_out.weight", []uint64{4096, 64}},
	}
	for i := range names {
		names[i].name = prefix + names[i].name
	}
	return names
}

// unsloth's and AtomicChat's Qwen-Image 2.1 quants keep ComfyUI's
// model.diffusion_model. prefix, and AtomicChat's carry no KVs at all. Both
// must classify as qwen_image with the caption width read, or the first loads
// with the wrong encoder/VAE and the second is served by llama-server as chat.
func TestReadGgufMetadata_QwenImageComfyLayouts(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		kvs  [][2]string
		pfx  string
	}{
		{"leejet bare names", [][2]string{{"general.architecture", "qwen_image"}}, ""},
		{"unsloth prefixed", [][2]string{{"general.architecture", "qwen_image"}}, "model.diffusion_model."},
		{"atomicchat prefixed, zero kvs", nil, "model.diffusion_model."},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, string(rune('a'+i))+".gguf")
			writeGgufTensors(t, p, tc.kvs, qwenImage21Tensors(tc.pfx))
			meta, err := ReadGgufMetadata(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := effectiveImageArch(meta); got != "qwen_image" {
				t.Errorf("effectiveImageArch = %q, want qwen_image", got)
			}
			if meta.CondHidden != 4096 {
				t.Errorf("CondHidden = %d, want 4096", meta.CondHidden)
			}
		})
	}
}

// The qwen_image sniff must not claim a BFL flux file, which also carries
// img_in/txt_in but names its blocks double_blocks.
func TestReadGgufMetadata_FluxNotSniffedAsQwenImage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "flux.gguf")
	writeGgufTensors(t, p, nil, []ggufTensor{
		{"img_in.weight", []uint64{64, 3072}},
		{"txt_in.weight", []uint64{4096, 3072}},
		{"double_blocks.0.img_attn.qkv.weight", []uint64{3072, 9216}},
	})
	meta, err := ReadGgufMetadata(p)
	if err != nil {
		t.Fatal(err)
	}
	if meta.DiffusionKind != "flux" {
		t.Errorf("DiffusionKind = %q, want flux", meta.DiffusionKind)
	}
}
