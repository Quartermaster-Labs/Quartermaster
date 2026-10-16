package server

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pcm16WAV(samples []int16, channels, rate int) []byte {
	data := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(data[2*i:], uint16(v))
	}
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(data)))
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], wavFormatPCM)
	binary.LittleEndian.PutUint16(hdr[22:], uint16(channels))
	binary.LittleEndian.PutUint32(hdr[24:], uint32(rate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(hdr[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(data)))
	return append(hdr, data...)
}

func TestMusicWAV_ParseDecodeRoundTrip(t *testing.T) {
	in := []int16{0, 16384, -16384, 32767, -32768}
	info, err := parseWAV(pcm16WAV(in, 1, 48000))
	if err != nil {
		t.Fatal(err)
	}
	if info.channels != 1 || info.sampleRate != 48000 || info.bitsPerSample != 16 {
		t.Fatalf("header = %+v", info)
	}
	got, err := decodeWAV(info)
	if err != nil {
		t.Fatal(err)
	}
	want := []float32{0, 0.5, -0.5, 32767.0 / 32768, -1}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}

	// Float WAV written here reads back bit-exact.
	f := encodeFloatWAV(want, 1, 44100)
	info, err = parseWAV(f)
	if err != nil {
		t.Fatal(err)
	}
	back, err := decodeWAV(info)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if back[i] != want[i] {
			t.Errorf("float sample %d = %v, want %v", i, back[i], want[i])
		}
	}
}

func TestMusicWAV_RejectsNonWAV(t *testing.T) {
	if _, err := parseWAV([]byte("ID3\x03 an mp3, really")); err == nil {
		t.Error("an mp3 parsed as WAV")
	}
}

func TestMusicWAV_Resample(t *testing.T) {
	const inRate, outRate, hz = 48000, 44100, 1000.0
	frames := inRate // one second
	in := make([]float32, 2*frames)
	for i := 0; i < frames; i++ {
		v := float32(0.5 * math.Sin(2*math.Pi*hz*float64(i)/inRate))
		in[2*i], in[2*i+1] = v, 0.25 // L: tone, R: DC
	}
	out := resample(in, 2, inRate, outRate)
	if len(out) != 2*outRate {
		t.Fatalf("len = %d, want %d", len(out), 2*outRate)
	}
	// Away from the edges: the tone keeps its frequency and amplitude, DC its level.
	var maxErr float64
	for o := 200; o < outRate-200; o++ {
		want := 0.5 * math.Sin(2*math.Pi*hz*float64(o)/outRate)
		maxErr = math.Max(maxErr, math.Abs(float64(out[2*o])-want))
		if d := math.Abs(float64(out[2*o+1]) - 0.25); d > 1e-4 {
			t.Fatalf("DC channel drifted to %v at %d", out[2*o+1], o)
		}
	}
	if maxErr > 1e-3 {
		t.Errorf("tone error %v, want < 1e-3", maxErr)
	}
}

func TestMusicRouteMismatch(t *testing.T) {
	cases := []struct {
		route        musicRoute
		family, task string
		ok           bool
	}{
		{musicGenerate, "ace_step", "gen", true},
		{musicGenerate, "minimax_music3", "gen", true},
		{musicGenerate, "kokoro", "tts", false},
		{musicEdit, "ace_step", "gen", true},
		{musicEdit, "minimax_music3", "gen", false},
		{musicSeparate, "htdemucs", "sep", true},
		{musicSeparate, "ace_step", "gen", false},
	}
	for _, c := range cases {
		if got := musicRouteMismatch(c.route, c.family, c.task) == ""; got != c.ok {
			t.Errorf("route %d family %s: ok = %v, want %v", c.route, c.family, got, c.ok)
		}
	}
}

func upstreamDoc(t *testing.T, body []byte) (string, map[string]any, map[string]string) {
	t.Helper()
	var doc struct {
		Model   string         `json:"model"`
		Request map[string]any `json:"request"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	opts := map[string]string{}
	for k, v := range doc.Request["options"].(map[string]any) {
		opts[k] = v.(string)
	}
	return doc.Model, doc.Request, opts
}

func ptr[T any](v T) *T { return &v }

func TestBuildMusicUpstream_Generate(t *testing.T) {
	seed, body, cleanup, err := buildMusicUpstream(musicGenerate, "ace_step", musicRequest{
		Model: "acestep", Prompt: "lofi piano", Lyrics: ptr("[Instrumental]"),
		Duration: ptr(30.0), Seed: ptr(int64(1234)), Steps: ptr(8), Guidance: ptr(1.0),
		Options: map[string]any{"bpm": 90.0, "keyscale": "C minor", "complete_track_classes": []any{"drums", "bass"}},
	})
	if err != nil || cleanup != nil {
		t.Fatalf("err=%v cleanup=%v", err, cleanup != nil)
	}
	if seed != 1234 {
		t.Errorf("seed = %d", seed)
	}
	model, req, opts := upstreamDoc(t, body)
	if model != "acestep" || req["text"] != "lofi piano" {
		t.Errorf("model=%q text=%v", model, req["text"])
	}
	if _, has := req["audio"]; has {
		t.Error("a generation sent an audio path")
	}
	want := map[string]string{
		"seed": "1234", "lyrics": "[Instrumental]", "duration_seconds": "30",
		"num_inference_steps": "8", "guidance_scale": "1", "route": "text2music",
		"bpm": "90", "keyscale": "C minor", "complete_track_classes": `["drums","bass"]`,
	}
	for k, v := range want {
		if opts[k] != v {
			t.Errorf("option %s = %q, want %q", k, opts[k], v)
		}
	}
}

func TestBuildMusicUpstream_RandomSeedIsExplicit(t *testing.T) {
	seed, body, _, err := buildMusicUpstream(musicGenerate, "ace_step", musicRequest{Model: "m", Prompt: "x", Seed: ptr(int64(-1))})
	if err != nil {
		t.Fatal(err)
	}
	_, _, opts := upstreamDoc(t, body)
	if opts["seed"] == "" || opts["seed"] != jsonUint(seed) {
		t.Errorf("seed option %q, echoed %d", opts["seed"], seed)
	}
	if seed > math.MaxUint32 {
		t.Errorf("random seed %d does not fit ACE-Step's u32", seed)
	}
}

func jsonUint(v uint64) string { b, _ := json.Marshal(v); return string(b) }

func TestBuildMusicUpstream_Rejects(t *testing.T) {
	cases := []struct {
		name   string
		route  musicRoute
		family string
		req    musicRequest
	}{
		{"path option", musicGenerate, "ace_step", musicRequest{Prompt: "x", Options: map[string]any{"noise_file": "C:/Windows/win.ini"}}},
		{"source_audio option", musicGenerate, "ace_step", musicRequest{Prompt: "x", Options: map[string]any{"source_audio": "/etc/passwd"}}},
		{"no prompt", musicGenerate, "ace_step", musicRequest{}},
		{"minimax without lyrics", musicGenerate, "minimax_music3", musicRequest{Prompt: "x"}},
		{"ace seed past u32", musicGenerate, "ace_step", musicRequest{Prompt: "x", Seed: ptr(int64(1 << 33))}},
		{"bad format", musicGenerate, "ace_step", musicRequest{Prompt: "x", ResponseFormat: "mp3"}},
		{"edit without audio", musicEdit, "ace_step", musicRequest{Prompt: "x", Task: "cover"}},
		{"repaint without window", musicEdit, "ace_step", musicRequest{Prompt: "x", Task: "repaint", audioBytes: pcm16WAV(make([]int16, 8), 1, 48000)}},
		{"inverted window", musicEdit, "ace_step", musicRequest{Prompt: "x", Task: "repaint", Start: ptr(20.0), End: ptr(10.0), audioBytes: pcm16WAV(make([]int16, 8), 1, 48000)}},
		{"unknown task", musicEdit, "ace_step", musicRequest{Prompt: "x", Task: "lego"}},
		{"separation of an mp3", musicSeparate, "htdemucs", musicRequest{audioBytes: []byte("ID3 not a wav")}},
	}
	for _, c := range cases {
		_, _, cleanup, err := buildMusicUpstream(c.route, c.family, c.req)
		if cleanup != nil {
			cleanup()
		}
		var me musicError
		if err == nil {
			t.Errorf("%s: accepted", c.name)
		} else if !errorsAs(err, &me) {
			t.Errorf("%s: %v is not a caller error (would be a 500)", c.name, err)
		}
	}
}

func errorsAs(err error, me *musicError) bool {
	v, ok := err.(musicError)
	if ok {
		*me = v
	}
	return ok
}

func withMusicTmp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := musicTmpDir
	musicTmpDir = func() string { return dir }
	t.Cleanup(func() { musicTmpDir = old })
	return dir
}

func TestBuildMusicUpstream_RepaintStagesAndCleans(t *testing.T) {
	dir := withMusicTmp(t)
	wav := pcm16WAV(make([]int16, 4800), 1, 48000)
	_, body, cleanup, err := buildMusicUpstream(musicEdit, "ace_step", musicRequest{
		Model: "acestep", Prompt: "rock", Task: "repaint", Start: ptr(10.0), End: ptr(20.0),
		RepaintMode: "balanced", RepaintStrength: ptr(0.7), Seed: ptr(int64(7)), audioBytes: wav,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, req, opts := upstreamDoc(t, body)
	path, _ := req["audio"].(string)
	if !filepath.IsAbs(path) || filepath.Dir(path) != dir {
		t.Fatalf("audio path %q is not an absolute path in the staging dir %q", path, dir)
	}
	// ACE-Step resamples its own input, so the upload is passed through untouched.
	if got, err := os.ReadFile(path); err != nil || string(got) != string(wav) {
		t.Errorf("staged file differs from the upload (err %v)", err)
	}
	for k, v := range map[string]string{"route": "repaint", "repainting_start": "10", "repainting_end": "20", "repaint_mode": "balanced", "repaint_strength": "0.7", "seed": "7"} {
		if opts[k] != v {
			t.Errorf("option %s = %q, want %q", k, opts[k], v)
		}
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("staged input survived cleanup: %v", err)
	}
}

func TestBuildMusicUpstream_SeparationResamplesTo44k(t *testing.T) {
	withMusicTmp(t)
	_, body, cleanup, err := buildMusicUpstream(musicSeparate, "htdemucs", musicRequest{
		Model: "htdemucs", audioBytes: pcm16WAV(make([]int16, 2*48000), 2, 48000),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	_, req, opts := upstreamDoc(t, body)
	if _, has := opts["seed"]; has {
		t.Error("a separation sent a seed")
	}
	if _, has := req["text"]; has {
		t.Error("a separation sent text")
	}
	b, err := os.ReadFile(req["audio"].(string))
	if err != nil {
		t.Fatal(err)
	}
	info, err := parseWAV(b)
	if err != nil {
		t.Fatal(err)
	}
	if info.sampleRate != 44100 || info.channels != 2 || len(info.data)/8 != 44100 {
		t.Errorf("staged %d Hz x%d, %d frames; want 44100 Hz x2, 44100 frames", info.sampleRate, info.channels, len(info.data)/8)
	}
}

func TestWriteMusicResponse(t *testing.T) {
	wav := pcm16WAV([]int16{1, 2, 3}, 1, 48000)
	b64 := base64.StdEncoding.EncodeToString(wav)
	gen, _ := json.Marshal(map[string]any{"audio": b64, "sample_rate": 48000, "channels": 2})

	rec := httptest.NewRecorder()
	writeMusicResponse(rec, httptest.NewRequest(http.MethodPost, "/", nil), musicGenerate, "", 42, gen)
	if rec.Header().Get("Content-Type") != "audio/wav" || rec.Header().Get("X-Seed") != "42" || rec.Body.String() != string(wav) {
		t.Errorf("wav reply: ct=%q seed=%q body len %d", rec.Header().Get("Content-Type"), rec.Header().Get("X-Seed"), rec.Body.Len())
	}

	rec = httptest.NewRecorder()
	writeMusicResponse(rec, httptest.NewRequest(http.MethodPost, "/", nil), musicGenerate, "b64_json", 42, gen)
	var j map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil || j["audio"] != b64 || j["seed"] != 42.0 {
		t.Errorf("b64_json reply = %s (err %v)", rec.Body.String(), err)
	}

	sep, _ := json.Marshal(map[string]any{"named_audio_outputs": []map[string]any{
		{"id": "drums", "audio": b64, "sample_rate": 44100, "channels": 2},
		{"id": "vocals", "audio": b64, "sample_rate": 44100, "channels": 2},
	}})
	rec = httptest.NewRecorder()
	writeMusicResponse(rec, httptest.NewRequest(http.MethodPost, "/", nil), musicSeparate, "", 0, sep)
	var s struct{ Stems []musicStem }
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil || len(s.Stems) != 2 || s.Stems[1].Name != "vocals" || s.Stems[0].SampleRate != 44100 {
		t.Errorf("separation reply = %s (err %v)", rec.Body.String(), err)
	}
	if strings.Contains(rec.Header().Get("X-Seed"), "0") {
		t.Error("separation echoed a seed")
	}
}
