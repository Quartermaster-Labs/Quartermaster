//go:build !windows

package process

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ExeLibEnv prepends the directory containing the backend executable to the
// dynamic-loader search path (LD_LIBRARY_PATH, DYLD_LIBRARY_PATH on darwin).
//
// Why: self-contained bundles (e.g. llama.cpp's Vulkan/ROCm tarballs) ship
// their own .so files next to the binary. Their RUNPATH is $ORIGIN, which only
// resolves a binary's DIRECT dependencies — transitive ones
// (libllama-server-impl.so -> libllama-common.so.0) are searched in the system
// paths and the spawn dies with "error while loading shared libraries". A
// directory on LD_LIBRARY_PATH is consulted for every dependency, direct or
// transitive, so this is the portable, no-root fix (a system-wide ldconfig
// entry only fixes this host, and requires sudo).
//
// The prepend is skipped when the exe's directory is one of the loader's
// default search locations, so a system-installed backend (/usr/bin/llama-
// server) keeps exactly its current library resolution.
func ExeLibEnv(env []string, exe string) []string {
	dir := filepath.Dir(exe)
	if dir == "" || dir == "." || isDefaultLibDir(dir) {
		return env
	}
	varName := "LD_LIBRARY_PATH"
	if runtime.GOOS == "darwin" {
		varName = "DYLD_LIBRARY_PATH"
	}
	prefix := dir
	rest := ""
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, varName+"="); ok {
			rest = v // replace, don't duplicate: the prepended entry wins anyway
			continue
		}
		out = append(out, kv)
	}
	if rest != "" {
		prefix = dir + string(os.PathListSeparator) + rest
	}
	return append([]string{varName + "=" + prefix}, out...)
}

func isDefaultLibDir(dir string) bool {
	switch dir {
	case "/lib", "/usr/lib", "/lib64", "/usr/lib64", "/usr/local/lib",
		"/usr/bin", "/bin", "/usr/sbin", "/sbin":
		return true
	}
	return false
}
