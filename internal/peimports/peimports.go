// Package peimports answers one question about a Windows binary: can the loader
// actually load it, and if not, which DLL is missing.
//
// It exists because a failed DLL load is invisible. When a backend's dependency
// chain is incomplete the process dies with STATUS_DLL_NOT_FOUND (0xC0000135)
// before main() runs — no stdout, no stderr, no log line. Everything upstream of
// that only sees a process that exited immediately, which is how a missing
// hipblas.dll surfaced as "upstream command exited prematurely" and cost an
// afternoon.
//
// The concrete case: stable-diffusion.cpp's Windows ROCm release is built
// against AMD's ROCm *pip wheels* and packaged with `7z a ... .\build\bin\*`,
// which copies no runtime (the CUDA job in the same workflow does copy its
// cudart/cublas). The wheels name the import `hipblas.dll`; AMD's HIP SDK
// installer ships the same library as `libhipblas.dll`. So the archive is
// unloadable on any machine but the CI runner, and nothing says so.
//
// Scope: load-time imports only. Delay-loaded imports (data directory 13) are
// deliberately ignored — a missing delay import fails at first call, not at
// load, so it is not what makes a process die silently at startup. Anything that
// is not a PE file (an ELF backend on Linux, a shell script) reports no
// problems rather than an error: the check is a diagnostic, never a gate.
package peimports

import (
	"debug/pe"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dep is one unresolvable import: Name could not be found on the loader's search
// path, and NeededBy is the base name of the module that imports it.
type Dep struct {
	Name     string
	NeededBy string
}

// maxDescriptors bounds the import-descriptor walk. Real binaries import a few
// dozen libraries; a much larger count means a malformed or hostile header, not
// a binary we should keep parsing.
const maxDescriptors = 4096

// Imports returns the load-time import DLL names of a PE file, in header order.
// A file that is not a PE, or a PE with no import directory, yields no names and
// no error.
func Imports(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	pf, err := pe.NewFile(f)
	if err != nil {
		return nil, nil // not a PE — nothing to say about it
	}
	defer pf.Close()

	var dd pe.DataDirectory
	switch oh := pf.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		if len(oh.DataDirectory) <= 1 {
			return nil, nil
		}
		dd = oh.DataDirectory[1]
	case *pe.OptionalHeader32:
		if len(oh.DataDirectory) <= 1 {
			return nil, nil
		}
		dd = oh.DataDirectory[1]
	default:
		return nil, nil
	}
	if dd.VirtualAddress == 0 {
		return nil, nil
	}

	var names []string
	for i := 0; i < maxDescriptors; i++ {
		// IMAGE_IMPORT_DESCRIPTOR is 20 bytes; the Name RVA sits at offset 12.
		// A descriptor that is entirely zero terminates the array.
		desc := make([]byte, 20)
		if err := readRVA(pf, dd.VirtualAddress+uint32(i*20), desc); err != nil {
			break
		}
		if allZero(desc) {
			break
		}
		nameRVA := le32(desc[12:])
		if nameRVA == 0 {
			break
		}
		name, err := readCString(pf, nameRVA)
		if err != nil || name == "" {
			break
		}
		names = append(names, name)
	}
	return names, nil
}

// Missing walks the import graph rooted at exe and reports every DLL the Windows
// loader would fail to find. The search path mirrors the default loader order
// that matters here — the application directory first, then the system
// directories, then PATH — which is also why dropping a DLL next to the exe is
// the fix for everything this function reports.
//
// It is best-effort by construction: an unreadable or non-PE dependency is
// skipped rather than reported, so a clean result means "found nothing wrong",
// not "proved it loads".
func Missing(exe string) []Dep {
	dir := filepath.Dir(exe)
	search := append([]string{dir}, systemSearchDirs()...)

	seen := map[string]bool{}
	missing := map[string]Dep{}
	queue := []string{exe}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		key := strings.ToLower(filepath.Base(cur))
		if seen[key] {
			continue
		}
		seen[key] = true

		imports, err := Imports(cur)
		if err != nil {
			continue
		}
		for _, imp := range imports {
			lower := strings.ToLower(imp)
			// API sets are loader-resolved virtual names with no file on disk.
			if strings.HasPrefix(lower, "api-ms-") || strings.HasPrefix(lower, "ext-ms-") {
				continue
			}
			if seen[lower] {
				continue
			}
			if found := lookup(imp, search); found != "" {
				queue = append(queue, found)
				continue
			}
			if _, dup := missing[lower]; !dup {
				missing[lower] = Dep{Name: imp, NeededBy: filepath.Base(cur)}
			}
		}
	}

	out := make([]Dep, 0, len(missing))
	for _, d := range missing {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Hint returns a one-line, actionable description of why exe cannot load, or ""
// when nothing is missing. It is safe to call on any path, including one that
// does not exist.
func Hint(exe string) string {
	deps := Missing(exe)
	if len(deps) == 0 {
		return ""
	}
	names := make([]string, 0, len(deps))
	for _, d := range deps {
		names = append(names, d.Name)
	}
	// Every dep usually comes from the same module; naming it points at the
	// component that is really under-packaged rather than at the exe.
	needed := deps[0].NeededBy
	sameSource := true
	for _, d := range deps[1:] {
		if d.NeededBy != needed {
			sameSource = false
			break
		}
	}
	msg := fmt.Sprintf("%s cannot load: missing %s", filepath.Base(exe), strings.Join(names, ", "))
	if sameSource && !strings.EqualFold(needed, filepath.Base(exe)) {
		msg += fmt.Sprintf(" (imported by %s)", needed)
	}
	if advice := runtimeAdvice(names); advice != "" {
		msg += "; " + advice
	}
	return msg
}

// runtimeAdvice turns a set of missing DLLs into what the user should do about
// them, so the message says how to fix it instead of only what is absent. Empty
// when the names do not point at a runtime we recognise.
func runtimeAdvice(names []string) string {
	var hip, cuda, driver, vc bool
	for _, n := range names {
		switch l := strings.ToLower(n); {
		// nvcuda/nvml ship with the NVIDIA display driver, not the toolkit.
		// Missing them means the host has no NVIDIA GPU at all, which is a
		// different (and much more common) mistake than a half-packaged archive:
		// a CUDA build installed on an AMD box.
		case strings.HasPrefix(l, "nvcuda"), strings.HasPrefix(l, "nvml"):
			driver = true
		case strings.HasPrefix(l, "amdhip"), strings.HasPrefix(l, "hip"),
			strings.HasPrefix(l, "roc"), strings.HasPrefix(l, "libhip"),
			strings.HasPrefix(l, "amd_comgr"):
			hip = true
		case strings.HasPrefix(l, "cudart"), strings.HasPrefix(l, "cublas"),
			strings.HasPrefix(l, "cudnn"), strings.HasPrefix(l, "nvrtc"):
			cuda = true
		case strings.HasPrefix(l, "msvcp"), strings.HasPrefix(l, "vcruntime"),
			strings.HasPrefix(l, "concrt"):
			vc = true
		}
	}
	switch {
	case hip:
		return "this build needs the AMD ROCm/HIP runtime next to the executable or on PATH"
	case cuda:
		return "this build needs the NVIDIA CUDA runtime next to the executable or on PATH"
	case driver:
		return "this is a CUDA build and needs an NVIDIA driver — it cannot run on this GPU"
	case vc:
		return "install the Microsoft Visual C++ redistributable"
	}
	return ""
}

// lookup finds a DLL by name across the search directories, case-insensitively
// as the loader does.
func lookup(name string, dirs []string) string {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// systemSearchDirs returns the loader's system directories followed by PATH.
// On non-Windows hosts the system entries simply do not exist and are skipped by
// lookup, so no build tag is needed.
func systemSearchDirs() []string {
	var dirs []string
	if win := os.Getenv("SystemRoot"); win != "" {
		dirs = append(dirs, filepath.Join(win, "System32"), filepath.Join(win, "SysWOW64"), win)
	}
	dirs = append(dirs, filepath.SplitList(os.Getenv("PATH"))...)
	return dirs
}

// readRVA reads len(buf) bytes at a virtual address by mapping it back to the
// section that contains it. Import descriptors live in whichever section the
// linker chose (.idata on MSVC, .rdata on clang/lld), which is why this cannot
// use debug/pe's .idata-only ImportedSymbols.
func readRVA(pf *pe.File, rva uint32, buf []byte) error {
	for _, s := range pf.Sections {
		size := s.VirtualSize
		if s.Size > size {
			size = s.Size
		}
		if rva < s.VirtualAddress || rva >= s.VirtualAddress+size {
			continue
		}
		off := int64(rva - s.VirtualAddress)
		if off+int64(len(buf)) > int64(s.Size) {
			return io.ErrUnexpectedEOF // in the virtual padding, not on disk
		}
		_, err := s.ReadAt(buf, off)
		return err
	}
	return io.ErrUnexpectedEOF
}

// readCString reads a NUL-terminated ASCII string at a virtual address.
func readCString(pf *pe.File, rva uint32) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for i := 0; i < 512; i++ {
		if err := readRVA(pf, rva+uint32(i), buf); err != nil {
			return "", err
		}
		if buf[0] == 0 {
			return sb.String(), nil
		}
		sb.WriteByte(buf[0])
	}
	return "", io.ErrUnexpectedEOF
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
