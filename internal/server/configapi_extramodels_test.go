package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
)

// The manual-model table round-trip: a UI row lands in the sidecar, survives a
// regen into the generated config, is listed with source "ui" beside the
// generate file's own row, and bad rows are refused on the save rather than
// silently SKIPPED in a file nobody reads.
func TestExtraModelsAPI(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	if err := os.MkdirAll(models, 0o755); err != nil {
		t.Fatal(err)
	}
	dit := filepath.Join(dir, "dit.safetensors")
	vae := filepath.Join(dir, "vae.safetensors")
	for _, f := range []string{dit, vae} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gen := filepath.Join(dir, "generate.yaml")
	genYAML := "settings:\n  modelsRoot: " + filepath.ToSlash(models) + "\n" +
		"  extraImageModels:\n    - name: file-model\n      modelPath: " + filepath.ToSlash(dit) + "\n"
	if err := os.WriteFile(gen, []byte(genYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	s := &Server{proxylog: logmon.NewWriter(io.Discard)}
	s.SetAutogenAdmin(&AutogenAdmin{GeneratePath: gen, ConfigPath: cfgPath})
	s.cfg.Store(&config.Config{Models: map[string]config.ModelConfig{
		"discovered": {Cmd: "llama-server -m /m/x.gguf"},
		"file-model": {Cmd: "sd-server -m " + dit},
	}})

	put := func(rows []extraModelDTO) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(rows)
		w := httptest.NewRecorder()
		s.handleExtraModelsPut(w, httptest.NewRequest("PUT", "/api/extra-models", bytes.NewReader(body)))
		return w
	}
	get := func() []extraModelDTO {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleExtraModelsGet(w, httptest.NewRequest("GET", "/api/extra-models", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET status = %d: %s", w.Code, w.Body.String())
		}
		var out []extraModelDTO
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	ok := extraModelDTO{Name: "my-dit", ModelPath: dit, ModelFlag: "--diffusion-model", VaePath: vae}
	// The UI sends the table as shown, file rows included.
	fileRow := extraModelDTO{Name: "file-model", ModelPath: dit, Source: "file"}
	if w := put([]extraModelDTO{fileRow, ok, {}}); w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", w.Code, w.Body.String())
	}
	got := get()
	if len(got) != 2 || got[0].Name != "file-model" || got[0].Source != "file" ||
		got[1].Name != "my-dit" || got[1].Source != "ui" || got[1].VaePath != vae {
		t.Fatalf("GET = %+v, want file-model(file) then my-dit(ui)", got)
	}
	gen2, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gen2), `"my-dit":`) || !strings.Contains(string(gen2), `"file-model":`) {
		t.Fatalf("generated config is missing an extra model:\n%s", gen2)
	}
	// The file row is not copied into the sidecar: the UI owns only its rows.
	side, err := autogen.LoadSidecarExtraImageModels(gen)
	if err != nil || len(side) != 1 || side[0].Name != "my-dit" {
		t.Fatalf("sidecar = %+v (%v), want only my-dit", side, err)
	}

	for _, tc := range []struct {
		name string
		row  extraModelDTO
		code int
	}{
		{"missing file", extraModelDTO{Name: "x", ModelPath: filepath.Join(dir, "nope.safetensors")}, http.StatusBadRequest},
		{"missing vae", extraModelDTO{Name: "x", ModelPath: dit, VaePath: filepath.Join(dir, "nope")}, http.StatusBadRequest},
		{"dir as model", extraModelDTO{Name: "x", ModelPath: models}, http.StatusBadRequest},
		{"no name", extraModelDTO{ModelPath: dit}, http.StatusBadRequest},
		{"bad flag", extraModelDTO{Name: "x", ModelPath: dit, ModelFlag: "--vae"}, http.StatusBadRequest},
		{"discovered id", extraModelDTO{Name: "discovered", ModelPath: dit}, http.StatusConflict},
	} {
		if w := put([]extraModelDTO{fileRow, ok, tc.row}); w.Code != tc.code {
			t.Errorf("%s: status = %d, want %d (%s)", tc.name, w.Code, tc.code, w.Body.String())
		}
	}
	// A refused save leaves the stored list alone.
	if side, _ := autogen.LoadSidecarExtraImageModels(gen); len(side) != 1 {
		t.Fatalf("sidecar changed by a refused save: %+v", side)
	}
	// Sending the file row back as shown keeps it and never copies it.
	if w := put([]extraModelDTO{ok, fileRow}); w.Code != http.StatusOK {
		t.Fatalf("resave: status = %d: %s", w.Code, w.Body.String())
	}
	if side, _ := autogen.LoadSidecarExtraImageModels(gen); len(side) != 1 {
		t.Fatalf("file row copied into the sidecar: %+v", side)
	}
	// Deleting every UI row empties the sidecar list and the file row stays.
	if w := put([]extraModelDTO{fileRow}); w.Code != http.StatusOK {
		t.Fatalf("clear: status = %d: %s", w.Code, w.Body.String())
	}
	if got := get(); len(got) != 1 || got[0].Name != "file-model" || got[0].Source != "file" {
		t.Fatalf("after clear GET = %+v, want only the file row", got)
	}

	genBefore, _ := os.ReadFile(gen)
	// Deleting the FILE row hides it everywhere without touching the file.
	if w := put([]extraModelDTO{}); w.Code != http.StatusOK {
		t.Fatalf("delete file row: status = %d: %s", w.Code, w.Body.String())
	}
	if got := get(); len(got) != 0 {
		t.Fatalf("after deleting the file row GET = %+v, want none", got)
	}
	if out, _ := os.ReadFile(cfgPath); strings.Contains(string(out), `"file-model":`) {
		t.Fatal("deleted file row is still in the generated config")
	}
	if genAfter, _ := os.ReadFile(gen); !bytes.Equal(genBefore, genAfter) {
		t.Fatal("deleting a file row rewrote the generate file")
	}
	// Adding a row back under that name undoes the delete (and is not a 409:
	// it was one of ours).
	if w := put([]extraModelDTO{{Name: "file-model", ModelPath: dit, Source: "ui"}}); w.Code != http.StatusOK {
		t.Fatalf("re-add: status = %d: %s", w.Code, w.Body.String())
	}
	if removed, _ := autogen.LoadSidecarRemovedExtraImageModels(gen); len(removed) != 0 {
		t.Fatalf("removed list = %v after re-adding, want empty", removed)
	}
	if got := get(); len(got) != 1 || got[0].Source != "ui" {
		t.Fatalf("after re-add GET = %+v, want one ui row", got)
	}
	// Trashing a UI row that shadows a file row removes both: the file's
	// version must not reappear from under it.
	if w := put([]extraModelDTO{}); w.Code != http.StatusOK {
		t.Fatalf("delete shadow: status = %d: %s", w.Code, w.Body.String())
	}
	if got := get(); len(got) != 0 {
		t.Fatalf("after deleting the shadowing row GET = %+v, want none", got)
	}
}
