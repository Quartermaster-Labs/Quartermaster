package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
)

// POST /v1/audio/transcriptions is forwarded untouched except in two cases,
// both decided per engine.
//
// audio.cpp + verbose_json + timestamp_granularities: the OpenAI multipart
// route cannot carry per-request options (audio.cpp's multipart handler reads
// file/model/language/prompt only), and word timestamps are the request option
// return_timestamps. So the upload is staged to a temp file and sent as JSON to
// /v1/audio/transcriptions/details, whose reply (sample offsets) is translated
// into OpenAI's verbose_json (seconds). Asking only on request keeps the plain
// path fast: the aligner pass roughly doubles the work. return_timestamps is
// therefore never a session default.
//
// The audio is sent at 16 kHz mono, and that is a correctness fix rather than a
// saving: audio.cpp rescales chunked word spans into the MODEL's rate (16 kHz)
// but reports the INPUT's rate as sample_rate beside them, so a 48 kHz upload
// would come back with every time a third of what it should be.
//
// parakeet-server: the upload is downmixed and resampled to 16 kHz mono PCM16,
// which is what the model consumes. Fed a 48 kHz stereo song it dropped the
// connection (a 502 with an empty body) where the same audio at 16 kHz mono
// transcribed. Its verbose_json puts the whole file in one segment, so words
// are always requested and the segments rebuilt from them.

const (
	// asrInputRate is the rate both engines' models consume.
	asrInputRate = 16000
	// asrSegmentGap splits segments on a silence at least this long, in
	// seconds. A sung line break is about that; a breath inside a line is not.
	asrSegmentGap = 0.8
	// asrResponseLimit caps a buffered transcription reply. Word lists for an
	// hour of speech are a few MB.
	asrResponseLimit = 64 << 20
)

type asrEngine int

const (
	asrPassthrough asrEngine = iota
	asrAudioCpp
	asrParakeet
)

// asrEngineOf names the engine behind a model, for the two this route adapts.
func asrEngineOf(mc config.ModelConfig) asrEngine {
	if mc.AudioCpp.Task == "asr" {
		return asrAudioCpp
	}
	if argv := config.ParseCmd(mc.Cmd).Argv; len(argv) > 0 {
		exe := strings.ToLower(filepath.Base(strings.ReplaceAll(argv[0], "\\", "/")))
		if strings.HasPrefix(exe, "parakeet-server") {
			return asrParakeet
		}
	}
	return asrPassthrough
}

func (s *Server) handleTranscription(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := shared.FetchContext(r, s.config())
		if err != nil || !strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			next.ServeHTTP(w, r)
			return
		}
		// The dispatcher owns the 403/404 answers and peers adapt for themselves.
		if models, scoped := listenerModelSet(r); scoped && !models[data.ModelID] {
			next.ServeHTTP(w, r)
			return
		}
		if models, scoped := apiKeyModelSet(r); scoped && !models[data.ModelID] {
			next.ServeHTTP(w, r)
			return
		}
		mc, ok := s.config().Models[data.ModelID]
		engine := asrEngineOf(mc)
		if !ok || engine == asrPassthrough || !s.local.Handles(data.ModelID) {
			next.ServeHTTP(w, r)
			return
		}

		if err := r.ParseMultipartForm(32 << 20); err != nil {
			shared.SendResponse(w, r, http.StatusBadRequest, "invalid multipart body: "+err.Error())
			return
		}
		defer r.MultipartForm.RemoveAll()
		form := r.MultipartForm
		format := formValue(form, "response_format")
		words, segments := timestampGranularities(form)

		if engine == asrAudioCpp {
			if format != "verbose_json" || (!words && !segments) {
				s.forwardTranscriptionForm(next, w, r, form, nil, nil)
				return
			}
			if mc.AudioCpp.Family == "qwen3_asr" && mc.AudioCpp.SessionOptions["qwen3_asr.forced_aligner_path"] == "" {
				shared.SendResponse(w, r, http.StatusBadRequest, fmt.Sprintf("model %q has no forced aligner, so it cannot return timestamps: download Qwen3-ForcedAligner-0.6B (GGUF) into a models folder and regenerate the config", data.Model))
				return
			}
			s.transcribeWithTimestamps(next, w, r, data.ModelID, form, words)
			return
		}
		s.transcribeParakeet(next, w, r, form, format, words)
	})
}

// transcribeWithTimestamps runs one audio.cpp /details transcription with
// return_timestamps and answers in OpenAI's verbose_json.
func (s *Server) transcribeWithTimestamps(next http.Handler, w http.ResponseWriter, r *http.Request, modelID string, form *multipart.Form, wantWords bool) {
	audio, err := readTranscriptionFile(form)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	wav, err := toASRInput(audio)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "word timestamps need a WAV upload: "+err.Error())
		return
	}
	path, err := stageMusicInput(wav)
	if err != nil {
		s.proxylog.Errorf("transcribe: model %s: staging audio: %v", modelID, err)
		shared.SendResponse(w, r, http.StatusInternalServerError, "could not stage the audio: "+err.Error())
		return
	}
	defer os.Remove(path)

	req := map[string]any{
		// The id the materialized audio.cpp config names (audiocppconfig.go).
		"model":      modelID,
		"audio_path": path,
		// Fixed chunks, not the default: with return_timestamps audio.cpp's
		// "auto" mode chunks by Silero VAD, a speech detector that drops sung
		// passages (a 167 s song kept 11 of 113 words). Fixed 15 s windows keep
		// everything, though a word on a window edge can still go missing.
		"options": map[string]string{"return_timestamps": "true", "audio_chunk_mode": "fixed"},
	}
	if v := formValue(form, "language"); v != "" {
		req["language"] = v
	}
	if v := formValue(form, "prompt"); v != "" {
		req["text"] = v
	}
	body, _ := json.Marshal(req)

	up := r.Clone(r.Context())
	up.URL.Path = "/v1/audio/transcriptions/details"
	up.URL.RawPath = ""
	up.MultipartForm = nil
	up.Body = io.NopCloser(bytes.NewReader(body))
	up.ContentLength = int64(len(body))
	up.Header.Set("Content-Type", "application/json")
	up.Header.Del("Content-Length")

	buf := newBufferedResponseWriterLimit(asrResponseLimit)
	next.ServeHTTP(buf, up)
	if r.Context().Err() != nil {
		return
	}
	if buf.over {
		shared.SendResponse(w, r, http.StatusBadGateway, "upstream response too large")
		return
	}
	if buf.code >= http.StatusBadRequest {
		buf.flushTo(w)
		return
	}
	out, err := verboseFromDetails(buf.body.Bytes(), formValue(form, "language"), wantWords)
	if err != nil {
		s.proxylog.Errorf("transcribe: model %s: %v", modelID, err)
		shared.SendResponse(w, r, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// transcribeParakeet forwards a Parakeet request with its audio at 16 kHz mono,
// and rebuilds a verbose_json reply's segments from its words.
func (s *Server) transcribeParakeet(next http.Handler, w http.ResponseWriter, r *http.Request, form *multipart.Form, format string, wantWords bool) {
	audio, err := readTranscriptionFile(form)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	wav, err := toASRInput(audio)
	if err != nil {
		shared.SendResponse(w, r, http.StatusBadRequest, "parakeet-server accepts WAV only: "+err.Error())
		return
	}
	var set map[string][]string
	if format == "verbose_json" {
		set = map[string][]string{"timestamp_granularities[]": {"word", "segment"}}
	}
	buf := newBufferedResponseWriterLimit(asrResponseLimit)
	s.forwardTranscriptionForm(next, buf, r, form, wav, set)
	if r.Context().Err() != nil {
		return
	}
	switch {
	case buf.over:
		shared.SendResponse(w, r, http.StatusBadGateway, "upstream response too large")
	case buf.code == http.StatusBadGateway && buf.body.Len() == 0:
		shared.SendResponse(w, r, http.StatusBadGateway, "parakeet-server closed the connection without a reply; it may have crashed, see the model's log")
	case buf.code < http.StatusBadRequest && format == "verbose_json":
		out, err := resegmentVerbose(buf.body.Bytes(), wantWords)
		if err != nil {
			buf.flushTo(w) // not the shape expected: hand it over as it came
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	default:
		buf.flushTo(w)
	}
}

// forwardTranscriptionForm re-encodes the parsed form (ParseMultipartForm
// consumed the body) and dispatches it. audio, when set, replaces the file
// part; set replaces whole fields.
func (s *Server) forwardTranscriptionForm(next http.Handler, w http.ResponseWriter, r *http.Request, form *multipart.Form, audio []byte, set map[string][]string) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	writeField := func(k, v string) error {
		f, err := mw.CreateFormField(k)
		if err == nil {
			_, err = f.Write([]byte(v))
		}
		return err
	}
	fail := func(err error) {
		shared.SendResponse(w, r, http.StatusInternalServerError, "could not re-encode the request: "+err.Error())
	}
	for k, vs := range form.Value {
		if _, ok := set[k]; ok {
			continue
		}
		for _, v := range vs {
			if err := writeField(k, v); err != nil {
				fail(err)
				return
			}
		}
	}
	for k, vs := range set {
		for _, v := range vs {
			if err := writeField(k, v); err != nil {
				fail(err)
				return
			}
		}
	}
	for k, fhs := range form.File {
		for _, fh := range fhs {
			part, err := mw.CreateFormFile(k, fh.Filename)
			if err != nil {
				fail(err)
				return
			}
			if k == "file" && audio != nil {
				_, err = part.Write(audio)
			} else {
				var f multipart.File
				if f, err = fh.Open(); err == nil {
					_, err = io.Copy(part, f)
					f.Close()
				}
			}
			if err != nil {
				fail(err)
				return
			}
		}
	}
	if err := mw.Close(); err != nil {
		fail(err)
		return
	}
	up := r.Clone(r.Context())
	up.MultipartForm = nil
	up.Body = io.NopCloser(bytes.NewReader(b.Bytes()))
	up.ContentLength = int64(b.Len())
	up.Header.Set("Content-Type", mw.FormDataContentType())
	up.Header.Del("Content-Length")
	up.Header.Del("Transfer-Encoding")
	next.ServeHTTP(w, up)
}

func formValue(f *multipart.Form, k string) string {
	if v := f.Value[k]; len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}

// timestampGranularities reads OpenAI's timestamp_granularities, which clients
// send as repeated "timestamp_granularities[]" parts (or without the brackets).
func timestampGranularities(f *multipart.Form) (words, segments bool) {
	for _, k := range []string{"timestamp_granularities[]", "timestamp_granularities"} {
		for _, v := range f.Value[k] {
			for _, g := range strings.Split(v, ",") {
				switch strings.ToLower(strings.TrimSpace(g)) {
				case "word":
					words = true
				case "segment":
					segments = true
				}
			}
		}
	}
	return words, segments
}

func readTranscriptionFile(f *multipart.Form) ([]byte, error) {
	fhs := f.File["file"]
	if len(fhs) == 0 {
		return nil, fmt.Errorf("missing the audio file part \"file\"")
	}
	if fhs[0].Size > maxMusicInputBytes {
		return nil, fmt.Errorf("audio exceeds %d MB", maxMusicInputBytes>>20)
	}
	file, err := fhs[0].Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maxMusicInputBytes))
}

// toASRInput returns a WAV as 16 kHz mono PCM16, untouched when it already is.
func toASRInput(b []byte) ([]byte, error) {
	info, err := parseWAV(b)
	if err != nil {
		return nil, err
	}
	if info.sampleRate == asrInputRate && info.channels == 1 && info.format == wavFormatPCM && info.bitsPerSample == 16 {
		return b, nil
	}
	samples, err := decodeWAV(info)
	if err != nil {
		return nil, err
	}
	mono := downmix(samples, info.channels)
	if info.sampleRate != asrInputRate {
		mono = resample(mono, 1, info.sampleRate, asrInputRate)
	}
	return encodePCM16WAV(mono, asrInputRate), nil
}

// downmix averages interleaved channels into one.
func downmix(in []float32, channels int) []float32 {
	if channels <= 1 {
		return in
	}
	out := make([]float32, len(in)/channels)
	for i := range out {
		var sum float32
		for c := 0; c < channels; c++ {
			sum += in[i*channels+c]
		}
		out[i] = sum / float32(channels)
	}
	return out
}

// encodePCM16WAV writes mono samples as 16-bit PCM, the encoding every WAV
// reader takes; a float WAV is not a safe bet for parakeet-server.
func encodePCM16WAV(samples []float32, rate int) []byte {
	dataLen := len(samples) * 2
	var b bytes.Buffer
	b.Grow(44 + dataLen)
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + dataLen))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(wavFormatPCM))
	w(uint16(1))
	w(uint32(rate))
	w(uint32(rate * 2))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(dataLen))
	buf := make([]byte, dataLen)
	for i, v := range samples {
		v = max(-1, min(1, v))
		binary.LittleEndian.PutUint16(buf[2*i:], uint16(int16(math.Round(float64(v)*32767))))
	}
	b.Write(buf)
	return b.Bytes()
}

// asrWord and asrSegment are OpenAI verbose_json's word and segment objects.
type asrWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type asrSegment struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type asrVerbose struct {
	Task     string       `json:"task"`
	Language string       `json:"language,omitempty"`
	Duration float64      `json:"duration"`
	Text     string       `json:"text"`
	Words    []asrWord    `json:"words,omitempty"`
	Segments []asrSegment `json:"segments"`
}

// verboseFromDetails translates audio.cpp's /details reply (word spans in
// sample offsets at sample_rate) into OpenAI verbose_json (seconds).
func verboseFromDetails(body []byte, language string, wantWords bool) ([]byte, error) {
	var d struct {
		Text       string `json:"text"`
		Language   string `json:"language"`
		SampleRate int    `json:"sample_rate"`
		Words      []struct {
			Word        string `json:"word"`
			StartSample int64  `json:"start_sample"`
			EndSample   int64  `json:"end_sample"`
		} `json:"words"`
		Timing struct {
			AudioDurationMs float64 `json:"audio_duration_ms"`
		} `json:"timing"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("unreadable transcription details: %w", err)
	}
	rate := d.SampleRate
	if rate <= 0 {
		rate = asrInputRate // the rate this route sent
	}
	words := make([]asrWord, 0, len(d.Words))
	for _, w := range d.Words {
		words = append(words, asrWord{
			Word:  w.Word,
			Start: round3(float64(w.StartSample) / float64(rate)),
			End:   round3(float64(w.EndSample) / float64(rate)),
		})
	}
	v := asrVerbose{
		Task:     "transcribe",
		Language: d.Language,
		Duration: round3(d.Timing.AudioDurationMs / 1000),
		Text:     d.Text,
		Segments: segmentWords(words, asrSegmentGap),
	}
	if v.Language == "" {
		v.Language = language
	}
	if wantWords {
		v.Words = words
	}
	return json.Marshal(v)
}

// resegmentVerbose rebuilds a verbose_json reply's segments from its words,
// keeping every other field, and drops the words the caller did not ask for.
// An error means the body is not the shape expected.
func resegmentVerbose(body []byte, wantWords bool) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	raw, ok := doc["words"]
	if !ok {
		return nil, fmt.Errorf("no words")
	}
	var words []asrWord
	if err := json.Unmarshal(raw, &words); err != nil {
		return nil, err
	}
	if len(words) > 0 {
		seg, _ := json.Marshal(segmentWords(words, asrSegmentGap))
		doc["segments"] = seg
	}
	if !wantWords {
		delete(doc, "words")
	}
	return json.Marshal(doc)
}

// segmentWords groups words into segments, starting a new one wherever the
// silence before a word is at least gap seconds.
func segmentWords(words []asrWord, gap float64) []asrSegment {
	segs := []asrSegment{}
	var text []string
	flush := func() {
		if len(text) > 0 {
			segs[len(segs)-1].Text = strings.Join(text, " ")
			text = text[:0]
		}
	}
	for i, w := range words {
		if i == 0 || w.Start-words[i-1].End >= gap {
			flush()
			segs = append(segs, asrSegment{ID: len(segs), Start: w.Start})
		}
		segs[len(segs)-1].End = w.End
		if t := strings.TrimSpace(w.Word); t != "" {
			text = append(text, t)
		}
	}
	flush()
	return segs
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
