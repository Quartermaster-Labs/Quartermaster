package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Server-side dispatch for the memory_save / memory_delete chat tools (advertised
// client-side in ui-svelte/src/lib/memoryTools.ts). Storage and the CRUD API live
// in memories.go; this file is only the tool surface.
//
// There is deliberately no memory_read / memory_list tool: the user's memories
// are injected into the chat system prompt every turn, so the model already has
// them in front of it. A read tool would add a schema to the KV-stable prefix of
// every conversation to fetch text that is already there — and would only fire
// when the model remembered to call it, which is exactly the failure the whole
// feature exists to avoid.
//
// Nor is there a "check before you save" rule anywhere in the prompt: duplicates
// are resolved by the store (upsertMemory), not by asking the model to audit its
// own block first. See the package comment in memories.go. What the store cannot
// resolve on its own - a rephrasing too loose to merge safely - it reports back
// on the save that created it (nearestMemory), so the model judges two texts
// side by side instead of scanning the block for something that might be there.

// dispatchMemory runs one memory tool call, returning (displayLabel, resultText).
// The label is what the chat card shows, so it is the fact (or its id), not the
// tool name.
func (tm *turnManager) dispatchMemory(at *activeTurn, tc toolCall) (string, string) {
	if at.user == "" {
		return "memory", "Memory is unavailable for this turn (no signed-in user)."
	}
	switch tc.Name {
	case "memory_save":
		return tm.memorySave(at, tc.Args)
	case "memory_delete":
		return tm.memoryDelete(at, tc.Args)
	}
	return "memory", "Unknown memory tool."
}

// memorySave upserts one memory. An `id` updates that entry; without one the
// store deduplicates (memories.go), so an idless save may create, fold into an
// existing entry, or do nothing at all. The result says which — a model told
// "saved" over a no-op goes on to tell the user something that did not happen.
func (tm *turnManager) memorySave(at *activeTurn, args string) (string, string) {
	var a struct {
		ID   string   `json:"id"`
		Text string   `json:"text"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "memory", "memory_save needs a JSON object like {\"text\":\"…\"} (optionally \"id\" to update an existing memory and \"tags\")."
	}
	out, outcome, err := tm.pg.upsertMemory(at.user, memoryEntry{
		ID:     strings.TrimSpace(a.ID),
		Text:   a.Text,
		Tags:   a.Tags,
		Source: "assistant",
	})
	if err != nil {
		return "memory", "Could not save this memory: " + err.Error()
	}
	// Nothing was written, so there is nothing to announce — and the block the
	// model is looking at already contains the fact, which is why it matched.
	if outcome == memoryDuplicate {
		return memoryLabel(out.Text), fmt.Sprintf(
			"You already remember this, as memory %s: %s\n\nNothing was written and nothing changed. Do NOT tell the user you have just remembered it - if it is worth mentioning at all, say you already knew.",
			out.ID, out.Text)
	}
	verb := "Saved"
	switch outcome {
	case memoryUpdated:
		verb = "Updated"
	case memoryMerged:
		verb = "Folded into an existing, near-identical"
	}
	// A brand-new entry may still be a rephrasing of something already known -
	// too different for the store to merge on its own, close enough that keeping
	// both is clutter. Put the candidate in front of the model rather than asking
	// it to have audited its block beforehand: it is being asked to compare two
	// specific texts, which it can do, and the fix is one more call it already
	// knows how to make. Only on a create - an update or a merge has, by
	// definition, already landed on the right entry.
	near := ""
	if outcome == memoryCreated {
		if n, ok := tm.pg.nearestMemory(at.user, out.Text, out.ID); ok {
			near = fmt.Sprintf(
				"\n\nHeads up: memory %s already says something close - %q. If that is the SAME fact in different words, merge them now: call memory_save with id %q and one text that covers both, then memory_delete %s. If they are genuinely two different facts, keep both and do nothing.",
				n.ID, n.Text, n.ID, out.ID)
		}
	}
	// The id is echoed because it is the only handle the model has for a later
	// correction, and the reminder about the next turn is load-bearing: the
	// injected block was rendered before this call, so the fact is NOT in the
	// prompt yet and a model that re-reads the block will think the save failed.
	return memoryLabel(out.Text), fmt.Sprintf(
		"%s memory %s, which now reads: %s\n\nIt is stored now and will be in your memory block from the user's next message onward (not in the one you were given this turn). Tell the user plainly that you have remembered it.%s",
		verb, out.ID, out.Text, near)
}

// memoryDelete removes one memory by id.
func (tm *turnManager) memoryDelete(at *activeTurn, args string) (string, string) {
	var a struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(args), &a)
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return "memory", "memory_delete needs the `id` of the memory to forget, as shown in your memory block."
	}
	gone, ok, err := tm.pg.deleteMemory(at.user, id)
	if err != nil {
		return "memory", "Could not delete that memory: " + err.Error()
	}
	if !ok {
		return "memory", fmt.Sprintf("No memory with id %q - it may already be gone. Use an id from your memory block.", id)
	}
	return "forget " + memoryLabel(gone.Text), fmt.Sprintf(
		"Deleted memory %s: %s\n\nIt is gone from storage; your memory block still shows it for the rest of this turn.", id, gone.Text)
}

// memoryLabel shortens a memory to a card title.
func memoryLabel(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if r := []rune(text); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return text
}
