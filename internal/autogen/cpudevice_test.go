package autogen

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOverride_InCpuPool(t *testing.T) {
	cases := []struct {
		ov   *Override
		want bool
	}{
		{nil, false},
		{&Override{}, false},
		{&Override{Device: DeviceCPU}, true},
		{&Override{Device: DeviceCPU, SwapGroup: "gpu"}, false},
		{&Override{Device: DeviceCPU, SwapGroup: "cpu"}, true},
		{&Override{Device: DeviceGPU, SwapGroup: "cpu"}, false},
	}
	for _, c := range cases {
		if got := c.ov.InCpuPool(); got != c.want {
			t.Errorf("InCpuPool(%+v) = %v, want %v", c.ov, got, c.want)
		}
	}
	if SwapGroupErr("gpu", "cpu") == nil {
		t.Error("a GPU-device model in the cpu swap group must be refused")
	}
	if SwapGroupErr("cpu", "gpu") != nil || SwapGroupErr("", "") != nil {
		t.Error("valid device/group pairs must pass")
	}
}

// The cpu group is the RAM-budgeted shape: it swaps among its own members and
// is never persistent, and it is exposed on every listener like the other
// coexistence groups.
func TestAutogen_emitGroups_cpuPool(t *testing.T) {
	s := Settings{Groups: []GroupSpec{
		{Name: "assistant", Listen: "0.0.0.0:1250", Match: []string{"*"}},
	}}
	out := emitGroupsCoexist(s, []string{"qwen", "kev"}, coexistSets{CPU: []string{"kev"}})

	assistant := section(out, "  assistant:\n", "\n  cpu:")
	if strings.Contains(assistant, `- "kev"`) {
		t.Fatalf("CPU-pool model must not be in the exclusive group:\n%s", out)
	}
	cpu := section(out, "  cpu:\n", "\nlisteners:")
	for _, want := range []string{"swap: true", "exclusive: false", "persistent: false", "pool: ram", `- "kev"`} {
		if !strings.Contains(cpu, want) {
			t.Fatalf("cpu group missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "groups: [assistant, cpu]") {
		t.Fatalf("cpu group must be exposed on every listener:\n%s", out)
	}
}

func TestEmitRamBudget(t *testing.T) {
	var b strings.Builder
	emitRamBudget(&b, Settings{MaxRamGB: 48})
	if !strings.Contains(b.String(), "ramBudgetGB: 48\n") {
		t.Fatalf("missing ramBudgetGB: %q", b.String())
	}
	off := false
	b.Reset()
	emitRamBudget(&b, Settings{MaxRamGB: 48, MultiResident: &off})
	if b.Len() != 0 {
		t.Fatalf("multiResident off must withhold ramBudgetGB: %q", b.String())
	}
}

// A CPU-device decision model launches with no GPU at all, and its whole
// footprint moves from estVramGB to estRamGB so the RAM pool can admit it.
func TestDecision_CpuDevice(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 8}
	row := GgufRow{FullPath: "C:/m/Kev-4B-Q8_0.gguf", SizeGB: 4}
	meta := Metadata{Architecture: "qwen3", DecisionType: "kev", ContextLength: 40960, BlockCount: 32}
	ov := &Override{Device: DeviceCPU, CpuOffload: 8}

	cmd := strings.Join(decisionCmdLines(s, row, ov, meta), " ")
	for _, want := range []string{"-ngl 0 ", "--device none"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("cpu decision cmd missing %q: %s", want, cmd)
		}
	}
	vram, ram := decisionEstGB(s, row, ov, meta)
	full, _ := decisionEstGB(s, row, nil, meta)
	if vram != 0 || ram != full {
		t.Fatalf("cpu device: vram=%g ram=%g, want 0 and %g", vram, ram, full)
	}
	vram, ram = decisionEstGB(s, row, &Override{CpuOffload: 8}, meta)
	if vram <= 0 || ram <= 0 || vram+ram < full-1e-9 || vram+ram > full+1e-9 {
		t.Fatalf("partial offload must split the footprint: vram=%g ram=%g full=%g", vram, ram, full)
	}
}

func TestEmbedding_CpuDevice(t *testing.T) {
	s := Settings{ServerExe: "llama-server", Threads: 8}
	row := GgufRow{FullPath: "C:/m/embed.gguf", SizeGB: 0.6}
	meta := Metadata{Architecture: "bert", PoolingType: 1, ContextLength: 512}
	ov := &Override{Device: DeviceCPU}

	cmd := strings.Join(embeddingCmdLines(s, row, ov, meta), " ")
	if !strings.Contains(cmd, "-ngl 0 --device none") || strings.Contains(cmd, "-ngl 99") {
		t.Fatalf("cpu embedding cmd: %s", cmd)
	}
	var b strings.Builder
	var emitted []string
	emitEmbeddingModel(&b, s, row, ov, "embed", meta, &emitted)
	if out := b.String(); strings.Contains(out, "estVramGB") || !strings.Contains(out, "estRamGB") {
		t.Fatalf("cpu embedder must be priced in RAM only:\n%s", out)
	}
}

// End to end: every profile of a CPU-device LLM (solo + ctx tiers) launches
// with no GPU, is priced in RAM only, and joins the cpu group.
func TestGenerate_cpuDeviceLLM(t *testing.T) {
	dir := t.TempDir()
	name := "Pins-8B-Q4_K_M.gguf"
	writeStub(t, dir, name, 4_000_000_000)
	seedMetaCache(t, filepath.Join(dir, name), pinsTestMeta())

	gf := GenerateFile{
		Settings:  Settings{ModelsRoot: dir},
		Overrides: []Override{{Match: "*", Device: DeviceCPU, CtxVariants: []int{8192}}},
	}
	gf.Settings.applyDefaults()
	out, err := Generate(gf, "T")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	models := section(out, "models:\n", "\ngroups:")
	if n := strings.Count(models, "cmd: >"); n < 2 {
		t.Fatalf("test assumption broken, want solo + ctx tier, got %d profiles:\n%s", n, models)
	}
	if strings.Count(models, "--device none") != strings.Count(models, "cmd: >") {
		t.Fatalf("every profile must carry --device none:\n%s", models)
	}
	if strings.Contains(models, "estVramGB") || !strings.Contains(models, "estRamGB") {
		t.Fatalf("CPU profiles must be priced in RAM only:\n%s", models)
	}
	cpu := section(out, "  cpu:\n", "\nlisteners:")
	if !strings.Contains(cpu, "pool: ram") || strings.Count(cpu, "      - ") != strings.Count(models, "cmd: >") {
		t.Fatalf("every profile must be a cpu group member:\n%s", out)
	}
}

// A CPU-device plan must report every VRAM component as zero, not just the
// total. The dashboard draws KV / compute buffer / headroom as separate VRAM
// segments, so a 0 GB plan that still carried a 1.3 GB compute buffer painted
// a CPU-only model as holding ~2 GB of VRAM.
func TestEstimatePlan_CpuDeviceZeroesVramComponents(t *testing.T) {
	meta := Metadata{
		Architecture: "llama", BlockCount: 32,
		HeadCountKv: 8, KeyLength: 128, ValueLength: 128,
		ContextLength: 131072, EmbeddingLength: 4096,
		FileSizeGB: 4,
	}
	s := Settings{
		TargetVramGB: 24, VramOverheadGB: 0.5, MaxRamGB: 64,
		DenseCtxLadder: []int{131072, 65536, 32768}, DenseMinCtx: 4096,
	}
	est, err := EstimatePlan(s, meta, EstimateInput{Ctx: 8192, CpuDevice: true})
	if err != nil {
		t.Fatal(err)
	}
	if est.EstVramGB != 0 || est.EstRamGB <= 0 {
		t.Fatalf("cpu device: estVram=%v estRam=%v, want 0 / >0", est.EstVramGB, est.EstRamGB)
	}
	for name, v := range map[string]float64{
		"kvReserve": est.KvReserveGB, "checkpoint": est.CheckpointGB, "draft": est.DraftGB,
		"computeBuf": est.ComputeBufGB, "mmproj": est.MmprojGB, "overhead": est.OverheadGB, "fixed": est.FixedGB,
	} {
		if v != 0 {
			t.Errorf("cpu device: %s = %v, want 0", name, v)
		}
	}
}
