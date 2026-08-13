package router

import (
	"reflect"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// budgetConf builds a one-group config with a VRAM budget and per-model
// estimates. Every model lands in group "g" (swap+exclusive, the autogen
// default) so the tests prove the budget rule OVERRIDES that static policy
// rather than riding on a permissive group setting.
func budgetConf(budgetGB float64, est map[string]float64) config.Config {
	models := make(map[string]config.ModelConfig, len(est))
	members := make([]string, 0, len(est))
	for id, gb := range est {
		models[id] = config.ModelConfig{EstVramGB: gb}
		members = append(members, id)
	}
	return config.Config{
		VramBudgetGB: budgetGB,
		Models:       models,
		Routing: config.RoutingConfig{
			Router: config.RouterConfig{
				Settings: config.RouterSettings{
					Groups: map[string]config.GroupConfig{
						"g": {Swap: true, Exclusive: true, Members: members},
					},
				},
			},
		},
	}
}

func budgetSwapper(conf config.Config) *groupSwapper {
	modelToGroup := map[string]string{}
	for gid, gcfg := range conf.Routing.Router.Settings.Groups {
		for _, mid := range gcfg.Members {
			modelToGroup[mid] = gid
		}
	}
	return &groupSwapper{config: conf, modelToGroup: modelToGroup}
}

// Under a budget, a model that fits alongside the resident set evicts nothing —
// even though its group is swap:true+exclusive:true, which is what the legacy
// policy would have evicted on.
func TestGroupSwapper_BudgetAdmitsWhenItFits(t *testing.T) {
	conf := budgetConf(24, map[string]float64{"a": 6, "b": 5, "c": 4})
	got := budgetSwapper(conf).EvictionFor("c", []string{"a", "b"})
	if len(got) != 0 {
		t.Fatalf("expected no eviction (6+5+4 <= 24), got %v", got)
	}
}

// Over budget, only as many residents are evicted as it takes to fit, taken
// least-recently-used first (runningSet order).
func TestGroupSwapper_BudgetEvictsLRUUntilItFits(t *testing.T) {
	conf := budgetConf(12, map[string]float64{"a": 5, "b": 5, "c": 5})
	// running is LRU-first: "a" is the coldest, "b" the most recent.
	got := budgetSwapper(conf).EvictionFor("c", []string{"a", "b"})
	if !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("expected only the LRU model evicted, got %v", got)
	}
}

// When even the whole evictable set doesn't free enough, everything evictable
// goes; the spawn-time guard then sizes or refuses the load.
func TestGroupSwapper_BudgetEvictsAllWhenStillOver(t *testing.T) {
	conf := budgetConf(8, map[string]float64{"a": 4, "b": 4, "big": 10})
	got := budgetSwapper(conf).EvictionFor("big", []string{"a", "b"})
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("expected every evictable resident, got %v", got)
	}
}

// A persistent group's members hold VRAM but can never be evicted: their
// estimate counts against the budget and pushes an ordinary resident out.
func TestGroupSwapper_BudgetChargesPersistentButKeepsIt(t *testing.T) {
	conf := budgetConf(12, map[string]float64{"a": 5, "c": 5})
	conf.Models["pin"] = config.ModelConfig{EstVramGB: 4}
	groups := conf.Routing.Router.Settings.Groups
	groups["pinned"] = config.GroupConfig{Persistent: true, Members: []string{"pin"}}

	got := budgetSwapper(conf).EvictionFor("c", []string{"pin", "a"})
	if !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("expected the persistent model kept and %q evicted, got %v", "a", got)
	}
}

// A resident with no estimate is always evicted: its footprint is unknown, so
// keeping it would silently overrun the budget.
func TestGroupSwapper_BudgetEvictsUnknownResident(t *testing.T) {
	conf := budgetConf(24, map[string]float64{"a": 4, "c": 4})
	conf.Models["mystery"] = config.ModelConfig{} // hand-written entry, no estimate
	groups := conf.Routing.Router.Settings.Groups
	g := groups["g"]
	g.Members = append(g.Members, "mystery")
	groups["g"] = g

	got := budgetSwapper(conf).EvictionFor("c", []string{"mystery", "a"})
	if !reflect.DeepEqual(got, []string{"mystery"}) {
		t.Fatalf("expected the unknown-footprint resident evicted and %q kept, got %v", "a", got)
	}
}

// An UNKNOWN TARGET falls back to the static group policy: admitting a model of
// unmeasured size against a budget is the case that OOMs the box.
func TestGroupSwapper_BudgetFallsBackForUnknownTarget(t *testing.T) {
	conf := budgetConf(24, map[string]float64{"a": 4, "b": 4})
	conf.Models["mystery"] = config.ModelConfig{}
	groups := conf.Routing.Router.Settings.Groups
	g := groups["g"]
	g.Members = append(g.Members, "mystery")
	groups["g"] = g

	got := budgetSwapper(conf).EvictionFor("mystery", []string{"a", "b"})
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("expected legacy swap-group eviction of every sibling, got %v", got)
	}
}

// No budget configured (hand-written config, or multiResident off) leaves the
// legacy static behaviour completely untouched.
func TestGroupSwapper_NoBudgetKeepsStaticPolicy(t *testing.T) {
	conf := budgetConf(0, map[string]float64{"a": 1, "b": 1, "c": 1})
	got := budgetSwapper(conf).EvictionFor("c", []string{"a", "b"})
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("expected swap-group eviction of every sibling, got %v", got)
	}
}
