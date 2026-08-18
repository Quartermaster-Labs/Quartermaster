package autogen

import (
	"strings"
	"testing"
)

func emitGroups(s Settings, emitted []string) string {
	return emitGroupsCoexist(s, emitted, coexistSets{})
}

// emitGroupsCoexist is emitGroups with the zero-VRAM model classes (SAM, CPU-only
// TTS.cpp, CPU-only Parakeet ASR) that get their own never-evicting groups.
func emitGroupsCoexist(s Settings, emitted []string, cs coexistSets) string {
	var b strings.Builder
	emitGroupsAndListeners(&b, s, emitted, cs)
	return b.String()
}

func TestAutogen_emitGroups_defaultSingleGroup(t *testing.T) {
	out := emitGroups(Settings{}, []string{"a", "b"})
	if !strings.Contains(out, "  exclusive:\n") {
		t.Fatalf("expected single 'exclusive' group, got:\n%s", out)
	}
	if strings.Contains(out, "listeners:") {
		t.Fatalf("default must not emit listeners, got:\n%s", out)
	}
	for _, m := range []string{`- "a"`, `- "b"`} {
		if !strings.Contains(out, m) {
			t.Fatalf("missing member %s in:\n%s", m, out)
		}
	}
}

func TestAutogen_emitGroups_nameGlobSplit(t *testing.T) {
	s := Settings{Groups: []GroupSpec{
		{Name: "game-judge", Listen: "0.0.0.0:1251", Match: []string{"*-game", "*-judge"}},
		{Name: "assistant", Listen: "0.0.0.0:1250", Match: []string{"*"}},
	}}
	emitted := []string{"qwen", "qwen-game", "qwen-judge", "gemma"}
	out := emitGroups(s, emitted)

	// game/judge variants land in game-judge; the rest in assistant (catch-all).
	gameJudge := section(out, "game-judge:", "assistant:")
	assistant := section(out, "assistant:", "")
	for _, want := range []string{`- "qwen-game"`, `- "qwen-judge"`} {
		if !strings.Contains(gameJudge, want) {
			t.Fatalf("%s not in game-judge group:\n%s", want, out)
		}
	}
	for _, want := range []string{`- "qwen"`, `- "gemma"`} {
		if !strings.Contains(assistant, want) {
			t.Fatalf("%s not in assistant group:\n%s", want, out)
		}
	}
	// listeners map each address to its group, in config order.
	if !strings.Contains(out, `"0.0.0.0:1251":`) || !strings.Contains(out, "groups: [game-judge]") {
		t.Fatalf("missing game-judge listener:\n%s", out)
	}
	if !strings.Contains(out, `"0.0.0.0:1250":`) || !strings.Contains(out, "groups: [assistant]") {
		t.Fatalf("missing assistant listener:\n%s", out)
	}
}

func TestAutogen_emitGroups_unmatchedToDefault(t *testing.T) {
	s := Settings{Groups: []GroupSpec{
		{Name: "special", Listen: "0.0.0.0:1251", Match: []string{"*-game"}},
	}}
	out := emitGroups(s, []string{"qwen", "qwen-game"})
	def := section(out, "default:", "")
	if !strings.Contains(def, `- "qwen"`) {
		t.Fatalf("unmatched 'qwen' should fall into default group:\n%s", out)
	}
	if strings.Contains(out, "groups: [default]") {
		t.Fatalf("default group must not get a listener:\n%s", out)
	}
}

// section returns the substring of out from header up to (but excluding) next,
// or to the end when next is "".
func section(out, header, next string) string {
	i := strings.Index(out, header)
	if i < 0 {
		return ""
	}
	rest := out[i:]
	if next == "" {
		return rest
	}
	if j := strings.Index(rest[len(header):], next); j >= 0 {
		return rest[:len(header)+j]
	}
	return rest
}

// A coexist group emits swap:false (members stay resident together) while keeping
// exclusive:true (the group still evicts the other groups). Non-coexist groups in
// the same config are unaffected.
func TestAutogen_emitGroups_coexistGroup(t *testing.T) {
	s := Settings{Groups: []GroupSpec{
		{Name: "evalfleet", Match: []string{"*-game"}, Coexist: true},
		{Name: "exclusive", Match: []string{"*"}},
	}}
	out := emitGroups(s, []string{"qwen-game", "gemma-game", "qwen"})

	// End marker is a newline + indented key, not the bare word: "exclusive:" also
	// matches the "exclusive: true" line INSIDE the evalfleet block and truncates it.
	fleet := section(out, "  evalfleet:\n", "\n  exclusive:")
	if !strings.Contains(fleet, "swap: false") || !strings.Contains(fleet, "exclusive: true") {
		t.Fatalf("evalfleet must be swap:false + exclusive:true, got:\n%s", out)
	}
	for _, want := range []string{`- "qwen-game"`, `- "gemma-game"`} {
		if !strings.Contains(fleet, want) {
			t.Fatalf("%s not in evalfleet group:\n%s", want, out)
		}
	}
	excl := section(out, "\n  exclusive:\n", "")
	if !strings.Contains(excl, "swap: true") {
		t.Fatalf("non-coexist group must stay swap:true, got:\n%s", out)
	}
	if strings.Contains(out, "listeners:") {
		t.Fatalf("no group has a listen address; listeners must be omitted:\n%s", out)
	}
}

// A CPU-only TTS.cpp voice must NOT sit in an exclusive swap group: reading a
// reply aloud would evict the chat model that produced it (one GPU, one pool) even
// though the voice costs no VRAM. It lands in the persistent "tts" coexistence
// group instead, and stays visible on every listener.
func TestAutogen_emitGroups_cpuTTSCoexists(t *testing.T) {
	s := Settings{Groups: []GroupSpec{
		{Name: "assistant", Listen: "0.0.0.0:1250", Match: []string{"*"}},
	}}
	out := emitGroupsCoexist(s, []string{"qwen", "kokoro-q8"}, coexistSets{TTS: []string{"kokoro-q8"}})

	assistant := section(out, "  assistant:\n", "\n  tts:")
	if strings.Contains(assistant, `- "kokoro-q8"`) {
		t.Fatalf("CPU TTS model must not be in the exclusive group:\n%s", out)
	}
	if !strings.Contains(assistant, `- "qwen"`) {
		t.Fatalf("LLM missing from the exclusive group:\n%s", out)
	}

	tts := section(out, "  tts:\n", "\nlisteners:")
	for _, want := range []string{"swap: false", "exclusive: false", "persistent: true", `- "kokoro-q8"`} {
		if !strings.Contains(tts, want) {
			t.Fatalf("tts group missing %q:\n%s", want, out)
		}
	}

	// The tts group binds no port of its own, so the listener must list it too or
	// read-aloud disappears from that catalog.
	if !strings.Contains(out, "groups: [assistant, tts]") {
		t.Fatalf("tts group must be exposed on every listener:\n%s", out)
	}
}

// A user group already named "tts" wins the name; ours is renamed rather than
// emitted as a duplicate YAML key that would silently drop theirs.
func TestAutogen_emitGroups_cpuTTSNameCollision(t *testing.T) {
	s := Settings{Groups: []GroupSpec{{Name: "tts", Match: []string{"*"}}}}
	out := emitGroupsCoexist(s, []string{"qwen", "kokoro-q8"}, coexistSets{TTS: []string{"kokoro-q8"}})
	if !strings.Contains(out, "  tts-auto:\n") {
		t.Fatalf("expected renamed tts-auto group:\n%s", out)
	}
	if strings.Count(out, "  tts:\n") != 1 {
		t.Fatalf("user's tts group must stay the only \"tts\" key:\n%s", out)
	}
}

// A CPU-only Parakeet ASR model is the same story as a TTS voice: dictating must
// not evict the chat model the transcript is headed for. Each class gets its OWN
// coexistence group so an empty class emits nothing at all.
func TestAutogen_emitGroups_cpuASRCoexists(t *testing.T) {
	s := Settings{Groups: []GroupSpec{
		{Name: "assistant", Listen: "0.0.0.0:1250", Match: []string{"*"}},
	}}
	cs := coexistSets{TTS: []string{"kokoro-q8"}, ASR: []string{"parakeet-q8"}}
	out := emitGroupsCoexist(s, []string{"qwen", "kokoro-q8", "parakeet-q8"}, cs)

	assistant := section(out, "  assistant:\n", "\n  tts:")
	if strings.Contains(assistant, `- "parakeet-q8"`) {
		t.Fatalf("CPU ASR model must not be in the exclusive group:\n%s", out)
	}

	asr := section(out, "  asr:\n", "\nlisteners:")
	for _, want := range []string{"swap: false", "exclusive: false", "persistent: true", `- "parakeet-q8"`} {
		if !strings.Contains(asr, want) {
			t.Fatalf("asr group missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "groups: [assistant, tts, asr]") {
		t.Fatalf("coexistence groups must be exposed on every listener:\n%s", out)
	}
	// No SAM models here, so no empty "sam" group and no dangling listener ref.
	if strings.Contains(out, "  sam:\n") {
		t.Fatalf("empty coexistence class must emit no group:\n%s", out)
	}
}
