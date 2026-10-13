package router

import (
	"reflect"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// ramConf is budgetConf plus a "cpu" ram-pool group (swap, non-exclusive, the
// shape autogen emits) and a RAM budget. gpu/cpu map id -> (vram, ram) GB.
func ramConf(vramBudget, ramBudget float64, gpu, cpu map[string][2]float64) config.Config {
	conf := budgetConf(vramBudget, nil)
	conf.RamBudgetGB = ramBudget
	groups := conf.Routing.Router.Settings.Groups
	g := groups["g"]
	for id, e := range gpu {
		conf.Models[id] = config.ModelConfig{EstVramGB: e[0], EstRamGB: e[1]}
		g.Members = append(g.Members, id)
	}
	groups["g"] = g
	var members []string
	for id, e := range cpu {
		conf.Models[id] = config.ModelConfig{EstVramGB: e[0], EstRamGB: e[1]}
		members = append(members, id)
	}
	groups["cpu"] = config.GroupConfig{Swap: true, Pool: config.GroupPoolRAM, Members: members}
	return conf
}

// Loading a CPU model never evicts a GPU model, even one that the static
// exclusive rule or a zero VRAM estimate would otherwise have removed.
func TestGroupSwapper_RAMTargetLeavesGPUAlone(t *testing.T) {
	conf := ramConf(24, 64, map[string][2]float64{"big": {20, 2}}, map[string][2]float64{"kev": {0, 5}})
	if got := budgetSwapper(conf).EvictionFor("kev", []string{"big"}); len(got) != 0 {
		t.Fatalf("CPU model evicted a GPU model: %v", got)
	}
}

// Loading a GPU model never evicts a resident CPU model for VRAM (its 0 VRAM
// estimate must not read as "unknown, evict").
func TestGroupSwapper_GPUTargetLeavesCPUAlone(t *testing.T) {
	conf := ramConf(24, 64, map[string][2]float64{"big": {20, 2}}, map[string][2]float64{"kev": {0, 5}})
	if got := budgetSwapper(conf).EvictionFor("big", []string{"kev"}); len(got) != 0 {
		t.Fatalf("GPU model evicted a CPU model: %v", got)
	}
}

// CPU models co-reside while RAM fits, and evict each other LRU-first when not.
func TestGroupSwapper_RAMBudgetAdmitsThenEvictsLRU(t *testing.T) {
	cpu := map[string][2]float64{"a": {0, 10}, "b": {0, 10}, "c": {0, 10}}
	if got := budgetSwapper(ramConf(24, 32, nil, cpu)).EvictionFor("c", []string{"a", "b"}); len(got) != 0 {
		t.Fatalf("3 x 10GB fits 32GB, got eviction %v", got)
	}
	if got := budgetSwapper(ramConf(24, 25, nil, cpu)).EvictionFor("c", []string{"a", "b"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("expected only LRU %q evicted, got %v", "a", got)
	}
}

// A GPU model's offloaded RAM share counts, and a GPU load that needs RAM can
// push CPU models out (but a CPU load can never push the GPU model out).
func TestGroupSwapper_GPUOffloadChargesRAM(t *testing.T) {
	conf := ramConf(24, 32, map[string][2]float64{"big": {20, 20}}, map[string][2]float64{"a": {0, 10}, "b": {0, 10}})
	if got := budgetSwapper(conf).EvictionFor("big", []string{"a", "b"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("expected %q evicted to make RAM room for the GPU model, got %v", "a", got)
	}
	if got := budgetSwapper(conf).EvictionFor("b", []string{"big", "a"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("expected only the CPU resident evicted, got %v", got)
	}
}

// Without a RAM budget the cpu group falls back to its static swap rule, still
// confined to its own pool.
func TestGroupSwapper_NoRAMBudgetSwapsWithinCPUGroup(t *testing.T) {
	conf := ramConf(24, 0, map[string][2]float64{"big": {20, 2}}, map[string][2]float64{"a": {0, 5}, "b": {0, 5}})
	if got := budgetSwapper(conf).EvictionFor("b", []string{"big", "a"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("expected only the CPU sibling swapped out, got %v", got)
	}
}
