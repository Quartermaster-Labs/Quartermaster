package autogen

import (
	"os"
	"path/filepath"
	"reflect"
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
		"ace_step":      "gen", // music
		"htdemucs":      "sep", // separation
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
		t.Error("audioCppKnownFamily(ace_step) = false, want true")
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

// A family we know about but do not serve (voice conversion, codecs, alignment)
// must not reach the config at all: no other engine here could serve it either,
// and an entry in the Speech tab would fail every request.
func TestEmitAudioCppModel_UnservedFamiliesAreSkipped(t *testing.T) {
	cases := []struct {
		name   string
		family string
		want   string
	}{
		{"voice conversion", "seed_vc", "voice-conversion"},
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

// A pin at another engine's row must be a no-op, not a broken launch: audio.cpp
// weights are its own format, so there is nothing for TTS.cpp to run here, and
// falling back to the bare "audiocpp_server" name (not on PATH) while warning
// that no backend is installed would be the worst of both.
func TestAudioCppBackend_IgnoresCrossEnginePin(t *testing.T) {
	s := audioCppSettings("vulkan", BackendEntry{
		ID: "ttscpp-row", Kind: "ttscpp", Name: "tts.cpp", Path: "/backends/tts-server",
	})
	be := audioCppBackend(s, &Override{Backend: "ttscpp-row"}, "tts")
	if be.Exe != "/backends/audiocpp_server" {
		t.Errorf("exe = %q, want the installed audio.cpp build", be.Exe)
	}

	// A pin at a specific audio.cpp BUILD still wins, which is what pinning is for.
	s.Backends = append(s.Backends, BackendEntry{
		ID: "build-audiocpp-cuda", Kind: "audiocpp", Path: "/backends/cuda/audiocpp_server",
		Managed: true, Build: true, Variant: "cuda",
	})
	be = audioCppBackend(s, &Override{Backend: "build-audiocpp-cuda"}, "tts")
	if be.Exe != "/backends/cuda/audiocpp_server" {
		t.Errorf("exe = %q, want the pinned cuda build", be.Exe)
	}
	if got := audioCppFlavour(s, be.ID); got != "cuda" {
		t.Errorf("flavour = %q, want cuda from the pinned build row", got)
	}
}

func TestEmitAudioCppModel_MusicGen(t *testing.T) {
	s := audioCppSettings("vulkan")
	row := audioCppRow("/models/music/acestep-v15-turbo-bf16.gguf")
	meta := Metadata{Architecture: "audiocpp", AudioFamily: "ace_step"}

	var b strings.Builder
	var emitted []string
	emitAudioCppModel(&b, s, row, &Override{}, "acestep-v15-turbo-bf16", meta, &emitted)
	out := b.String()

	for _, want := range []string{`task: "gen"`, `family: "ace_step"`, "music: true", "musicEdit: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted YAML missing %q:\n%s", want, out)
		}
	}
	// A music generator has no speech surface: text->audio would advertise
	// audio_speech, and voice cloning does not apply.
	for _, bad := range []string{"in: [text]", "out: [audio]", "voiceClone"} {
		if strings.Contains(out, bad) {
			t.Errorf("music entry advertises %q:\n%s", bad, out)
		}
	}
	if len(emitted) != 1 {
		t.Fatalf("emitted = %v, want 1 entry", emitted)
	}
}

func TestEmitAudioCppModel_Separation(t *testing.T) {
	s := audioCppSettings("vulkan")
	row := audioCppRow("/models/music/htdemucs-f16.gguf")
	meta := Metadata{Architecture: "audiocpp", AudioFamily: "htdemucs"}

	var b strings.Builder
	var emitted []string
	emitAudioCppModel(&b, s, row, &Override{}, "htdemucs-f16", meta, &emitted)
	out := b.String()

	if !strings.Contains(out, `task: "sep"`) || !strings.Contains(out, "separation: true") {
		t.Errorf("separation entry missing task/capability:\n%s", out)
	}
	if strings.Contains(out, "music: true") {
		t.Errorf("separation entry advertises music:\n%s", out)
	}
	if strings.Contains(out, "in: [") {
		t.Errorf("separation entry declares speech in/out:\n%s", out)
	}
	if len(emitted) != 1 {
		t.Fatalf("emitted = %v, want 1 entry", emitted)
	}
}

// writeMiniMaxPackage lays out a MiniMax Music 3 package directory: the shared
// vocoder carries the family id in its header, the other components carry none.
// Each file in files is written as a non-empty placeholder, except the shared
// vocoder and condition encoder, which are real GGUF headers.
func writeMiniMaxPackage(t *testing.T, dir string, files ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	family := [][2]any{
		{"general.architecture", "audiocpp"},
		{"audiocpp.model_spec.family", "minimax_music3"},
	}
	for _, f := range files {
		p := filepath.Join(dir, f)
		if f == "vocoder.gguf" || f == "condition_encoder.gguf" {
			writeGgufKVs(t, p, family)
			continue
		}
		if err := os.WriteFile(p, []byte("placeholder-weights"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// miniMaxRow is a GgufRow for a file inside a package directory, with FileName
// set to the base name (audioCppRow splits on "/" only, which a Windows
// filepath.Join path does not contain).
func miniMaxRow(path string) GgufRow {
	row := audioCppRow(filepath.ToSlash(path))
	row.FileName = filepath.Base(path)
	return row
}

func TestEmitAudioCppModel_MiniMaxDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "MiniMax-Music-3-GGUF")
	writeMiniMaxPackage(t, dir,
		"vocoder.gguf", "condition_encoder.gguf",
		"language_model_q8_0.gguf", "transformer_q8_0.gguf", "rvq_depth_decoder_q8_0.gguf",
		"language_model_q4_0.gguf", // no transformer_q4_0: q4_0 must not be emitted
	)
	s := audioCppSettings("vulkan")
	row := miniMaxRow(filepath.Join(dir, "vocoder.gguf"))
	meta := Metadata{Architecture: "audiocpp", AudioFamily: "minimax_music3"}

	var b strings.Builder
	var emitted []string
	emitAudioCppModel(&b, s, row, &Override{}, "vocoder", meta, &emitted)
	out := b.String()

	for _, want := range []string{
		`"minimax-music-3-q8_0":`,
		`language_model_gguf: "language_model_q8_0.gguf"`,
		`flow_transformer_gguf: "transformer_q8_0.gguf"`,
		`rvq_depth_decoder_gguf: "rvq_depth_decoder_q8_0.gguf"`,
		"sessionOptions:",
		"music: true",
		`path: "` + filepath.ToSlash(dir) + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted YAML missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "minimax-music-3-q4_0") {
		t.Errorf("emitted q4_0 with no transformer_q4_0 on disk:\n%s", out)
	}
	// MiniMax has no edit routes.
	if strings.Contains(out, "musicEdit") {
		t.Errorf("MiniMax entry advertises musicEdit:\n%s", out)
	}
	if want := []string{"minimax-music-3-q8_0"}; !reflect.DeepEqual(emitted, want) {
		t.Fatalf("emitted = %v, want %v", emitted, want)
	}
}

func TestEmitAudioCppModel_MiniMaxComponents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "MiniMax-Music-3-GGUF")
	writeMiniMaxPackage(t, dir,
		"vocoder.gguf", "condition_encoder.gguf",
		"language_model_q8_0.gguf", "transformer_q8_0.gguf", "rvq_depth_decoder_q8_0.gguf",
	)
	s := audioCppSettings("vulkan")

	// A family-less per-quant component is explained by its package, not skipped.
	var b strings.Builder
	var emitted []string
	emitAudioCppModel(&b, s, miniMaxRow(filepath.Join(dir, "transformer_q8_0.gguf")), &Override{},
		"transformer_q8_0", Metadata{Architecture: "audiocpp"}, &emitted)
	out := b.String()
	if !strings.Contains(out, "component of the minimax_music3 package") {
		t.Errorf("no package note for a family-less component:\n%s", out)
	}
	if strings.Contains(out, "SKIPPED") {
		t.Errorf("family-less component of a known package was SKIPPED:\n%s", out)
	}
	if len(emitted) != 0 {
		t.Errorf("emitted = %v for a component, want none", emitted)
	}

	// The shared condition encoder carries the family but is not the anchor.
	b.Reset()
	emitAudioCppModel(&b, s, miniMaxRow(filepath.Join(dir, "condition_encoder.gguf")), &Override{},
		"condition_encoder", Metadata{Architecture: "audiocpp", AudioFamily: "minimax_music3"}, &emitted)
	out = b.String()
	if !strings.Contains(out, "shared component") {
		t.Errorf("no shared-component note for condition_encoder.gguf:\n%s", out)
	}
	if len(emitted) != 0 {
		t.Errorf("emitted = %v for the shared encoder, want none", emitted)
	}
}

func TestEmitAudioCppModel_MiniMaxIncomplete(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "MiniMax-Music-3-GGUF")
	writeMiniMaxPackage(t, dir, "vocoder.gguf", "language_model_q8_0.gguf")
	s := audioCppSettings("vulkan")

	var b strings.Builder
	var emitted []string
	emitAudioCppModel(&b, s, miniMaxRow(filepath.Join(dir, "vocoder.gguf")), &Override{},
		"vocoder", Metadata{Architecture: "audiocpp", AudioFamily: "minimax_music3"}, &emitted)
	out := b.String()
	if !strings.Contains(out, "SKIPPED minimax_music3: no complete quant") {
		t.Errorf("no skip note for a package with no complete quant:\n%s", out)
	}
	if len(emitted) != 0 {
		t.Errorf("emitted = %v for an incomplete package, want none", emitted)
	}
}
