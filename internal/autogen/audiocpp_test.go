package autogen

import (
	"strings"
	"testing"
)

// audioCppSettings is a fleet with one installed audio.cpp build of the given
// variant, plus whatever other backend rows the case needs.
func audioCppSettings(variant string, extra ...BackendEntry) Settings {
	rows := []BackendEntry{{
		ID: "managed-audiocpp-server", Kind: "audiocpp", Name: "audio.cpp",
		Path: "/backends/audiocpp_server", Managed: true, Variant: variant, Default: true,
	}}
	return Settings{Backends: append(rows, extra...), TtlSec: 600}
}

func audioCppRow(path string) GgufRow {
	return GgufRow{FullPath: path, FileName: path[strings.LastIndexByte(path, '/')+1:], SizeGB: 0.2}
}

func TestIsAudioCppModel(t *testing.T) {
	cases := []struct {
		name string
		meta Metadata
		want bool
	}{
		{"arch", Metadata{Architecture: "audiocpp"}, true},
		{"family only", Metadata{AudioFamily: "moss_tts_nano"}, true},
		{"chat llm", Metadata{Architecture: "qwen35"}, false},
		{"qwentts talker", Metadata{Architecture: "qwen3-tts"}, false},
		{"kokoro", Metadata{Architecture: "kokoro"}, false},
	}
	for _, c := range cases {
		if got := IsAudioCppModel(c.meta); got != c.want {
			t.Errorf("%s: IsAudioCppModel = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAudioCppTask(t *testing.T) {
	cases := map[string]string{
		"moss_tts_nano": "tts",
		"qwen3_asr":     "asr",
		"nemotron_asr":  "asr",
		"echo_tts":      "tts", // clone-only is still speech synthesis
		"firered_audio": "tts", // dual-task: one config entry, tts wins
		"ace_step":      "",    // music: known, not served here
		"htdemucs":      "",    // separation
		"seed_vc":       "",    // voice conversion
		"not_a_family":  "",
	}
	for family, want := range cases {
		if got := audioCppTask(family); got != want {
			t.Errorf("audioCppTask(%q) = %q, want %q", family, got, want)
		}
	}
	if audioCppKnownFamily("not_a_family") {
		t.Error("audioCppKnownFamily(not_a_family) = true")
	}
	if !audioCppKnownFamily("ace_step") {
		t.Error("audioCppKnownFamily(ace_step) = false, want true (known, just unserved)")
	}
}

func TestEmitAudioCppModel_TTS(t *testing.T) {
	s := audioCppSettings("vulkan")
	row := audioCppRow("/models/speech/moss-tts-nano-100m-q8_0.gguf")
	meta := Metadata{Architecture: "audiocpp", AudioFamily: "moss_tts_nano"}

	var b strings.Builder
	var emitted []string
	emitAudioCppModel(&b, s, row, &Override{}, "moss-tts-nano-100m-q8_0", meta, &emitted)
	out := b.String()

	for _, want := range []string{
		"/backends/audiocpp_server",
		"--port ${PORT}",
		"--backend vulkan",
		"--max-loaded-models 1",
		"--voice-dir /models/speech/voices",
		"    audiocpp:",
		`      family: "moss_tts_nano"`,
		`      path: "/models/speech/moss-tts-nano-100m-q8_0.gguf"`,
		`      task: "tts"`,
		"concurrencyLimit: 1",
		"checkEndpoint: /health",
		"in: [text]",
		"out: [audio]",
		"voiceClone: true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted YAML missing %q:\n%s", want, out)
		}
	}
	// There is no --model flag on audiocpp_server; naming the gguf on argv would
	// be rejected outright.
	if strings.Contains(out, "--model ") {
		t.Errorf("emitted a --model flag, which audiocpp_server does not accept:\n%s", out)
	}
	if len(emitted) != 1 {
		t.Fatalf("emitted = %v, want 1 entry", emitted)
	}
}

func TestEmitAudioCppModel_ASR(t *testing.T) {
	s := audioCppSettings("cuda")
	row := audioCppRow("/models/speech/qwen3-asr-1.7b-q8_0.gguf")
	meta := Metadata{Architecture: "audiocpp", AudioFamily: "qwen3_asr"}

	var b strings.Builder
	var emitted []string
	emitAudioCppModel(&b, s, row, &Override{}, "qwen3-asr-1.7b-q8_0", meta, &emitted)
	out := b.String()

	for _, want := range []string{"--backend cuda", `task: "asr"`, "in: [audio]", "out: [text]"} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted YAML missing %q:\n%s", want, out)
		}
	}
	// An ASR model has no speech surface to clone a voice for.
	if strings.Contains(out, "voiceClone") {
		t.Errorf("ASR entry advertises voiceClone:\n%s", out)
	}
	if len(emitted) != 1 {
		t.Fatalf("emitted = %v, want 1 entry", emitted)
	}
}

// A family we know about but do not serve (music, separation, voice conversion)
// must not reach the config at all: no other engine here could serve it either,
// and an entry in the Speech tab would fail every request.
func TestEmitAudioCppModel_UnservedFamiliesAreSkipped(t *testing.T) {
	cases := []struct {
		name   string
		family string
		want   string
	}{
		{"music", "ace_step", "music / separation / voice-conversion"},
		{"unknown", "brand_new_tts", "unknown audio.cpp family"},
		{"no family id", "", "no audiocpp.model_spec.family"},
	}
	for _, c := range cases {
		s := audioCppSettings("vulkan")
		row := audioCppRow("/models/speech/x-q8_0.gguf")
		var b strings.Builder
		var emitted []string
		emitAudioCppModel(&b, s, row, &Override{}, "x-q8_0", Metadata{Architecture: "audiocpp", AudioFamily: c.family}, &emitted)
		out := b.String()
		if len(emitted) != 0 {
			t.Errorf("%s: emitted = %v, want none", c.name, emitted)
		}
		if !strings.Contains(out, "# SKIPPED") || !strings.Contains(out, c.want) {
			t.Errorf("%s: want a SKIPPED comment mentioning %q, got:\n%s", c.name, c.want, out)
		}
		if strings.Contains(out, "cmd:") {
			t.Errorf("%s: emitted a launch command for an unserved family:\n%s", c.name, out)
		}
	}
}

// A hand-entered row records no build variant, so no --backend can be derived.
// audio.cpp's own default is CUDA, which is the wrong guess on most machines, so
// the config has to say so rather than silently emit nothing.
func TestEmitAudioCppModel_NoVariantWarns(t *testing.T) {
	s := Settings{TtlSec: 600, Backends: []BackendEntry{{
		ID: "hand", Kind: "audiocpp", Name: "audio.cpp", Path: "/opt/audiocpp_server", Default: true,
	}}}
	var b strings.Builder
	var emitted []string
	emitAudioCppModel(&b, s, audioCppRow("/models/speech/supertonic-3-q8_0.gguf"), &Override{},
		"supertonic-3-q8_0", Metadata{Architecture: "audiocpp", AudioFamily: "supertonic"}, &emitted)
	out := b.String()
	if strings.Contains(out, "\n      --backend ") {
		t.Errorf("guessed a --backend for a row with no variant:\n%s", out)
	}
	if !strings.Contains(out, "# NOTE:") {
		t.Errorf("no note about the cuda default:\n%s", out)
	}
}

// With no audio.cpp build registered the entry is still emitted (so the model is
// visible and starts working the moment one is installed), but it says so.
func TestEmitAudioCppModel_NoBackendRegistered(t *testing.T) {
	var b strings.Builder
	var emitted []string
	emitAudioCppModel(&b, Settings{TtlSec: 600}, audioCppRow("/models/speech/vibevoice-1.5b-q8_0.gguf"),
		&Override{}, "vibevoice-1.5b-q8_0", Metadata{Architecture: "audiocpp", AudioFamily: "vibevoice"}, &emitted)
	out := b.String()
	if !strings.Contains(out, "# WARNING: no audio.cpp backend registered") {
		t.Errorf("no warning about the missing backend:\n%s", out)
	}
	if len(emitted) != 1 {
		t.Fatalf("emitted = %v, want the model to still be listed", emitted)
	}
}

// audio.cpp serves the tts class, so it can be the class default -- but it reads
// neither legacy engine's weights. A Kokoro gguf on a box where audio.cpp owns
// the tts default must still resolve to TTS.cpp, not to an audiocpp_server
// command built around a --model-path it does not accept.
func TestTTSBackend_IgnoresAudioCppRow(t *testing.T) {
	s := Settings{TtsServerExe: "tts-server", Backends: []BackendEntry{{
		ID: "managed-audiocpp-server", Kind: "audiocpp", Path: "/backends/audiocpp_server", Default: true,
	}}}
	row := GgufRow{FullPath: "/models/tts/Kokoro_espeak_f16.gguf", FileName: "Kokoro_espeak_f16.gguf"}
	kind, exe := ttsBackend(s, row, &Override{}, Metadata{Architecture: "kokoro"})
	if kind != ttsKindTTSCpp {
		t.Errorf("kind = %q, want %q", kind, ttsKindTTSCpp)
	}
	if strings.Contains(exe, "audiocpp") {
		t.Errorf("exe = %q, want the legacy tts exe", exe)
	}
}

// audio.cpp is the first kind serving two classes at once, which is what the
// registry's "an install never steals a populated class" rule has to reason
// about: arriving on a box that already has an ASR engine must not hand it the
// tts auto-pick either, because the two travel together in one row.
func TestAudioCppIsMultiClass(t *testing.T) {
	for _, class := range []string{"tts", "asr"} {
		if !KindServesClass("audiocpp", class) {
			t.Errorf("KindServesClass(audiocpp, %q) = false", class)
		}
	}
	if got := KindClasses("audiocpp"); len(got) != 2 {
		t.Errorf("KindClasses(audiocpp) = %v, want two classes", got)
	}
	if !ClassTaken([]BackendEntry{{ID: "parakeet", Kind: "asr"}}, "audiocpp") {
		t.Error("ClassTaken = false with an asr row present, want true")
	}
	if ClassTaken([]BackendEntry{{ID: "llama", Kind: "llama"}}, "audiocpp") {
		t.Error("ClassTaken = true against an llm-only registry, want false")
	}
}

// The same rule on the other class this kind serves: a Parakeet gguf on a box
// where audio.cpp owns the asr default must still launch parakeet-server, not
// audiocpp_server wearing parakeet's argv.
func TestASRExe_IgnoresAudioCppRow(t *testing.T) {
	s := Settings{AsrServerExe: "parakeet-server", Backends: []BackendEntry{{
		ID: "managed-audiocpp-server", Kind: "audiocpp", Path: "/backends/audiocpp_server", Default: true,
	}}}
	if exe := asrExe(s, &Override{}); exe != "parakeet-server" {
		t.Errorf("exe = %q, want the legacy asr exe", exe)
	}
	// A real ASR backend row still wins, as it does for every other class.
	s.Backends = append(s.Backends, BackendEntry{ID: "pk", Kind: "parakeet", Path: "/backends/parakeet", Default: true})
	if exe := asrExe(s, &Override{}); exe != "/backends/parakeet" {
		t.Errorf("exe = %q, want the registered parakeet row", exe)
	}
}
