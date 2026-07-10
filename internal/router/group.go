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
		return &groupSwapper{config: c, modelToGroup: modelToGroup}, want, nil
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
}

func (p *groupSwapper) EvictionFor(target string, running []string) []string {
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

func (p *groupSwapper) OnSwapStart(target string, running []string) {}
