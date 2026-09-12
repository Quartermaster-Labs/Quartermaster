package autogen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// trellisDefaultExe is the trellis2-server binary name. TRELLIS.2 is the second
// backend with no legacy Settings exe (SAM was the first): when no backend
// registry entry of class "3d" is installed the exe derives as a sibling of
// ServerExe, where a managed install puts it, and as the bare name only when
// ServerExe itself carries no directory.
const trellisDefaultExe = "trellis2-server"

// trellisPkgFile marks a TRELLIS.2 package directory. The layout the upstream
// tools/download_weights.py writes is one folder holding pipeline.json (plus
// texturing_pipeline.json) and ckpts/*.safetensors.
const trellisPkgFile = "pipeline.json"

// trellisCacheBudgetMiB is the GPU stage cache handed to the server with
// --model-cache-budget-mib. A 4B package is ~16 GB of bf16 checkpoints, more
// than a consumer card holds, so the server streams a stage at a time through a
// bounded cache instead of making the whole set resident; this is how much it
// may keep. 8 GB holds a stage and its neighbour on a 16 GB card with room left
// for the encoder and activations.
const trellisCacheBudgetMiB = 8192

// trellisEstVramGB is the router's admission estimate: the stage cache above,
// plus the DINOv3 encoder (~1.2 GB) and one pass's working set (shape and
// texture activations, the Vulkan texture baker, the mesh postprocessor). It is
// deliberately NOT the on-disk package size, since the checkpoints never land on
// the GPU together. It stays an estimate: estVramGB in the config overrides it,
// and the dashboard's per-process VRAM reading is the number to trust once a
// model has actually run.
var trellisEstVramGB = float64(trellisCacheBudgetMiB+3*1024) / 1024

// The two folders a TRELLIS.2 package is paired with: the DINOv3 image encoder
// (required - the shape stage conditions on its features) and BiRefNet (the
// background remover, only needed for inputs that are not already isolated).
var (
	trellisDinoRe  = regexp.MustCompile(`(?i)dino`)
	trellisBirefRe = regexp.MustCompile(`(?i)birefnet`)
)

// trellisFallbackExe returns the sibling-of-ServerExe trellis2-server path, or
// the bare name when ServerExe carries no directory (mirrors samFallbackExe).
func trellisFallbackExe(s Settings) string {
	if strings.ContainsAny(s.ServerExe, `/\`) {
		return filepath.Join(filepath.Dir(s.ServerExe), trellisDefaultExe)
	}
	return trellisDefaultExe
}

// trellisExe resolves the trellis2-server binary: the backend registry's class
// "3d" entry when one is installed, else the sibling-of-ServerExe fallback, else
// PATH.
func trellisExe(s Settings, ov *Override) string {
	if exe := resolveBackend(s, ov, "3d").Exe; exe != "" {
		return exe
	}
	return trellisFallbackExe(s)
}

// IsTrellisPackageDir reports whether dir holds a TRELLIS.2 package, which is
// what makes it a servable 3D model: pipeline.json plus a ckpts/ folder of
// safetensors. The directory is the "model" — there is no file to point at, and
// no gguf header to read.
func IsTrellisPackageDir(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, trellisPkgFile))
	return err == nil && !fi.IsDir() && fi.Size() > 0
}

// trellisRow builds the row for a TRELLIS.2 package directory: FullPath is the
// directory the server is pointed at with --model, and SizeGB is the whole
// package (every checkpoint), which is what the emitted comment reports.
func trellisRow(pkgDir string) GgufRow {
	base := filepath.Base(pkgDir)
	id := slugify(base)
	return GgufRow{
		ID:        id,
		BaseID:    id,
		FullPath:  pkgDir,
		FileName:  base,
		SizeGB:    dirSizeGB(pkgDir),
		IsTrellis: true,
	}
}

// dirSizeGB sums every file under dir, for the row's size (a package directory
// has no single file to stat). Errors are ignored like the walk's are: an
// unreadable subtree just contributes nothing.
func dirSizeGB(dir string) float64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			total += fi.Size()
		}
		return nil
	})
	return round(float64(total)/gib, 2)
}

// trellisSidecars locates the DINOv3 encoder directory and the BiRefNet gguf
// that go with a TRELLIS.2 package. Each is looked for inside the package first
// and then beside it, because the layout the weights script and README describe
// puts all three folders under one parent:
//
//	TRELLIS.2/TRELLIS.2-4B/{pipeline.json,ckpts/}   <- --model
//	TRELLIS.2/dinov3-vitl16-.../{model.safetensors,config.json}  <- --dino
//	TRELLIS.2/BiRefNet/BiRefNet-F16.gguf            <- --birefnet
//
// Either result may be "" - the caller decides whether that is fatal.
func trellisSidecars(pkgDir, modelsRoot string) (dinoDir, birefNet string) {
	dirs := []string{pkgDir, filepath.Dir(pkgDir)}
	// The hub downloads a repo to `<models root>/<owner>/<repo>`, so a package
	// and its encoder come from two different owners and land in two different
	// owner folders. Looking one level into the models root is what makes that
	// layout work without the user moving anything; the two above still win, so
	// an explicitly arranged folder always beats a stray match elsewhere.
	if modelsRoot != "" {
		entries, err := os.ReadDir(modelsRoot)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(modelsRoot, e.Name()))
				}
			}
		}
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if dinoDir == "" && trellisDinoRe.MatchString(e.Name()) && trellisIsDinoDir(p) {
					dinoDir = p
				}
				if birefNet == "" && trellisBirefRe.MatchString(e.Name()) {
					birefNet = trellisBirefNetIn(p)
				}
				continue
			}
			name := e.Name()
			if birefNet == "" && strings.EqualFold(filepath.Ext(name), ".gguf") && trellisBirefRe.MatchString(name) {
				birefNet = p
			}
		}
	}
	return dinoDir, birefNet
}

// trellisIsDinoDir requires the encoder's two files, the same pair the server's
// own readiness check insists on. A folder that merely matches the name (an
// empty leftover, a download that stopped after config.json) must not be passed
// as --dino: the server would then refuse every request with "DINOv3 weights are
// missing or incomplete".
func trellisIsDinoDir(dir string) bool {
	for _, f := range []string{"model.safetensors", "config.json"} {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil || fi.Size() == 0 {
			return false
		}
	}
	return true
}

// trellisBirefNetIn returns the first .gguf directly inside a BiRefNet folder.
func trellisBirefNetIn(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".gguf") {
			continue
		}
		return filepath.Join(dir, e.Name())
	}
	return ""
}

// trellisCmdLines builds the trellis2-server argv (exe first) for a TRELLIS.2
// package directory. Shared by emitTrellisModel and RenderSoloCmd so the editor
// preview matches a save. `${PORT}` is what makes the config derive the proxy
// URL.
//
// No --pipeline / --steps / --texture-size: the server's defaults are the 512
// profile, 12 steps and 1024px textures, which is the profile that works on
// every backend we ship (the 1024 profile resets RDNA3 drivers - see the docs).
// extraArgs can add them; an explicit --model-cache-budget-mib in extraArgs
// instead overrides the one emitted here.
func trellisCmdLines(s Settings, row GgufRow, ov *Override) ([]string, error) {
	dinoDir, birefNet := trellisSidecars(row.FullPath, s.ModelsRoot)
	if dinoDir == "" {
		return nil, fmt.Errorf("no DINOv3 encoder near %s (expected a dinov3-* folder holding model.safetensors and config.json, in the package folder, beside it, or anywhere under the models root; the shape stage cannot run without it)", row.FullPath)
	}
	lines := []string{
		trellisExe(s, ov),
		fmt.Sprintf("--model %s", cmdPath(row.FullPath)),
		fmt.Sprintf("--dino %s", cmdPath(dinoDir)),
	}
	if birefNet != "" {
		lines = append(lines, fmt.Sprintf("--birefnet %s", cmdPath(birefNet)))
	}
	lines = append(lines,
		"--host 127.0.0.1",
		"--port ${PORT}",
		fmt.Sprintf("--model-cache-budget-mib %d", trellisCacheBudgetMiB),
	)
	if ov != nil {
		if extra := strings.TrimSpace(ov.ExtraArgs); extra != "" {
			lines = append(lines, extra)
		}
	}
	return lines, nil
}

// emitTrellisModel writes a trellis2-server YAML entry for a TRELLIS.2 package.
// Served via POST /v1/3d/generations?model=<id> with the source image as the
// body (the id cannot ride in the body - the body IS the image) and the
// generated GLB fetched from /output/<name>; see the 3D generation docs.
//
// Deliberately not a coexist group: the server holds ~10 GB of VRAM while it
// runs, so it takes its turn in the exclusive group like any other large model.
// That also means an idle one is evictable by the VRAM budget, which is what
// keeps it from blocking a chat model on a 16 GB card.
func emitTrellisModel(b *strings.Builder, s Settings, row GgufRow, ov *Override, name string, emitted *[]string) error {
	lines, err := trellisCmdLines(s, row, ov)
	if err != nil {
		return err
	}
	fmt.Fprintf(b, "\n  # size=%gGB (TRELLIS.2 image-to-3D, trellis2-server, %s)\n", row.SizeGB, row.FileName)
	fmt.Fprintf(b, "  %q:\n", name)
	b.WriteString("    cmd: >\n")
	for _, line := range lines {
		fmt.Fprintf(b, "      %s\n", line)
	}
	fmt.Fprintf(b, "    ttl: %d\n", s.TtlSec)
	writeSingleDeviceEnv(b, s, trellisExe(s, ov))
	writeEstVram(b, trellisEstVramGB)
	// /health, not /ready: the process being up is what the spawn wait needs. A
	// package that is still downloading answers /health but 503s on /generate
	// with the reason, which beats a process that never becomes ready.
	b.WriteString("    checkEndpoint: /health\n")
	if ov != nil && ov.Unlisted {
		b.WriteString("    unlisted: true\n")
	}
	b.WriteString("    capabilities:\n")
	b.WriteString("      in: [image]\n")
	b.WriteString("      out: [3d]\n")
	writeDisplayName(b, s, name)
	*emitted = append(*emitted, name)
	return nil
}
