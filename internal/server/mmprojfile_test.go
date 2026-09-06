package server

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

// writeMinimalGguf emits the smallest well-formed GGUF that carries a
// general.architecture string: magic, version, zero tensors, one KV pair. The
// validator only reads the header, so this is enough to stand in for a real
// projector (arch "clip") or a real model (anything else) without dragging a
// multi-gigabyte file into the test.
func writeMinimalGguf(t *testing.T, path, arch string) string {
	t.Helper()
	var b []byte
	u64 := func(v uint64) { b = binary.LittleEndian.AppendUint64(b, v) }
	u32 := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	str := func(s string) { u64(uint64(len(s))); b = append(b, s...) }

	b = append(b, "GGUF"...)
	u32(3) // version
	u64(0) // tensor count
	u64(1) // kv count
	str("general.architecture")
	u32(8) // GGUF_TYPE_STRING
	str(arch)

	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A projector pointed at the wrong file is the one mistake this field can make
// that the user never sees at runtime: llama-server starts, the vision twin
// serves, and every image answer is confabulated. So save-time validation has
// to reject more than "missing" - it checks the gguf header says clip, the same
// rule discovery pairs projectors by.
func TestServer_mmprojFileErr(t *testing.T) {
	dir := t.TempDir()
	clip := writeMinimalGguf(t, filepath.Join(dir, "mmproj-f16.gguf"), "clip")
	model := writeMinimalGguf(t, filepath.Join(dir, "qwen3-8b-q4.gguf"), "qwen3")
	notGguf := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notGguf, []byte("not a gguf"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		wantSub string // "" => must be accepted
	}{
		{"empty falls back to discovery", "", ""},
		{"none sentinel", autogen.NoneSentinel, ""},
		{"a real projector", clip, ""},
		{"whitespace is trimmed", "  " + clip + "  ", ""},
		{"missing file", filepath.Join(dir, "gone.gguf"), "not found"},
		{"a directory", dir, "directory"},
		{"not a gguf at all", notGguf, "not a readable gguf"},
		{"a normal model gguf", model, "not a vision projector"},
	}
	for _, tc := range tests {
		got := mmprojFileErr(tc.path)
		switch {
		case tc.wantSub == "" && got != "":
			t.Errorf("%s: rejected with %q, want accepted", tc.name, got)
		case tc.wantSub != "" && !strings.Contains(got, tc.wantSub):
			t.Errorf("%s: got %q, want a message containing %q", tc.name, got, tc.wantSub)
		}
	}
}
