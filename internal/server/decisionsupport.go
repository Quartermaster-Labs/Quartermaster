package server

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Decision models (/v1/systemone) need a llama.cpp from after upstream PR
// #29818. An older llama-server either refuses the gguf or loads it as a plain
// LLM and 404s every request, and in both cases the user sees a model that
// "doesn't work" with nothing pointing at the backend. So the UI is told up
// front, and asks for an update before it loads one.
//
// The build is judged by what it CONTAINS, not by its version number: forks
// number their own releases (lemonade's ROCm b1343 serves the route, upstream's
// first build with it is b11361), so no single threshold is right. Newer builds
// keep the server in llama-server-impl.dll / libllama-server-impl.so beside a
// thin launcher, so the scan covers every llama/server library in the exe's
// folder, never only the exe. Nothing is launched.

// systemOneNeedle is the bare route name: llama-server registers "systemone"
// and joins the /v1/ prefix at runtime, so "/v1/systemone" appears in no build
// (lemonade b1343's llama-server-impl.dll holds only "systemone"). Builds from
// before the feature contain the word nowhere.
const systemOneNeedle = "systemone"

// systemOneScanMaxBytes skips anything too big to be server code (a ggml GPU
// backend DLL runs to hundreds of MB and never holds a route string).
const systemOneScanMaxBytes = 256 << 20

var systemOneCache = struct {
	sync.Mutex
	m map[string]bool // folder fingerprint -> route present
}{m: map[string]bool{}}

// backendServesSystemOne reports whether the llama-server at exe has the
// /v1/systemone route. known is false when the exe cannot be found, and then
// nothing should be claimed either way.
func backendServesSystemOne(exe string) (supported, known bool) {
	path := resolveBackendExe(exe)
	if path == "" {
		return false, false
	}
	files := systemOneCandidates(path)
	if len(files) == 0 {
		return false, false
	}
	// The fingerprint is name+size+mtime of every candidate, so swapping the
	// backend in place (an update) invalidates the cached answer.
	var fp strings.Builder
	for _, f := range files {
		fmt.Fprintf(&fp, "%s|%d|%d;", f.path, f.size, f.mod)
	}
	key := fp.String()
	systemOneCache.Lock()
	v, ok := systemOneCache.m[key]
	systemOneCache.Unlock()
	if ok {
		return v, true
	}
	for _, f := range files {
		if fileContains(f.path, []byte(systemOneNeedle)) {
			v = true
			break
		}
	}
	systemOneCache.Lock()
	systemOneCache.m[key] = v
	systemOneCache.Unlock()
	return v, true
}

// resolveBackendExe turns a cmd's argv[0] into a file on disk: as written,
// then via PATH for a bare name.
func resolveBackendExe(exe string) string {
	exe = strings.Trim(strings.TrimSpace(exe), `"`)
	if exe == "" {
		return ""
	}
	if st, err := os.Stat(exe); err == nil && !st.IsDir() {
		return exe
	}
	if p, err := exec.LookPath(exe); err == nil {
		return p
	}
	return ""
}

type scanFile struct {
	path      string
	size, mod int64
}

// systemOneCandidates lists the exe plus the llama/server libraries next to it.
func systemOneCandidates(exe string) []scanFile {
	var out []scanFile
	add := func(p string) {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Size() > systemOneScanMaxBytes {
			return
		}
		out = append(out, scanFile{p, st.Size(), st.ModTime().UnixNano()})
	}
	add(exe)
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if e.IsDir() || filepath.Join(filepath.Dir(exe), e.Name()) == exe {
			continue
		}
		lib := strings.HasSuffix(name, ".dll") || strings.HasSuffix(name, ".dylib") || strings.Contains(name, ".so")
		if lib && (strings.Contains(name, "llama") || strings.Contains(name, "server")) {
			add(filepath.Join(filepath.Dir(exe), e.Name()))
		}
	}
	return out
}

// fileContains streams path looking for needle, carrying an overlap across
// chunk boundaries so a match split between two reads is still found.
func fileContains(path string, needle []byte) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 1<<20)
	carry := 0
	for {
		n, err := f.Read(buf[carry:])
		if n > 0 {
			window := buf[:carry+n]
			if bytes.Contains(window, needle) {
				return true
			}
			carry = min(len(needle)-1, len(window))
			copy(buf, window[len(window)-carry:])
		}
		if err == io.EOF {
			return false
		}
		if err != nil {
			return false
		}
	}
}
