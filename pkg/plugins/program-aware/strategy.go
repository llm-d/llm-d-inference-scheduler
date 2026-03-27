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
	case "drr":
		return &DRRStrategy{
			weightDeficit:  floatOr(cfg.WeightDeficit, defaultDRRWeightDeficit),
			weightHeadWait: floatOr(cfg.WeightDRRHeadWait, defaultDRRWeightHeadWait),
			quantumTokens:  int64Or(cfg.QuantumTokens, defaultDRRQuantumTokens),
		}, nil
	case "", "service":
		return &ServiceStrategy{
			weightService:   floatOr(cfg.WeightService, defaultServiceWeightService),
			weightHeadWait:  floatOr(cfg.WeightServiceHeadWait, defaultServiceWeightHeadWait),
			decayFactor:     floatOr(cfg.ServiceDecayFactor, defaultServiceDecayFactor),
			halfLifeSeconds: floatOr(cfg.ServiceHalfLifeSeconds, 0),
		}, nil
	default:
		return nil, fmt.Errorf("unknown scoring strategy %q: valid values are \"drr\", \"service\"", cfg.Strategy)
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
	defaultDRRWeightDeficit  float64 = 0.8
	defaultDRRWeightHeadWait float64 = 0.2
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
	metrics.DeductTokens(weightInputToken*promptTokens + weightOutputToken*completionTokens)
}

// =============================================================================
// Service Strategy
// =============================================================================

// Service dimension indices.
const (
	serviceDimService  = 0
	serviceDimHeadWait = 1
	serviceNumDims     = 2
)

// Default Service strategy values.
const (
	defaultServiceWeightService  float64 = 0.8
	defaultServiceWeightHeadWait float64 = 0.2
	defaultServiceDecayFactor    float64 = 0.995
)

// ServiceStrategy scores queues by equalizing attained service (weighted tokens
// consumed) across programs. Programs with lower attained service receive higher
// scores, directly targeting fair resource allocation.
//
//   - attainedService (inverted): time-decayed accumulator of weighted tokens
//     consumed — lower service → higher score (underserved programs promoted).
//   - headWait: age of the oldest request — tiebreaker for cold start when
//     all programs have zero attained service.
//
// On each Pick() cycle, every queue's attained service is decayed by decayFactor,
// causing old service to be gradually forgotten. On each completion, the actual
// weighted token cost is added to the program's attained service.
//
// Weights and decay factor are configurable via the plugin config.
type ServiceStrategy struct {
	weightService   float64
	weightHeadWait  float64
	decayFactor     float64
	halfLifeSeconds float64 // if > 0, use time-based decay instead of per-cycle decayFactor
}

// Name returns "service".
func (s *ServiceStrategy) Name() string { return "service" }

// OnPickStart decays the attained service counter for active queues,
// causing old service to be gradually forgotten.
// Uses time-based decay if halfLifeSeconds is configured, otherwise per-cycle decayFactor.
func (s *ServiceStrategy) OnPickStart(_ string, _ int, metrics *ProgramMetrics) {
	if metrics == nil {
		return
	}
	if s.halfLifeSeconds > 0 {
		metrics.DecayServiceTimed(s.halfLifeSeconds, time.Now())
	} else {
		metrics.DecayService(s.decayFactor)
	}
}

// NumDimensions returns 2 (attainedService, headWait).
func (s *ServiceStrategy) NumDimensions() int { return serviceNumDims }

// CollectRaw extracts unnormalized [attainedService, headWaitMs].
func (s *ServiceStrategy) CollectRaw(queue flowcontrol.FlowQueueAccessor, metrics *ProgramMetrics) []float64 {
	raw := make([]float64, serviceNumDims)

	if metrics != nil {
		raw[serviceDimService] = metrics.AttainedService()
	}

	if head := queue.PeekHead(); head != nil {
		raw[serviceDimHeadWait] = float64(time.Since(head.EnqueueTime()).Milliseconds())
	}

	return raw
}

// NormalizeDimension performs range normalization, inverting the service
// dimension so that lower attained service maps to a higher normalized score.
func (s *ServiceStrategy) NormalizeDimension(dim int, raw, min, max float64) float64 {
	if dim == serviceDimService {
		return 1 - rangeNormalize(raw, min, max)
	}
	return rangeNormalize(raw, min, max)
}

// Score computes the weighted combination of (inverted) attained service and head wait.
func (s *ServiceStrategy) Score(normalized []float64) float64 {
	return s.weightService*normalized[serviceDimService] +
		s.weightHeadWait*normalized[serviceDimHeadWait]
}

// OnCompleted accumulates the weighted token cost into the program's attained service.
func (s *ServiceStrategy) OnCompleted(metrics *ProgramMetrics, promptTokens, completionTokens int64) {
	if metrics == nil {
		return
	}
	cost := float64(weightInputToken*promptTokens + weightOutputToken*completionTokens)
	metrics.AddService(cost)
}
