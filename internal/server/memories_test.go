package server

import (
	"fmt"
	"strings"
	"testing"
)

func testPlayground(t *testing.T) *Playground {
	t.Helper()
	return &Playground{DataDir: t.TempDir()}
}

func TestPlayground_MemoryUpsertAndList(t *testing.T) {
	p := testPlayground(t)

	a, _, err := p.upsertMemory("bob", memoryEntry{Text: "  Prefers metric units  ", Tags: []string{"Units", "units", " "}})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if a.ID == "" {
		t.Fatal("expected a generated id")
	}
	if a.Text != "Prefers metric units" {
		t.Fatalf("text not trimmed: %q", a.Text)
	}
	if len(a.Tags) != 1 || a.Tags[0] != "units" {
		t.Fatalf("tags not normalized: %v", a.Tags)
	}
	if a.Source != "assistant" {
		t.Fatalf("source = %q, want assistant", a.Source)
	}

	if _, _, err := p.upsertMemory("bob", memoryEntry{Text: "Runs an RX 7900 XTX", Source: "user"}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	// Another user's list must be untouched — memories are per user.
	if got := len(p.listMemories("alice")); got != 0 {
		t.Fatalf("alice sees %d memories", got)
	}
	if got := len(p.listMemories("bob")); got != 2 {
		t.Fatalf("bob has %d memories, want 2", got)
	}
}

func TestPlayground_MemoryUpdateByID(t *testing.T) {
	p := testPlayground(t)
	a, _, _ := p.upsertMemory("bob", memoryEntry{Text: "Uses a 3090"})

	got, _, err := p.upsertMemory("bob", memoryEntry{ID: a.ID, Text: "Uses an RX 7900 XTX"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.ID != a.ID {
		t.Fatalf("id changed on update: %q → %q", a.ID, got.ID)
	}
	// An update REPLACES rather than adding a second, conflicting memory — that
	// is the whole point of the model passing an id back.
	if all := p.listMemories("bob"); len(all) != 1 || all[0].Text != "Uses an RX 7900 XTX" {
		t.Fatalf("update did not replace in place: %+v", all)
	}
	if got.CreatedAt != a.CreatedAt {
		t.Fatalf("createdAt rewritten on update")
	}
}

func TestPlayground_MemoryUnknownIDIsError(t *testing.T) {
	p := testPlayground(t)
	// A wrong id means the model is editing a memory it never read; creating a
	// new one instead would silently duplicate.
	if _, _, err := p.upsertMemory("bob", memoryEntry{ID: "deadbeef", Text: "x"}); err == nil {
		t.Fatal("expected an error for an unknown id")
	}
	if got := len(p.listMemories("bob")); got != 0 {
		t.Fatalf("failed update created %d memories", got)
	}
}

func TestPlayground_MemoryRejectsEmptyAndOversize(t *testing.T) {
	p := testPlayground(t)
	if _, _, err := p.upsertMemory("bob", memoryEntry{Text: "   "}); err == nil {
		t.Fatal("expected an error for empty text")
	}
	if _, _, err := p.upsertMemory("bob", memoryEntry{Text: strings.Repeat("x", maxMemoryLen+1)}); err == nil {
		t.Fatal("expected an error past maxMemoryLen")
	}
}

func TestPlayground_MemoryDelete(t *testing.T) {
	p := testPlayground(t)
	a, _, _ := p.upsertMemory("bob", memoryEntry{Text: "Keeps this"})
	b, _, _ := p.upsertMemory("bob", memoryEntry{Text: "Drops this"})

	gone, ok, err := p.deleteMemory("bob", b.ID)
	if err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	if gone.Text != "Drops this" {
		t.Fatalf("deleted the wrong entry: %q", gone.Text)
	}
	if all := p.listMemories("bob"); len(all) != 1 || all[0].ID != a.ID {
		t.Fatalf("survivor wrong: %+v", all)
	}
	if _, ok, _ := p.deleteMemory("bob", b.ID); ok {
		t.Fatal("second delete reported a hit")
	}
}

func TestPlayground_MemoryFullIsRefusedNotEvicted(t *testing.T) {
	p := testPlayground(t)
	for i := 0; i < maxMemories; i++ {
		if _, _, err := p.upsertMemory("bob", memoryEntry{Text: fmt.Sprintf("distinct fact number %d", i)}); err != nil {
			t.Fatalf("fill at %d: %v", i, err)
		}
	}
	// Dropping something the user asked to be remembered is worse than refusing.
	if _, _, err := p.upsertMemory("bob", memoryEntry{Text: "one too many"}); err == nil {
		t.Fatal("expected an error past maxMemories")
	}
	if got := len(p.listMemories("bob")); got != maxMemories {
		t.Fatalf("list is %d, want %d — entries were evicted", got, maxMemories)
	}
}

// An idless save that restates a fact already stored must not create a second
// entry — the model is told to save whenever it sees a fact, so the store is what
// keeps the list from filling with rephrasings of the same thing.
func TestPlayground_MemoryDedupesOnSave(t *testing.T) {
	p := testPlayground(t)
	first, _, err := p.upsertMemory("bob", memoryEntry{Text: "Runs an RX 7900 XTX", Tags: []string{"hardware"}})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Verbatim (modulo case and punctuation): nothing is written at all.
	got, outcome, err := p.upsertMemory("bob", memoryEntry{Text: "runs an rx 7900 xtx."})
	if err != nil {
		t.Fatalf("duplicate upsert: %v", err)
	}
	if outcome != memoryDuplicate {
		t.Fatalf("outcome = %v, want memoryDuplicate", outcome)
	}
	if got.ID != first.ID || got.Text != first.Text || got.UpdatedAt != first.UpdatedAt {
		t.Fatalf("a verbatim duplicate must change nothing: %+v vs %+v", got, first)
	}

	// A refinement contains the original, so it folds in and the longer text wins.
	got, outcome, err = p.upsertMemory("bob", memoryEntry{Text: "Runs an RX 7900 XTX with 24GB of VRAM", Tags: []string{"gpu"}})
	if err != nil {
		t.Fatalf("merge upsert: %v", err)
	}
	if outcome != memoryMerged {
		t.Fatalf("outcome = %v, want memoryMerged", outcome)
	}
	if got.ID != first.ID {
		t.Fatalf("merge made a new entry %s, want %s", got.ID, first.ID)
	}
	if got.Text != "Runs an RX 7900 XTX with 24GB of VRAM" {
		t.Fatalf("longer text must win, got %q", got.Text)
	}
	if got.CreatedAt != first.CreatedAt {
		t.Fatal("merge must preserve CreatedAt - the injected block is ordered by it")
	}
	if len(got.Tags) != 2 {
		t.Fatalf("tags not unioned: %v", got.Tags)
	}
	// A shorter restatement folds in too, but must not throw detail away.
	if got, _, _ = p.upsertMemory("bob", memoryEntry{Text: "Runs an RX 7900 XTX"}); got.Text != "Runs an RX 7900 XTX with 24GB of VRAM" {
		t.Fatalf("a shorter restatement dropped detail: %q", got.Text)
	}

	// A different fact is still a different fact.
	if _, outcome, _ = p.upsertMemory("bob", memoryEntry{Text: "Prefers metric units"}); outcome != memoryCreated {
		t.Fatalf("outcome = %v, want memoryCreated", outcome)
	}
	if n := len(p.listMemories("bob")); n != 2 {
		t.Fatalf("bob has %d memories, want 2", n)
	}
}

// Re-saying something already remembered has to keep working once the list is
// full: the write is a no-op, so there is nothing for the cap to refuse.
func TestPlayground_MemoryDuplicateSurvivesFullList(t *testing.T) {
	p := testPlayground(t)
	for i := 0; i < maxMemories; i++ {
		if _, _, err := p.upsertMemory("bob", memoryEntry{Text: fmt.Sprintf("distinct fact number %d", i)}); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	if _, outcome, err := p.upsertMemory("bob", memoryEntry{Text: "distinct fact number 7"}); err != nil || outcome != memoryDuplicate {
		t.Fatalf("duplicate on a full list: outcome=%v err=%v", outcome, err)
	}
}

func TestMemoryDuplicateOf(t *testing.T) {
	cases := []struct {
		existing, incoming string
		want               bool
	}{
		{"Prefers metric units", "prefers metric units.", true},        // punctuation + case
		{"Prefers metric units", "  Prefers  metric   units ", true},   // whitespace
		{"Runs an RX 7900 XTX", "Runs an RX 7900 XTX with 24GB", true}, // containment
		{"Prefers metric units", "Prefers imperial units", false},      // one content word apart
		{"Has a dog named Rex", "Has a dog named Max", false},
		{"Lives in Bucharest", "Lives in Berlin", false},
		{"Uses a 3090", "Prefers dark mode", false},
		{"Vegan", "Vegan cooking is a hobby of theirs", false}, // too short to swallow
	}
	for _, c := range cases {
		if got := memoryDuplicateOf(c.existing, c.incoming); got != c.want {
			t.Errorf("memoryDuplicateOf(%q, %q) = %v, want %v", c.existing, c.incoming, got, c.want)
		}
	}
}
