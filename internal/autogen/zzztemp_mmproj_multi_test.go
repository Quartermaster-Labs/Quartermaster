package autogen

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStr(buf []byte, s string) []byte {
	l := make([]byte, 8)
	binary.LittleEndian.PutUint64(l, uint64(len(s)))
	buf = append(buf, l...)
	buf = append(buf, []byte(s)...)
	return buf
}

func writeClipGguf(t *testing.T, path string) {
	t.Helper()
	var buf []byte
	buf = append(buf, []byte("GGUF")...)
	ver := make([]byte, 4)
	binary.LittleEndian.PutUint32(ver, 3)
	buf = append(buf, ver...)
	tc := make([]byte, 8)
	binary.LittleEndian.PutUint64(tc, 0)
	buf = append(buf, tc...)
	kvc := make([]byte, 8)
	binary.LittleEndian.PutUint64(kvc, 1)
	buf = append(buf, kvc...)
	buf = writeStr(buf, "general.architecture")
	typ := make([]byte, 4)
	binary.LittleEndian.PutUint32(typ, ggufString)
	buf = append(buf, typ...)
	buf = writeStr(buf, "clip")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeU32(buf []byte, v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return append(buf, b...)
}

func writeKvInt(buf []byte, key string, v uint32) []byte {
	buf = writeStr(buf, key)
	buf = writeU32(buf, ggufU32)
	buf = writeU32(buf, v)
	return buf
}

func writeLlamaGguf(t *testing.T, path string) {
	t.Helper()
	var buf []byte
	buf = append(buf, []byte("GGUF")...)
	buf = writeU32(buf, 3)
	tc := make([]byte, 8)
	binary.LittleEndian.PutUint64(tc, 0)
	buf = append(buf, tc...)
	kvs := []struct {
		key string
		val uint32
	}{
		{"llama.block_count", 2},
		{"llama.attention.head_count", 2},
		{"llama.attention.head_count_kv", 2},
		{"llama.attention.key_length", 8},
		{"llama.attention.value_length", 8},
		{"llama.context_length", 2048},
		{"llama.embedding_length", 16},
	}
	kvc := make([]byte, 8)
	binary.LittleEndian.PutUint64(kvc, uint64(1+len(kvs)))
	buf = append(buf, kvc...)
	buf = writeStr(buf, "general.architecture")
	buf = writeU32(buf, ggufString)
	buf = writeStr(buf, "llama")
	for _, kv := range kvs {
		buf = writeKvInt(buf, kv.key, kv.val)
	}
	buf = append(buf, make([]byte, 2<<20)...) // pad so FileSizeGB rounds nonzero
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestZZZ_Generate_VisionTwinPerQuant(t *testing.T) {
	dir := t.TempDir()
	writeLlamaGguf(t, filepath.Join(dir, "Model-Q4_K_M.gguf"))
	writeLlamaGguf(t, filepath.Join(dir, "Model-Q8_0.gguf"))
	writeClipGguf(t, filepath.Join(dir, "mmproj-model-f16.gguf"))

	gf := GenerateFile{Settings: Settings{ModelsRoot: dir, TargetVramGB: 24}}
	gf.Settings.applyDefaults()
	out, err := Generate(gf, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(out)
	count := strings.Count(out, "-vision:")
	if count != 2 {
		t.Fatalf("want 2 vision twins emitted, got %d\n%s", count, out)
	}
}

func TestZZZ_MmprojPairing_MultiQuant(t *testing.T) {
	dir := t.TempDir()
	writeStub(t, dir, "Model-Q4_K_M.gguf", 1024)
	writeStub(t, dir, "Model-Q8_0.gguf", 2048)
	writeClipGguf(t, filepath.Join(dir, "mmproj-model-f16.gguf"))

	rows, err := DiscoverGgufModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		t.Logf("row ID=%s MmprojPath=%q", row.ID, row.MmprojPath)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	for _, row := range rows {
		if row.MmprojPath == "" {
			t.Errorf("row %s: MmprojPath not set", row.ID)
		}
	}
}
