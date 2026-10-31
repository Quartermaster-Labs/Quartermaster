package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

func TestAsrEngineOf(t *testing.T) {
	cases := []struct {
		name string
		mc   config.ModelConfig
		want asrEngine
	}{
		{"audio.cpp asr", config.ModelConfig{AudioCpp: config.AudioCppConfig{Family: "qwen3_asr", Task: "asr"}}, asrAudioCpp},
		{"audio.cpp tts", config.ModelConfig{AudioCpp: config.AudioCppConfig{Family: "kokoro", Task: "tts"}}, asrPassthrough},
		// Forward slashes: off Windows the cmd splits as POSIX shell words, where
		// a backslash is an escape and E:\Apps\... would lose its separators.
		{"parakeet", config.ModelConfig{Cmd: "E:/Apps/parakeet-v0.4.0/parakeet-server.exe\n--model D:/m.gguf\n--port 5000"}, asrParakeet},
		{"other", config.ModelConfig{Cmd: "whisper-server --model m.bin"}, asrPassthrough},
	}
	for _, c := range cases {
		if got := asrEngineOf(c.mc); got != c.want {
			t.Errorf("%s: asrEngineOf = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestToASRInput_DownmixesAndResamples(t *testing.T) {
	// One second of 48 kHz stereo: left +0.5, right -0.5 cancels to silence.
	samples := make([]int16, 2*48000)
	for i := 0; i < len(samples); i += 2 {
		samples[i], samples[i+1] = 16384, -16384
	}
	out, err := toASRInput(pcm16WAV(samples, 2, 48000))
	if err != nil {
		t.Fatal(err)
	}
	info, err := parseWAV(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.sampleRate != 16000 || info.channels != 1 || info.bitsPerSample != 16 || info.format != wavFormatPCM {
		t.Fatalf("got %d Hz x%d %d-bit fmt %d, want 16 kHz mono PCM16", info.sampleRate, info.channels, info.bitsPerSample, info.format)
	}
	if n := len(info.data) / 2; n < 15990 || n > 16010 {
		t.Errorf("got %d samples, want ~16000", n)
	}

	already := pcm16WAV(make([]int16, 160), 1, 16000)
	if got, _ := toASRInput(already); !bytes.Equal(got, already) {
		t.Error("a 16 kHz mono PCM16 file was re-encoded")
	}
	if _, err := toASRInput([]byte("ID3 not a wav")); err == nil {
		t.Error("a non-WAV upload was accepted")
	}
}

func TestSegmentWords_SplitsOnGaps(t *testing.T) {
	words := []asrWord{
		{"Cold", 12.0, 12.4}, {"and", 12.45, 12.6}, {"lonely", 12.7, 13.5},
		{"Ash", 59.0, 59.3}, {"into", 59.35, 59.6}, {"bloom", 59.65, 60.4},
	}
	segs := segmentWords(words, 0.8)
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(segs), segs)
	}
	if segs[0].Text != "Cold and lonely" || segs[0].Start != 12.0 || segs[0].End != 13.5 {
		t.Errorf("segment 0 = %+v", segs[0])
	}
	if segs[1].ID != 1 || segs[1].Text != "Ash into bloom" || segs[1].Start != 59.0 || segs[1].End != 60.4 {
		t.Errorf("segment 1 = %+v", segs[1])
	}
	if got := segmentWords(nil, 0.8); got == nil || len(got) != 0 {
		t.Errorf("no words = %v, want an empty (non-null) list", got)
	}
}

func TestVerboseFromDetails_SamplesToSeconds(t *testing.T) {
	details := `{"text":"Cold and lonely.","language":"English",
		"words":[{"word":"Cold","start_sample":192000,"end_sample":198400,"confidence":0.9},
		         {"word":"and","start_sample":199200,"end_sample":201600,"confidence":0.9}],
		"sample_rate":16000,"timing":{"wall_ms":800,"audio_duration_ms":167040,"rtf":0.005}}`
	out, err := verboseFromDetails([]byte(details), "", true)
	if err != nil {
		t.Fatal(err)
	}
	var v asrVerbose
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if v.Text != "Cold and lonely." || v.Language != "English" || v.Duration != 167.04 || v.Task != "transcribe" {
		t.Errorf("header fields = %+v", v)
	}
	if len(v.Words) != 2 || v.Words[0] != (asrWord{"Cold", 12, 12.4}) || v.Words[1] != (asrWord{"and", 12.45, 12.6}) {
		t.Errorf("words = %+v", v.Words)
	}
	if len(v.Segments) != 1 || v.Segments[0].Text != "Cold and" {
		t.Errorf("segments = %+v", v.Segments)
	}

	// Segment granularity alone: no words key at all, and the request's
	// language stands in when the model reports none.
	out, _ = verboseFromDetails([]byte(`{"text":"x","words":[{"word":"x","start_sample":0,"end_sample":8000}],"timing":{}}`), "en", false)
	if strings.Contains(string(out), `"words"`) || !strings.Contains(string(out), `"language":"en"`) {
		t.Errorf("segment-only reply = %s", out)
	}
}

func TestResegmentVerbose_KeepsOtherFields(t *testing.T) {
	in := `{"text":"a b","duration":3,"x_extra":1,"segments":[{"id":0,"start":0,"end":3,"text":"a b"}],
		"words":[{"word":"a","start":0,"end":0.5},{"word":"b","start":2,"end":3}]}`
	out, err := resegmentVerbose([]byte(in), false)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(out, &doc)
	if _, ok := doc["x_extra"]; !ok {
		t.Error("dropped an unrelated field")
	}
	if _, ok := doc["words"]; ok {
		t.Error("kept words the caller did not ask for")
	}
	var segs []asrSegment
	_ = json.Unmarshal(doc["segments"], &segs)
	if len(segs) != 2 {
		t.Errorf("segments = %+v, want 2", segs)
	}
	if _, err := resegmentVerbose([]byte(`{"text":"no words"}`), false); err == nil {
		t.Error("a reply without words was not reported as unexpected")
	}
}

// transcriptionRequest builds a multipart transcription POST with a parsed form.
func transcriptionRequest(t *testing.T, wav []byte, fields map[string][]string) (*http.Request, *multipart.Form) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, vs := range fields {
		for _, v := range vs {
			_ = mw.WriteField(k, v)
		}
	}
	fw, _ := mw.CreateFormFile("file", "song.wav")
	_, _ = fw.Write(wav)
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &b)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		t.Fatal(err)
	}
	return r, r.MultipartForm
}

func TestTranscribeWithTimestamps_SendsDetailsJSON(t *testing.T) {
	tmp := t.TempDir()
	prev := musicTmpDir
	musicTmpDir = func() string { return tmp }
	t.Cleanup(func() { musicTmpDir = prev })

	r, form := transcriptionRequest(t, pcm16WAV(make([]int16, 2*48000), 2, 48000), map[string][]string{
		"model": {"qwen3-asr"}, "response_format": {"verbose_json"}, "timestamp_granularities[]": {"word"}, "language": {"English"},
	})
	var staged string
	next := http.HandlerFunc(func(w http.ResponseWriter, up *http.Request) {
		if up.URL.Path != "/v1/audio/transcriptions/details" || up.Header.Get("Content-Type") != "application/json" {
			t.Errorf("forwarded to %s (%s)", up.URL.Path, up.Header.Get("Content-Type"))
		}
		var body struct {
			Model     string            `json:"model"`
			AudioPath string            `json:"audio_path"`
			Language  string            `json:"language"`
			Options   map[string]string `json:"options"`
		}
		raw, _ := io.ReadAll(up.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "qwen3-asr-id" || body.Language != "English" || body.Options["return_timestamps"] != "true" || body.Options["audio_chunk_mode"] != "fixed" {
			t.Errorf("upstream body = %s", raw)
		}
		staged = body.AudioPath
		wav, err := os.ReadFile(staged)
		if err != nil {
			t.Fatal(err)
		}
		if info, _ := parseWAV(wav); info.sampleRate != 16000 || info.channels != 1 {
			t.Errorf("staged %d Hz x%d, want 16 kHz mono", info.sampleRate, info.channels)
		}
		_, _ = w.Write([]byte(`{"text":"hi","words":[{"word":"hi","start_sample":8000,"end_sample":16000}],"sample_rate":16000,"timing":{"audio_duration_ms":1000}}`))
	})
	rec := httptest.NewRecorder()
	(&Server{}).transcribeWithTimestamps(next, rec, r, "qwen3-asr-id", form, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var v asrVerbose
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if len(v.Words) != 1 || v.Words[0] != (asrWord{"hi", 0.5, 1}) || v.Duration != 1 {
		t.Errorf("reply = %s", rec.Body)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("staged input %s was not removed", staged)
	}
}

func TestTranscribeParakeet_NormalizesAndResegments(t *testing.T) {
	r, form := transcriptionRequest(t, pcm16WAV(make([]int16, 2*48000), 2, 48000), map[string][]string{
		"model": {"tdt"}, "response_format": {"verbose_json"},
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, up *http.Request) {
		if err := up.ParseMultipartForm(32 << 20); err != nil {
			t.Fatal(err)
		}
		if g := up.MultipartForm.Value["timestamp_granularities[]"]; len(g) != 2 {
			t.Errorf("granularities forwarded = %v, want word+segment", g)
		}
		f, _ := up.MultipartForm.File["file"][0].Open()
		wav, _ := io.ReadAll(f)
		f.Close()
		if info, _ := parseWAV(wav); info.sampleRate != 16000 || info.channels != 1 {
			t.Errorf("forwarded %d Hz x%d, want 16 kHz mono", info.sampleRate, info.channels)
		}
		_, _ = w.Write([]byte(`{"text":"a b","segments":[{"id":0,"start":0,"end":9,"text":"a b"}],"words":[{"word":"a","start":0,"end":1},{"word":"b","start":5,"end":9}]}`))
	})
	rec := httptest.NewRecorder()
	(&Server{}).transcribeParakeet(next, rec, r, form, "verbose_json", false)
	if !strings.Contains(rec.Body.String(), `"text":"b"`) || strings.Contains(rec.Body.String(), `"words"`) {
		t.Errorf("reply = %s", rec.Body)
	}

	// A dropped connection is named instead of passed on as an empty 502.
	r, form = transcriptionRequest(t, pcm16WAV(make([]int16, 160), 1, 16000), map[string][]string{"model": {"tdt"}})
	rec = httptest.NewRecorder()
	(&Server{}).transcribeParakeet(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}), rec, r, form, "", false)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "without a reply") {
		t.Errorf("empty 502 -> %d %q", rec.Code, rec.Body)
	}
}
