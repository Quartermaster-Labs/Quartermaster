package setup

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/quartermaster-labs/quartermaster/internal/autogen"
	"github.com/quartermaster-labs/quartermaster/internal/backends"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/perf"
)

// gpuProbeTimeout bounds the one-shot GPU sample. The wizard opens on the
// result, so a machine where the probe hangs (a wedged nvidia-smi, a driver
// mid-reset) must fall through to the manual picker rather than show a spinner
// forever — the answer is a recommendation, not a requirement.
const gpuProbeTimeout = 4 * time.Second

// GpuNames returns the host's GPU names, deduped, or nil if none could be read.
//
// This mirrors (*server.Server).gpuNames, but stands alone because the wizard
// runs without a Server: it spins up a throwaway perf sampler, takes the first
// reading, and tears it down. Logging goes to io.Discard — the backends behind
// perf chatter about which detection path they picked, which is noise in a
// setup window.
func GpuNames() []string {
	ctx, cancel := context.WithTimeout(context.Background(), gpuProbeTimeout)
	defer cancel()

	ch, err := perf.GetGpuStats(ctx, time.Second, logmon.NewWriter(io.Discard))
	if err != nil {
		return nil
	}
	select {
	case stats := <-ch:
		seen := map[string]bool{}
		var out []string
		for _, g := range stats {
			n := g.Name
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
		return out
	case <-ctx.Done():
		return nil
	}
}

// VariantOption is one compute-backend choice offered on the backend step.
type VariantOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note"`
}

// ComponentOption is one installable backend offered on the backend step.
type ComponentOption struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Summary  string `json:"summary"`
	Selected bool   `json:"selected"`
}

// Probe is everything the wizard needs to render its first frame: the defaults
// it opens with and the hardware-derived recommendation.
type Probe struct {
	OS         string            `json:"os"`
	DefaultDir string            `json:"defaultDir"`
	Gpus       []string          `json:"gpus"`
	Variant    string            `json:"variant"` // recommended
	Variants   []VariantOption   `json:"variants"`
	Components []ComponentOption `json:"components"`
	HomeDir    string            `json:"homeDir"`
}

// probeComponents are the backends the wizard offers, in display order.
//
// This is deliberately a SHORT list, not backends.Catalog(). First run is not
// the place to explain vLLM's Python requirement or to expose per-version
// pickers; Settings -> Backends does all of that against the same manager once
// the app is up. The wizard's job is to get one text and one image backend on
// disk so the dashboard is not empty.
var probeComponents = []struct {
	id       string
	summary  string
	selected bool
}{
	{"llama-server", "Text models (GGUF). The one you almost certainly want.", true},
	{"sd-server", "Image generation and editing.", true},
	{"yt-dlp", "Lets chat models read YouTube transcripts. Small helper, not a backend.", false},
}

// NewProbe assembles the wizard's opening state.
func NewProbe(defaultDir string) Probe {
	gpus := GpuNames()
	p := Probe{
		OS:         runtime.GOOS,
		DefaultDir: defaultDir,
		Gpus:       gpus,
	}
	if home, err := os.UserHomeDir(); err == nil {
		p.HomeDir = home
	}

	// Variant labels and notes come from the llama-server entry rather than
	// being retyped here: the catalog already carries per-variant caveats
	// (Vulkan's 2 GB allocation cap, CUDA being Windows-only upstream) and a
	// second copy would drift from the one the Settings page shows.
	//
	// DefaultVariant rather than SuggestVariant for the recommendation: an
	// NVIDIA card suggests CUDA, but llama.cpp publishes no Linux CUDA asset, so
	// on Linux the honest recommendation is Vulkan. DefaultVariant applies that
	// fallback; SuggestVariant alone would recommend a build that cannot be
	// downloaded. A variant with no pattern for this GOOS is likewise not shown.
	if c, ok := backends.Find("llama-server"); ok {
		p.Variant = c.DefaultVariant(gpus, runtime.GOOS)
		for _, v := range c.Variants {
			if len(v.Patterns[runtime.GOOS]) == 0 {
				continue
			}
			p.Variants = append(p.Variants, VariantOption{ID: v.ID, Label: v.Label, Note: v.Note})
		}
	}
	if p.Variant == "" && len(p.Variants) > 0 {
		p.Variant = p.Variants[0].ID
	}

	for _, pc := range probeComponents {
		c, ok := backends.Find(pc.id)
		if !ok {
			continue
		}
		p.Components = append(p.Components, ComponentOption{
			ID: c.ID, Name: c.Name, Summary: pc.summary, Selected: pc.selected,
		})
	}
	return p
}

// ScanResult reports what a candidate models folder actually holds.
type ScanResult struct {
	Path   string  `json:"path"`
	Exists bool    `json:"exists"`
	Count  int     `json:"count"`
	SizeGB float64 `json:"sizeGB"`
	Error  string  `json:"error,omitempty"`
}

// Scan counts the models a folder would contribute to the catalog.
//
// It runs the REAL discovery walk rather than counting *.gguf files, so the
// number shown is the number of rows the dashboard will have: split shards
// collapse to one model, vision projectors and draft sidecars are paired to
// their parent instead of counted, and unloadable FastMTP heads are skipped. A
// raw file count would routinely overstate a well-organised folder by half.
func Scan(ctx context.Context, path string) ScanResult {
	res := ScanResult{Path: path}
	if path == "" {
		return res
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		res.Path = abs
	}
	st, err := os.Stat(res.Path)
	if err != nil || !st.IsDir() {
		return res
	}
	res.Exists = true

	// The walk is unbounded and the user is typing, so it runs detached and the
	// caller's deadline wins. A slow network share must not wedge the wizard.
	type out struct {
		rows []autogen.GgufRow
		err  error
	}
	done := make(chan out, 1)
	go func() {
		rows, err := autogen.DiscoverGgufModels(res.Path)
		done <- out{rows, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			res.Error = r.err.Error()
			return res
		}
		res.Count = len(r.rows)
		var gb float64
		for _, row := range r.rows {
			gb += row.SizeGB + row.DraftSizeGB
		}
		res.SizeGB = gb
		return res
	case <-ctx.Done():
		res.Error = "still scanning"
		return res
	}
}
