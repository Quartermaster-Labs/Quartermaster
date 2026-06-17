package autogen

import (
	"strings"
	"testing"
)

func emitGroups(s Settings, emitted []string) string {
	var b strings.Builder
	emitGroupsAndListeners(&b, s, emitted)
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
