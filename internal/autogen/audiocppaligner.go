package autogen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Qwen3-ASR word timestamps come from a second model: after transcribing, the
// session runs Qwen3 ForcedAligner over the transcript and audio, which is what
// fills each word's start/end sample. audio.cpp loads the aligner INSIDE the ASR
// session (qwen3_asr.forced_aligner_path), so it is a companion, not a served
// model: the aligner gguf stays class 0 and only ever rides along with an ASR.
//
// Unlike audioCppCompanions, the aligner ships as its own repo and lands in a
// different folder, so the option carries an absolute path rather than a name
// relative to the ASR's package directory.

const (
	qwen3AsrFamily       = "qwen3_asr"
	qwen3AlignerFamily   = "qwen3_forced_aligner"
	qwen3AlignerOption   = "qwen3_asr.forced_aligner_path"
	qwen3AsrVadOption    = "qwen3_asr.vad_model_path"
	audioCppSileroVadRel = "assets/framework/models/silero_vad"
)

// audioCppAligner is the forced-aligner gguf discovery found, if any.
type audioCppAligner struct {
	path   string // forward-slashed absolute path
	sizeGB float64
}

// detectQwen3Aligner finds a Qwen3 ForcedAligner gguf among the discovered rows.
// Only rows whose file name says "align" have their header read: a header read
// parses the whole metadata block (an LLM's 150k-entry tokenizer included), and
// Generate already reads every header once in emitModel. The header then has the
// final word, so a file merely named like an aligner is not taken for one. Rows
// arrive sorted, so the same tree always picks the same file.
func detectQwen3Aligner(rows []GgufRow) *audioCppAligner {
	for _, row := range rows {
		if row.IsSam || !strings.Contains(strings.ToLower(row.FileName), "align") {
			continue
		}
		meta, err := ReadGgufMetadata(row.FullPath)
		if err != nil || !strings.EqualFold(strings.TrimSpace(meta.AudioFamily), qwen3AlignerFamily) {
			continue
		}
		return &audioCppAligner{path: filepath.ToSlash(row.FullPath), sizeGB: row.SizeGB}
	}
	return nil
}

// qwen3AsrSessionOptions adds the timestamp companions to a qwen3_asr entry and
// returns its options and admission estimate. The aligner is resident for the
// session's whole life (it is loaded with the ASR, not per request), so its
// weights are counted in estGB whether or not a request asks for timestamps.
//
// The VAD path is pinned absolute when the install has it: audio.cpp resolves
// its relative default against the process cwd, and quartermaster spawns every
// backend from its OWN directory (process.spawnDir), where no assets/ exists.
// With timestamps on, audio_chunk_mode=auto chunks on VAD, so a missing VAD
// would fail exactly the requests the aligner was added for.
func qwen3AsrSessionOptions(b *strings.Builder, s Settings, ov *Override, base map[string]string, estGB float64) (map[string]string, float64) {
	al := s.qwen3Aligner
	if al == nil {
		b.WriteString("\n  # NOTE: no Qwen3 ForcedAligner gguf found, so this ASR serves text only (no word timestamps)")
		return base, estGB
	}
	opts := make(map[string]string, len(base)+2)
	for k, v := range base {
		opts[k] = v
	}
	opts[qwen3AlignerOption] = al.path
	if vad := audioCppAssetPath(audioCppBackend(s, ov, audioCppClass(qwen3AsrFamily)).Exe, audioCppSileroVadRel); vad != "" {
		opts[qwen3AsrVadOption] = vad
	} else {
		fmt.Fprintf(b, "\n  # NOTE: %s not found beside the audio.cpp install; timestamp requests chunk on VAD and may fail", audioCppSileroVadRel)
	}
	return opts, estGB + al.sizeGB
}

// audioCppAssetPath finds rel in the nearest ancestor of the audio.cpp binary's
// directory that holds it, the same walk audiocpp.SpecsDir makes for
// model_specs: a release ships assets beside the binary, a source build keeps
// them at the checkout root above build/<preset>/bin. "" when none has it.
func audioCppAssetPath(exe, rel string) string {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		return ""
	}
	dir := filepath.Dir(exe)
	for i := 0; i <= 3; i++ {
		cand := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(cand); err == nil {
			return filepath.ToSlash(cand)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
