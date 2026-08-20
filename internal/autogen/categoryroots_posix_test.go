//go:build !windows && !darwin

package autogen

import "testing"

// On a case-sensitive filesystem /srv/Models and /srv/models are two different
// directories, so RootList must keep BOTH. Folding case here (as the shared
// dedup key used to, unconditionally) silently dropped a legitimately distinct
// category root — the same bug that made a model disappear from the catalog.
func TestSettings_RootList_dedupIsCaseSensitive(t *testing.T) {
	s := Settings{
		ModelsRoot: `/srv/Models`,
		CategoryRoots: map[string]string{
			"image":      `/srv/Image`,
			"transcribe": `/srv/models`, // NOT a dup: different directory here
		},
	}
	got := s.RootList()
	want := []string{`/srv/Models`, `/srv/Image`, `/srv/models`}
	if len(got) != len(want) {
		t.Fatalf("RootList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RootList[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}
