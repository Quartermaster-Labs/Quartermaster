package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
)

// upscaleMu serializes upscale runs. The ncnn upscaler holds GPU memory for the
// duration of a run; stacking two concurrent runs risks a VRAM OOM on a box that
// is also holding an SD/LLM model. Local single-user inference doesn't need
// parallel upscales, so one-at-a-time is the safe default.
var upscaleMu sync.Mutex

// upscaleModelName guards the -n argument against path traversal / arg injection:
// a model name maps to <dir>/<name>.param + <dir>/<name>.bin, so only bare
// basename characters are allowed.
var upscaleModelName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// upscaleTimeout caps a single upscale run. A 4x pass on a large image is a few
// seconds on GPU; a minute is a generous ceiling before we treat it as hung.
const upscaleTimeout = 120 * time.Second

type upscaleRequest struct {
	Image string `json:"image"` // data URL or raw base64 PNG/JPEG
	Model string `json:"model"` // optional: ncnn model name (default: first discovered)
	Scale int    `json:"scale"` // optional: 2..4 (default 4)
}

type upscaleResponse struct {
	Image string `json:"image"` // data URL (PNG)
	Model string `json:"model"`
	Scale int    `json:"scale"`
}

// handleUpscale runs a standalone ESRGAN/RealESRGAN upscale via the
// realesrgan-ncnn-vulkan CLI (exec-per-request — no persistent process, no
// scheduler/VRAM-swap accounting). The upscaler binary is the "upscale"-kind
// entry in the backend registry; its ncnn model files (<name>.param/.bin) live
// in <exeDir>/models (Upscayl layout) or beside the exe.
func (s *Server) handleUpscale(w http.ResponseWriter, r *http.Request) {
	if s.autogen == nil {
		http.Error(w, "upscale requires -generate mode", http.StatusNotImplemented)
		return
	}

	var req upscaleRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	raw, err := decodeImageData(req.Image)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	bin, modelDir, modelName, err := s.resolveUpscaler(req.Model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	scale := req.Scale
	if scale == 0 {
		scale = 4
	}
	if scale < 2 || scale > 4 {
		http.Error(w, "scale must be between 2 and 4", http.StatusBadRequest)
		return
	}

	tmp, err := os.MkdirTemp("", "qm-upscale-")
	if err != nil {
		http.Error(w, "scratch dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmp)
	inPath := filepath.Join(tmp, "in.png")
	outPath := filepath.Join(tmp, "out.png")
	if err := os.WriteFile(inPath, raw, 0o600); err != nil {
		http.Error(w, "write input: "+err.Error(), http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), upscaleTimeout)
	defer cancel()

	// -s must be the MODEL's native scale, not the caller's wish. An ncnn ESRGAN
	// net has a fixed output ratio baked into its weights; -s only tells the CLI
	// how big to make the output canvas and how far apart to place each processed
	// tile. Passing -s 2 to an x4 model (all we ship) makes it write 4x-sized tiles
	// at 2x stride into a 2x canvas: tiles overwrite each other, land at the wrong
	// offsets, and the tail of the image is never covered — a stitched-together
	// collage with white gaps, not an upscale. So run native, then resample down to
	// whatever the caller asked for.
	native := upscaleNativeScale(modelName)
	cmd := exec.CommandContext(ctx, bin,
		"-i", inPath,
		"-o", outPath,
		"-n", modelName,
		"-m", modelDir,
		"-s", strconv.Itoa(native),
		"-g", "0",
		"-t", "200",
		"-f", "png",
	)
	hideConsole(cmd) // no CLI window popup (Windows)

	upscaleMu.Lock()
	out, runErr := cmd.CombinedOutput()
	upscaleMu.Unlock()

	if runErr != nil {
		s.proxylog.Warnf("upscale: %s failed: %v\n%s", filepath.Base(bin), runErr, strings.TrimSpace(string(out)))
		msg := "upscale failed: " + runErr.Error()
		if ctx.Err() == context.DeadlineExceeded {
			msg = "upscale timed out"
		}
		http.Error(w, msg, http.StatusBadGateway)
		return
	}

	result, err := os.ReadFile(outPath)
	if err != nil || len(result) == 0 {
		s.proxylog.Warnf("upscale: no output produced by %s\n%s", filepath.Base(bin), strings.TrimSpace(string(out)))
		http.Error(w, "upscale produced no output", http.StatusBadGateway)
		return
	}

	if scale != native {
		shrunk, serr := resamplePNG(result, float64(scale)/float64(native))
		if serr != nil {
			// The native-scale image is correct, just bigger than asked. Returning it
			// beats failing: callers resize to their exact target anyway.
			s.proxylog.Warnf("upscale: downscale %dx->%dx failed: %v", native, scale, serr)
			scale = native
		} else {
			result = shrunk
		}
	}

	resp := upscaleResponse{
		Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(result),
		Model: modelName,
		Scale: scale,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// upscaleNativeScaleRe matches the ratio baked into an ncnn model's file name —
// both the "x4plus" and the "4x-UltraSharp" / "up2x" naming conventions.
var upscaleNativeScaleRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:x([234])|([234])x)`)

// upscaleNativeScale reports the fixed output ratio of an ncnn ESRGAN model,
// read off its name. Defaults to 4: every RealESRGAN weight in common
// circulation is x4, and guessing high is the safe error — a too-large result
// gets resampled down, while a too-small one mis-tiles (see handleUpscale).
func upscaleNativeScale(modelName string) int {
	m := upscaleNativeScaleRe.FindStringSubmatch(modelName)
	if m == nil {
		return 4
	}
	digit := m[1]
	if digit == "" {
		digit = m[2]
	}
	n, err := strconv.Atoi(digit)
	if err != nil || n < 2 || n > 4 {
		return 4
	}
	return n
}

// resamplePNG scales a PNG by ratio (< 1 here — we only ever shrink a native-scale
// result down to the requested scale). Box-average, not nearest: the ratio is
// typically 1/2, where averaging each source block is both the correct downsample
// and cheap. Stdlib only, so no new dependency for one resize.
func resamplePNG(pngBytes []byte, ratio float64) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	sb := src.Bounds()
	dw := int(float64(sb.Dx())*ratio + 0.5)
	dh := int(float64(sb.Dy())*ratio + 0.5)
	if dw < 1 || dh < 1 {
		return nil, fmt.Errorf("target size %dx%d is empty", dw, dh)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0 := sb.Min.Y + y*sb.Dy()/dh
		y1 := sb.Min.Y + (y+1)*sb.Dy()/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dw; x++ {
			x0 := sb.Min.X + x*sb.Dx()/dw
			x1 := sb.Min.X + (x+1)*sb.Dx()/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					// Premultiplied, so fully transparent pixels can't drag colour in.
					pr, pg, pb, pa := src.At(sx, sy).RGBA()
					r, g, b, a, n = r+uint64(pr), g+uint64(pg), b+uint64(pb), a+uint64(pa), n+1
				}
			}
			r, g, b, a = r/n, g/n, b/n, a/n
			// Un-premultiply back to NRGBA.
			if a > 0 {
				r, g, b = r*0xffff/a, g*0xffff/a, b*0xffff/a
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(min64(r, 0xffff) >> 8)
			dst.Pix[i+1] = uint8(min64(g, 0xffff) >> 8)
			dst.Pix[i+2] = uint8(min64(b, 0xffff) >> 8)
			dst.Pix[i+3] = uint8(a >> 8)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return buf.Bytes(), nil
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// resolveUpscaler returns the upscaler exe, its model directory, and the model
// name to use. The exe is the ★default (or first) "upscale"-kind entry in the
// backend registry. The model directory is <exeDir>/models if present, else the
// exe's own directory. modelName is the request's model (validated to exist) or
// the first discovered .param when unspecified.
func (s *Server) resolveUpscaler(want string) (bin, modelDir, modelName string, err error) {
	entries, lerr := autogen.LoadSidecarBackendList(s.autogen.GeneratePath)
	if lerr != nil {
		return "", "", "", fmt.Errorf("load backends: %w", lerr)
	}
	bin = pickUpscalerExe(entries)
	if bin == "" {
		return "", "", "", fmt.Errorf("no upscale backend configured (add a backend of kind 'upscale' pointing at realesrgan-ncnn-vulkan)")
	}
	if _, statErr := os.Stat(bin); statErr != nil {
		return "", "", "", fmt.Errorf("upscaler exe not found: %s", bin)
	}

	exeDir := filepath.Dir(bin)
	modelDir = filepath.Join(exeDir, "models")
	if _, statErr := os.Stat(modelDir); statErr != nil {
		modelDir = exeDir
	}

	names := discoverUpscaleModels(modelDir)
	if len(names) == 0 {
		return "", "", "", fmt.Errorf("no upscale models (*.param) found in %s", modelDir)
	}

	want = strings.TrimSpace(want)
	if want == "" {
		return bin, modelDir, names[0], nil
	}
	if !upscaleModelName.MatchString(want) {
		return "", "", "", fmt.Errorf("invalid model name")
	}
	for _, n := range names {
		if n == want {
			return bin, modelDir, want, nil
		}
	}
	return "", "", "", fmt.Errorf("upscale model %q not found in %s", want, modelDir)
}

// pickUpscalerExe returns the path of the ★default upscale-kind backend, else
// the first upscale-kind entry, else empty.
func pickUpscalerExe(entries []autogen.BackendEntry) string {
	var first string
	for _, e := range entries {
		if !isUpscaleKind(e.Kind) {
			continue
		}
		p := strings.TrimSpace(e.Path)
		if p == "" {
			continue
		}
		if e.Default {
			return p
		}
		if first == "" {
			first = p
		}
	}
	return first
}

func isUpscaleKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "upscale", "realesrgan", "esrgan":
		return true
	}
	return false
}

// discoverUpscaleModels lists the ncnn model base names (files with both a
// .param and a .bin) in dir.
func discoverUpscaleModels(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".param") {
			continue
		}
		base := name[:len(name)-len(".param")]
		if _, err := os.Stat(filepath.Join(dir, base+".bin")); err != nil {
			continue
		}
		names = append(names, base)
	}
	return names
}

// decodeImageData accepts a data URL (data:image/...;base64,XXXX) or a bare
// base64 string and returns the decoded bytes.
func decodeImageData(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("no image provided")
	}
	if i := strings.Index(s, ","); strings.HasPrefix(s, "data:") && i >= 0 {
		s = s[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty image")
	}
	return raw, nil
}
