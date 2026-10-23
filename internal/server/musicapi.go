package server

// Music generation, music edits and stem separation, served by audio.cpp.
//
// audio.cpp runs all three through one generic route, POST /v1/tasks/run, whose
// body is a free-form option map plus an `audio` field that is a FILE PATH the
// server opens. Exposing that route as-is would hand every API client a
// read-any-file primitive on this machine, and an option map with no schema.
// So quartermaster serves three typed routes instead and does the translation
// itself:
//
//	POST /v1/music/generations   text (+ lyrics) -> one WAV
//	POST /v1/music/edits         WAV + repaint|complete|cover -> one WAV (ACE-Step)
//	POST /v1/audio/separations   WAV -> named stems (HTDemucs)
//
// Audio arrives inline (base64 in JSON, or a multipart file), is written to a
// private temp file under our cache directory, and the path audio.cpp is given
// is always one we made. Options are whitelisted per family, which keeps the
// path-valued ones (noise_file, source_audio) out of reach.
//
// All three are SYNCHRONOUS. A turbo ACE-Step song takes seconds to tens of
// seconds and a separation about as long, which is well inside what an HTTP
// client waits for, and the request stays open for the whole run, so the
// scheduler counts the model as busy without the lease machinery video needs
// (videojobs.go exists only because sd-server's job API returns before the work
// does). The upstream transport has no response-header timeout by default.
// audio.cpp has no cancel, so a caller that hangs up mid-run gets the process
// stopped instead (stopAbandonedMusic).
//
// The seed is always explicit upstream: one the caller omitted is drawn here
// and echoed back (X-Seed, and "seed" in JSON), so any result can be replayed,
// and replayed on audiocpp_cli, which is the reference this is checked against.

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/apppaths"
	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/process"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

type musicRoute int

const (
	musicGenerate musicRoute = iota
	musicEdit
	musicSeparate
	musicTranscribe
)

const (
	// maxMusicInputBytes caps one uploaded clip: ten minutes of 48 kHz stereo
	// float is ~230 MB, which is past any track a separator is useful on.
	maxMusicInputBytes = 256 << 20
	// musicResponseLimit caps the buffered upstream reply. Four PCM16 stems of a
	// five-minute track are ~210 MB of base64; this leaves room for long ones.
	musicResponseLimit = 1 << 30
	// musicTmpMaxAge is how old a leftover input file has to be before the next
	// request sweeps it. Each request removes its own file; this only catches
	// the ones a crash left behind.
	musicTmpMaxAge = time.Hour
)

// musicTmpDir is where uploaded clips are staged for audio.cpp. A variable so a
// test can redirect it (see audioCppConfigDir for why).
var musicTmpDir = func() string { return filepath.Join(apppaths.CacheDir(), "audiocpp-tmp") }

// musicFamilyInputRate is the sample rate a family's input MUST have. HTDemucs
// rejects anything else outright; ACE-Step resamples internally and is absent.
var musicFamilyInputRate = map[string]int{"htdemucs": 44100}

// musicFamilyOptions is the per-family whitelist for the free-form `options`
// object. Typed request fields (seed, lyrics, duration, steps, guidance, the
// edit window) are not listed: they have their own fields and win over this.
// A score is not listed either (YuE2's abc): it is the typed `score` field,
// mapped per family through autogen.AudioCppMusicScore, so one door checks it.
// Path-valued options (noise_file, source_audio, and YuE2's abc_file,
// semantic_prefix_file, nar_noise_file) are deliberately absent, and so are
// YuE2's stop_after and export_semantic: a run stopped before the audio stage,
// or one carrying a semantic artifact, is not a WAV this route can return.
var musicFamilyOptions = map[string]map[string]bool{
	"ace_step": setOf(
		"bpm", "keyscale", "timesignature", "negative_prompt", "instruction",
		"thinking", "shift", "retake_seed", "retake_variance", "infer_method",
		"sampler_mode", "use_adg", "cfg_interval_start", "cfg_interval_end",
		"lm_temperature", "lm_top_k", "lm_top_p", "lm_cfg_scale", "lm_repetition_penalty",
		"use_cot_caption", "use_cot_language", "use_cot_metas", "rewrite_caption",
		"audio_cover_strength", "cover_noise_strength", "repaint_crossfade_frames",
		"repaint_injection_ratio", "complete_track_classes", "track_name",
	),
	"minimax_music3": setOf("ar_guidance_scale", "top_k"),
	"sheetsage2":     setOf("max_tokens"),
	"yue2": setOf(
		"cot", "semantic_prefix",
		"abc_temperature", "abc_top_p", "abc_top_k", "abc_repetition_penalty",
		"abc_penalty_window", "abc_min_tokens", "abc_max_tokens",
		"semantic_temperature", "semantic_top_p", "semantic_top_k", "semantic_repetition_penalty",
		"semantic_penalty_window", "semantic_min_tokens", "semantic_max_tokens",
	),
}

// maxMusicScoreBytes caps an inline score. A whole song in ABC is a few KB;
// the cap only stops a multi-MB body being tokenized as a prompt.
const maxMusicScoreBytes = 64 << 10

// applyMusicScore puts a typed score into the family's own option. A blank
// score is no score. Refused: a family that takes none, an edit (the score
// conditions generation from nothing), and a planning mode that skips the
// score stage, which would otherwise drop it without a word.
func applyMusicScore(route musicRoute, family string, score *string, opts map[string]string) error {
	if score == nil || strings.TrimSpace(*score) == "" {
		return nil
	}
	sc, ok := autogen.AudioCppMusicScore(family)
	switch {
	case !ok:
		return musicError(family + " takes no score")
	case route != musicGenerate:
		return musicError("a score applies to generation only")
	case len(*score) > maxMusicScoreBytes:
		return musicError(fmt.Sprintf("score is larger than %d KiB", maxMusicScoreBytes>>10))
	case sc.ModeOption != "" && opts[sc.ModeOption] == sc.ModeOff:
		return musicError(fmt.Sprintf("a score is ignored with %s=%s; use another %s value", sc.ModeOption, sc.ModeOff, sc.ModeOption))
	}
	opts[sc.Option] = *score
	return nil
}

func setOf(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// musicRequest is the union of the three routes' fields. JSON and multipart
// both decode into it.
type musicRequest struct {
	Model  string  `json:"model"`
	Prompt string  `json:"prompt"`
	Lyrics *string `json:"lyrics"`
	// Score is a written score to condition generation on, in the notation the
	// model's capabilities.musicScore names. Generation only.
	Score          *string        `json:"score"`
	Duration       *float64       `json:"duration"`
	Seed           *int64         `json:"seed"`
	Steps          *int           `json:"steps"`
	Guidance       *float64       `json:"guidance"`
	Language       string         `json:"language"`
	ResponseFormat string         `json:"response_format"`
	Options        map[string]any `json:"options"`

	// Edits.
	Task            string   `json:"task"`
	Start           *float64 `json:"start"`
	End             *float64 `json:"end"`
	RepaintMode     string   `json:"repaint_mode"`
	RepaintStrength *float64 `json:"repaint_strength"`

	// Audio is base64 WAV (a data: URL prefix is accepted) in JSON; multipart
	// sends it as a file part and fills audioBytes directly.
	Audio      string `json:"audio"`
	audioBytes []byte
}

// musicError is a 400 with a message meant for the caller.
type musicError string

func (e musicError) Error() string { return string(e) }

// handleMusic returns the handler for one of the three music routes. It runs
// inside the model chain, so auth, scoping and filters have already applied,
// and dispatches the rewritten request through localPeerHandler so the model
// is swapped in and counted like any other request.
func (s *Server) handleMusic(route musicRoute) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := shared.FetchContext(r, s.config())
		if err != nil {
			shared.SendError(w, r, shared.ErrNoModelInContext)
			return
		}
		// Anything this request may not reach, and anything not served locally,
		// goes straight to the dispatcher: it owns the 404/403 answers, and a
		// peer (another quartermaster) serves these routes itself.
		if models, scoped := listenerModelSet(r); scoped && !models[data.ModelID] {
			s.localPeerHandler(w, r)
			return
		}
		if models, scoped := apiKeyModelSet(r); scoped && !models[data.ModelID] {
			s.localPeerHandler(w, r)
			return
		}
		if !s.local.Handles(data.ModelID) {
			s.localPeerHandler(w, r)
			return
		}

		mc := s.config().Models[data.ModelID]
		ac := mc.AudioCpp
		if msg := musicRouteMismatch(route, ac.Family, ac.Task); msg != "" {
			shared.SendResponse(w, r, http.StatusBadRequest, fmt.Sprintf("model %q %s", data.Model, msg))
			return
		}

		req, err := readMusicRequest(r)
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		if err != nil {
			shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
			return
		}
		seed, upstream, cleanup, err := buildMusicUpstream(route, ac.Family, req)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			var me musicError
			if errors.As(err, &me) {
				shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
			} else {
				s.proxylog.Errorf("music: model %s: %v", data.ModelID, err)
				shared.SendResponse(w, r, http.StatusInternalServerError, "could not stage the request: "+err.Error())
			}
			return
		}

		// A clone, not the request itself: the metrics and activity middleware
		// read the path of the request they wrapped, and that should stay the
		// route the client called, not audio.cpp's.
		up := r.Clone(r.Context())
		up.URL.Path = "/v1/tasks/run"
		up.URL.RawPath = ""
		up.Body = io.NopCloser(bytes.NewReader(upstream))
		up.ContentLength = int64(len(upstream))
		up.Header.Set("Content-Type", "application/json")
		up.Header.Del("Content-Length")

		buf := newBufferedResponseWriterLimit(musicResponseLimit)
		s.localPeerHandler(buf, up)
		if r.Context().Err() != nil {
			s.stopAbandonedMusic(data.ModelID)
			return
		}
		if buf.over {
			s.proxylog.Errorf("music: upstream response for model %s exceeded %d bytes", data.ModelID, musicResponseLimit)
			shared.SendResponse(w, r, http.StatusBadGateway, "upstream response too large")
			return
		}
		if buf.code >= http.StatusBadRequest {
			if msg, ok := musicCapacityError(route, buf.body.Bytes(), req.audioBytes); ok {
				shared.SendResponse(w, r, http.StatusBadRequest, msg)
				return
			}
			buf.flushTo(w)
			return
		}
		writeMusicResponse(w, r, route, req.ResponseFormat, seed, buf.body.Bytes())
	}
}

// stopAbandonedMusic stops a model whose caller hung up mid-run. audio.cpp
// cannot cancel a job: closing the connection leaves it running to the end
// (its busy timeout "does not cancel backend execution"), holding the GPU and
// queueing the user's next take behind a song nobody will hear. Killing the
// process is the only stop there is, so the playground's Stop button, an API
// client's timeout and a closed tab all land here.
//
// Unload takes down every request on the process, so it is skipped while
// anything else is using the model (another user's take queued behind this
// one, a held lease) and while it is not Ready (a caller that gave up during
// the swap-in may share that load with others still waiting for it). A
// request landing between the check and the stop is lost the same way; that
// window is one run-loop round trip.
func (s *Server) stopAbandonedMusic(modelID string) {
	n, known := s.local.Inflight(modelID)
	if !abandonedMusicStoppable(n, known, s.local.RunningModels()[modelID]) {
		return
	}
	s.proxylog.Infof("music: caller for %s hung up mid-run; stopping it (audio.cpp cannot cancel a job)", modelID)
	s.local.UnloadWithReason(process.StopManual, apiUnloadTimeout, modelID)
}

func abandonedMusicStoppable(inflight int64, known bool, state process.ProcessState) bool {
	return known && inflight == 0 && state == process.StateReady
}

// musicCapacityError turns audio.cpp's bare allocation failure for an ACE-Step
// edit into an answer the caller can act on. The VAE encoder runs over the
// whole source in one graph (decode is chunked, encode is not, as of audio.cpp
// v0.9.2), and its largest activation grows by about 86 MB per second of
// audio. A Vulkan driver that caps one buffer at 2 GB (AMD's) therefore fails
// every source longer than about 24 s, with a 500 that names neither cause.
// No limit is enforced here: the cap is the driver's, and CUDA or CPU builds
// encode far longer sources. Anything else passes through untouched.
func musicCapacityError(route musicRoute, upstreamBody, source []byte) (string, bool) {
	if route != musicEdit {
		return "", false
	}
	msg := string(upstreamBody)
	var doc struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(upstreamBody, &doc) == nil && doc.Error.Message != "" {
		msg = doc.Error.Message
	}
	if !strings.Contains(msg, "VAE encoder") || !strings.Contains(msg, "allocation failed") {
		return "", false
	}
	length := ""
	if info, err := parseWAV(source); err == nil && info.bitsPerSample > 0 {
		frames := len(info.data) / (info.channels * info.bitsPerSample / 8)
		length = fmt.Sprintf(" This source is %.1f s.", float64(frames)/float64(info.sampleRate))
	}
	return fmt.Sprintf("the backend could not allocate GPU memory to encode the source audio (%s). "+
		"The ACE-Step VAE encoder needs about 86 MB per second of source in one buffer, so on a Vulkan "+
		"driver that caps a buffer at 2 GB (AMD) sources longer than about 24 s fail.%s "+
		"Trim the source, or serve the model from a CUDA or CPU build.", msg, length), true
}

// musicRouteMismatch reports why a model cannot serve route, or "" if it can.
// Keyed off the generated audiocpp block, not the capability flags, because the
// family is what decides which options mean anything.
func musicRouteMismatch(route musicRoute, family, task string) string {
	switch route {
	case musicGenerate:
		if task != "gen" {
			return "is not a music generation model"
		}
	case musicEdit:
		if family != "ace_step" {
			return "does not support music edits (ACE-Step only)"
		}
	case musicSeparate:
		if task != "sep" {
			return "is not a source separation model"
		}
	case musicTranscribe:
		if task != "midi" {
			return "is not a score transcription model"
		}
	}
	return ""
}

// readMusicRequest decodes a JSON or multipart body into a musicRequest, with
// the uploaded audio (if any) decoded into audioBytes.
func readMusicRequest(r *http.Request) (musicRequest, error) {
	var req musicRequest
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return req, fmt.Errorf("invalid multipart body: %w", err)
		}
		if err := musicRequestFromForm(r.MultipartForm, &req); err != nil {
			return req, err
		}
		return req, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMusicInputBytes*4/3+(1<<20)))
	if err != nil {
		return req, fmt.Errorf("reading body: %w", err)
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return req, fmt.Errorf("invalid JSON body: %w", err)
	}
	if req.Audio != "" {
		b, err := decodeBase64Audio(req.Audio)
		if err != nil {
			return req, err
		}
		req.audioBytes = b
		req.Audio = ""
	}
	return req, nil
}

func decodeBase64Audio(s string) ([]byte, error) {
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ","); i >= 0 {
			s = s[i+1:]
		}
	}
	s = strings.TrimSpace(s)
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "=")); err != nil {
			return nil, errors.New("audio is not valid base64")
		}
	}
	if len(b) > maxMusicInputBytes {
		return nil, fmt.Errorf("audio exceeds %d MB", maxMusicInputBytes>>20)
	}
	return b, nil
}

// musicRequestFromForm fills req from multipart fields of the same names as
// the JSON body. `options` is a JSON object in a text field; the audio is a
// file part named "audio" (or "file", the OpenAI transcription name).
func musicRequestFromForm(f *multipart.Form, req *musicRequest) error {
	get := func(k string) string {
		if v := f.Value[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	req.Model = get("model")
	req.Prompt = get("prompt")
	req.Language = get("language")
	req.ResponseFormat = get("response_format")
	req.Task = get("task")
	req.RepaintMode = get("repaint_mode")
	if _, ok := f.Value["lyrics"]; ok {
		v := get("lyrics")
		req.Lyrics = &v
	}
	if _, ok := f.Value["score"]; ok {
		v := get("score")
		req.Score = &v
	}
	floats := map[string]**float64{"duration": &req.Duration, "guidance": &req.Guidance, "start": &req.Start, "end": &req.End, "repaint_strength": &req.RepaintStrength}
	for k, dst := range floats {
		if v := strings.TrimSpace(get(k)); v != "" {
			x, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("%s: not a number", k)
			}
			*dst = &x
		}
	}
	if v := strings.TrimSpace(get("seed")); v != "" {
		x, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return errors.New("seed: not an integer")
		}
		req.Seed = &x
	}
	if v := strings.TrimSpace(get("steps")); v != "" {
		x, err := strconv.Atoi(v)
		if err != nil {
			return errors.New("steps: not an integer")
		}
		req.Steps = &x
	}
	if v := strings.TrimSpace(get("options")); v != "" {
		if err := json.Unmarshal([]byte(v), &req.Options); err != nil {
			return errors.New("options: not a JSON object")
		}
	}
	for _, name := range []string{"audio", "file"} {
		fhs := f.File[name]
		if len(fhs) == 0 {
			continue
		}
		fh, err := fhs[0].Open()
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		b, err := io.ReadAll(io.LimitReader(fh, maxMusicInputBytes+1))
		fh.Close()
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		if len(b) > maxMusicInputBytes {
			return fmt.Errorf("audio exceeds %d MB", maxMusicInputBytes>>20)
		}
		req.audioBytes = b
		break
	}
	if req.audioBytes == nil && get("audio") != "" {
		b, err := decodeBase64Audio(get("audio"))
		if err != nil {
			return err
		}
		req.audioBytes = b
	}
	return nil
}

// buildMusicUpstream validates req for route and family and returns the seed
// sent, the audio.cpp /v1/tasks/run body, and a cleanup for the staged input
// file (nil when there is none). Caller mistakes come back as musicError.
func buildMusicUpstream(route musicRoute, family string, req musicRequest) (uint64, []byte, func(), error) {
	opts := map[string]string{}
	allowed := musicFamilyOptions[family]
	keys := make([]string, 0, len(req.Options))
	for k := range req.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			return 0, nil, nil, musicError(fmt.Sprintf("option %q is not supported for %s (allowed: %s)", k, family, strings.Join(musicOptionNames(allowed), ", ")))
		}
		v, err := musicOptionString(req.Options[k])
		if err != nil {
			return 0, nil, nil, musicError(fmt.Sprintf("option %q: %v", k, err))
		}
		opts[k] = v
	}

	switch req.ResponseFormat {
	case "", "wav", "b64_json":
	default:
		return 0, nil, nil, musicError("response_format must be wav or b64_json")
	}

	// Separation and transcription read a recording and nothing else: no
	// prompt, no seed (neither samples), no text input.
	takesPrompt := route == musicGenerate || route == musicEdit
	var seed uint64
	if takesPrompt {
		if strings.TrimSpace(req.Prompt) == "" {
			return 0, nil, nil, musicError("prompt is required")
		}
		switch {
		case req.Seed == nil || *req.Seed < 0:
			seed = randomSeed()
		case family == "ace_step" && *req.Seed > math.MaxUint32:
			return 0, nil, nil, musicError("seed must fit in 32 bits for ACE-Step")
		default:
			seed = uint64(*req.Seed)
		}
		opts["seed"] = strconv.FormatUint(seed, 10)
		if req.Lyrics != nil {
			opts["lyrics"] = *req.Lyrics
		}
		if family == "minimax_music3" && strings.TrimSpace(opts["lyrics"]) == "" {
			return 0, nil, nil, musicError("lyrics are required for MiniMax Music")
		}
		if family == "yue2" {
			// YuE2 takes its prompt as the style option, and reads lyrics from the
			// text input when the lyrics option is absent, which would sing the
			// style tags. An empty lyrics option only leaves the prompt's [Lyrics]
			// section empty: YuE2 has no instrumental mode, and the base model is a
			// vocal model that can still sing made-up words. Upstream's route to
			// instrumentals is a separate AR LoRA (cot=full, "[instrumental]").
			opts["style"] = req.Prompt
			if req.Lyrics == nil {
				opts["lyrics"] = ""
			}
			if req.Duration != nil {
				return 0, nil, nil, musicError("YuE2 has no duration: the song is as long as its lyrics")
			}
		}
		if err := applyMusicScore(route, family, req.Score, opts); err != nil {
			return 0, nil, nil, err
		}
		if req.Duration != nil {
			if *req.Duration <= 0 {
				return 0, nil, nil, musicError("duration must be positive")
			}
			opts["duration_seconds"] = fmtFloat(*req.Duration)
		}
		if req.Steps != nil {
			if *req.Steps < 1 {
				return 0, nil, nil, musicError("steps must be at least 1")
			}
			opts["num_inference_steps"] = strconv.Itoa(*req.Steps)
		}
		if req.Guidance != nil {
			opts["guidance_scale"] = fmtFloat(*req.Guidance)
		}
	}

	if route == musicEdit {
		switch req.Task {
		case "repaint":
			if req.Start == nil || req.End == nil {
				return 0, nil, nil, musicError("repaint needs start and end (seconds; end -1 = to the end)")
			}
			if *req.Start < 0 || (*req.End >= 0 && *req.End <= *req.Start) {
				return 0, nil, nil, musicError("repaint window must have 0 <= start < end")
			}
		case "complete", "cover":
		default:
			return 0, nil, nil, musicError("task must be repaint, complete or cover")
		}
		opts["route"] = req.Task
		if req.Start != nil {
			opts["repainting_start"] = fmtFloat(*req.Start)
		}
		if req.End != nil {
			opts["repainting_end"] = fmtFloat(*req.End)
		}
		switch req.RepaintMode {
		case "":
		case "balanced", "conservative", "aggressive":
			opts["repaint_mode"] = req.RepaintMode
		default:
			return 0, nil, nil, musicError("repaint_mode must be balanced, conservative or aggressive")
		}
		if req.RepaintStrength != nil {
			opts["repaint_strength"] = fmtFloat(*req.RepaintStrength)
		}
	} else if route == musicGenerate && family == "ace_step" {
		opts["route"] = "text2music"
	}

	inner := map[string]any{"options": opts}
	if takesPrompt {
		inner["text"] = req.Prompt
		if req.Language != "" {
			inner["language"] = req.Language
		}
	}

	var cleanup func()
	if !takesPrompt || route == musicEdit {
		if len(req.audioBytes) == 0 {
			return 0, nil, nil, musicError("audio is required (base64 WAV in \"audio\", or a multipart file)")
		}
		wav, err := prepareMusicInput(req.audioBytes, musicFamilyInputRate[family])
		if err != nil {
			return 0, nil, nil, err
		}
		path, err := stageMusicInput(wav)
		if err != nil {
			return 0, nil, nil, err
		}
		cleanup = func() { _ = os.Remove(path) }
		inner["audio"] = path
	}

	body, err := json.Marshal(map[string]any{"model": req.Model, "request": inner})
	if err != nil {
		return 0, nil, cleanup, err
	}
	return seed, body, cleanup, nil
}

// prepareMusicInput validates an uploaded WAV and, when the family needs a
// fixed rate the clip does not have, resamples it. A clip that needs nothing
// is returned untouched.
func prepareMusicInput(b []byte, wantRate int) ([]byte, error) {
	info, err := parseWAV(b)
	if err != nil {
		return nil, musicError(err.Error())
	}
	if wantRate == 0 || info.sampleRate == wantRate {
		return b, nil
	}
	if info.channels > 2 {
		return nil, musicError(fmt.Sprintf("audio has %d channels; send mono or stereo", info.channels))
	}
	samples, err := decodeWAV(info)
	if err != nil {
		return nil, musicError(err.Error())
	}
	return encodeFloatWAV(resample(samples, info.channels, info.sampleRate, wantRate), info.channels, wantRate), nil
}

// stageMusicInput writes wav to a fresh, randomly named file in musicTmpDir and
// returns its absolute path. audio.cpp resolves relative paths against its own
// binary directory, so the path has to be absolute.
func stageMusicInput(wav []byte) (string, error) {
	dir := musicTmpDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	sweepMusicTmp(dir)
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "in-"+hex.EncodeToString(rnd[:])+".wav")
	if err := os.WriteFile(path, wav, 0o600); err != nil {
		os.Remove(path)
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return abs, nil
}

// sweepMusicTmp removes staged inputs older than musicTmpMaxAge.
func sweepMusicTmp(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-musicTmpMaxAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "in-") {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// musicOptionString renders an option value the way audio.cpp's option map
// holds it: everything is a string.
func musicOptionString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case float64:
		return fmtFloat(x), nil
	case []any:
		// complete_track_classes is a list; audio.cpp parses an option that
		// starts with '[' as a JSON array of strings.
		for _, p := range x {
			if _, ok := p.(string); !ok {
				return "", errors.New("list items must be strings")
			}
		}
		b, err := json.Marshal(x)
		return string(b), err
	}
	return "", errors.New("must be a string, number or boolean")
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func musicOptionNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// randomSeed draws a seed that fits every family (ACE-Step's is 32-bit).
func randomSeed() uint64 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint64(time.Now().UnixNano() & math.MaxUint32)
	}
	return uint64(binary.LittleEndian.Uint32(b[:]))
}

// audioCppTaskResult is the part of audio.cpp's task result the music routes
// read. Every audio field is a base64 PCM16 WAV.
type audioCppTaskResult struct {
	Audio      string `json:"audio"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Named      []struct {
		ID         string `json:"id"`
		Audio      string `json:"audio"`
		SampleRate int    `json:"sample_rate"`
		Channels   int    `json:"channels"`
	} `json:"named_audio_outputs"`
	Artifacts []audioCppArtifact `json:"artifacts"`
}

// audioCppArtifact is a non-audio task output: SheetSage2 writes its score as
// one (payload = base64 of the ABC text) next to an events-JSON one.
type audioCppArtifact struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Payload string            `json:"payload"`
	Meta    map[string]string `json:"meta"`
}

// abcScore picks the ABC score out of a transcription's artifacts: the one
// whose meta names ABC (a mime such as text/vnd.abc), else the first whose
// text reads as ABC (an X: reference line and a K: key line). Returns "" when
// none does.
func abcScore(arts []audioCppArtifact) string {
	text := func(a audioCppArtifact) string {
		b, err := base64.StdEncoding.DecodeString(a.Payload)
		if err != nil {
			return ""
		}
		return string(b)
	}
	looksABC := func(s string) bool {
		s = "\n" + strings.ReplaceAll(s, "\r", "")
		return strings.Contains(s, "\nX:") && strings.Contains(s, "\nK:")
	}
	for _, a := range arts {
		for _, v := range a.Meta {
			if strings.Contains(strings.ToLower(v), "abc") && !strings.Contains(strings.ToLower(v), "json") {
				if t := text(a); t != "" {
					return t
				}
			}
		}
	}
	for _, a := range arts {
		if t := text(a); looksABC(t) {
			return t
		}
	}
	return ""
}

type musicStem struct {
	Name       string `json:"name"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Audio      string `json:"audio"`
}

// writeMusicResponse turns audio.cpp's task result into the route's reply.
func writeMusicResponse(w http.ResponseWriter, r *http.Request, route musicRoute, format string, seed uint64, upstream []byte) {
	var res audioCppTaskResult
	if err := json.Unmarshal(upstream, &res); err != nil {
		shared.SendResponse(w, r, http.StatusBadGateway, "upstream returned an unreadable result")
		return
	}
	if route == musicTranscribe {
		score := abcScore(res.Artifacts)
		if score == "" {
			shared.SendResponse(w, r, http.StatusBadGateway, "upstream returned no ABC score")
			return
		}
		writeJSON(w, map[string]any{"score": score, "format": "abc"})
		return
	}
	if route == musicSeparate {
		stems := make([]musicStem, 0, len(res.Named))
		for _, n := range res.Named {
			stems = append(stems, musicStem{Name: n.ID, SampleRate: n.SampleRate, Channels: n.Channels, Audio: n.Audio})
		}
		if len(stems) == 0 {
			shared.SendResponse(w, r, http.StatusBadGateway, "upstream returned no stems")
			return
		}
		writeJSON(w, map[string]any{"stems": stems})
		return
	}
	if res.Audio == "" {
		shared.SendResponse(w, r, http.StatusBadGateway, "upstream returned no audio")
		return
	}
	seedStr := strconv.FormatUint(seed, 10)
	w.Header().Set("X-Seed", seedStr)
	w.Header().Set("Access-Control-Expose-Headers", "X-Seed")
	if format == "b64_json" {
		writeJSON(w, map[string]any{
			"audio":       res.Audio,
			"format":      "wav",
			"sample_rate": res.SampleRate,
			"channels":    res.Channels,
			"seed":        seed,
		})
		return
	}
	wav, err := base64.StdEncoding.DecodeString(res.Audio)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadGateway, "upstream audio is not valid base64")
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", strconv.Itoa(len(wav)))
	_, _ = w.Write(wav)
}
