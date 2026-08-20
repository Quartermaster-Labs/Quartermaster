//go:build windows

package autogen

// Separator folding is the Windows-only half: `E:\Models` and `E:/models` are one
// directory there, and filepath.ToSlash only rewrites backslashes on Windows.

import "testing"

func TestSettings_RootList_dedupFoldsSeparators(t *testing.T) {
	s := Settings{
		ModelsRoot: `E:\Models`,
		CategoryRoots: map[string]string{
			"image":      `E:\Image`,
			"transcribe": `E:/models`, // dup of ModelsRoot (case + separator)
		},
	}
	got := s.RootList()
	want := []string{`E:\Models`, `E:\Image`}
	if len(got) != len(want) {
		t.Fatalf("RootList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RootList[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}
