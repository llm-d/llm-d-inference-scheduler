package programaware

import (
	"fmt"
	"time"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol"
)

// ScoringStrategy determines how program queues are prioritized for dispatch.
// All methods must be safe for concurrent use; Pick(), PreRequest(), and
// ResponseComplete() may execute on different goroutines.
//
// Scoring uses per-cycle relative normalization: each dimension is normalized
// against the observed min/max across all queues in the current Pick() cycle.
// This eliminates fixed caps and adapts automatically to any workload pattern.
type ScoringStrategy interface {
	Name() string

	// OnPickStart is called once per queue per Pick() cycle, before scoring.
	OnPickStart(programID string, queueLen int, metrics *ProgramMetrics)

	// NumDimensions returns the number of raw metric dimensions this strategy uses.
	NumDimensions() int

	// CollectRaw extracts unnormalized metric values for a queue.
	// Returns a slice of length NumDimensions().
	CollectRaw(queue flowcontrol.FlowQueueAccessor, metrics *ProgramMetrics) []float64

	// NormalizeDimension normalizes a single raw value given the observed min/max
	// for that dimension across all queues in this Pick() cycle.
	// Returns 0.5 when min == max (no discriminative signal).
	NormalizeDimension(dim int, raw, min, max float64) float64

	// Score computes the final weighted score from normalized [0,1] values.
	Score(normalized []float64) float64

	// OnCompleted is called when a response finishes with actual token usage.
	OnCompleted(metrics *ProgramMetrics, promptTokens, completionTokens int64)
}

// newStrategy constructs a ScoringStrategy from the plugin config.
func newStrategy(cfg Config) (ScoringStrategy, error) {
	switch cfg.Strategy {
	case "", "ewma":
		return &EWMAStrategy{
			weightHeadWait:        floatOr(cfg.WeightHeadWait, defaultEWMAWeightHeadWait),
			weightAvgWait:         floatOr(cfg.WeightAvgWait, defaultEWMAWeightAvgWait),
			weightTotalDispatched: floatOr(cfg.WeightTotalDispatched, defaultEWMAWeightTotalDispatched),
		}, nil
	case "drr":
		return &DRRStrategy{
			weightDeficit:  floatOr(cfg.WeightDeficit, defaultDRRWeightDeficit),
			weightHeadWait: floatOr(cfg.WeightDRRHeadWait, defaultDRRWeightHeadWait),
			quantumTokens:  int64Or(cfg.QuantumTokens, defaultDRRQuantumTokens),
		}, nil
	default:
		return nil, fmt.Errorf("unknown scoring strategy %q: valid values are \"ewma\", \"drr\"", cfg.Strategy)
	}
}

// floatOr returns *p if non-nil, otherwise the default.
func floatOr(p *float64, def float64) float64 {
	if p != nil {
		return *p
	}
	return def
}

// int64Or returns *p if non-nil, otherwise the default.
func int64Or(p *int64, def int64) int64 {
	if p != nil {
		return *p
	}
	return def
}

// rangeNormalize performs min-max normalization: (v - min) / (max - min) → [0, 1].
// Returns 0.5 when min == max (no discriminative signal for this dimension).
func rangeNormalize(v, min, max float64) float64 {
	if max == min {
		return 0.5
	}
	return (v - min) / (max - min)
}

// =============================================================================
// EWMA Strategy (default)
// =============================================================================

// EWMA dimension indices.
const (
	ewmaDimHeadWait        = 0
	ewmaDimAvgWait         = 1
	ewmaDimTotalDispatched = 2
	ewmaNumDimensions      = 3
)

// Default EWMA strategy weights.
const (
	defaultEWMAWeightHeadWait        = 0.5
	defaultEWMAWeightAvgWait         = 0.3
	defaultEWMAWeightTotalDispatched = 0.2
)

// EWMAStrategy scores queues using three normalized signals:
//   - headWait: age of the oldest request — rate-neutral starvation guard.
//   - avgWait: EWMA of historical wait times — accumulated fairness debt.
//   - dispatched (penalty): anti-monopoly signal based on request count.
//
// Weights are configurable via the plugin config; defaults are 0.5/0.3/0.2.
type EWMAStrategy struct {
	weightHeadWait        float64
	weightAvgWait         float64
	weightTotalDispatched float64
}

// Name returns "ewma".
func (s *EWMAStrategy) Name() string { return "ewma" }

// OnPickStart is a no-op for EWMA; state is derived from request timestamps, not round counters.
func (s *EWMAStrategy) OnPickStart(_ string, _ int, _ *ProgramMetrics) {}

// NumDimensions returns 3 (headWait, avgWait, totalDispatched).
func (s *EWMAStrategy) NumDimensions() int { return ewmaNumDimensions }

// CollectRaw extracts unnormalized [headWaitMs, avgWaitMs, totalDispatched].
func (s *EWMAStrategy) CollectRaw(queue flowcontrol.FlowQueueAccessor, metrics *ProgramMetrics) []float64 {
	raw := make([]float64, ewmaNumDimensions)

	if metrics != nil {
		raw[ewmaDimAvgWait] = metrics.AverageWaitTime()
		raw[ewmaDimTotalDispatched] = float64(metrics.DispatchedCount())
	}

	if head := queue.PeekHead(); head != nil {
		raw[ewmaDimHeadWait] = float64(time.Since(head.EnqueueTime()).Milliseconds())
	}

	return raw
}

// NormalizeDimension performs range normalization for all EWMA dimensions.
func (s *EWMAStrategy) NormalizeDimension(_ int, raw, min, max float64) float64 {
	return rangeNormalize(raw, min, max)
}

// Score computes the final score using the stronger of the two starvation signals
// (headWait, avgWait) minus a dispatch-count penalty.
//
// headWait and avgWait are correlated: a starved flow has both high head-of-queue
// age AND high EWMA wait. Using max() instead of addition prevents double-counting
// and ensures new flows (avgWait=0) score the same as existing flows with identical
// queue age, eliminating the cold-start disadvantage.
func (s *EWMAStrategy) Score(normalized []float64) float64 {
	return max(
		s.weightHeadWait*normalized[ewmaDimHeadWait],
		s.weightAvgWait*normalized[ewmaDimAvgWait],
	) - s.weightTotalDispatched*normalized[ewmaDimTotalDispatched]
}

// OnCompleted is a no-op for EWMA; token usage is not tracked in this strategy.
func (s *EWMAStrategy) OnCompleted(_ *ProgramMetrics, _, _ int64) {}

// =============================================================================
// DRR Strategy
// =============================================================================

// DRR dimension indices.
const (
	drrDimDeficit    = 0
	drrDimHeadWait   = 1
	drrNumDimensions = 2
)

// Default DRR strategy values.
const (
	defaultDRRQuantumTokens  int64   = 1000
	defaultDRRWeightDeficit  float64 = 0.7
	defaultDRRWeightHeadWait float64 = 0.3
)

// DRRStrategy implements Deficit Round Robin adapted for token-based LLM scheduling.
//
// Classic DRR (https://dl.acm.org/doi/pdf/10.1145/217391.217453) assigns each active flow a fixed
// byte quantum per round, serves the highest-deficit flow first, and deducts actual bytes
// served from the deficit counter. This guarantees proportional bandwidth allocation
// — in contrast to EWMA which counts requests, not compute.
//
// Mapping for program-aware scheduler:
//   - "bytes"   = prompt + completion tokens (actual cost known at response completion)
//   - "quantum" = quantumTokens added per Pick() cycle per non-empty queue
//   - Actual token cost is deducted in OnCompleted() (ResponseComplete hook)
//   - Idle queues have their deficit reset to 0: standard DRR behavior prevents programs
//     from accumulating unbounded credit while inactive
//
// headWaitMs is used as a secondary signal to prevent starvation of
// new or returning programs that start with deficit=0.
//
// Weights and quantum are configurable via the plugin config; defaults are 0.7/0.3/1000.
type DRRStrategy struct {
	weightDeficit  float64
	weightHeadWait float64
	quantumTokens  int64
}

// Name returns "drr".
func (s *DRRStrategy) Name() string { return "drr" }

// OnPickStart allocates a token quantum for active queues and resets deficit for idle queues.
func (s *DRRStrategy) OnPickStart(_ string, queueLen int, metrics *ProgramMetrics) {
	if metrics == nil {
		return
	}
	if queueLen == 0 {
		// Standard DRR: reset deficit when the queue drains.
		// Prevents programs from stockpiling credit during idle periods and
		// bursting at the expense of other programs when they resume.
		metrics.ResetDeficit()
	} else {
		// Allocate this round's token quantum.
		metrics.AddDeficit(s.quantumTokens)
	}
}

// NumDimensions returns 2 (deficit, headWait).
func (s *DRRStrategy) NumDimensions() int { return drrNumDimensions }

// CollectRaw extracts unnormalized [deficit, headWaitMs].
func (s *DRRStrategy) CollectRaw(queue flowcontrol.FlowQueueAccessor, metrics *ProgramMetrics) []float64 {
	raw := make([]float64, drrNumDimensions)

	if metrics != nil {
		raw[drrDimDeficit] = float64(metrics.Deficit())
	}

	if head := queue.PeekHead(); head != nil {
		raw[drrDimHeadWait] = float64(time.Since(head.EnqueueTime()).Milliseconds())
	}

	return raw
}

// NormalizeDimension performs range normalization for all DRR dimensions.
// For deficit, this naturally maps negative deficit (overserved) to low scores
// and positive deficit (owed service) to high scores.
func (s *DRRStrategy) NormalizeDimension(_ int, raw, min, max float64) float64 {
	return rangeNormalize(raw, min, max)
}

// Score computes the weighted combination of deficit and head wait.
func (s *DRRStrategy) Score(normalized []float64) float64 {
	return s.weightDeficit*normalized[drrDimDeficit] +
		s.weightHeadWait*normalized[drrDimHeadWait]
}

// OnCompleted deducts actual token usage from the deficit counter.
func (s *DRRStrategy) OnCompleted(metrics *ProgramMetrics, promptTokens, completionTokens int64) {
	if metrics == nil {
		return
	}
	// Deduct actual token cost from the deficit counter.
	// Programs that consumed more than their quantum will have a negative deficit
	// and be deprioritized in future rounds until quanta restore parity.
	metrics.DeductTokens(promptTokens + completionTokens)
}
