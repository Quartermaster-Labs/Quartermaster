package server

// Voice registration for audio.cpp models.
//
// audio.cpp has no POST /v1/audio/voices. Its voice list is the set of *.wav
// files in --voice-dir - handle_voices scans that directory live, per request -
// and naming one of those stems in a speech request resolves the file as the
// clone reference. There is no route that ADDS one, so the playground's clone
// flow, which POSTs a base64 WAV to /v1/audio/voices, had nothing to talk to:
// the proxy forwarded it and audio.cpp answered 404.
//
// So quartermaster registers the voice itself. It writes <voice-dir>/<name>.wav
// and, when the clip came with a transcript, a "<name>|<text>" line in the
// <voice-dir>/prompt_text mapping file audio.cpp reads reference text from. The
// model is deliberately NOT started to do it: the scan is per request, so a
// voice registered against an idle model is there the next time it serves one,
// and cloning a voice should not cost a model swap.
//
// This is what makes the clone-ONLY packages usable at all. Qwen3-TTS Base ships
// no built-in speakers, and audio.cpp rejects every request that carries no
// reference with "Qwen3 base TTS requires voice clone reference audio", so
// before this the package could be loaded but never spoken with.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// maxVoiceRefBytes mirrors audio.cpp's own 5 MiB cap on inline reference audio
// (runtime.cpp kMaxVoiceRefBytes). Matching it means a clip that would be
// rejected at synthesis time is rejected at registration time instead of sitting
// on disk as a voice that can never be used.
const maxVoiceRefBytes = 5 << 20

// audioCppVoiceDir is the directory this model's voices live in: the argv
// --voice-dir the generator emits next to the model's own folder. Empty when the
// model is not an audio.cpp model or was hand-written without the flag, and an
// empty answer means "not ours to handle" everywhere below.
func (s *Server) audioCppVoiceDir(modelID string) string {
	mc, ok := s.config().Models[modelID]
	if !ok || mc.AudioCpp.Empty() {
		return ""
	}
	dir, _ := config.ParseCmd(mc.Cmd).Value("--voice-dir")
	return strings.TrimSpace(dir)
}

// validVoiceName mirrors audio.cpp's resolve_voice_library_wav: the name is used
// as a FILENAME under the voice dir, so anything that could climb out of it (a
// separator, a drive letter, "..") is refused rather than sanitized. Sanitizing
// would silently register a voice under a name the caller cannot then select.
func validVoiceName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 120 {
		return false
	}
	if strings.ContainsAny(name, "/\\:") {
		return false
	}
	return filepath.Base(name) == name
}

type audioCppVoiceReq struct {
	Model   string `json:"model"`
	Name    string `json:"name"`
	WavB64  string `json:"wav_b64"`
	RefText string `json:"ref_text"`
}

// handleAudioCppVoicePost intercepts POST /v1/audio/voices for audio.cpp models
// and passes every other model's request straight through to the upstream that
// does implement the route (qwentts.cpp, TTS.cpp).
func (s *Server) handleAudioCppVoicePost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The body is read to decide ownership, so it has to be restored for the
		// pass-through path before anything else touches it.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxVoiceRefBytes*2))
		if err != nil {
			shared.SendResponse(w, r, http.StatusBadRequest, "voice clip is too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		var req audioCppVoiceReq
		if json.Unmarshal(body, &req) != nil {
			next.ServeHTTP(w, r)
			return
		}
		dir := s.audioCppVoiceDir(req.Model)
		if dir == "" {
			next.ServeHTTP(w, r)
			return
		}
		if err := writeAudioCppVoice(dir, req); err != nil {
			shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "ok", "name": strings.TrimSpace(req.Name)})
	})
}

func writeAudioCppVoice(dir string, req audioCppVoiceReq) error {
	name := strings.TrimSpace(req.Name)
	if !validVoiceName(name) {
		return fmt.Errorf("voice name %q cannot be used as a file name", req.Name)
	}
	// The playground sends a bare base64 payload, but a data: URI is the shape a
	// hand-written client reaches for first, so accept both.
	payload := req.WavB64
	if _, after, ok := strings.Cut(payload, ";base64,"); ok {
		payload = after
	}
	wav, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return fmt.Errorf("voice clip is not valid base64: %w", err)
	}
	if len(wav) == 0 {
		return fmt.Errorf("voice clip is empty")
	}
	if len(wav) > maxVoiceRefBytes {
		return fmt.Errorf("voice clip is %d bytes, over audio.cpp's 5 MiB reference limit", len(wav))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Atomic, because audio.cpp lists and reads this directory while we write: a
	// half-written wav would appear in the voice list and then fail to decode.
	path := filepath.Join(dir, name+".wav")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, wav, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if text := strings.TrimSpace(req.RefText); text != "" {
		if err := upsertPromptText(dir, name, text); err != nil {
			return err
		}
	}
	return nil
}

// upsertPromptText maintains <voice-dir>/prompt_text, audio.cpp's
// "<name>|<transcript>" mapping (load_voice_library_text). The whole file is
// rewritten so re-cloning a name replaces its line instead of appending a second
// one - the reader takes the FIRST match, so a stale duplicate would win forever.
func upsertPromptText(dir, name, text string) error {
	kept, err := promptTextWithout(dir, name)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// A transcript with a newline in it would split into a line that parses as a
	// different voice, so flatten it.
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	kept = append(kept, name+"|"+text)
	return os.WriteFile(filepath.Join(dir, "prompt_text"), []byte(strings.Join(kept, "\n")+"\n"), 0o644)
}

// promptTextWithout reads prompt_text and returns every line that does NOT name
// this voice.
func promptTextWithout(dir, name string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "prompt_text"))
	if err != nil {
		return nil, err
	}
	var kept []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if before, _, ok := strings.Cut(line, "|"); ok && strings.TrimSpace(before) == name {
			continue
		}
		kept = append(kept, line)
	}
	return kept, nil
}

// handleAudioCppVoiceDelete is the DELETE half: remove the wav and its
// prompt_text line. Same pass-through rule for every other engine.
func (s *Server) handleAudioCppVoiceDelete(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dir := s.audioCppVoiceDir(r.URL.Query().Get("model"))
		if dir == "" {
			next.ServeHTTP(w, r)
			return
		}
		name := r.PathValue("name")
		if !validVoiceName(name) {
			shared.SendResponse(w, r, http.StatusBadRequest, "invalid voice name")
			return
		}
		if err := os.Remove(filepath.Join(dir, name+".wav")); err != nil && !os.IsNotExist(err) {
			shared.SendResponse(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		// Best effort: a leftover prompt_text line for a wav that is gone names no
		// voice, since the list itself is built from the wavs.
		_ = removePromptText(dir, name)
		writeJSON(w, map[string]string{"status": "ok"})
	})
}

func removePromptText(dir, name string) error {
	kept, err := promptTextWithout(dir, name)
	if err != nil {
		return err
	}
	if len(kept) == 0 {
		return os.Remove(filepath.Join(dir, "prompt_text"))
	}
	return os.WriteFile(filepath.Join(dir, "prompt_text"), []byte(strings.Join(kept, "\n")+"\n"), 0o644)
}
