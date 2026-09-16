package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/audiocpp"
	"github.com/quartermaster-labs/quartermaster/internal/backends"
	"github.com/quartermaster-labs/quartermaster/internal/hub"
)

type catalogResp struct {
	Installed bool   `json:"installed"`
	SpecsDir  string `json:"specsDir"`
	Families  []struct {
		audiocpp.Family
		Task      string `json:"task"`
		Supported bool   `json:"supported"`
		Reason    string `json:"reason"`
		Packages  []struct {
			audiocpp.Package
			Local     bool  `json:"local"`
			SizeBytes int64 `json:"sizeBytes"`
		} `json:"packages"`
	} `json:"families"`
}

// spec bodies, trimmed to the fields the reader uses. moss_tts_nano and ace_step
// are both real families in internal/autogen's table: one served as tts, one
// known and deliberately not served (music).
const (
	specTTS = `{"family":"moss_tts_nano","display_name":"MossTTS Nano","category":"tts",
	  "package_defaults":{"download":{"kind":"huggingface_snapshot","repo":"audio-cpp/audio.cpp-gguf"}},
	  "packages":[{"id":"q8","display_name":"Q8_0","default":true,"format":"gguf","files":["Moss/moss-q8_0.gguf"]},
	              {"id":"bf16","display_name":"BF16","format":"gguf","files":["Moss/moss-bf16.gguf"]}]}`
	specMusic = `{"family":"ace_step","display_name":"ACE-Step","category":"audio_generation",
	  "package_defaults":{"download":{"kind":"huggingface_snapshot","repo":"audio-cpp/audio.cpp-gguf"}},
	  "packages":[{"id":"turbo","display_name":"Turbo","format":"gguf","files":["Ace/turbo.gguf"]}]}`
	specUnknown = `{"family":"not_a_real_family_yet","display_name":"Newer Than Us","category":"tts",
	  "package_defaults":{"download":{"kind":"huggingface_snapshot","repo":"audio-cpp/audio.cpp-gguf"}},
	  "packages":[{"id":"q8","display_name":"Q8_0","format":"gguf","files":["New/new-q8_0.gguf"]}]}`
)

// The catalog is read out of the INSTALLED backend, annotated with what this
// build would actually do with each family, and told which packages are already
// on disk. All three are the reasons it is not just a proxied file listing.
func TestHandleAPIHubAudioCpp_CatalogFromInstall(t *testing.T) {
	gen := newGenerateFile(t)
	backendRoot := t.TempDir()
	exe := fakeInstall(t, backendRoot, "audiocpp-server", "v0.8.0", "vulkan", exeName("audiocpp_server"))
	specs := filepath.Join(filepath.Dir(exe), audiocpp.DirName)
	if err := os.MkdirAll(specs, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"moss_tts_nano.json": specTTS,
		"ace_step.json":      specMusic,
		"newer.json":         specUnknown,
	} {
		if err := os.WriteFile(filepath.Join(specs, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := AdoptInstalledBackends(gen, backends.NewManager(backendRoot, nil), nil); err != nil {
		t.Fatal(err)
	}

	// One package's file already downloaded, in the layout the hub writes.
	modelsRoot := t.TempDir()
	have := filepath.Join(modelsRoot, "audio-cpp", "audio.cpp-gguf", "Moss")
	if err := os.MkdirAll(have, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(have, "moss-q8_0.gguf"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Server{autogen: &AutogenAdmin{GeneratePath: gen, ModelsDir: modelsRoot}}
	s.hub = hub.NewManager(s.hubModelsRoot, nil)

	rec := httptest.NewRecorder()
	s.handleAPIHubAudioCpp(rec, httptest.NewRequest(http.MethodGet, "/api/hub/audiocpp", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got catalogResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Installed || got.SpecsDir != specs {
		t.Fatalf("installed=%v specsDir=%q, want the install's own catalog at %q", got.Installed, got.SpecsDir, specs)
	}
	if len(got.Families) != 3 {
		t.Fatalf("families = %d, want 3", len(got.Families))
	}
	byFamily := map[string]int{}
	for i, f := range got.Families {
		byFamily[f.Family.Family] = i
	}

	tts := got.Families[byFamily["moss_tts_nano"]]
	if !tts.Supported || tts.Task != "tts" || tts.Reason != "" {
		t.Errorf("moss_tts_nano = %+v, want a supported tts family", tts)
	}
	// Upstream's default package leads, and the file we placed marks it installed
	// while its sibling precision stays a download.
	if tts.Packages[0].ID != "q8" || !tts.Packages[0].Local {
		t.Errorf("packages[0] = %+v, want the default one, already local", tts.Packages[0])
	}
	if tts.Packages[1].Local {
		t.Errorf("packages[1] = %+v, want not local", tts.Packages[1])
	}
	// No hub source in this test, so the size can only come from the copy on
	// disk - which is exactly the offline fallback worth pinning down.
	if tts.Packages[0].SizeBytes != int64(len("weights")) {
		t.Errorf("packages[0].sizeBytes = %d, want the on-disk size", tts.Packages[0].SizeBytes)
	}
	if tts.Packages[1].SizeBytes != 0 {
		t.Errorf("packages[1].sizeBytes = %d, want unknown rather than a partial sum", tts.Packages[1].SizeBytes)
	}

	// A family audio.cpp serves and we do not is listed with a reason, not hidden
	// and not offered as if it would work.
	music := got.Families[byFamily["ace_step"]]
	if music.Supported || music.Task != "" {
		t.Errorf("ace_step = %+v, want unsupported", music)
	}
	if music.Reason == "" {
		t.Error("ace_step has no reason line")
	}

	// And a family newer than our table says THAT, which is a different fix.
	unknown := got.Families[byFamily["not_a_real_family_yet"]]
	if unknown.Supported || unknown.Reason == music.Reason {
		t.Errorf("unknown family = %+v, want its own reason", unknown)
	}
}

// No audio.cpp installed is an empty catalog and a 200, not an error: the
// browser hides the tab for a backend the user does not use.
func TestHandleAPIHubAudioCpp_NoInstall(t *testing.T) {
	s := &Server{autogen: &AutogenAdmin{GeneratePath: newGenerateFile(t), ModelsDir: t.TempDir()}}
	s.hub = hub.NewManager(s.hubModelsRoot, nil)

	rec := httptest.NewRecorder()
	s.handleAPIHubAudioCpp(rec, httptest.NewRequest(http.MethodGet, "/api/hub/audiocpp", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got catalogResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Installed || len(got.Families) != 0 {
		t.Errorf("got %+v, want an empty catalog", got)
	}
}
