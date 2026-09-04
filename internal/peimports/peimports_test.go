package peimports

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildPE writes a minimal but real PE64 file whose import directory lists the
// given DLL names. The descriptors are placed in .rdata rather than .idata on
// purpose: that is where clang/lld puts them (stable-diffusion.dll included),
// and it is exactly the case debug/pe's own ImportedSymbols cannot read.
func buildPE(t *testing.T, path string, imports []string) {
	t.Helper()

	const (
		peOff   = 0x80
		optSize = 240 // OptionalHeader64: 112 fixed + 16 data directories
		secRVA  = 0x1000
		secRaw  = 0x400
	)

	// Section body: descriptor array, then the NUL-terminated names.
	desc := make([]byte, (len(imports)+1)*20)
	var names []byte
	for i, name := range imports {
		nameOff := len(desc) + len(names)
		binary.LittleEndian.PutUint32(desc[i*20+12:], uint32(secRVA+nameOff))
		// A non-zero OriginalFirstThunk keeps the descriptor from looking like
		// the all-zero terminator.
		binary.LittleEndian.PutUint32(desc[i*20:], uint32(secRVA+0x800))
		names = append(names, []byte(name+"\x00")...)
	}
	sec := append(desc, names...)

	buf := make([]byte, secRaw+len(sec))
	copy(buf, "MZ")
	binary.LittleEndian.PutUint32(buf[0x3C:], peOff)
	copy(buf[peOff:], "PE\x00\x00")

	coff := buf[peOff+4:]
	binary.LittleEndian.PutUint16(coff[0:], 0x8664) // Machine: AMD64
	binary.LittleEndian.PutUint16(coff[2:], 1)      // NumberOfSections
	binary.LittleEndian.PutUint16(coff[16:], optSize)
	binary.LittleEndian.PutUint16(coff[18:], 0x2022) // EXECUTABLE_IMAGE | DLL

	opt := coff[20:]
	binary.LittleEndian.PutUint16(opt[0:], 0x20B) // PE32+
	binary.LittleEndian.PutUint32(opt[108:], 16)  // NumberOfRvaAndSizes
	dd := opt[112:]
	binary.LittleEndian.PutUint32(dd[8:], secRVA)            // DataDirectory[1].VirtualAddress
	binary.LittleEndian.PutUint32(dd[12:], uint32(len(sec))) // .Size

	sh := opt[optSize:]
	copy(sh[0:], ".rdata")
	binary.LittleEndian.PutUint32(sh[8:], uint32(len(sec)))  // VirtualSize
	binary.LittleEndian.PutUint32(sh[12:], secRVA)           // VirtualAddress
	binary.LittleEndian.PutUint32(sh[16:], uint32(len(sec))) // SizeOfRawData
	binary.LittleEndian.PutUint32(sh[20:], secRaw)           // PointerToRawData

	copy(buf[secRaw:], sec)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPEImports_Imports(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sd-server.exe")
	want := []string{"stable-diffusion.dll", "KERNEL32.dll"}
	buildPE(t, exe, want)

	got, err := Imports(exe)
	if err != nil {
		t.Fatalf("Imports: %v", err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Imports = %v, want %v", got, want)
	}
}

// A non-PE file is not an error. Backends on Linux are ELF and the check must
// stay a silent no-op there rather than reporting every model as broken.
func TestPEImports_NonPEIsSilent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sd-server")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec real-server \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := Imports(p); err != nil || got != nil {
		t.Fatalf("Imports(script) = %v, %v; want nil, nil", got, err)
	}
	if h := Hint(p); h != "" {
		t.Fatalf("Hint(script) = %q, want empty", h)
	}
	if h := Hint(filepath.Join(t.TempDir(), "does-not-exist.exe")); h != "" {
		t.Fatalf("Hint(missing) = %q, want empty", h)
	}
}

// The shape of the real failure: sd-server.exe imports stable-diffusion.dll,
// which is present but itself imports a HIP runtime that upstream never shipped.
// The missing library must be attributed to the DLL that wants it, and the
// message must name the runtime to go and get.
func TestPEImports_MissingTransitiveDep(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sd-server.exe")
	dll := filepath.Join(dir, "stable-diffusion.dll")
	// No KERNEL32 here on purpose. It resolves from System32 on Windows and from
	// nowhere on a Linux runner, where it would arrive as a third missing dep owned
	// by the exe rather than the dll, and Hint drops its "imported by" clause as
	// soon as the deps stop sharing one parent. The attribution this test is about
	// is platform-independent; the fixture should be too.
	// The HIP libraries carry a -qmtest suffix so that NO real machine can own
	// them. Named exactly as upstream imports them, this test fails on a host
	// with AMD drivers installed: amdhip64_7.dll is then a real file in
	// System32, lookup resolves it like the loader would, and only one of the
	// two deps comes back missing. It passed on CI and failed on precisely the
	// hardware the feature exists for. The prefixes are what matter here --
	// runtimeHint keys off "amdhip"/"hip", not off the full name -- so the
	// classification under test is unchanged.
	buildPE(t, exe, []string{"stable-diffusion.dll"})
	buildPE(t, dll, []string{"amdhip64-qmtest.dll", "hipblas-qmtest.dll", "api-ms-win-crt-heap-l1-1-0.dll"})

	got := Missing(exe)
	if len(got) != 2 {
		t.Fatalf("Missing = %+v, want 2 entries", got)
	}
	// Sorted by name, so amdhip64 first.
	if got[0].Name != "amdhip64-qmtest.dll" || got[1].Name != "hipblas-qmtest.dll" {
		t.Fatalf("missing names = %+v", got)
	}
	for _, d := range got {
		if d.NeededBy != "stable-diffusion.dll" {
			t.Errorf("%s attributed to %s, want stable-diffusion.dll", d.Name, d.NeededBy)
		}
	}

	hint := Hint(exe)
	for _, want := range []string{"sd-server.exe", "amdhip64-qmtest.dll", "hipblas-qmtest.dll",
		"imported by stable-diffusion.dll", "AMD ROCm/HIP runtime"} {
		if !strings.Contains(hint, want) {
			t.Errorf("Hint missing %q:\n%s", want, hint)
		}
	}
	// API sets are loader-virtual and have no file on disk; reporting them would
	// mark every healthy binary as broken.
	if strings.Contains(hint, "api-ms-") {
		t.Errorf("hint reported an API set: %s", hint)
	}
}

// A dependency that exists next to the exe is resolved, even when it is not
// itself a readable PE — the check must never invent a problem.
func TestPEImports_SatisfiedDepIsClean(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "llama-server.exe")
	buildPE(t, exe, []string{"ggml-hip.dll"})
	if err := os.WriteFile(filepath.Join(dir, "ggml-hip.dll"), []byte("not really a pe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Missing(exe); len(got) != 0 {
		t.Fatalf("Missing = %+v, want none", got)
	}
}
