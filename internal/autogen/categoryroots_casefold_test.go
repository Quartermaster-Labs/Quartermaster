//go:build windows || darwin

package autogen

// Windows and a stock macOS volume both fold case, so a root spelled with a
// different case is the SAME directory and RootList must collapse it. Slash
// paths only: filepath.ToSlash leaves a backslash alone on darwin, so a drive
// path would prove nothing here (see categoryroots_windows_test.go for that).

import "testing"

func TestSettings_RootList_dedupIsCaseInsensitive(t *testing.T) {
	s := Settings{
		ModelsRoot: `/srv/Models`,
		CategoryRoots: map[string]string{
			"image":      `/srv/Image`,
			"transcribe": `/srv/models`, // same directory on a case-folding FS
		},
	}
	got := s.RootList()
	want := []string{`/srv/Models`, `/srv/Image`}
	if len(got) != len(want) {
		t.Fatalf("RootList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RootList[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}
