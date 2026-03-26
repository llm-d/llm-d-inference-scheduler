// Package programaware implements a flow-control fairness policy that schedules
// programs using their accumulated metrics using scoring strategies (EWMA or DRR).
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

	// defaultFairnessID is the flow key assigned by the upstream framework when
	// no x-gateway-inference-fairness-id header is present on the request.
	// Matches the constant in sigs.k8s.io/gateway-api-inference-extension/pkg/epp/handlers/request.go.
	defaultFairnessID = "default-flow"
)

// Config holds the JSON-decoded configuration for the plugin.
type Config struct {
	// Strategy selects the fairness scoring algorithm used by Pick().
	// Valid values: "ewma" (default), "drr", "throughput".
	//
	//   "ewma" — head-of-queue age + EWMA historical wait + dispatch-count penalty.
	//            Practical heuristic; strong starvation prevention.
	//
	//   "drr"  — Deficit Round Robin adapted for tokens [Shreedhar & Varghese 1995].
	//            Each round every active queue earns a token quantum; actual token
	//            usage is deducted at response completion. Provides provably
	//            proportional fairness independent of request rate or size.
	Strategy string `json:"strategy"`

	// --- EWMA weights (only used when strategy == "ewma") ---

	// WeightHeadWait is the weight for head-of-queue age (starvation guard).
	// Default: 0.5.
	WeightHeadWait *float64 `json:"weightHeadWait,omitempty"`

	// WeightAvgWait is the weight for EWMA historical wait time (fairness debt).
	// Default: 0.3.
	WeightAvgWait *float64 `json:"weightAvgWait,omitempty"`

	// WeightAvgTokens is the penalty weight for EWMA per-request token usage.
	// Programs with heavier recent requests are penalized more. Default: 0.2.
	WeightAvgTokens *float64 `json:"weightAvgTokens,omitempty"`

	// --- DRR weights (only used when strategy == "drr") ---

	// WeightDeficit is the weight for the deficit counter signal.
	// Default: 0.7.
	WeightDeficit *float64 `json:"weightDeficit,omitempty"`

	// WeightDRRHeadWait is the weight for head-of-queue age in DRR.
	// Default: 0.3.
	WeightDRRHeadWait *float64 `json:"weightDrrHeadWait,omitempty"`

	// QuantumTokens is the token budget added to each non-empty queue per Pick() cycle.
	// Default: 1000.
	QuantumTokens *int64 `json:"quantumTokens,omitempty"`

	// --- Throughput weights (only used when strategy == "throughput") ---

	// WeightThroughput is the weight for the inverted average throughput signal.
	// Programs with lower throughput score higher. Default: 0.8.
	WeightThroughput *float64 `json:"weightThroughput,omitempty"`

	// WeightThroughputHeadWait is the weight for head-of-queue age in throughput strategy.
	// Acts as a tiebreaker for cold start. Default: 0.2.
	WeightThroughputHeadWait *float64 `json:"weightThroughputHeadWait,omitempty"`
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
//
//nolint:revive
func ProgramAwarePluginFactory(name string, rawCfg json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	cfg := Config{Strategy: "ewma"}
	if len(rawCfg) > 0 {
		if err := json.Unmarshal(rawCfg, &cfg); err != nil {
			return nil, fmt.Errorf("invalid config for %s plugin %q: %w", ProgramAwarePluginType, name, err)
		}
	}
	strategy, err := newStrategy(cfg)
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
//
//nolint:revive
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
		return &EWMAStrategy{
			weightHeadWait:  defaultEWMAWeightHeadWait,
			weightAvgWait:   defaultEWMAWeightAvgWait,
			weightAvgTokens: defaultEWMAWeightAvgTokens,
		}
	}
	return p.strategy
}

// --- FairnessPolicy interface ---

// NewState creates per-PriorityBand state. This plugin uses its own sync.Map
// for all state, so no per-band state is needed.
func (p *ProgramAwarePlugin) NewState(_ context.Context) any {
	return nil
}

// queueEntry holds collected data for a non-empty queue during the two-pass Pick.
type queueEntry struct {
	queue flowcontrol.FlowQueueAccessor
	raw   []float64
}

// Pick selects which program queue to service next.
//
// Uses a two-pass approach for adaptive normalization:
//  1. Pass 1: OnPickStart for all queues + CollectRaw for non-empty ones, tracking
//     per-dimension min/max across all queues.
//  2. Pass 2: Normalize using observed ranges, score, and select the best queue.
//
// This eliminates fixed normalization caps and adapts to any workload pattern.
func (p *ProgramAwarePlugin) Pick(_ context.Context, band flowcontrol.PriorityBandAccessor) (flowcontrol.FlowQueueAccessor, error) {
	start := time.Now()
	defer func() {
		pickLatencyUs.Observe(float64(time.Since(start).Microseconds()))
	}()

	if band == nil {
		return nil, nil //nolint:nilnil
	}

	strategy := p.getStrategy()
	numDims := strategy.NumDimensions()

	// --- Pass 1: OnPickStart + CollectRaw, track per-dimension min/max ---
	var entries []queueEntry
	dimMin := make([]float64, numDims)
	dimMax := make([]float64, numDims)
	first := true

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

		raw := strategy.CollectRaw(queue, metrics)
		entries = append(entries, queueEntry{queue: queue, raw: raw})

		if first {
			copy(dimMin, raw)
			copy(dimMax, raw)
			first = false
		} else {
			for d := range numDims {
				if raw[d] < dimMin[d] {
					dimMin[d] = raw[d]
				}
				if raw[d] > dimMax[d] {
					dimMax[d] = raw[d]
				}
			}
		}
		return true
	})

	// --- Pass 2: Normalize + Score, select best ---
	var bestQueue flowcontrol.FlowQueueAccessor
	bestScore := math.Inf(-1)

	normalized := make([]float64, numDims)
	for _, e := range entries {
		for d := range numDims {
			normalized[d] = strategy.NormalizeDimension(d, e.raw[d], dimMin[d], dimMax[d])
		}
		score := strategy.Score(normalized)
		queueScore.WithLabelValues(e.queue.FlowKey().ID).Set(score)
		if score > bestScore {
			bestScore = score
			bestQueue = e.queue
		}
	}

	// Record the selected item's enqueue time so PreRequest can compute
	// the actual flow-control queue wait time (enqueue → dispatch).
	if bestQueue != nil {
		if head := bestQueue.PeekHead(); head != nil {
			p.requestTimestamps.Store(head.OriginalRequest().ID(), head.EnqueueTime())
		}
	}

	fairnessIndex.Set(p.computeFairnessIndex())

	return bestQueue, nil
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

// computeFairnessIndex returns Jain's Fairness Index over the average per-request
// throughput (tokens/sec) for each program. Throughput directly measures what each
// program is getting from the system, making it a better fairness signal than wait time.
// Returns 1.0 when fewer than 2 programs have throughput data.
func (p *ProgramAwarePlugin) computeFairnessIndex() float64 {
	var sum, sumSq float64
	var n float64
	p.programMetrics.Range(func(_, value any) bool {
		m := value.(*ProgramMetrics)
		x := m.AverageThroughput()
		if x == 0 {
			return true
		}
		sum += x
		sumSq += x * x
		n++
		return true
	})
	if n <= 1 || sumSq == 0 {
		return 1.0
	}
	return (sum * sum) / (n * sumSq)
}
