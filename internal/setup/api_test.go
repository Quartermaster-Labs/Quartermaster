package setup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestWizard builds a wizard with no platform hooks. Place/Launch are left
// nil so a test can drive a run without touching the machine it runs on.
func newTestWizard(t *testing.T) *Wizard {
	t.Helper()
	return New(Options{DefaultDir: t.TempDir()})
}

func TestWizard_GuardRejectsBadToken(t *testing.T) {
	w := newTestWizard(t)
	h := w.Handler()

	cases := []struct {
		name  string
		token string
	}{
		{"missing", ""},
		{"wrong", "0123456789abcdef0123456789abcdef"},
		{"prefix", w.Token()[:8]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/setup/status", nil)
			if tc.token != "" {
				req.Header.Set("X-QM-Setup-Token", tc.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("got %d, want 403", rec.Code)
			}
		})
	}
}

// A correct token from a non-loopback Host must still fail: that combination is
// exactly what a DNS-rebinding page produces once it has read the token out of
// a response it was allowed to make.
func TestWizard_GuardRejectsForeignHost(t *testing.T) {
	w := newTestWizard(t)
	req := httptest.NewRequest(http.MethodGet, "/api/setup/status", nil)
	req.Host = "evil.example.com"
	req.Header.Set("X-QM-Setup-Token", w.Token())

	rec := httptest.NewRecorder()
	w.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

func TestWizard_StatusWithToken(t *testing.T) {
	w := newTestWizard(t)
	req := httptest.NewRequest(http.MethodGet, "/api/setup/status", nil)
	// httptest defaults the Host to example.com, which the guard rejects on
	// purpose -- a real request always arrives at 127.0.0.1.
	req.Host = "127.0.0.1:1234"
	req.Header.Set("X-QM-Setup-Token", w.Token())

	rec := httptest.NewRecorder()
	w.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decoding status: %v", err)
	}
	if st.Phase != PhaseIdle {
		t.Fatalf("phase = %q, want %q", st.Phase, PhaseIdle)
	}
}

// The UI cannot call anything without the token, and the token only reaches it
// through the injected script tag -- so a bundle served without it is a wizard
// that renders and then 403s on its own first request.
func TestWizard_ServeUIInjectsToken(t *testing.T) {
	w := newTestWizard(t)
	rec := httptest.NewRecorder()
	w.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rec.Body.String()
	if rec.Code == http.StatusNotFound && strings.Contains(body, "not built") {
		t.Skip("ui bundle not built into this tree")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if !strings.Contains(body, "window.__QM_SETUP_TOKEN=\""+w.Token()+"\"") {
		t.Fatalf("token not injected into index.html")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestWizard_TokenIsUnique(t *testing.T) {
	a, b := newTestWizard(t), newTestWizard(t)
	if a.Token() == "" || a.Token() == b.Token() {
		t.Fatalf("tokens not unique: %q / %q", a.Token(), b.Token())
	}
}

// setSettingsKey edits lines instead of round-tripping through yaml.v3, because
// the generate file is heavily commented and a re-marshal would strip every
// comment the user relies on. These cases are the ones that would break if it
// were ever "simplified" back to a marshal.
func TestWizard_SetSettingsKey(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		key   string
		value string
		want  string
	}{
		{
			name:  "replaces an existing key and keeps its comment",
			in:    "settings:\n  # where models live\n  modelsRoot: C:/old\n  ctx: 4096\n",
			key:   "modelsRoot",
			value: "D:/LLM/Models",
			want:  "settings:\n  # where models live\n  modelsRoot: \"D:/LLM/Models\"\n  ctx: 4096\n",
		},
		{
			name:  "appends a key the file does not have",
			in:    "settings:\n  ctx: 4096\n",
			key:   "modelsRoot",
			value: "D:/Models",
			want:  "settings:\n  ctx: 4096\n  modelsRoot: \"D:/Models\"\n",
		},
		{
			// The same key name under overrides: belongs to a specific model.
			// Rewriting it would silently repoint one model's config.
			name:  "ignores a same-named key outside settings",
			in:    "settings:\n  modelsRoot: A\noverrides:\n  qwen:\n    modelsRoot: B\n",
			key:   "modelsRoot",
			value: "C",
			// Unquoted: quoteScalar only quotes what YAML would misread, and a
			// bare word is not that.
			want: "settings:\n  modelsRoot: C\noverrides:\n  qwen:\n    modelsRoot: B\n",
		},
		{
			name:  "normalises backslashes",
			in:    "settings:\n  modelsRoot: x\n",
			key:   "modelsRoot",
			value: `D:\LLM\Models`,
			want:  "settings:\n  modelsRoot: \"D:/LLM/Models\"\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "quartermaster-generate.yaml")
			if err := os.WriteFile(path, []byte(tc.in), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := setSettingsKey(path, tc.key, tc.value); err != nil {
				t.Fatalf("setSettingsKey: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// CRLF is what a user gets after editing the generate file in Notepad. Writing
// the file back with LF endings would show as a whole-file diff in git and, on
// some editors, as one long line.
func TestWizard_SetSettingsKeyPreservesCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gen.yaml")
	if err := os.WriteFile(path, []byte("settings:\r\n  modelsRoot: old\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setSettingsKey(path, "modelsRoot", "D:/x"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "settings:\r\n  modelsRoot: \"D:/x\"\r\n"; string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
