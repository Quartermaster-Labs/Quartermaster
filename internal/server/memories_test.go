package server

import (
	"strings"
	"testing"
)

func testPlayground(t *testing.T) *Playground {
	t.Helper()
	return &Playground{DataDir: t.TempDir()}
}

func TestPlayground_MemoryUpsertAndList(t *testing.T) {
	p := testPlayground(t)

	a, err := p.upsertMemory("bob", memoryEntry{Text: "  Prefers metric units  ", Tags: []string{"Units", "units", " "}})
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

	if _, err := p.upsertMemory("bob", memoryEntry{Text: "Runs an RX 7900 XTX", Source: "user"}); err != nil {
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
	a, _ := p.upsertMemory("bob", memoryEntry{Text: "Uses a 3090"})

	got, err := p.upsertMemory("bob", memoryEntry{ID: a.ID, Text: "Uses an RX 7900 XTX"})
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
	if _, err := p.upsertMemory("bob", memoryEntry{ID: "deadbeef", Text: "x"}); err == nil {
		t.Fatal("expected an error for an unknown id")
	}
	if got := len(p.listMemories("bob")); got != 0 {
		t.Fatalf("failed update created %d memories", got)
	}
}

func TestPlayground_MemoryRejectsEmptyAndOversize(t *testing.T) {
	p := testPlayground(t)
	if _, err := p.upsertMemory("bob", memoryEntry{Text: "   "}); err == nil {
		t.Fatal("expected an error for empty text")
	}
	if _, err := p.upsertMemory("bob", memoryEntry{Text: strings.Repeat("x", maxMemoryLen+1)}); err == nil {
		t.Fatal("expected an error past maxMemoryLen")
	}
}

func TestPlayground_MemoryDelete(t *testing.T) {
	p := testPlayground(t)
	a, _ := p.upsertMemory("bob", memoryEntry{Text: "Keeps this"})
	b, _ := p.upsertMemory("bob", memoryEntry{Text: "Drops this"})

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
		if _, err := p.upsertMemory("bob", memoryEntry{Text: "fact"}); err != nil {
			t.Fatalf("fill at %d: %v", i, err)
		}
	}
	// Dropping something the user asked to be remembered is worse than refusing.
	if _, err := p.upsertMemory("bob", memoryEntry{Text: "one too many"}); err == nil {
		t.Fatal("expected an error past maxMemories")
	}
	if got := len(p.listMemories("bob")); got != maxMemories {
		t.Fatalf("list is %d, want %d — entries were evicted", got, maxMemories)
	}
}
