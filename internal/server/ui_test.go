package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestServer_SelectEncoding(t *testing.T) {
	cases := []struct {
		accept   string
		encoding string
		ext      string
	}{
		{"", "", ""},
		{"gzip", "gzip", ".gz"},
		{"gzip, deflate, br", "br", ".br"},
		{"deflate", "", ""},
		{"br;q=1.0, gzip;q=0.8", "br", ".br"},
	}
	for _, c := range cases {
		enc, ext := selectEncoding(c.accept)
		if enc != c.encoding || ext != c.ext {
			t.Errorf("selectEncoding(%q) = (%q, %q), want (%q, %q)", c.accept, enc, ext, c.encoding, c.ext)
		}
	}
}

func uiTestFS() http.FileSystem {
	return http.FS(fstest.MapFS{
		"index.html":  {Data: []byte("<html>app</html>")},
		"app.js":      {Data: []byte("plain")},
		"app.js.br":   {Data: []byte("brotli")},
		"app.js.gz":   {Data: []byte("gzipped")},
		"favicon.ico": {Data: []byte("icon")},
		"worker.mjs":  {Data: []byte("export{}")},
		"w.mjs.br":    {Data: []byte("brotli-module")},
		"w.mjs":       {Data: []byte("export{}")},
	})
}

func serveUIRequest(t *testing.T, path, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	w := httptest.NewRecorder()
	serveUI(uiTestFS(), w, req)
	return w
}

func TestServer_ServeUI_File(t *testing.T) {
	w := serveUIRequest(t, "/ui/app.js", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "plain" {
		t.Errorf("body = %q, want plain", w.Body.String())
	}
}

func TestServer_ServeUI_Brotli(t *testing.T) {
	w := serveUIRequest(t, "/ui/app.js", "gzip, br")
	if got := w.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q, want br", got)
	}
	if w.Body.String() != "brotli" {
		t.Errorf("body = %q, want brotli", w.Body.String())
	}
}

func TestServer_ServeUI_IndexAndRoot(t *testing.T) {
	for _, path := range []string{"/ui/", "/ui/index.html"} {
		w := serveUIRequest(t, path, "")
		if w.Code != http.StatusOK || w.Body.String() != "<html>app</html>" {
			t.Errorf("%s: status=%d body=%q", path, w.Code, w.Body.String())
		}
	}
}

func TestServer_ServeUI_SPAFallback(t *testing.T) {
	w := serveUIRequest(t, "/ui/models", "")
	if w.Code != http.StatusOK || w.Body.String() != "<html>app</html>" {
		t.Errorf("SPA fallback: status=%d body=%q", w.Code, w.Body.String())
	}
}

// A module served as text/plain is refused by the browser, which is exactly how
// the pdf.js worker ships (.mjs). Windows has no registry row for it, so the
// type must come from our own table — on both the plain and pre-compressed path.
func TestServer_ServeUI_ModuleContentType(t *testing.T) {
	for _, c := range []struct{ path, accept string }{
		{"/ui/worker.mjs", ""},
		{"/ui/w.mjs", "br"},
	} {
		w := serveUIRequest(t, c.path, c.accept)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", c.path, w.Code)
		}
		if got := w.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q, want text/javascript", c.path, got)
		}
	}
}

func TestServer_ServeUI_MissingFile(t *testing.T) {
	w := serveUIRequest(t, "/ui/missing.js", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
