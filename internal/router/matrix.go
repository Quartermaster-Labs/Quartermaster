package router

import (
	"fmt"

	"github.com/quartermaster-labs/quartermaster/internal/config"
	"github.com/quartermaster-labs/quartermaster/internal/logmon"
	"github.com/quartermaster-labs/quartermaster/internal/process"
	"github.com/quartermaster-labs/quartermaster/internal/router/scheduler"
)

type Matrix struct {
	*baseRouter
}

func NewMatrix(conf config.Config, proxylog, upstreamlog *logmon.Monitor) (*Matrix, error) {
	// plan derives the eviction planner and the desired process set. The matrix
	// router runs a process for every model in the config — any model can run
	// alone even if it is not part of a set. Shared by construction + ApplyConfig.
	plan := func(c config.Config) (scheduler.Swapper, map[string]config.ModelConfig, error) {
		mtx := c.Routing.Router.Settings.Matrix
		if mtx == nil {
			return nil, nil, fmt.Errorf("matrix router requires a matrix configuration")
		}
		want := make(map[string]config.ModelConfig, len(c.Models))
		for mid, mc := range c.Models {
			want[mid] = mc
		}
		swapper := &matrixSwapper{
			solver: newMatrixSolver(mtx.ExpandedSets, mtx.ResolvedEvictCosts()),
			logger: proxylog,
		}
		return swapper, want, nil
	}

	swapper, want, err := plan(conf)
	if err != nil {
		return nil, err
	}

	processes := make(map[string]process.Process, len(want))
	base, err := newBaseRouter("matrix", conf, processes, proxylog, swapper)
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

	r := &Matrix{baseRouter: base}
	go base.run()
	return r, nil
}

// matrixSwapper decides evictions by asking the matrix solver against the
// running set the scheduler hands it.
type matrixSwapper struct {
	solver *matrixSolver
	logger *logmon.Monitor
}

func (p *matrixSwapper) EvictionFor(target string, running []string) []string {
	return p.solver.Solve(target, running).Evict
}

func (p *matrixSwapper) OnSwapStart(target string, running []string) {
	result := p.solver.Solve(target, running)
	switch {
	case len(result.Evict) > 0:
		// "Who is being kicked out for whom" is logged once for every router by
		// baseRouter.doSwap; what is solver-specific — the set membership, the
		// raw DSL and the cost it minimised — belongs at Debug.
		p.logger.Debugf("matrix: model=%s set=%s dsl=%q evict=%v target=%v cost=%d",
			target, result.SetName, result.DSL, result.Evict, result.TargetSet, result.TotalCost)
	case len(running) == 0:
		p.logger.Debugf("matrix: model=%s starting (no models running)", target)
	default:
		p.logger.Debugf("matrix: model=%s already running in set=%s dsl=%q", target, result.SetName, result.DSL)
	}
}
