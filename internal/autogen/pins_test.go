package autogen

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestPinsFromArgs(t *testing.T) {
	cases := []struct {
		name string
		text string
		want Pins
	}{
		{name: "empty", text: ""},
		{name: "comment only", text: "# nothing here\n"},
		{
			name: "short spellings",
			text: "-c 5000 -ngl 20 -ncmoe 4 -ctk q8_0 -ctv q8_0 -ub 512 -np 2 --no-kv-offload --rope-scaling yarn --ctx-checkpoints 0 -cms 512",
			want: Pins{
				Ctx: intptr(5000), Ngl: intptr(20), NCpuMoe: intptr(4),
				KvK: "q8_0", KvV: "q8_0", Ub: intptr(512), Parallel: intptr(2), KvInRam: true,
				RopeScaling: "yarn", CtxCheckpoints: intptr(0), CheckpointMinStep: intptr(512),
			},
		},
		{
			name: "long spellings and inline values",
			text: "--ctx-size=6000 --n-gpu-layers 12 --ubatch-size=256 --parallel=3",
			want: Pins{Ctx: intptr(6000), Ngl: intptr(12), Ub: intptr(256), Parallel: intptr(3)},
		},
		{
			name: "spec chain accumulates in order",
			text: "--spec-type draft-mtp\n--spec-type ngram-map-k4v",
			want: Pins{Spec: "draft-mtp+ngram-map-k4v"},
		},
		{
			name: "last occurrence wins",
			text: "-c 4096 -ctk f16\n-c 8192 -ctk q8_0",
			want: Pins{Ctx: intptr(8192), KvK: "q8_0"},
		},
		{
			name: "junk and unknown flags are skipped",
			text: "-c abc --made-up 5 ---cache-ram 2048 -ctv",
			want: Pins{},
		},
		{
			name: "zero ctx is not a pin",
			text: "-c 0 --ctx-size -1 -ub 0",
			want: Pins{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PinsFromArgs(tc.text)
			if got.Ctx != nil != (tc.want.Ctx != nil) || (got.Ctx != nil && *got.Ctx != *tc.want.Ctx) {
				t.Fatalf("Ctx = %v, want %v", pinStr(got.Ctx), pinStr(tc.want.Ctx))
			}
			if pinStr(got.Ngl) != pinStr(tc.want.Ngl) || pinStr(got.NCpuMoe) != pinStr(tc.want.NCpuMoe) ||
				pinStr(got.Ub) != pinStr(tc.want.Ub) || pinStr(got.Parallel) != pinStr(tc.want.Parallel) {
				t.Fatalf("int pins = %+v, want %+v", got, tc.want)
			}
			if got.KvK != tc.want.KvK || got.KvV != tc.want.KvV || got.Spec != tc.want.Spec ||
				got.RopeScaling != tc.want.RopeScaling || pinStr(got.CtxCheckpoints) != pinStr(tc.want.CtxCheckpoints) ||
				pinStr(got.CheckpointMinStep) != pinStr(tc.want.CheckpointMinStep) {
				t.Fatalf("string pins = %+v, want %+v", got, tc.want)
			}
			if got.KvInRam != tc.want.KvInRam {
				t.Fatalf("KvInRam = %v, want %v", got.KvInRam, tc.want.KvInRam)
			}
			if got.Empty() != (tc.want == Pins{}) {
				t.Fatalf("Empty() = %v for %+v", got.Empty(), got)
			}
		})
	}
}

func pinStr(p *int) string {
	if p == nil {
		return "-"
	}
	return strconv.Itoa(*p)
}

// A pinned -c is the process's window, not a suggestion: it divides by the slot
// count (profile.Ctx is per-slot, -c is the shared pool) and skips the 4096
// rounding the sizer applies to its own picks.
func TestPins_ApplyToProfile(t *testing.T) {
	meta := Metadata{Architecture: "llama", BlockCount: 65}
	pins := PinsFromArgs("-c 8000 --parallel 2 -ngl 45 -ctk q8_0 -ub 256 --ctx-checkpoints 4 -cms 256")

	prof := profile{Name: "p"}
	pins.ApplyToProfile(&prof, meta, 2)
	if prof.Ctx != 4000 || !prof.CtxExact {
		t.Fatalf("ctx = %d exact=%v, want 4000 exact", prof.Ctx, prof.CtxExact)
	}
	if prof.IsLong {
		t.Fatal("4000 must not be a long profile")
	}
	if prof.CpuOffload != 20 || !prof.CpuOffloadSet {
		t.Fatalf("cpu offload = %d set=%v, want 20 set (65-45)", prof.CpuOffload, prof.CpuOffloadSet)
	}
	if prof.KvK != "q8_0" || prof.Ub != 256 || prof.CtxCheckpoints == nil || *prof.CtxCheckpoints != 4 || prof.CheckpointMinStep != 256 {
		t.Fatalf("profile = %+v", prof)
	}

	// -ngl 65 on a 65-block model pins every layer to the GPU: the zero value
	// means "let the sizer decide", so the Set flag carries it.
	prof = profile{Name: "dense"}
	PinsFromArgs("-ngl 65").ApplyToProfile(&prof, meta, 1)
	if prof.CpuOffload != 0 || !prof.CpuOffloadSet {
		t.Fatalf("ngl=blocks: offload = %d set=%v, want 0 set", prof.CpuOffload, prof.CpuOffloadSet)
	}

	// MoE placement is an expert split: --n-cpu-moe maps, -ngl is not guessed at.
	moe := Metadata{Architecture: "qwen3moe", BlockCount: 48, IsMoE: true}
	prof = profile{Name: "moe"}
	PinsFromArgs("-ngl 10").ApplyToProfile(&prof, moe, 1)
	if prof.CpuOffloadSet {
		t.Fatalf("-ngl must not force a MoE expert split: %+v", prof)
	}
	PinsFromArgs("--n-cpu-moe 0").ApplyToProfile(&prof, moe, 1)
	if prof.CpuOffload != 0 || !prof.CpuOffloadSet {
		t.Fatalf("n-cpu-moe 0: offload = %d set=%v, want 0 set", prof.CpuOffload, prof.CpuOffloadSet)
	}

	// A variant with its own text carries its own engine knobs into sizing.
	v := VariantSpec{Name: "tiny"}
	prof = profile{Name: "tiny", Variant: &v}
	PinsFromArgs("--parallel 4 --no-kv-offload --rope-scaling linear").ApplyToProfile(&prof, meta, 1)
	if v.Parallel != 4 || !v.KvInRam || v.RopeScaling != "linear" {
		t.Fatalf("variant spec not pinned: %+v", v)
	}
}

// The estimate endpoint's counterpart: pins overwrite the form params they
// address and convert -c to the per-slot window the sizer works in.
func TestPins_ApplyToEstimate(t *testing.T) {
	in := EstimateInput{Ctx: 32768, KvK: "f16", CpuOffload: 3}
	PinsFromArgs("-c 8000 --parallel 2 -ctk q8_0 -ngl 60").ApplyToEstimate(&in, Metadata{BlockCount: 65})
	if in.Ctx != 4000 || !in.CtxExact {
		t.Fatalf("ctx = %d exact=%v, want 4000 exact", in.Ctx, in.CtxExact)
	}
	if in.Parallel != 2 {
		t.Fatalf("parallel = %d, want 2", in.Parallel)
	}
	if in.KvK != "q8_0" || in.KvV != "" {
		t.Fatalf("kv = %q/%q, want q8_0/unset", in.KvK, in.KvV)
	}
	if in.CpuOffload != 5 || !in.CpuOffloadSet {
		t.Fatalf("cpu offload = %d set=%v, want 5 set", in.CpuOffload, in.CpuOffloadSet)
	}
}

// A pinned ctx must survive the sizer exactly, in the estimate and in the
// command the editor previews: 5000 means 5000, not RoundedCtx(5000) = 4096.
func TestPinnedCtxIsExact(t *testing.T) {
	meta := pinsTestMeta()
	s := Settings{TargetVramGB: 40, VramOverheadGB: 1, ComputeBufFactor: 1}
	s.applyDefaults()

	// Without the exact flag the dense ladder rounds the same input down: this
	// is the behaviour the pin must be able to turn off.
	rounded, err := EstimatePlan(s, meta, EstimateInput{Ctx: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if rounded.Ctx != 4096 {
		t.Fatalf("unpinned 5000 rounded to %d, want 4096 (test assumption broken)", rounded.Ctx)
	}

	var in EstimateInput
	PinsFromArgs("-c 5000 -ctk q8_0").ApplyToEstimate(&in, meta)
	pinned, err := EstimatePlan(s, meta, in)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Ctx != 5000 {
		t.Fatalf("pinned ctx = %d, want 5000", pinned.Ctx)
	}
	wantKv := KvReserveGB(5000, GetKvCostModel(meta, "q8_0", "q8_0").SlopeGB, GetKvCostModel(meta, "q8_0", "q8_0").ConstGB)
	if math.Abs(pinned.KvReserveGB-wantKv) > 1e-9 {
		t.Fatalf("kv reserve = %.4f, want %.4f at the pinned window", pinned.KvReserveGB, wantKv)
	}
	// The whole point: the pinned preview is lighter than the sizer's own pick of
	// the same input, because the KV it charges is 5000 tokens wide.
	if pinned.EstVramGB >= rounded.EstVramGB {
		t.Fatalf("pinned est %.3f not below rounded est %.3f", pinned.EstVramGB, rounded.EstVramGB)
	}
}

// --parallel N gives N slots each carrying the form's per-slot window, over one
// -c pool of N x that window (buildCmdLines emits ctx*parallel), so the KV the
// sizer solves against is N x the per-slot slope. Missing that under-sized every
// multi-slot model. A PINNED -c is the pool itself: it divides across the slots
// and the reserve stays whatever the pool is, however many slots share it.
func TestEstimatePlan_parallelChargesEverySlot(t *testing.T) {
	meta := pinsTestMeta()
	s := Settings{TargetVramGB: 40, VramOverheadGB: 1, ComputeBufFactor: 1}
	s.applyDefaults()

	r1, err := EstimatePlan(s, meta, EstimateInput{Ctx: 8192, CtxExact: true})
	if err != nil {
		t.Fatal(err)
	}
	two := EstimateInput{Ctx: 8192, CtxExact: true, Parallel: 2}
	if slots := EffectiveParallel(&Override{Parallel: two.Parallel}); slots != 2 {
		t.Fatalf("test assumption broken: %d slots", slots)
	}
	r2, err := EstimatePlan(s, meta, two)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Ctx != 8192 {
		t.Fatalf("per-slot ctx = %d, want the form's 8192", r2.Ctx)
	}
	if math.Abs(r2.KvReserveGB-2*r1.KvReserveGB) > 1e-9 {
		t.Fatalf("kv reserve = %.4f for 2 slots, want 2 x %.4f", r2.KvReserveGB, r1.KvReserveGB)
	}

	// The pool form: -c 8192 split two ways is 4096 per slot, and the reserve is
	// the same 8192-token pool as the single-slot pin.
	var pooled EstimateInput
	PinsFromArgs("-c 8192 --parallel 2").ApplyToEstimate(&pooled, meta)
	if pooled.Ctx != 4096 {
		t.Fatalf("pooled ctx = %d, want 4096 per slot", pooled.Ctx)
	}
	rp, err := EstimatePlan(s, meta, pooled)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(rp.KvReserveGB-r1.KvReserveGB) > 1e-9 {
		t.Fatalf("pooled reserve = %.4f, want the same pool as one slot (%.4f)", rp.KvReserveGB, r1.KvReserveGB)
	}
}

// End to end through the emitter: the saved config's -c and its baked
// estVramGB describe the pinned launch (issue #38's panel, after the fix).
func TestGenerate_pinnedCtxSizesThePlan(t *testing.T) {
	dir := t.TempDir()
	name := "Pins-8B-Q4_K_M.gguf"
	writeStub(t, dir, name, 4_000_000_000)
	seedMetaCache(t, filepath.Join(dir, name), pinsTestMeta())

	gen := func(custom string) string {
		t.Helper()
		gf := GenerateFile{
			Settings:  Settings{ModelsRoot: dir},
			Overrides: []Override{{Match: "*", CustomArgs: custom}},
		}
		gf.Settings.applyDefaults()
		out, err := Generate(gf, "T")
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		return out
	}

	auto := gen("")
	pinned := gen("-c 5000")

	if !strings.Contains(pinned, "-c 5000") {
		t.Fatalf("pinned -c not emitted:\n%s", pinned)
	}
	if strings.Contains(pinned, "-c 4096") {
		t.Fatalf("pinned -c was rounded to 4096:\n%s", pinned)
	}
	if !strings.Contains(auto, "-c 32768") {
		t.Fatalf("test assumption broken, auto plan is not the trained window:\n%s", auto)
	}
	// The pin sizes the plan, not just the argv: a 5000-token window reserves
	// ~0.02 GB of KV where the auto 32768 reserves ~0.12, so the baked figure has
	// to come down with it.
	if a, p := firstEstVram(t, auto), firstEstVram(t, pinned); p >= a {
		t.Fatalf("estVramGB = %.2f pinned, %.2f auto; the pin must size the plan", p, a)
	}
}

// The same acceptance through the editor preview path: the generated layer the
// UI shows carries the pinned ctx, and the composed command says it once.
func TestRenderSoloCmd_pinnedCtxSizesThePreview(t *testing.T) {
	meta := pinsTestMeta()
	s := Settings{}
	s.applyDefaults()
	row := GgufRow{FullPath: "/pins.gguf", FileName: "Pins-8B-Q4_K_M.gguf"}

	cc, err := RenderSoloCmdLayers(s, meta, row, Override{CustomArgs: "-c 5000"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cc.Generated, "-c 5000") {
		t.Fatalf("generated layer not sized to the pin: %s", cc.Generated)
	}
	if strings.Count(cc.Effective, "-c ") != 1 || !strings.Contains(cc.Effective, "-c 5000") {
		t.Fatalf("composed command = %s, want exactly one -c 5000", cc.Effective)
	}
	if strings.Contains(cc.Effective, "-c 4096") {
		t.Fatalf("composed command was rounded: %s", cc.Effective)
	}
}

func pinsTestMeta() Metadata {
	return Metadata{
		Architecture: "llama", BlockCount: 32, HeadCountKv: 8,
		KeyLength: 128, ValueLength: 128,
		FileSizeGB: 4.0, ContextLength: 32768,
	}
}

// seedMetaCache registers metadata for a stub file so the generate path sizes it
// with no real gguf header (ReadGgufMetadataCached reads the cache first).
func seedMetaCache(t *testing.T, path string, meta Metadata) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	metaCacheMu.Lock()
	metaCache[filepath.ToSlash(path)] = cachedMeta{size: fi.Size(), mtime: fi.ModTime().UnixNano(), meta: meta}
	metaCacheMu.Unlock()
}

var estVramRe = regexp.MustCompile(`estVramGB: ([0-9.]+)`)

func firstEstVram(t *testing.T, out string) float64 {
	t.Helper()
	m := estVramRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no estVramGB in:\n%s", out)
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
