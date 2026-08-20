package router

import (
	"fmt"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/process"
	"github.com/quartermaster-labs/quartermaster/internal/router/scheduler"
)

type Group struct {
	*baseRouter
}

func NewGroup(conf config.Config, proxylog, upstreamlog *logmon.Monitor) (*Group, error) {
	// plan derives the eviction planner and the model→config set of every process
	// the group router should run under a config. Shared by initial construction
	// and ApplyConfig (live reload).
	// One holder for every Swapper this router will ever build: plan() runs again
	// on each ApplyConfig, and the probe is installed once, after construction.
	liveVram := &liveVramHolder{}

	plan := func(c config.Config) (scheduler.Swapper, map[string]config.ModelConfig, error) {
		modelToGroup, err := buildModelToGroup(c)
		if err != nil {
			return nil, nil, err
		}
		want := make(map[string]config.ModelConfig, len(modelToGroup))
		for mid := range modelToGroup {
			modelCfg, _, ok := c.FindConfig(mid)
			if !ok {
				return nil, nil, fmt.Errorf("no model config for %q", mid)
			}
			want[mid] = modelCfg
		}
		return &groupSwapper{config: c, modelToGroup: modelToGroup, logger: proxylog, liveVram: liveVram}, want, nil
	}

	swapper, want, err := plan(conf)
	if err != nil {
		return nil, err
	}

	processes := make(map[string]process.Process, len(want))
	base, err := newBaseRouter("group", conf, processes, proxylog, swapper)
	if err != nil {
		return nil, fmt.Errorf("creating base router: %w", err)
	}
	base.liveVram = liveVram
	base.plan = plan
	base.makeProcess = func(id string, mc config.ModelConfig) (process.Process, error) {
		return process.New(base.procCtx, id, mc, logmon.NewWriter(upstreamlog), proxylog)
	}

	for mid, modelCfg := range want {
		p, err := base.makeProcess(mid, modelCfg)
		if err != nil {
			base.shutdownFn()
			base.procCancel()
			return nil, fmt.Errorf("creating process for %q: %w", mid, err)
		}
		processes[mid] = p
	}

	g := &Group{baseRouter: base}
	go base.run()
	return g, nil
}

// buildModelToGroup maps each grouped model ID to its group, rejecting a model
// that appears in more than one group.
func buildModelToGroup(conf config.Config) (map[string]string, error) {
	modelToGroup := make(map[string]string)
	for gid, gcfg := range conf.Routing.Router.Settings.Groups {
		for _, mid := range gcfg.Members {
			if existing, dup := modelToGroup[mid]; dup {
				return nil, fmt.Errorf("model %q is in multiple groups: %q and %q", mid, existing, gid)
			}
			modelToGroup[mid] = gid
		}
	}
	return modelToGroup, nil
}

// groupSwapper decides evictions from static group configuration.
//
// Same-group siblings are stopped when the group has swap=true. Cross-group
// members are stopped only when the target's group is exclusive; loading a
// model from a non-exclusive group leaves running exclusive groups alone,
// matching the gotcha in the original ProcessGroup behaviour.
type groupSwapper struct {
	config       config.Config
	modelToGroup map[string]string
	logger       *logmon.Monitor
	// liveVram is the live VRAM-ceiling probe budgetEviction tightens against.
	// Read-only here; nil in tests that only exercise the static policy.
	liveVram *liveVramHolder
}

func (p *groupSwapper) EvictionFor(target string, running []string) []string {
	if evict, ok := p.budgetEviction(target, running); ok {
		return evict
	}

	tg := p.modelToGroup[target]
	tgCfg := p.config.Routing.Router.Settings.Groups[tg]

	seen := make(map[string]struct{})
	var result []string
	consider := func(mID string) {
		if mID == target {
			return
		}
		if _, dup := seen[mID]; dup {
			return
		}
		og := p.modelToGroup[mID]
		switch {
		case og == tg && tgCfg.Swap:
			seen[mID] = struct{}{}
			result = append(result, mID)
		// the previous ProcessGroup behaviour did not unload exclusive groups
		// when loading a non-exclusive model. This maintains that gotcha
		// for backwards compatibility. The newer swap matrix approach does not
		// have this issue.
		case og != tg && tgCfg.Exclusive:
			if ogCfg := p.config.Routing.Router.Settings.Groups[og]; !ogCfg.Persistent {
				seen[mID] = struct{}{}
				result = append(result, mID)
			}
		}
	}

	for _, mID := range running {
		consider(mID)
	}
	return result
}

// budgetEviction is the VRAM-budget admission rule, which REPLACES the static
// group policy above whenever config.VramBudgetGB is set and the target's
// footprint is known: models stay co-resident as long as the sum of their
// estVramGB fits the budget, and only when the incoming model would push the
// total over it are residents evicted — least-recently-used first, and only as
// many as it takes to fit.
//
// This is also what keeps the fork's cross-port accounting honest once groups
// stop being exclusive: one scheduler sees every group's residents, so the sum
// it admits against covers all listeners rather than relying on group
// exclusivity to serialise the GPU.
//
// ok=false hands the decision back to the static policy. That happens when no
// budget is configured (hand-written config, or multiResident off) or when the
// TARGET has no estimate — admitting a model of unknown size against a budget is
// exactly the case where guessing OOMs the box, so it falls back to the
// conservative "evict per group config" behaviour.
//
// Two rules keep the accounting from lying:
//   - persistent-group members can never be evicted, but they still OCCUPY the
//     budget (they hold real VRAM). ASR/SAM/TTS.cpp models emit no estimate
//     precisely because they are CPU-resident, so they add nothing here.
//   - an evictable resident with NO estimate is always evicted. Its footprint is
//     unknown, so keeping it would silently overrun the budget; this degenerates
//     to the legacy one-at-a-time behaviour for models the sizer can't measure.
//
// Purity: reads only config, the args, and a live VRAM SNAPSHOT (a lock-free
// read of a value the server samples elsewhere) - no mutation (see
// Swapper.EvictionFor).
func (p *groupSwapper) budgetEviction(target string, running []string) ([]string, bool) {
	budget, ok := p.effectiveBudgetGB()
	if !ok {
		return nil, false
	}
	targetGB := p.config.Models[target].EstVramGB
	if targetGB <= 0 {
		return nil, false
	}

	total := targetGB
	var victims []string
	var candidates []string // evictable, known cost, LRU-first (runningSet order)
	seen := map[string]struct{}{target: {}}
	for _, id := range running {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}

		gcfg := p.config.Routing.Router.Settings.Groups[p.modelToGroup[id]]
		estGB := p.config.Models[id].EstVramGB
		switch {
		case gcfg.Persistent:
			total += estGB
		case estGB <= 0:
			victims = append(victims, id)
		default:
			total += estGB
			candidates = append(candidates, id)
		}
	}

	// Free the least-recently-used residents until the target fits. If even
	// evicting every candidate leaves us over budget, they all go — the spawn
	// guard (autogen.LiveOffloadArgs) then sizes or refuses the load against
	// what the GPU actually has free.
	for _, id := range candidates {
		if total <= budget {
			break
		}
		total -= p.config.Models[id].EstVramGB
		victims = append(victims, id)
	}
	return victims, true
}

// effectiveBudgetGB is the VRAM the resident model set is admitted into: the
// configured vramBudgetGB, TIGHTENED to the live ceiling whenever a foreign GPU
// client is holding memory the static budget assumed we had.
//
// This is the OOM guard's admission half. Without it the router happily evicts
// residents to make room inside a 24 GB budget while a game holds 8 GB of the
// card, and the spawn guard (autogen.LiveOffloadArgs) then refuses the load
// anyway - the worst of both outcomes, since the evicted models are gone and
// the requested one never arrives. Charging the foreign VRAM here instead means
// the router either keeps fewer models co-resident or declines to disturb the
// resident set at all.
//
// ok=false hands the decision back to the static group policy: no budget
// configured. A missing or untrustworthy live reading is NOT a reason to fall
// back - it just leaves the static budget in force, which is the behaviour that
// shipped before the probe existed.
func (p *groupSwapper) effectiveBudgetGB() (float64, bool) {
	budget := p.config.VramBudgetGB
	if budget <= 0 {
		return 0, false
	}
	if live, ok := p.liveVram.ceiling(); ok && live < budget {
		budget = live
	}
	return budget, true
}

// OnSwapStart logs the budget arithmetic behind this swap — the one place a
// Swapper may log, since it fires once per real swap while EvictionFor runs on
// every request and every queue drain.
func (p *groupSwapper) OnSwapStart(target string, running []string) {
	if p.logger == nil || p.config.VramBudgetGB <= 0 {
		return
	}
	residentGB := 0.0
	for _, id := range running {
		if id != target {
			residentGB += p.config.Models[id].EstVramGB
		}
	}
	budget, _ := p.effectiveBudgetGB()
	note := ""
	if budget < p.config.VramBudgetGB {
		note = fmt.Sprintf(" (tightened from %.2fGB - foreign GPU use)", p.config.VramBudgetGB)
	}
	p.logger.Debugf("group: admitting %s (est %.2fGB) against resident %.2fGB of %.2fGB budget%s",
		target, p.config.Models[target].EstVramGB, residentGB, budget, note)
}
