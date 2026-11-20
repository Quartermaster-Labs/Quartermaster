package autogen

import (
	"math"
	"testing"
)

// The 27B q4_k_p the guard placed at -ngl 56/50/52 on 2026-11-20: 16.692 GB
// file, 65 blocks, 1.637 GB of token_embd + output.weight.
const (
	q4kpSize   = 16.692
	q4kpIO     = 1.637
	q4kpBlocks = 65
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// With no io figure the costing is the old whole-file split, exactly, so a
// file whose tensors could not be sized keeps the numbers it always had.
func TestDenseCost_UnknownIoIsTheOldCosting(t *testing.T) {
	const reserve = 2.5
	for _, ngl := range []int{0, 1, 32, 64, 65, 66, 99} {
		frac := math.Min(float64(ngl)/q4kpBlocks, 1)
		v, r := denseCost(q4kpSize, 0, reserve, ngl, q4kpBlocks)
		if !near(v, frac*(q4kpSize+reserve)) || !near(r, (1-frac)*(q4kpSize+reserve)) {
			t.Errorf("ngl %d: got vram %.4f ram %.4f, want the whole-file split", ngl, v, r)
		}
	}
}

// A partial placement keeps the io weights in host memory: VRAM drops by the
// GPU share the old split charged them, RAM gains it, and the total is the same
// model. Past block_count the whole-file costing (and its calibration) stands.
func TestDenseCost_PartialLeavesIoOnHost(t *testing.T) {
	const reserve = 2.5
	for _, ngl := range []int{0, 50, 52, 56, 65} {
		frac := float64(ngl) / q4kpBlocks
		oldV, oldR := denseCost(q4kpSize, 0, reserve, ngl, q4kpBlocks)
		v, r := denseCost(q4kpSize, q4kpIO, reserve, ngl, q4kpBlocks)
		if want := oldV - frac*q4kpIO; !near(v, want) {
			t.Errorf("ngl %d: vram %.4f, want %.4f", ngl, v, want)
		}
		if want := oldR + frac*q4kpIO; !near(r, want) {
			t.Errorf("ngl %d: ram %.4f, want %.4f", ngl, r, want)
		}
	}
	// -ngl 56 was charged ~1.4 GB of weights that never left host memory; the
	// measured load sat ~1.9 GB under its estimate (16714 MB vs 18.3 GB).
	oldV, _ := denseCost(q4kpSize, 0, 0, 56, q4kpBlocks)
	v, _ := denseCost(q4kpSize, q4kpIO, 0, 56, q4kpBlocks)
	if d := oldV - v; d < 1.4 || d > 1.42 {
		t.Errorf("-ngl 56 carve-out %.3f GB, want ~1.41", d)
	}
	for _, ngl := range []int{66, 99} {
		v, r := denseCost(q4kpSize, q4kpIO, reserve, ngl, q4kpBlocks)
		if !near(v, q4kpSize+reserve) || r != 0 {
			t.Errorf("ngl %d: got %.4f/%.4f, want the whole model in VRAM", ngl, v, r)
		}
	}
}

// densePlacement may take the step past block_count (output head onto the GPU)
// only where the whole-file rule allowed it: the cheaper per-layer cost must
// not let a budget that cannot hold the head place it there.
func TestDensePlacement_HeadStepUsesWholeFileRule(t *testing.T) {
	const kv, overhead = 2.0, 0.5
	whole := q4kpSize + kv
	// Enough for every block on the carved cost, not for blocks+1 on the whole file.
	target := whole + overhead - 0.05
	ngl, v, _ := densePlacement(q4kpSize, q4kpIO, kv, q4kpBlocks, target, overhead)
	if ngl != q4kpBlocks {
		t.Fatalf("got -ngl %d, want %d (head stays on the host)", ngl, q4kpBlocks)
	}
	if v > target {
		t.Errorf("est %.2f GB over the %.2f GB target", v, target)
	}
	// Room for the whole-file blocks+1 step: unchanged from before.
	target = whole*float64(q4kpBlocks+1)/q4kpBlocks + overhead + 0.01
	ngl0, v0, r0 := densePlacement(q4kpSize, 0, kv, q4kpBlocks, target, overhead)
	ngl1, v1, r1 := densePlacement(q4kpSize, q4kpIO, kv, q4kpBlocks, target, overhead)
	if ngl0 != q4kpBlocks+1 || ngl1 != ngl0 || !near(v0, v1) || r0 != r1 {
		t.Errorf("whole-model step: io-blind %d/%.3f/%.3f vs io-aware %d/%.3f/%.3f", ngl0, v0, r0, ngl1, v1, r1)
	}
	// A tight budget fits more layers once the io weights stop being charged.
	nOld, _, _ := densePlacement(q4kpSize, 0, kv, q4kpBlocks, 17, overhead)
	nNew, vNew, _ := densePlacement(q4kpSize, q4kpIO, kv, q4kpBlocks, 17, overhead)
	if nNew <= nOld || vNew > 17 {
		t.Errorf("17 GB target: -ngl %d -> %d (est %.2f), want more layers within budget", nOld, nNew, vNew)
	}
}
