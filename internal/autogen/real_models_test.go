package autogen

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realModelsRoot is the local GGUF tree. Tests here are skipped when it is
// absent (empty string => every os.Stat guard fails) so CI without the models
// still passes.
//
// Resolved at runtime, not hardcoded: the tree has already moved between drives
// once, and the leftover empty directory was still Stat-able — so the guards
// passed, discovery found no models, and the tests failed instead of skipping.
// A root only counts when it actually holds a gguf. Override with
// QM_TEST_MODELS_ROOT.
var realModelsRoot = findRealModelsRoot()

func findRealModelsRoot() string {
	for _, c := range []string{os.Getenv("QM_TEST_MODELS_ROOT"), `D:\LLM\Models`, `E:\Apps\LLM\Models`} {
		if c != "" && hasGguf(c, 3) {
			return c
		}
	}
	return ""
}

// hasGguf reports whether dir contains a *.gguf within depth levels. Depth is
// bounded so a wrong candidate costs a shallow scan, not a full-tree walk.
func hasGguf(dir string, depth int) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".gguf") {
			return true
		}
	}
	if depth <= 1 {
		return false
	}
	for _, e := range ents {
		if e.IsDir() && hasGguf(filepath.Join(dir, e.Name()), depth-1) {
			return true
		}
	}
	return false
}

// ref captures the PowerShell Read-GgufMetadata + Get-LlamaLoadPlan output for a
// model, used as the golden reference for the Go port. Generated with
// TargetVramGB=7, MaxRamGB=24, KvCacheReserveGB=1.0, CudaOverheadGB=1.0.
type ref struct {
	rel       string
	arch      string
	blocks    int64
	sizeGB    float64
	moe       bool
	ctxLen    int64
	hcKv      int64
	kLen      int64
	vLen      int64
	slidWin   int64
	fullAttn  int64
	ngl       int
	ncpuMoe   int
	estVramGB float64
	estRamGB  float64

	// KV cost model (kvK=kvV=q8_0), from PowerShell Get-KvCostModel.
	kvSlope  float64
	kvConst  float64
	kvGlobal int
	kvLocal  int
	kvSsm    int

	// expertShare is the fraction of weight bytes in expert tensors, derived
	// from the gguf tensor section. 0 for dense models.
	expertShare float64
}

var refModels = []ref{
	{
		rel:  `lmstudio-community\gemma-4-E2B-it-GGUF\gemma-4-E2B-it-Q6_K.gguf`,
		arch: "gemma4", blocks: 35, sizeGB: 3.581, moe: false, ctxLen: 131072,
		hcKv: 1, kLen: 512, vLen: 512, slidWin: 512,
		ngl: 99, ncpuMoe: 0, estVramGB: 5.58, estRamGB: 0,
		kvSlope: 5.07e-06, kvConst: 0.007782, kvGlobal: 5, kvLocal: 30, kvSsm: 0,
	},
	{
		rel:  `gaston-parravicini\LFM2.5-8B-A1B-Uncensored-Gaston-GGUF\LFM2.5-8B-A1B-Uncensored-Gaston-Q4_K_M.gguf`,
		arch: "lfm2moe", blocks: 24, sizeGB: 4.801, moe: true, ctxLen: 128000,
		hcKv: 8, kLen: 64, vLen: 64,
		ngl: 99, ncpuMoe: 0, estVramGB: 6.8, estRamGB: 0,
		kvSlope: 6.08e-06, kvConst: 0, kvGlobal: 6, kvLocal: 0, kvSsm: 0,
		expertShare: 0.9059, // derived from tensor section (heuristic table said 0.78)
	},
	{
		rel:  `Jackrong\Qwen3.5-9B-Claude-4.6-Opus-Reasoning-Distilled-v2-GGUF\Qwen3.5-9B.Q4_K_M.gguf`,
		arch: "qwen35", blocks: 32, sizeGB: 5.243, moe: false, ctxLen: 262144,
		hcKv: 4, kLen: 256, vLen: 256, fullAttn: 4,
		ngl: 30, ncpuMoe: 0, estVramGB: 6.85, estRamGB: 0.39,
		kvSlope: 1.621e-05, kvConst: 0.047974, kvGlobal: 8, kvLocal: 0, kvSsm: 24,
	},
	{
		rel:  `bartowski\mistralai_Ministral-3-14B-Instruct-2512-GGUF\mistralai_Ministral-3-14B-Instruct-2512-IQ4_NL.gguf`,
		arch: "mistral3", blocks: 40, sizeGB: 7.27, moe: false, ctxLen: 262144,
		hcKv: 8, kLen: 128, vLen: 128,
		ngl: 29, ncpuMoe: 0, estVramGB: 7.00, estRamGB: 2.27,
		kvSlope: 8.106e-05, kvConst: 0, kvGlobal: 40, kvLocal: 0, kvSsm: 0,
	},
}

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.01 {
		t.Errorf("%s = %.4f, want %.4f", name, got, want)
	}
}

func TestReadGgufMetadata_VsPowerShell(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	for _, r := range refModels {
		path := filepath.Join(realModelsRoot, r.rel)
		if _, err := os.Stat(path); err != nil {
			t.Logf("skip missing %s", r.rel)
			continue
		}
		m, err := ReadGgufMetadata(path)
		if err != nil {
			t.Errorf("%s: %v", r.rel, err)
			continue
		}
		if m.Architecture != r.arch {
			t.Errorf("%s arch=%q want %q", r.rel, m.Architecture, r.arch)
		}
		if m.BlockCount != r.blocks {
			t.Errorf("%s blocks=%d want %d", r.rel, m.BlockCount, r.blocks)
		}
		if m.IsMoE != r.moe {
			t.Errorf("%s moe=%v want %v", r.rel, m.IsMoE, r.moe)
		}
		if m.ContextLength != r.ctxLen {
			t.Errorf("%s ctxLen=%d want %d", r.rel, m.ContextLength, r.ctxLen)
		}
		if m.HeadCountKv != r.hcKv {
			t.Errorf("%s hcKv=%d want %d", r.rel, m.HeadCountKv, r.hcKv)
		}
		if m.KeyLength != r.kLen {
			t.Errorf("%s kLen=%d want %d", r.rel, m.KeyLength, r.kLen)
		}
		if m.ValueLength != r.vLen {
			t.Errorf("%s vLen=%d want %d", r.rel, m.ValueLength, r.vLen)
		}
		if m.SlidingWindow != r.slidWin {
			t.Errorf("%s slidWin=%d want %d", r.rel, m.SlidingWindow, r.slidWin)
		}
		if m.FullAttnInterval != r.fullAttn {
			t.Errorf("%s fullAttn=%d want %d", r.rel, m.FullAttnInterval, r.fullAttn)
		}
		approx(t, r.rel+" sizeGB", m.FileSizeGB, r.sizeGB)
	}
}

func TestExpertWeightShare_Derived(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	for _, r := range refModels {
		path := filepath.Join(realModelsRoot, r.rel)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		m, err := ReadGgufMetadata(path)
		if err != nil {
			t.Fatalf("%s: %v", r.rel, err)
		}
		if !r.moe {
			if m.ExpertWeightShare != 0 {
				t.Errorf("%s: dense model has non-zero expert share %.4f", r.rel, m.ExpertWeightShare)
			}
			continue
		}
		if math.Abs(m.ExpertWeightShare-r.expertShare) > 0.001 {
			t.Errorf("%s expertShare=%.4f want %.4f", r.rel, m.ExpertWeightShare, r.expertShare)
		}
	}
}

func TestGetKvCostModel_VsPowerShell(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	for _, r := range refModels {
		path := filepath.Join(realModelsRoot, r.rel)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		m, err := ReadGgufMetadata(path)
		if err != nil {
			t.Fatalf("%s: %v", r.rel, err)
		}
		kv := GetKvCostModel(m, "q8_0", "q8_0")
		if !kv.OK {
			t.Errorf("%s: kv cost model not OK", r.rel)
			continue
		}
		// Slope is ~1e-5; compare with a tight absolute tolerance.
		if math.Abs(kv.SlopeGB-r.kvSlope) > 1e-7 {
			t.Errorf("%s slope=%.8g want %.8g", r.rel, kv.SlopeGB, r.kvSlope)
		}
		if math.Abs(kv.ConstGB-r.kvConst) > 1e-5 {
			t.Errorf("%s const=%.6g want %.6g", r.rel, kv.ConstGB, r.kvConst)
		}
		if kv.GlobalLayers != r.kvGlobal {
			t.Errorf("%s globalLayers=%d want %d", r.rel, kv.GlobalLayers, r.kvGlobal)
		}
		if kv.LocalLayers != r.kvLocal {
			t.Errorf("%s localLayers=%d want %d", r.rel, kv.LocalLayers, r.kvLocal)
		}
		if kv.SsmLayers != r.kvSsm {
			t.Errorf("%s ssmLayers=%d want %d", r.rel, kv.SsmLayers, r.kvSsm)
		}
	}
}

func TestGetLoadPlan_VsPowerShell(t *testing.T) {
	if _, err := os.Stat(realModelsRoot); err != nil {
		t.Skipf("models root %s absent", realModelsRoot)
	}
	for _, r := range refModels {
		path := filepath.Join(realModelsRoot, r.rel)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		m, err := ReadGgufMetadata(path)
		if err != nil {
			t.Fatalf("%s: %v", r.rel, err)
		}
		plan, err := GetLoadPlan(m, PlanOptions{TargetVramGB: 7, MaxRamGB: 24, KvCacheReserveGB: 1.0, CudaOverheadGB: 1.0, kvReserveSet: true, cudaSet: true})
		if err != nil {
			t.Fatalf("%s plan: %v", r.rel, err)
		}
		if plan.Ngl != r.ngl {
			t.Errorf("%s ngl=%d want %d", r.rel, plan.Ngl, r.ngl)
		}
		if plan.NCpuMoe != r.ncpuMoe {
			t.Errorf("%s ncpuMoe=%d want %d", r.rel, plan.NCpuMoe, r.ncpuMoe)
		}
		approx(t, r.rel+" estVram", plan.EstVramGB, r.estVramGB)
		approx(t, r.rel+" estRam", plan.EstRamGB, r.estRamGB)
	}
}
