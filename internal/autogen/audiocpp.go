package autogen

// audio.cpp (0xShug0/audio.cpp) is one ggml runtime for ~72 speech and audio
// model families: text-to-speech, voice cloning, speech recognition, and (not
// wired here yet) music, separation and voice conversion. It speaks the same
// OpenAI surface the existing engines do - /v1/audio/speech,
// /v1/audio/transcriptions, /v1/audio/voices, /health - so it lands in the
// existing tts and asr classes rather than needing new ones, and it is the first
// backend kind to serve TWO classes at once (see kindClasses in vllm.go).
//
// Two things make its emitter unlike every other class here:
//
//  1. audiocpp_server has NO --model flag. A model can only be named in a JSON
//     config file passed as --config. Every other knob is an argv flag, and argv
//     wins over the file. Rather than litter the disk with one checked-in JSON
//     per model, the emitted YAML carries a typed `audiocpp:` block and the
//     spawn hook materializes a single-model config from it at launch (see
//     internal/server/audiocppconfig.go), deleting it on stop.
//
//  2. It ships a model manager of its own - lazy loading, an LRU residency cap,
//     idle unloads, a free-memory admission guard. That is a SECOND scheduler
//     sitting behind ours, making residency decisions the router cannot see. It
//     is contained rather than used: one model per generated config (structural,
//     so there is nothing for an LRU to choose between), --max-loaded-models 1,
//     idle_unload_ms and min_free_memory_mb left at 0, lazy_load left at its
//     false default so the listen socket opening really does mean the weights
//     are resident, and concurrencyLimit 1 in the YAML so requests queue in our
//     scheduler instead of on audio.cpp's busy mutex. The /v1/models/load,
//     /v1/models/unload and /v1/tasks/unload_models routes are deliberately not
//     proxied anywhere.

import (
	"fmt"
	"path/filepath"
	"strings"
)

// audioCppKind is the registry kind for an audio.cpp build. The aliases are the
// ones kindClasses accepts, so anything that can reach the registry can be
// tested with isAudioCppKind.
const audioCppKind = "audiocpp"

// isAudioCppKind reports whether a registry kind names an audio.cpp build. The
// other speech emitters use it as a negative guard: audio.cpp serves their class
// but reads neither of their weight formats, so a resolved audio.cpp row must
// read to them as "nothing matched" rather than as a usable engine.
func isAudioCppKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "audiocpp", "audio.cpp", "audiocpp-server":
		return true
	}
	return false
}

// The classes an audio.cpp family serves, as a set: a handful of families (see
// firered_audio, vevo2) do several jobs, and the emitted capabilities block has
// to describe the one task the generated config actually configures.
const (
	audioClsTTS = 1 << iota
	audioClsASR
)

// audioCppFamilies maps audio.cpp's family id - the value its converter stamps
// into the gguf as audiocpp.model_spec.family - onto the classes we serve it
// under. Derived from upstream's model_specs/*.json, one entry per spec file,
// with that spec's own task list and support status in the trailing comment.
//
// A family at 0 is KNOWN and deliberately not emitted: music, stem separation,
// voice conversion, codecs, alignment, diarization and speech-to-speech have no
// class, route or UI here yet. Emitting them as speech models would put a music
// generator in the Speech tab, where every request would fail.
//
// A family MISSING from this table is one upstream added after the table was
// written; emitAudioCppModel says so in the YAML rather than guessing a class
// from the name.
//
// "clone" counts as tts: a clone-only family (echo_tts, confucius4_tts) is a
// speech synthesizer whose voice must come from a reference clip rather than a
// preset, which is a request-shape difference, not a different class.
var audioCppFamilies = map[string]int{
	"ace_step":             0,                         // music,edit (supported)
	"audio8_asr":           audioClsASR,               // asr (community)
	"audio8_tts":           audioClsTTS,               // tts,clone (community)
	"audiosr":              0,                         // s2s (experimental)
	"breeze_tts":           audioClsTTS,               // tts,clone,design (supported)
	"bs_roformer":          0,                         // sep (supported)
	"chatterbox":           audioClsTTS,               // tts,clone,vc (supported)
	"chatterbox_turbo":     audioClsTTS,               // tts (testing)
	"citrinet_asr":         audioClsASR,               // asr (supported)
	"confucius4_tts":       audioClsTTS,               // clone (experimental)
	"controlfoley":         0,                         // sfx (experimental)
	"cosyvoice3":           audioClsTTS,               // tts,clone (supported)
	"dots_tts":             audioClsTTS,               // tts,clone (supported)
	"dramabox":             audioClsTTS,               // tts,clone (experimental)
	"echo_tts":             audioClsTTS,               // clone (experimental)
	"f5_tts":               audioClsTTS,               // tts,clone (community)
	"firered_audio":        audioClsTTS | audioClsASR, // asr,tts,clone,design,edit (experimental)
	"fireredtts3":          audioClsTTS,               // tts,clone,design (experimental)
	"fish_audio":           audioClsTTS,               // tts,clone (supported)
	"fun_asr_nano":         audioClsASR,               // asr (wip)
	"glm_tts":              audioClsTTS,               // tts,clone (community)
	"granite5asr":          audioClsASR,               // asr (supported)
	"heartmula":            0,                         // music (supported)
	"higgs_audio_stt":      audioClsASR,               // asr (supported)
	"higgs_audio_tts":      audioClsTTS,               // tts,clone (supported)
	"htdemucs":             0,                         // sep (supported)
	"hviske_asr":           audioClsASR,               // asr (supported)
	"index_tts2":           audioClsTTS,               // tts,clone (supported)
	"inflect_v2":           audioClsTTS,               // tts (community)
	"irodori_tts":          audioClsTTS,               // tts,clone,design (supported)
	"kroko_asr":            audioClsASR,               // asr (community)
	"magpie_tts":           audioClsTTS,               // tts (supported)
	"meanvc2":              0,                         // vc (supported)
	"mel_band_roformer":    0,                         // sep (supported)
	"midashenglm_gen":      0,                         // music (experimental)
	"minimax_h3":           0,                         // tts,music,sfx (experimental)
	"minimax_music3":       0,                         // music (experimental)
	"miocodec":             0,                         // codec,vc,s2s (supported)
	"miotts":               audioClsTTS,               // tts,clone (supported)
	"mira_tts":             audioClsTTS,               // tts,clone (experimental)
	"mms_forced_aligner":   0,                         // align (community)
	"moss_tts_local":       audioClsTTS,               // tts,clone (supported)
	"moss_tts_nano":        audioClsTTS,               // tts,clone (supported)
	"moss_voicegen":        0,                         // vdes (community)
	"muscriptor":           0,                         // midi (supported)
	"nemotron_asr":         audioClsASR,               // asr (supported)
	"neutts":               audioClsTTS,               // tts (supported)
	"omnivoice":            audioClsTTS,               // tts,clone,design (supported)
	"outetts":              audioClsTTS,               // tts,clone (community)
	"parakeet_tdt":         audioClsASR,               // asr (community)
	"personaplex":          0,                         // s2s (supported)
	"pocket_tts":           audioClsTTS,               // tts,clone (supported)
	"qwen3_asr":            audioClsASR,               // asr (supported)
	"qwen3_forced_aligner": 0,                         // align (supported)
	"qwen3_tts":            audioClsTTS,               // tts,clone,design (supported)
	"rvc":                  0,                         // vc (experimental)
	"sanotts":              audioClsTTS,               // tts (community)
	"seed_vc":              0,                         // vc,svc (supported)
	"sense_asr":            audioClsASR,               // asr (community)
	"soprano_tts":          audioClsTTS,               // tts (community)
	"sopro_tts":            audioClsTTS,               // tts,clone (community)
	"sortformer_diar":      0,                         // diar (supported)
	"stable_audio":         0,                         // music,sfx,edit (supported)
	"supertonic":           audioClsTTS,               // tts (supported)
	"vevo2":                audioClsTTS,               // tts,music,vc,edit,svc,s2s (supported)
	"vibeasr":              audioClsASR,               // asr (community)
	"vibevoice":            audioClsTTS,               // tts (supported)
	"vibevoice_asr":        audioClsASR,               // asr (supported)
	"vietneu_tts":          audioClsTTS,               // tts,clone (community)
	"voxcpm1":              audioClsTTS,               // tts,clone (supported)
	"voxcpm2":              audioClsTTS,               // tts,clone,design (supported)
	"voxtral_realtime":     audioClsASR,               // asr (supported)
}

// audioCppClones lists the families that can register a NEW voice from a
// reference clip, which is what gates the playground's cloning UI. Same source
// as audioCppFamilies: the spec's task list carries "clone".
var audioCppClones = map[string]bool{
	"audio8_tts": true, "breeze_tts": true, "chatterbox": true,
	"confucius4_tts": true, "cosyvoice3": true, "dots_tts": true,
	"dramabox": true, "echo_tts": true, "f5_tts": true, "firered_audio": true,
	"fireredtts3": true, "fish_audio": true, "glm_tts": true,
	"higgs_audio_tts": true, "index_tts2": true, "irodori_tts": true,
	"miotts": true, "mira_tts": true, "moss_tts_local": true,
	"moss_tts_nano": true, "omnivoice": true, "outetts": true,
	"pocket_tts": true, "qwen3_tts": true, "sopro_tts": true,
	"vietneu_tts": true, "voxcpm1": true, "voxcpm2": true,
}

// audioCppOverheadGB pads a family's weights into a VRAM admission estimate: the
// session buffers, the vocoder or audio tokenizer that loads alongside the model
// and the runtime's own context. A flat pad, for the same reason qwentts gets
// one - these are sub-GB models with no KV window to size and no offload to plan.
const audioCppOverheadGB = 0.4

// IsAudioCppModel reports whether a GGUF is an audio.cpp conversion. Unlike
// every other speech detector here there is no filename heuristic and no arch
// guessing: audio.cpp's converter writes general.architecture=audiocpp and its
// own family id into the header, so the file states outright both that it is
// ours and which family it belongs to.
func IsAudioCppModel(meta Metadata) bool {
	return strings.EqualFold(strings.TrimSpace(meta.Architecture), audioCppKind) ||
		strings.TrimSpace(meta.AudioFamily) != ""
}

// audioCppTask is the "task" the generated config declares for a family, and
// with it the capabilities the model is advertised under. ONE task, even for a
// dual-task family: the config entry names a single task, so advertising
// transcription on a process configured for speech would route requests at a
// route it was not set up to answer. TTS wins the tie, which is what the
// dual-task families are primarily published as. Returns "" for a family with no
// class here (music et al) and for one the table does not know.
func audioCppTask(family string) string {
	switch cls := audioCppFamilies[strings.ToLower(strings.TrimSpace(family))]; {
	case cls&audioClsTTS != 0:
		return "tts"
	case cls&audioClsASR != 0:
		return "asr"
	}
	return ""
}

// audioCppKnownFamily reports whether the family appears in the table at all,
// which is what tells "upstream added a family since this table was written"
// apart from "this family is a music model we do not serve yet".
func audioCppKnownFamily(family string) bool {
	_, ok := audioCppFamilies[strings.ToLower(strings.TrimSpace(family))]
	return ok
}

// audioCppClass is the backend class the family is served under here.
func audioCppClass(family string) string {
	if audioCppTask(family) == "asr" {
		return "asr"
	}
	return "tts"
}

// audioCppBackend resolves the audio.cpp build serving this model. Kind-checked
// rather than class-resolved: audio.cpp shares the tts and asr classes with
// qwentts.cpp, TTS.cpp and parakeet.cpp, and falling back to the class default
// would launch one of those against weights it cannot read. A zero exe means no
// audio.cpp build is registered, which emitAudioCppModel reports in the YAML.
// withoutAudioCpp hides the audio.cpp rows from a resolver run on behalf of
// another engine. Dropping the row is stronger than checking the answer
// afterwards: audio.cpp can hold the class star while a real parakeet or
// TTS.cpp row sits further down the registry, and "the star is audio.cpp" must
// degrade to the NEXT installed engine of that class, not all the way past the
// registry to the legacy derived exe. The audio.cpp emitter never comes through
// here -- it resolves its own row by kind (audioCppBackend).
func withoutAudioCpp(s Settings) Settings {
	keep := make([]BackendEntry, 0, len(s.Backends))
	for _, e := range s.Backends {
		if isAudioCppKind(e.Kind) {
			continue
		}
		keep = append(keep, e)
	}
	s.Backends = keep
	return s
}

// onlyAudioCpp is withoutAudioCpp's mirror: the registry as an audio.cpp model
// sees it. Filtering before resolution rather than checking the answer is what
// makes a cross-engine pin a NO-OP instead of a broken launch - Override.Backend
// is looked up in this list, so a row pinned to TTS.cpp resolves to nothing and
// falls through to the installed audio.cpp build, while a pin at a specific
// audio.cpp BUILD row (vulkan vs cuda) still wins, which is what pinning is for.
// The model editor offers these ggufs no other engine anyway: audio.cpp weights
// are its own format, and the header, not the pin, is what routes them here.
func onlyAudioCpp(s Settings) Settings {
	keep := make([]BackendEntry, 0, len(s.Backends))
	for _, e := range s.Backends {
		if isAudioCppKind(e.Kind) {
			keep = append(keep, e)
		}
	}
	s.Backends = keep
	return s
}

func audioCppBackend(s Settings, ov *Override, class string) resolvedBackend {
	be := resolveBackendPreferring(onlyAudioCpp(s), ov, class, audioCppKind)
	if !isAudioCppKind(be.Kind) {
		return resolvedBackend{}
	}
	return be
}

// audioCppFlavour maps the installed variant id onto audio.cpp's --backend
// value. This is load-bearing, not cosmetic: the server's built-in default is
// CUDA, so a Vulkan or CPU build launched without the flag tries a device it was
// not compiled for. Returns "" for a hand-entered path with no recorded variant,
// where guessing would be the same mistake in the other direction.
func audioCppFlavour(s Settings, id string) string {
	variant := ""
	for _, e := range s.Backends {
		if e.ID == id {
			variant = strings.ToLower(strings.TrimSpace(e.Variant))
			break
		}
	}
	switch variant {
	case "cuda":
		return "cuda"
	case "vulkan":
		return "vulkan"
	case "metal", "metal-x64":
		return "metal"
	case "cpu", "cpu-portable":
		return "cpu"
	}
	return ""
}

// audioCppVoiceDir is where this model's reference voices live: a "voices"
// folder beside the gguf, shared by every audio.cpp model in that folder. The
// per-model split qwentts.cpp gets is deliberately not copied - those are
// learned speaker latents bound to one talker, while these are plain reference
// WAVs plus a prompt_text mapping, reusable by any family that clones.
func audioCppVoiceDir(row GgufRow) string {
	return strings.ReplaceAll(filepath.Join(filepath.Dir(row.FullPath), "voices"), "\\", "/")
}

// audioCppCmdLines builds the audiocpp_server argv (exe first). Shared by
// emitAudioCppModel and RenderSoloCmd so the editor preview matches a save.
//
// Note what is NOT here: --model, because there is no such flag (the model
// arrives through the materialized --config, injected at spawn), and any
// residency knob beyond the cap, because --max-loaded-models 1 is the whole of
// our containment and the defaults for idle unload and the memory guard are
// already off.
func audioCppCmdLines(s Settings, row GgufRow, ov *Override, class string) []string {
	be := audioCppBackend(s, ov, class)
	exe := be.Exe
	if exe == "" {
		exe = "audiocpp_server"
	}
	lines := []string{
		exe,
		"--host 127.0.0.1",
		"--port ${PORT}",
	}
	if flavour := audioCppFlavour(s, be.ID); flavour != "" {
		lines = append(lines, "--backend "+flavour)
	}
	// One model per process, so the LRU this bounds has nothing to choose
	// between. It is emitted anyway as the belt to the config's braces: a hand
	// edit that adds a second model to the block must not silently gain a second
	// set of weights the router never budgeted for.
	lines = append(lines, "--max-loaded-models 1")
	lines = append(lines, "--voice-dir "+audioCppVoiceDir(row))
	if ov != nil && ov.Threads > 0 {
		lines = append(lines, fmt.Sprintf("--threads %d", ov.Threads))
	}
	if ov != nil {
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	return lines
}

// emitAudioCppModel writes an audiocpp_server YAML entry for an audio.cpp GGUF.
//
// The `audiocpp:` block is the part with no precedent in this file: it is not
// launch state but the models[] entry of the JSON config the spawn hook writes,
// carried in the model config so the materializer needs no second source of
// truth and nothing has to be written to disk at generate time.
//
// checkEndpoint is "/health": with lazy_load left false the server loads weights
// before it opens the listen socket, so a 200 there really does mean resident,
// and gating readiness on it holds the first request instead of racing a load.
func emitAudioCppModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name string, meta Metadata, emitted *[]string) {
	family := strings.ToLower(strings.TrimSpace(meta.AudioFamily))
	if family == "" {
		// Nothing can be done from here: family is a required field of the config
		// entry, and it is the one thing the server cannot work out for itself
		// from the weights. A conversion that dropped the spec (a third-party
		// "-orig" export) has to be re-fetched from audio.cpp's own GGUF repo.
		fmt.Fprintf(b, "\n  # SKIPPED %s: audio.cpp gguf with no audiocpp.model_spec.family in its header - audiocpp_server cannot be configured for it; re-download the audio.cpp GGUF build of this model\n", name)
		return
	}
	task := audioCppTask(family)
	if task == "" {
		if audioCppKnownFamily(family) {
			fmt.Fprintf(b, "\n  # SKIPPED %s: audio.cpp family %q is a music / separation / voice-conversion model, which has no class or route here yet\n", name, family)
		} else {
			fmt.Fprintf(b, "\n  # SKIPPED %s: unknown audio.cpp family %q - add it to audioCppFamilies in internal/autogen/audiocpp.go\n", name, family)
		}
		return
	}
	class := audioCppClass(family)
	be := audioCppBackend(s, ov, class)

	fmt.Fprintf(b, "\n  # family=%s task=%s size=%gGB (audio.cpp audiocpp_server)\n", family, task, row.SizeGB)
	if be.Exe == "" {
		fmt.Fprintf(b, "  # WARNING: no audio.cpp backend registered (Settings -> Backends, \"audio.cpp\"); %s will not start until one is installed\n", name)
	} else if audioCppFlavour(s, be.ID) == "" {
		// Worth a line in the config: the failure mode is a CUDA init error on a
		// machine with no CUDA, which reads as a broken model rather than as a
		// missing flag.
		fmt.Fprintf(b, "  # NOTE: audio.cpp backend %q records no build variant, so no --backend flag is emitted and the server's own default (cuda) applies; add \"--backend vulkan|cpu|metal\" to this model's extra arguments if that is wrong\n", be.ID)
	}

	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range audioCppCmdLines(s, row, ov, class) {
		fmt.Fprintf(b, "      %s\n", line)
	}
	b.WriteString("    audiocpp:\n")
	fmt.Fprintf(b, "      family: %q\n", family)
	fmt.Fprintf(b, "      path: %q\n", strings.ReplaceAll(row.FullPath, "\\", "/"))
	fmt.Fprintf(b, "      task: %q\n", task)
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	// audio.cpp serializes inference behind a per-model mutex and 503s a request
	// that waits out its busy timeout. Queuing here instead keeps that from ever
	// being reached, and keeps the wait visible to our own scheduler.
	b.WriteString("    concurrencyLimit: 1\n")
	writeEstVram(b, row.SizeGB+audioCppOverheadGB)
	b.WriteString("    checkEndpoint: /health\n")
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	b.WriteString("    capabilities:\n")
	if task == "asr" {
		b.WriteString("      in: [audio]\n")
		b.WriteString("      out: [text]\n")
	} else {
		b.WriteString("      in: [text]\n")
		b.WriteString("      out: [audio]\n")
		if audioCppClones[family] {
			b.WriteString("      voiceClone: true\n")
		}
	}
	writeDisplayName(b, s, name)
	*emitted = append(*emitted, name)
}
