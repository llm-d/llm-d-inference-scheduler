package programaware

import (
	"fmt"
	"math"
	"time"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol"
)

// ScoringStrategy determines how program queues are prioritized for dispatch.
// All methods must be safe for concurrent use; Pick(), PreRequest(), and
// ResponseComplete() may execute on different goroutines.
type ScoringStrategy interface {
	// Name returns the human-readable identifier used in config and logs.
	Name() string

	// OnPickStart is called once per queue per Pick() cycle, before scoring.
	//   - queueLen == 0: the queue is idle/drained — reset per-flow state (e.g., DRR deficit).
	//   - queueLen  > 0: the queue is active   — allocate per-round budget (e.g., DRR quantum).
	// metrics is guaranteed non-nil.
	OnPickStart(programID string, queueLen int, metrics *ProgramMetrics)

	// ScoreQueue returns a priority score for the given queue.
	// Higher scores cause the queue to be dispatched sooner.
	// metrics may be nil if no requests have been seen for this program yet.
	ScoreQueue(queue flowcontrol.FlowQueueAccessor, metrics *ProgramMetrics) float64

	// OnCompleted is called when a response finishes with actual token usage.
	// Use this to deduct resource consumption from per-flow accounting (e.g., DRR deficit).
	// metrics is guaranteed non-nil.
	OnCompleted(metrics *ProgramMetrics, promptTokens, completionTokens int64)
}

// newStrategy constructs a ScoringStrategy by name.
// Valid names: "ewma" (default), "drr".
func newStrategy(name string) (ScoringStrategy, error) {
	switch name {
	case "", "ewma":
		return &EWMAStrategy{}, nil
	case "drr":
		return &DRRStrategy{}, nil
	default:
		return nil, fmt.Errorf("unknown scoring strategy %q: valid values are \"ewma\", \"drr\"", name)
	}
}

// =============================================================================
// EWMA Strategy (default)
// =============================================================================

// EWMA strategy constants.
const (
	ewmaWeightHeadWait        = 0.5
	ewmaWeightAvgWait         = 0.3
	ewmaWeightTotalDispatched = 0.2
	// Currently it is a constant, but would make it relative
	ewmaCapHeadWaitMs         = 5000.0
	ewmaCapAvgWaitMs          = 5000.0
	ewmaCapTotalDispatched    = 1000.0
)

// EWMAStrategy scores queues using three normalized signals:
//   - headWait (0.5): age of the oldest request — rate-neutral starvation guard.
//   - avgWait  (0.3): EWMA of historical wait times — accumulated fairness debt.
//   - dispatched (0.2, penalty): anti-monopoly signal based on request count.
//
// This is a practical heuristic based on EWMA of wait time
type EWMAStrategy struct{}

func (s *EWMAStrategy) Name() string { return "ewma" }

func (s *EWMAStrategy) OnPickStart(_ string, _ int, _ *ProgramMetrics) {}

func (s *EWMAStrategy) ScoreQueue(queue flowcontrol.FlowQueueAccessor, metrics *ProgramMetrics) float64 {
	avgWaitMs := 0.0
	totalDispatched := 0.0
	if metrics != nil {
		avgWaitMs = metrics.AverageWaitTime()
		totalDispatched = float64(metrics.DispatchedCount())
	}

	headWaitMs := 0.0
	if head := queue.PeekHead(); head != nil {
		headWaitMs = float64(time.Since(head.EnqueueTime()).Milliseconds())
	}

	return ewmaWeightHeadWait*normalize(headWaitMs, ewmaCapHeadWaitMs) +
		ewmaWeightAvgWait*normalize(avgWaitMs, ewmaCapAvgWaitMs) -
		ewmaWeightTotalDispatched*normalize(totalDispatched, ewmaCapTotalDispatched)
}

func (s *EWMAStrategy) OnCompleted(_ *ProgramMetrics, _, _ int64) {}

// =============================================================================
// DRR Strategy
// =============================================================================

// DRR strategy constants.
const (
	// drrQuantumTokens is the token budget added to each non-empty queue's deficit
	// per Pick() cycle. 1000 tokens ≈ one average LLM request (700 in / 300 out).
	// Increase for coarser but more stable fairness; decrease for finer granularity.
	drrQuantumTokens int64 = 1000

	// drrCapDeficit is the symmetric normalization range for the deficit counter.
	// Deficit is mapped from [-cap, +cap] → [0, 1]. 50k tokens ≈ 50 average requests.
	drrCapDeficit float64 = 50000.0

	drrWeightDeficit  float64 = 0.7
	drrWeightHeadWait float64 = 0.3
	drrCapHeadWaitMs  float64 = 5000.0
)

// DRRStrategy implements Deficit Round Robin adapted for token-based LLM scheduling.
//
// Classic DRR [Shreedhar & Varghese, IEEE/ACM ToN 1995] assigns each active flow a fixed
// byte quantum per round, serves the highest-deficit flow first, and deducts actual bytes
// served from the deficit counter. This guarantees proportional bandwidth allocation
// independent of packet sizes — in contrast to EWMA which counts requests, not compute.
//
// Adaptation for LLM tokens:
//   - "bytes"   → prompt + completion tokens (actual cost known at response completion)
//   - "quantum" → drrQuantumTokens added per Pick() cycle per non-empty queue
//   - Actual token cost is deducted in OnCompleted() (ResponseComplete hook)
//   - Idle queues have their deficit reset to 0: standard DRR behavior prevents programs
//     from accumulating unbounded credit while inactive
//
// headWaitMs is retained as a secondary signal (weight 0.3) to prevent starvation of
// new or returning programs that start with deficit=0.
type DRRStrategy struct{}

func (s *DRRStrategy) Name() string { return "drr" }

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
		metrics.AddDeficit(drrQuantumTokens)
	}
}

func (s *DRRStrategy) ScoreQueue(queue flowcontrol.FlowQueueAccessor, metrics *ProgramMetrics) float64 {
	deficit := 0.0
	if metrics != nil {
		deficit = float64(metrics.Deficit())
	}

	// Shift-normalize deficit from [-cap, +cap] → [0, 1].
	// Positive deficit (owed service) scores above 0.5.
	// Negative deficit (overserved) scores below 0.5.
	deficitNorm := math.Min(math.Max((deficit+drrCapDeficit)/(2*drrCapDeficit), 0), 1)

	headWaitMs := 0.0
	if head := queue.PeekHead(); head != nil {
		headWaitMs = float64(time.Since(head.EnqueueTime()).Milliseconds())
	}

	return drrWeightDeficit*deficitNorm +
		drrWeightHeadWait*normalize(headWaitMs, drrCapHeadWaitMs)
}

func (s *DRRStrategy) OnCompleted(metrics *ProgramMetrics, promptTokens, completionTokens int64) {
	if metrics == nil {
		return
	}
	// Deduct actual token cost from the deficit counter.
	// Programs that consumed more than their quantum will have a negative deficit
	// and be deprioritized in future rounds until quanta restore parity.
	metrics.DeductTokens(promptTokens + completionTokens)
}
