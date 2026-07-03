package autogen

import (
	"math"
	"testing"
)

// A "-vision" twin's projector footprint (weights + VisionOverheadGB CLIP
// reserve) must reach EstVramGB via EstimateInput.MmprojGB — this is the charge
// the spawn-time guard (LiveOffloadArgs) feeds in so it sizes the twin like the
// baked plan, instead of sizing a projector-blind bare LLM and under-offloading.
func TestEstimatePlan_MmprojChargedToVram(t *testing.T) {
	// Small dense model with a big VRAM target -> fully GPU-resident, fixed ctx,
	// so any EstVram delta is exactly the overhead delta (placement unchanged).
	meta := Metadata{Architecture: "llama", BlockCount: 32, HeadCountKv: 8,
		KeyLength: 128, ValueLength: 128, FileSizeGB: 4, ContextLength: 8192}
	s := Settings{TargetVramGB: 40, VramOverheadGB: 1, ComputeBufFactor: 1, VisionOverheadGB: 1}
	base := EstimateInput{Ctx: 4096}

	r0, err := EstimatePlan(s, meta, base)
	if err != nil {
		t.Fatal(err)
	}
	withProj := base
	withProj.MmprojGB = mmprojVramGB(0.7, s) // 0.7 GB projector + 1.0 reserve = 1.7
	r1, err := EstimatePlan(s, meta, withProj)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Ngl != r0.Ngl {
		t.Fatalf("placement changed (ngl %d->%d); test assumption broken", r0.Ngl, r1.Ngl)
	}
	delta := r1.EstVramGB - r0.EstVramGB
	if math.Abs(delta-1.7) > 1e-6 {
		t.Fatalf("EstVram delta=%.4f want 1.7 (projector 0.7 + reserve 1.0)", delta)
	}

	// mmprojVramGB with no reserve set falls back to the default via LoadGenerateFile,
	// but the helper itself is pure: fileSize + whatever VisionOverheadGB holds.
	if g := mmprojVramGB(0.7, Settings{VisionOverheadGB: 1.5}); math.Abs(g-2.2) > 1e-9 {
		t.Fatalf("mmprojVramGB=%.4f want 2.2", g)
	}
}
