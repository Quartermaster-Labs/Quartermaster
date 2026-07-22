package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// filterLorasResponse wraps the /sdapi/v1/loras dispatch and drops rows that are
// not LoRAs at all. sd-server scans `--lora-model-dir` and lists EVERY weight file
// in it; autogen points that dir at the model gguf's own folder by default (so a
// LoRA dropped next to its base checkpoint is zero-config), which means the folder
// also holds the checkpoints and encoders themselves. Those showed up in the
// playground's LoRA picker and are guaranteed failures if selected.
//
// The filter is by file identity, not by guesswork: every path any configured
// model launches with (`-m`, `--diffusion-model`, `--vae`, `--clip_l`, `--t5xxl`,
// `--llm`, ...) is a model file, so any listed row resolving to one of those is
// removed. A genuine LoRA is never a launch argument, so it always survives.
func (s *Server) filterLorasResponse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ask upstream for plain JSON: a compressed body would fail to parse and
		// silently pass the unfiltered list through.
		r.Header.Del("Accept-Encoding")
		rec := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		body := rec.body.Bytes()
		if rec.status == http.StatusOK {
			if filtered, ok := stripModelFiles(body, s.modelFileNames()); ok {
				body = filtered
			}
		}
		for k, vs := range rec.header {
			// Content-Length is recomputed below; the rest passes through.
			if strings.EqualFold(k, "Content-Length") {
				continue
			}
			w.Header()[k] = vs
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(rec.status)
		_, _ = w.Write(body)
	})
}

// bufferedResponse collects an upstream response so the body can be rewritten
// before anything reaches the client. The LoRA list is a small one-shot JSON
// array, so buffering it whole is free — do NOT reuse this for streaming routes.
type bufferedResponse struct {
	header      http.Header
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if !b.wroteHeader {
		b.status = status
		b.wroteHeader = true
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	b.wroteHeader = true
	return b.body.Write(p)
}

// modelFileNames is the set of base file names every configured model launches
// with — checkpoints, VAEs, text encoders. Compared by base name (lowercased)
// because sd-server reports paths relative to its own --lora-model-dir while the
// config carries absolute ones.
func (s *Server) modelFileNames() map[string]struct{} {
	names := map[string]struct{}{}
	for _, m := range s.config().Models {
		for _, tok := range strings.Fields(m.Cmd) {
			tok = strings.Trim(tok, `"'`)
			lower := strings.ToLower(tok)
			if !strings.HasSuffix(lower, ".gguf") && !strings.HasSuffix(lower, ".safetensors") &&
				!strings.HasSuffix(lower, ".ckpt") && !strings.HasSuffix(lower, ".pt") {
				continue
			}
			names[baseName(lower)] = struct{}{}
		}
	}
	return names
}

func baseName(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// stripModelFiles removes rows whose path names a known model file. ok=false
// means the body wasn't the expected JSON array — pass it through untouched
// rather than mangling an error payload or a future response shape.
func stripModelFiles(body []byte, modelFiles map[string]struct{}) ([]byte, bool) {
	var rows []json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, false
	}
	kept := make([]json.RawMessage, 0, len(rows))
	for _, raw := range rows {
		var row struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &row); err == nil {
			if _, isModel := modelFiles[baseName(strings.ToLower(row.Path))]; isModel {
				continue
			}
		}
		kept = append(kept, raw)
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return nil, false
	}
	return out, true
}
