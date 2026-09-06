package autogen

import "strings"

// NoneSentinel is the value a variant writes on an inheriting free-form string
// knob to mean "explicitly nothing" rather than "inherit the model-wide value".
//
// Every free-form string on VariantSpec uses empty => inherit, which leaves a
// variant no way to express "the model pins a chat template, this profile wants
// the gguf's baked-in one". The field carries three states through two, and the
// third one was simply unreachable. Rather than widen each field to *string
// (the PreserveThinking treatment - correct, but it moves the yaml round-trip,
// both DTO directions and every `||` coalesce in the editor), the off state gets
// a name, matching the vocabulary Mmproj already uses ("gpu"/"ram"/"none").
//
// Only free-form and path knobs take the sentinel. The enum knobs already spell
// out their own off state (flashAttn "off", mmap "off", mmproj "none",
// ropeScaling "none", contextShift "off"), and giving those a second spelling
// would be ambiguous, not consistent.
//
// A literal file named "none" is thus unreachable as a bare value; write it as
// "./none" or an absolute path.
const NoneSentinel = "none"

// isNone reports whether s is the "explicitly nothing" sentinel. Case- and
// space-insensitive: it is typed by hand into a text box.
func isNone(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), NoneSentinel)
}

// inheritStr resolves one inheriting free-form string: empty inherits the
// model-wide value, the sentinel forces the knob off, anything else pins.
func inheritStr(variant, model string) string {
	switch v := strings.TrimSpace(variant); {
	case v == "":
		return model
	case isNone(v):
		return ""
	default:
		return variant
	}
}

// clearNone maps a model-wide sentinel to empty. At model level empty ALREADY
// means "no flag", so "none" there is redundant - but a user who learned the
// word on a variant will write it on the model too, and left alone it would be
// emitted as a literal path.
func clearNone(s string) string {
	if isNone(s) {
		return ""
	}
	return s
}

// mergeInheritStrings layers a variant's free-form string knobs over the
// effective override, honouring the sentinel. It is the single place that
// decides which knobs are sentinel-aware; the enum knobs stay in the plain
// non-empty-wins chain in buildProfiles.
func mergeInheritStrings(eff *Override, v *VariantSpec) {
	eff.ChatTemplateFile = inheritStr(v.ChatTemplateFile, eff.ChatTemplateFile)
	eff.ExtraArgs = inheritStr(v.ExtraArgs, eff.ExtraArgs)
	eff.TensorSplit = inheritStr(v.TensorSplit, eff.TensorSplit)
	eff.OverrideTensor = inheritStr(v.OverrideTensor, eff.OverrideTensor)
	eff.KvKDraft = inheritStr(v.KvKDraft, eff.KvKDraft)
	eff.KvVDraft = inheritStr(v.KvVDraft, eff.KvVDraft)
}

// mergeInheritImageStrings is mergeInheritStrings for the sd-server component
// paths, which merge in mergeImageVariant rather than the llama chain. Dropping
// an inherited encoder ("this preset runs without the external T5") was
// unreachable for the same reason.
func mergeInheritImageStrings(o *Override, v *VariantSpec) {
	o.VaePath = inheritStr(v.VaePath, o.VaePath)
	o.ClipLPath = inheritStr(v.ClipLPath, o.ClipLPath)
	o.ClipGPath = inheritStr(v.ClipGPath, o.ClipGPath)
	o.T5Path = inheritStr(v.T5Path, o.T5Path)
	o.TextEncoderPath = inheritStr(v.TextEncoderPath, o.TextEncoderPath)
	o.LlmVisionPath = inheritStr(v.LlmVisionPath, o.LlmVisionPath)
	o.LoraDir = inheritStr(v.LoraDir, o.LoraDir)
}

// NormalizeNone clears a sentinel written at MODEL level on every knob
// mergeInheritStrings / mergeInheritImageStrings cover. At model level empty
// already means "no flag", so the two are equivalent and resolving early keeps
// every consumer honest.
//
// It must run on every door an Override comes in through, not just the config
// file: the launch-command preview builds one straight from the editor's DTO
// and renders it without ever touching LoadGenerateFile, so a sentinel left
// unresolved there renders as --chat-template-file "none" and the box silently
// disagrees with what a save would produce.
//
// Never call it on a VariantSpec. There the sentinel is the whole point.
func NormalizeNone(o *Override) {
	o.ChatTemplateFile = clearNone(o.ChatTemplateFile)
	o.ExtraArgs = clearNone(o.ExtraArgs)
	o.TensorSplit = clearNone(o.TensorSplit)
	o.OverrideTensor = clearNone(o.OverrideTensor)
	o.KvKDraft = clearNone(o.KvKDraft)
	o.KvVDraft = clearNone(o.KvVDraft)
	o.VaePath = clearNone(o.VaePath)
	o.ClipLPath = clearNone(o.ClipLPath)
	o.ClipGPath = clearNone(o.ClipGPath)
	o.T5Path = clearNone(o.T5Path)
	o.TextEncoderPath = clearNone(o.TextEncoderPath)
	o.LlmVisionPath = clearNone(o.LlmVisionPath)
	o.LoraDir = clearNone(o.LoraDir)
}
