package backends

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The asset matcher is the one maintenance point when upstream renames release
// assets, so pin the current naming for every catalog component.
func TestBackends_MatchAssets(t *testing.T) {
	llama, _ := Find("llama-server")
	// Verbatim asset list from ggml-org/llama.cpp release b10240.
	names := []string{
		"llama-b10240-bin-android-arm64.tar.gz",
		"llama-b10240-bin-macos-arm64.tar.gz",
		"llama-b10240-bin-macos-x64.tar.gz",
		"llama-b10240-bin-ubuntu-arm64.tar.gz",
		"llama-b10240-bin-ubuntu-rocm-7.2-x64.tar.gz",
		"llama-b10240-bin-ubuntu-sycl-fp16-x64.tar.gz",
		"llama-b10240-bin-ubuntu-vulkan-arm64.tar.gz",
		"llama-b10240-bin-ubuntu-vulkan-x64.tar.gz",
		"llama-b10240-bin-ubuntu-x64.tar.gz",
		"llama-b10240-bin-win-cpu-arm64.zip",
		"llama-b10240-bin-win-cpu-x64.zip",
		"llama-b10240-bin-win-cuda-12.4-x64.zip",
		"llama-b10240-bin-win-cuda-13.3-x64.zip",
		"llama-b10240-bin-win-hip-radeon-x64.zip",
		"llama-b10240-bin-win-opencl-adreno-arm64.zip",
		"llama-b10240-bin-win-sycl-x64.zip",
		"llama-b10240-bin-win-vulkan-x64.zip",
		"cudart-llama-bin-win-cuda-12.4-x64.zip",
		"cudart-llama-bin-win-cuda-13.3-x64.zip",
	}
	cases := []struct {
		variant, goos, want string
		wantExtra           string
	}{
		{"vulkan", osWin, "llama-b10240-bin-win-vulkan-x64.zip", ""},
		// The newest CUDA build wins, and the cudart it is paired with must be the
		// one for the SAME toolkit — matching by list order would take 12.4's.
		{"cuda", osWin, "llama-b10240-bin-win-cuda-13.3-x64.zip", "cudart-llama-bin-win-cuda-13.3-x64.zip"},
		{"rocm", osWin, "llama-b10240-bin-win-hip-radeon-x64.zip", ""},
		{"cpu", osWin, "llama-b10240-bin-win-cpu-x64.zip", ""},
		{"vulkan", osLinux, "llama-b10240-bin-ubuntu-vulkan-x64.tar.gz", ""},
		{"rocm", osLinux, "llama-b10240-bin-ubuntu-rocm-7.2-x64.tar.gz", ""},
		{"cpu", osLinux, "llama-b10240-bin-ubuntu-x64.tar.gz", ""},
		{"cpu", osMac, "llama-b10240-bin-macos-arm64.tar.gz", ""},
	}
	for _, c := range cases {
		got, extra, err := llama.MatchAssets(c.variant, c.goos, names)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.variant, c.goos, err)
		}
		if got != c.want {
			t.Errorf("%s/%s: got %q want %q", c.variant, c.goos, got, c.want)
		}
		if c.wantExtra == "" && len(extra) != 0 {
			t.Errorf("%s/%s: unexpected extras %v", c.variant, c.goos, extra)
		}
		if c.wantExtra != "" && (len(extra) != 1 || extra[0] != c.wantExtra) {
			t.Errorf("%s/%s: extras %v want [%s]", c.variant, c.goos, extra, c.wantExtra)
		}
	}
	if _, _, err := llama.MatchAssets("vulkan", osWin, []string{"nope.zip"}); err == nil {
		t.Error("expected an error when no asset matches")
	}

	// stable-diffusion.cpp: capitalised platform segments, its own cudart zip,
	// and an upstream ROCm build. Verbatim from release master-809-eb7f35c.
	sd, _ := Find("sd-server")
	sdNames := []string{
		"cudart-sd-bin-win-cu12-x64.zip",
		"sd-master-eb7f35c-bin-Darwin-macOS-26.5.2-arm64.zip",
		"sd-master-eb7f35c-bin-Linux-Ubuntu-24.04-x86_64-rocm-7.14.0.zip",
		"sd-master-eb7f35c-bin-Linux-Ubuntu-24.04-x86_64-vulkan.zip",
		"sd-master-eb7f35c-bin-Linux-Ubuntu-24.04-x86_64.zip",
		"sd-master-eb7f35c-bin-win-cpu-x64.zip",
		"sd-master-eb7f35c-bin-win-cuda12-x64.zip",
		"sd-master-eb7f35c-bin-win-rocm-7.14.0-x64.zip",
		"sd-master-eb7f35c-bin-win-vulkan-x64.zip",
	}
	sdCases := []struct {
		variant, goos, want string
		wantExtra           string
	}{
		{"vulkan", osWin, "sd-master-eb7f35c-bin-win-vulkan-x64.zip", ""},
		{"cuda", osWin, "sd-master-eb7f35c-bin-win-cuda12-x64.zip", "cudart-sd-bin-win-cu12-x64.zip"},
		{"rocm", osWin, "sd-master-eb7f35c-bin-win-rocm-7.14.0-x64.zip", ""},
		{"cpu", osWin, "sd-master-eb7f35c-bin-win-cpu-x64.zip", ""},
		{"vulkan", osLinux, "sd-master-eb7f35c-bin-Linux-Ubuntu-24.04-x86_64-vulkan.zip", ""},
		{"rocm", osLinux, "sd-master-eb7f35c-bin-Linux-Ubuntu-24.04-x86_64-rocm-7.14.0.zip", ""},
		{"cpu", osLinux, "sd-master-eb7f35c-bin-Linux-Ubuntu-24.04-x86_64.zip", ""},
		{"cpu", osMac, "sd-master-eb7f35c-bin-Darwin-macOS-26.5.2-arm64.zip", ""},
	}
	for _, c := range sdCases {
		got, extra, err := sd.MatchAssets(c.variant, c.goos, sdNames)
		if err != nil {
			t.Fatalf("sd %s/%s: %v", c.variant, c.goos, err)
		}
		if got != c.want {
			t.Errorf("sd %s/%s: got %q want %q", c.variant, c.goos, got, c.want)
		}
		if c.wantExtra == "" && len(extra) != 0 {
			t.Errorf("sd %s/%s: unexpected extras %v", c.variant, c.goos, extra)
		}
		if c.wantExtra != "" && (len(extra) != 1 || extra[0] != c.wantExtra) {
			t.Errorf("sd %s/%s: extras %v want [%s]", c.variant, c.goos, extra, c.wantExtra)
		}
	}

	// Real-ESRGAN's assets live on an older tag than its newest release.
	up, _ := Find("upscaler")
	if got, _, err := up.MatchAssets("any", osWin, []string{"realesrgan-ncnn-vulkan-20220424-windows.zip"}); err != nil || got != "realesrgan-ncnn-vulkan-20220424-windows.zip" {
		t.Errorf("upscaler: got %q err %v", got, err)
	}
	// yt-dlp's release ships several look-alike assets; the bare exe must win.
	yt, _ := Find("yt-dlp")
	got, _, err := yt.MatchAssets("any", osWin, []string{"yt-dlp", "yt-dlp_win.zip", "yt-dlp.exe.sha256", "yt-dlp.exe", "yt-dlp_x86.exe"})
	if err != nil || got != "yt-dlp.exe" {
		t.Errorf("yt-dlp: got %q err %v", got, err)
	}
}

// A manual entry is catalogued so the UI can describe the engine, but it has no
// installable asset — the manager must refuse rather than start a job that can
// only fail at asset matching.
func TestBackends_ManualComponentRefusesInstall(t *testing.T) {
	c, ok := Find("vllm")
	if !ok {
		t.Fatal("vllm missing from the catalog")
	}
	if !c.Manual || c.Setup == "" {
		t.Fatalf("vllm must be Manual with setup text, got manual=%v setup=%q", c.Manual, c.Setup)
	}
	if len(c.Variants) != 0 {
		t.Fatalf("a manual component has nothing to download, got %d variants", len(c.Variants))
	}
	m := NewManager(t.TempDir(), nil)
	if _, err := m.Install("vllm", "", ""); err == nil {
		t.Fatal("Install(vllm) should be refused")
	}
}

func TestBackends_SuggestVariant(t *testing.T) {
	cases := []struct {
		gpus []string
		want string
	}{
		{[]string{"NVIDIA GeForce RTX 4090"}, "cuda"},
		{[]string{"AMD Radeon RX 7900 XTX"}, "vulkan"},
		{[]string{"Intel(R) Arc(TM) A770"}, "vulkan"},
		{[]string{"AMD Radeon Graphics", "NVIDIA RTX 4090"}, "cuda"}, // discrete NVIDIA wins
		{nil, "cpu"},
	}
	for _, c := range cases {
		if got := SuggestVariant(c.gpus); got != c.want {
			t.Errorf("SuggestVariant(%v) = %q want %q", c.gpus, got, c.want)
		}
	}
	// A component publishing only an "any" build ignores the GPU suggestion.
	up, _ := Find("upscaler")
	if got := up.DefaultVariant([]string{"NVIDIA RTX 4090"}, osWin); got != "any" {
		t.Errorf("upscaler default variant = %q want any", got)
	}
}

// A crafted archive must not be able to write outside the install directory.
func TestBackends_ExtractRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("../escaped.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("pwned")); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	f.Close()

	dest := filepath.Join(dir, "install")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extract(zipPath, dest); err == nil {
		t.Fatal("expected extract to reject a ../ entry")
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); err == nil {
		t.Fatal("archive escaped the install directory")
	}
}

// Installed rows come from per-directory manifests, so a half-extracted or
// hand-emptied folder must not be reported as an installed build.
func TestBackends_InstalledScan(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	dir := m.InstallDir("llama-server", "b6543", "vulkan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "build", "bin", "llama-server.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := m.Installed("llama-server"); len(got) != 0 {
		t.Fatalf("no manifest yet, got %v", got)
	}
	if err := writeManifest(dir, manifest{
		Component: "llama-server", Version: "b6543", Variant: "vulkan",
		Exe: "build/bin/llama-server.exe", InstalledAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	got := m.Installed("llama-server")
	if len(got) != 1 || got[0].Exe != exe {
		t.Fatalf("got %+v want exe %s", got, exe)
	}
	// Size is the whole install, not the executable: llama-server.exe is a stub
	// in front of the ggml DLLs, so measuring only the exe reported "0 MB".
	dll := filepath.Join(dir, "build", "bin", "ggml.dll")
	if err := os.WriteFile(dll, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := m.Installed("llama-server"); len(got) != 1 || got[0].SizeBytes <= 4096 {
		t.Fatalf("size should cover the whole install dir, got %+v", got)
	}

	// A manifest whose executable vanished is not an install either.
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if got := m.Installed("llama-server"); len(got) != 0 {
		t.Fatalf("missing exe should not list, got %v", got)
	}
}

// Versioned side-by-side layout: two builds of one component coexist.
func TestBackends_InstallDirIsVersioned(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "app"), nil)
	a := m.InstallDir("llama-server", "b1", "vulkan")
	b := m.InstallDir("llama-server", "b2", "vulkan")
	c := m.InstallDir("llama-server", "b1", "cuda")
	if a == b || a == c {
		t.Fatalf("install dirs collide: %s %s %s", a, b, c)
	}
	// Tags with path separators can't escape the component directory.
	weird := m.InstallDir("llama-server", "../../etc", "vulkan")
	if !strings.HasPrefix(weird, m.ComponentDir("llama-server")) {
		t.Fatalf("tag escaped the component dir: %s", weird)
	}
}

func TestBackends_PickRelease(t *testing.T) {
	rels := []Release{
		{Tag: "b3", Prerelease: true},
		{Tag: "b2"},
		{Tag: "b1"},
	}
	if r, _ := pickRelease(rels, "", nil); r.Tag != "b2" {
		t.Errorf("latest skipped prerelease? got %s", r.Tag)
	}
	if r, _ := pickRelease(rels, "b1", nil); r.Tag != "b1" {
		t.Errorf("exact tag: got %s", r.Tag)
	}
	if _, ok := pickRelease(rels, "b99", nil); ok {
		t.Error("unknown tag should not resolve")
	}
	// A source-only newest release (Real-ESRGAN does this) must be skipped in
	// favour of the newest release that actually ships a usable asset.
	only := func(tag string) func(Release) bool {
		return func(r Release) bool { return r.Tag == tag }
	}
	if r, _ := pickRelease(rels, "", only("b1")); r.Tag != "b1" {
		t.Errorf("latest should skip releases with no installable asset, got %s", r.Tag)
	}
	// Prereleases are still a better answer than nothing installable at all.
	if r, _ := pickRelease(rels, "", only("b3")); r.Tag != "b3" {
		t.Errorf("should fall back to an installable prerelease, got %s", r.Tag)
	}
}

func TestBackends_ValidAssetURL(t *testing.T) {
	if err := validAssetURL("https://github.com/o/r/releases/download/x/y.zip"); err != nil {
		t.Errorf("github url rejected: %v", err)
	}
	for _, bad := range []string{
		"http://github.com/o/r/y.zip",
		"https://evil.example.com/y.zip",
		"https://github.com.evil.com/y.zip",
	} {
		if err := validAssetURL(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
