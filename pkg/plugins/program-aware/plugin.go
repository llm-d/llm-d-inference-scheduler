package programaware

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	requestcontrol "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
)

const (
	// ProgramAwarePluginType is the registered type name for this plugin.
	ProgramAwarePluginType = "program-aware-fairness"

	// fairnessIDHeader is the standard header used to identify the program.
	fairnessIDHeader = "x-gateway-inference-fairness-id"
)

// Config holds the JSON-decoded configuration for the plugin.
type Config struct {
	// Strategy selects the fairness scoring algorithm used by Pick().
	// Valid values: "ewma" (default), "drr".
	//
	//   "ewma" — head-of-queue age + EWMA historical wait + dispatch-count penalty.
	//            Practical heuristic; strong starvation prevention.
	//
	//   "drr"  — Deficit Round Robin adapted for tokens [Shreedhar & Varghese 1995].
	//            Each round every active queue earns a token quantum; actual token
	//            usage is deducted at response completion. Provides provably
	//            proportional fairness independent of request rate or size.
	Strategy string `json:"strategy"`
}

// Compile-time interface assertions.
var (
	_ flowcontrol.FairnessPolicy       = &ProgramAwarePlugin{}
	_ requestcontrol.PrepareDataPlugin = &ProgramAwarePlugin{}
	_ requestcontrol.PreRequest        = &ProgramAwarePlugin{}
	_ requestcontrol.ResponseComplete  = &ProgramAwarePlugin{}
)

// ProgramAwarePluginFactory creates a new ProgramAwarePlugin from JSON config.
// Example config: {"strategy": "drr"}
func ProgramAwarePluginFactory(name string, rawCfg json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	cfg := Config{Strategy: "ewma"}
	if len(rawCfg) > 0 {
		if err := json.Unmarshal(rawCfg, &cfg); err != nil {
			return nil, fmt.Errorf("invalid config for %s plugin %q: %w", ProgramAwarePluginType, name, err)
		}
	}
	strategy, err := newStrategy(cfg.Strategy)
	if err != nil {
		return nil, fmt.Errorf("%s plugin %q: %w", ProgramAwarePluginType, name, err)
	}
	return &ProgramAwarePlugin{
		name:     name,
		strategy: strategy,
	}, nil
}

// ProgramAwarePlugin implements a FairnessPolicy that selects which program's
// queue to service next, and request lifecycle hooks that track per-program metrics.
//
// Fairness behaviour is determined by the configured ScoringStrategy (default: EWMA).
// Program identity comes from the x-gateway-inference-fairness-id request header.
type ProgramAwarePlugin struct {
	name     string
	strategy ScoringStrategy

	// programMetrics stores aggregated metrics per program.
	// Key: program ID (string), Value: *ProgramMetrics.
	programMetrics sync.Map

	// requestTimestamps tracks when Pick() dispatched each request,
	// used to compute flow-control queue wait time in PreRequest.
	// Key: request ID (string), Value: time.Time.
	requestTimestamps sync.Map
}

// TypedName returns the plugin type and instance name.
func (p *ProgramAwarePlugin) TypedName() plugin.TypedName {
	return plugin.TypedName{
		Type: ProgramAwarePluginType,
		Name: p.name,
	}
}

// getStrategy returns the configured strategy, falling back to EWMA for zero-value
// plugin instances constructed directly in tests.
func (p *ProgramAwarePlugin) getStrategy() ScoringStrategy {
	if p.strategy == nil {
		return &EWMAStrategy{}
	}
	return p.strategy
}

// --- FairnessPolicy interface ---

// NewState creates per-PriorityBand state. This plugin uses its own sync.Map
// for all state, so no per-band state is needed.
func (p *ProgramAwarePlugin) NewState(_ context.Context) any {
	return nil
}

// Pick selects which program queue to service next.
//
// For each queue in the band, the configured ScoringStrategy is given a chance
// to update its per-program state (OnPickStart), then the queue with the highest
// score is selected for dispatch.
func (p *ProgramAwarePlugin) Pick(_ context.Context, band flowcontrol.PriorityBandAccessor) (flowcontrol.FlowQueueAccessor, error) {
	start := time.Now()
	defer func() {
		pickLatencyUs.Observe(float64(time.Since(start).Microseconds()))
	}()

	if band == nil {
		return nil, nil
	}

	var bestQueue flowcontrol.FlowQueueAccessor
	bestScore := -1.0
	strategy := p.getStrategy()

	band.IterateQueues(func(queue flowcontrol.FlowQueueAccessor) (keepIterating bool) {
		if queue == nil {
			return true
		}

		queueLen := queue.Len()
		metrics := p.getOrCreateMetrics(queue.FlowKey().ID)

		// Strategy hook: runs for every queue, including empty ones.
		// DRR: allocates quantum for active queues, resets deficit for idle queues.
		// EWMA: no-op.
		strategy.OnPickStart(queue.FlowKey().ID, queueLen, metrics)

		if queueLen == 0 {
			return true
		}

		score := p.scoreQueue(queue)
		if score > bestScore {
			bestScore = score
			bestQueue = queue
		}
		return true
	})

	// Record the selected item's enqueue time so PreRequest can compute
	// the actual flow-control queue wait time (enqueue → dispatch).
	if bestQueue != nil {
		if head := bestQueue.PeekHead(); head != nil {
			p.requestTimestamps.Store(head.OriginalRequest().ID(), head.EnqueueTime())
		}
	}

	return bestQueue, nil
}

// scoreQueue delegates to the configured ScoringStrategy.
func (p *ProgramAwarePlugin) scoreQueue(queue flowcontrol.FlowQueueAccessor) float64 {
	var metrics *ProgramMetrics
	if metricsRaw, ok := p.programMetrics.Load(queue.FlowKey().ID); ok {
		metrics = metricsRaw.(*ProgramMetrics)
	}
	return p.getStrategy().ScoreQueue(queue, metrics)
}

// getOrCreateMetrics returns the ProgramMetrics for the given program ID, creating if needed.
func (p *ProgramAwarePlugin) getOrCreateMetrics(programID string) *ProgramMetrics {
	if metricsRaw, ok := p.programMetrics.Load(programID); ok {
		return metricsRaw.(*ProgramMetrics)
	}
	m := &ProgramMetrics{}
	actual, _ := p.programMetrics.LoadOrStore(programID, m)
	return actual.(*ProgramMetrics)
}

// normalize clamps v/cap to [0, 1].
func normalize(v, cap float64) float64 {
	if cap <= 0 {
		return 0
	}
	return math.Min(math.Max(v/cap, 0), 1)
}
