package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/shared"
	"github.com/tidwall/gjson"
)

const qwenAlphaWrapped = "This is an RGBA image with transparency. a red apple The image has alpha channel and the background is transparent."

func TestServer_AlphaPromptFor(t *testing.T) {
	for _, id := range []string{"qwen_image_2.1-q8_0", "qwen_image_2.1_turbo-q8_0", "Qwen-Image-2.1-Q8"} {
		if alphaPromptFor(id) == nil {
			t.Errorf("%s: want an alpha prompt", id)
		}
	}
	for _, id := range []string{"qwen-rapid-nsfw", "z-image-turbo", "qwen-image-2512"} {
		if alphaPromptFor(id) != nil {
			t.Errorf("%s: want none", id)
		}
	}
}

func TestServer_ApplyImageTransparent(t *testing.T) {
	const m = "qwen_image_2.1_turbo-q8_0"
	cases := []struct {
		name, body, model, wantPrompt string
		wantErr                       bool
	}{
		{"wraps", `{"prompt":"  a  red apple ","transparent":true}`, m, qwenAlphaWrapped, false},
		{"false is dropped, prompt untouched", `{"prompt":"a red apple","transparent":false}`, m, "a red apple", false},
		{"absent leaves body alone", `{"prompt":"a red apple"}`, m, "a red apple", false},
		{"already wrapped is not wrapped twice", `{"prompt":"` + qwenAlphaWrapped + `","transparent":true}`, m, qwenAlphaWrapped, false},
		{"unsupported model errors", `{"prompt":"a red apple","transparent":true}`, "z-image-turbo", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := applyImageTransparent([]byte(c.body), c.model)
			if c.wantErr {
				if err != errAlphaUnsupported {
					t.Fatalf("err = %v, want errAlphaUnsupported", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := gjson.GetBytes(out, "prompt").String(); got != c.wantPrompt {
				t.Errorf("prompt = %q, want %q", got, c.wantPrompt)
			}
			if gjson.GetBytes(out, "transparent").Exists() {
				t.Errorf("transparent field forwarded: %s", out)
			}
		})
	}
}

// The rewrite only applies to sd-server models on the /sdapi routes: the PE
// rewrite models share the qwen-image-2.1 prefix but run llama-server.
func TestServer_FilterMiddlewareTransparent(t *testing.T) {
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"qwen_image_2.1_turbo-q8_0":    {Cmd: "sd-server.exe --diffusion-model x.gguf"},
		"qwen-image-2.1-pe-t2i-q5_k_m": {Cmd: "llama-server.exe -m pe.gguf"},
		"z-image-turbo":                {Cmd: "sd-server.exe --diffusion-model z.gguf"},
	}}
	run := func(path, model, body string) (int, string) {
		var got string
		h := CreateFilterMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
		}))
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(shared.SetContext(req.Context(), shared.ReqContextData{Model: model, ModelID: model}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, got
	}

	body := `{"model":"m","prompt":"a red apple","transparent":true}`
	if code, got := run("/sdapi/v1/txt2img", "qwen_image_2.1_turbo-q8_0", body); code != 200 || gjson.Get(got, "prompt").String() != qwenAlphaWrapped {
		t.Errorf("txt2img: code %d, body %s", code, got)
	}
	if code, got := run("/sdapi/v1/img2img", "qwen_image_2.1_turbo-q8_0", body); code != 200 || gjson.Get(got, "prompt").String() != qwenAlphaWrapped {
		t.Errorf("img2img: code %d, body %s", code, got)
	}
	if code, _ := run("/sdapi/v1/txt2img", "z-image-turbo", body); code != http.StatusBadRequest {
		t.Errorf("unsupported model: code %d, want 400", code)
	}
	if code, got := run("/v1/chat/completions", "qwen-image-2.1-pe-t2i-q5_k_m", body); code != 200 || gjson.Get(got, "prompt").String() != "a red apple" {
		t.Errorf("llama PE model touched: code %d, body %s", code, got)
	}
}
