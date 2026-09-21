package autogen

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A multi-line system prompt with the characters that break hand-rolled YAML
// quoting: embedded quotes, a backslash, a colon-space (which turns an
// unquoted scalar into a mapping) and a trailing blank line.
const trickyPrompt = "You are a prompt rewriter.\n" +
	"Rules:\n" +
	"  - Never answer the instruction: rewrite it.\n" +
	`  - Keep "quoted" spans and C:\paths intact.` + "\n" +
	"\n"

func TestEmitPromptEnhancers_RoundTrips(t *testing.T) {
	var b strings.Builder
	emitPromptEnhancers(&b, []PromptEnhancer{
		{Model: "pe-t2i", Name: "T2I", SystemPrompt: "short one"},
		{Model: "pe-i2i", Name: "I2I", SystemPrompt: trickyPrompt, Vision: true},
	})
	out := b.String()

	// Sorted by id, so a no-change regen produces a byte-identical file.
	if strings.Index(out, `"pe-i2i"`) > strings.Index(out, `"pe-t2i"`) {
		t.Errorf("entries not sorted by id:\n%s", out)
	}

	var parsed struct {
		PromptEnhancers map[string]struct {
			Name         string `yaml:"name"`
			Vision       bool   `yaml:"vision"`
			SystemPrompt string `yaml:"systemPrompt"`
		} `yaml:"promptEnhancers"`
	}
	if err := yaml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("emitted YAML does not parse: %v\n%s", err, out)
	}
	got, ok := parsed.PromptEnhancers["pe-i2i"]
	if !ok {
		t.Fatalf("pe-i2i missing from %#v", parsed.PromptEnhancers)
	}
	// The whole point of marshalling instead of Fprintf: the prompt survives
	// byte for byte, including the quotes, the backslash and the "key: value"
	// shaped line.
	if got.SystemPrompt != trickyPrompt {
		t.Errorf("system prompt mangled:\n got %q\nwant %q", got.SystemPrompt, trickyPrompt)
	}
	if !got.Vision {
		t.Error("vision flag lost")
	}
	if got.Name != "I2I" {
		t.Errorf("name = %q, want I2I", got.Name)
	}
}

func TestEmitPromptEnhancers_Empty(t *testing.T) {
	var b strings.Builder
	emitPromptEnhancers(&b, nil)
	emitPromptEnhancers(&b, []PromptEnhancer{{Model: "   "}})
	if b.String() != "" {
		t.Errorf("expected no block for an empty/blank list, got:\n%s", b.String())
	}
}

func TestWritePromptEnhancer_EmitsIDWithoutRow(t *testing.T) {
	byID := enhancerByID([]PromptEnhancer{{Model: "PE-I2I", SystemPrompt: "x"}})

	var b strings.Builder
	// Case-insensitive hit: the id is filename-derived and gets retyped by hand.
	writePromptEnhancer(&b, "pe-i2i", byID)
	if !strings.Contains(b.String(), `promptEnhancer: "PE-I2I"`) {
		t.Errorf("case-insensitive lookup failed, got %q", b.String())
	}

	// An id with NO settings row is emitted as typed. A row is a place to put a
	// shared system prompt, not a registration: the id can come from free text or
	// from name detection, and the prompt can live on the image model. Dropping it
	// here used to hide the button with no explanation.
	b.Reset()
	writePromptEnhancer(&b, "typed-by-hand", byID)
	if got := b.String(); got != "    promptEnhancer: \"typed-by-hand\"\n" {
		t.Errorf("row-less id: got %q", got)
	}

	// Blank emits nothing, and so does the explicit "none": that one is how a user
	// says no to an auto-detected candidate, so it must not reach the config as an
	// id the client would try to call.
	b.Reset()
	writePromptEnhancer(&b, "", byID)
	writePromptEnhancer(&b, " NONE ", byID)
	if b.String() != "" {
		t.Errorf("blank/none should emit nothing, got %q", b.String())
	}
}

func TestEnhancerByID_LastDuplicateWins(t *testing.T) {
	byID := enhancerByID([]PromptEnhancer{
		{Model: "pe", SystemPrompt: "old"},
		{Model: "PE", SystemPrompt: "new"},
	})
	if len(byID) != 1 {
		t.Fatalf("want 1 entry, got %d", len(byID))
	}
	if byID["pe"].SystemPrompt != "new" {
		t.Errorf("want the later entry to win, got %q", byID["pe"].SystemPrompt)
	}
}

func TestAutogen_Sidecar_PromptEnhancerRoundTrip(t *testing.T) {
	gen := writeGen(t)

	if err := UpsertSidecarPromptEnhancers(gen, []PromptEnhancer{
		{Model: "  pe-i2i  ", Name: "  I2I  ", SystemPrompt: trickyPrompt, Vision: true},
		{Model: "", Name: "no model, dropped"},
		{Model: "PE-I2I", Name: "I2I v2", SystemPrompt: "second"},
		{Model: "pe-t2i", Name: "T2I"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := LoadSidecarPromptEnhancers(gen)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 enhancers after dedupe, got %d: %#v", len(got), got)
	}
	// Dedupe is case-insensitive and last-wins, but keeps the FIRST position, so
	// editing a row in the settings table does not make it jump to the bottom.
	if got[0].Model != "PE-I2I" || got[0].Name != "I2I v2" || got[0].SystemPrompt != "second" {
		t.Errorf("row 0 = %#v, want the later PE-I2I in the first slot", got[0])
	}
	if got[1].Model != "pe-t2i" {
		t.Errorf("row 1 = %#v, want pe-t2i", got[1])
	}

	// Whitespace-only differences in the system prompt must survive: a published
	// PE prompt's indentation and trailing newline are part of the prompt.
	if err := UpsertSidecarPromptEnhancers(gen, []PromptEnhancer{
		{Model: "pe-i2i", SystemPrompt: trickyPrompt},
	}); err != nil {
		t.Fatal(err)
	}
	got, err = LoadSidecarPromptEnhancers(gen)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SystemPrompt != trickyPrompt {
		t.Fatalf("system prompt not preserved verbatim: %#v", got)
	}

	// The sidecar block replaces the generate file's wholesale, so clearing the
	// table must actually clear it rather than leave the old rows in force.
	if err := UpsertSidecarPromptEnhancers(gen, nil); err != nil {
		t.Fatal(err)
	}
	if got, err = LoadSidecarPromptEnhancers(gen); err != nil || len(got) != 0 {
		t.Fatalf("want empty after clear, got %#v (err %v)", got, err)
	}
}

func TestAutogen_Sidecar_PromptEnhancerSurvivesOverrideWrite(t *testing.T) {
	gen := writeGen(t)

	if err := UpsertSidecarPromptEnhancers(gen, []PromptEnhancer{
		{Model: "pe-i2i", SystemPrompt: trickyPrompt},
	}); err != nil {
		t.Fatal(err)
	}
	// An unrelated sidecar write (what a VRAM re-tune does) must not take the
	// user's hand-pasted system prompts with it.
	if _, err := UpsertSidecarOverride(gen, Override{Match: "*qwen_image*", Ctx: 4096}); err != nil {
		t.Fatal(err)
	}

	got, err := LoadSidecarPromptEnhancers(gen)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SystemPrompt != trickyPrompt {
		t.Fatalf("enhancers lost across an override write: %#v", got)
	}
}

// A model may name a different enhancer per direction (Qwen ships PE-T2I and
// PE-I2I, which are not interchangeable), either alone, or neither.
func TestWritePromptEnhancer_BothDirections(t *testing.T) {
	byID := enhancerByID([]PromptEnhancer{
		{Model: "pe-t2i", SystemPrompt: "compose"},
		{Model: "pe-i2i", SystemPrompt: "edit", Vision: true},
	})

	var b strings.Builder
	writePromptEnhancer(&b, "pe-t2i", byID)
	writePromptEnhancerEdit(&b, "pe-i2i", byID)
	got := b.String()
	if want := "    promptEnhancer: \"pe-t2i\"\n    promptEnhancerEdit: \"pe-i2i\"\n"; got != want {
		t.Errorf("pair:\n got %q\nwant %q", got, want)
	}

	// The keys must not collide: promptEnhancerEdit has to be its own key, not a
	// second promptEnhancer line that the YAML loader would silently collapse.
	var parsed map[string]string
	if err := yaml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("pair does not parse: %v", err)
	}
	if len(parsed) != 2 {
		t.Errorf("want 2 distinct keys, got %#v", parsed)
	}

	// Either half alone is valid: one enhancer covers both directions.
	b.Reset()
	writePromptEnhancerEdit(&b, "pe-i2i", byID)
	if got := b.String(); got != "    promptEnhancerEdit: \"pe-i2i\"\n" {
		t.Errorf("edit alone: got %q", got)
	}

	// And the edit half follows the same row-less rule as the text one.
	b.Reset()
	writePromptEnhancerEdit(&b, "pe-gone", byID)
	if got := b.String(); got != "    promptEnhancerEdit: \"pe-gone\"\n" {
		t.Errorf("row-less edit id: got %q", got)
	}
	b.Reset()
	writePromptEnhancerEdit(&b, "", byID)
	writePromptEnhancerEdit(&b, "none", byID)
	if b.String() != "" {
		t.Errorf("blank/none edit id should emit nothing, got %q", b.String())
	}
}

func TestOvPromptEnhancer_NilSafe(t *testing.T) {
	if ovPromptEnhancer(nil) != "" || ovPromptEnhancerEdit(nil) != "" {
		t.Error("a nil override is the ordinary no-rule-matched case, not a panic")
	}
	ov := &Override{PromptEnhancer: "t", PromptEnhancerEdit: "i"}
	if ovPromptEnhancer(ov) != "t" || ovPromptEnhancerEdit(ov) != "i" {
		t.Error("the two directions must not read the same field")
	}
}
