package programaware

import (
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol"
	requestcontrol "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	scheduling "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

// ScoringStrategy determines how program queues are prioritized for dispatch.
// All methods must be safe for concurrent use; Pick() and OnCompleted() may
// execute on different goroutines.
type ScoringStrategy interface {
	Name() string

	// Pick receives all queues in the band keyed by program ID (including
	// empty ones for bookkeeping) and returns the selected queue plus
	// per-queue scores for observability. Returns (nil, nil) if no queue
	// is eligible.
	Pick(queues map[string]QueueInfo) (selected flowcontrol.FlowQueueAccessor, scores map[string]float64)

	// OnPreRequest is called before each request dispatch to reset per-cycle state.
	OnPreRequest(metrics *ProgramMetrics, request *scheduling.LLMRequest)

	// OnCompleted is called when a response finishes with actual token usage.
	OnCompleted(metrics *ProgramMetrics, request *scheduling.LLMRequest, response *requestcontrol.Response)
}

// QueueInfo bundles read-only data for each queue passed to Pick.
type QueueInfo struct {
	Queue   flowcontrol.FlowQueueAccessor
	Metrics *ProgramMetrics
	Len     int
}

// newStrategy constructs a ScoringStrategy from the plugin config.
func newStrategy(cfg Config) (ScoringStrategy, error) {
	switch cfg.Strategy {
	case "drr":
		s := &DRRStrategy{
			weightDeficit:  floatOr(cfg.WeightDeficit, defaultDRRWeightDeficit),
			weightHeadWait: floatOr(cfg.WeightDRRHeadWait, defaultDRRWeightHeadWait),
			quantumTokens:  int64Or(cfg.QuantumTokens, defaultDRRQuantumTokens),
		}
		s.addQuantum.Store(true)
		return s, nil
	case "", "las":
		return &LASStrategy{
			weightService:   floatOr(cfg.WeightService, defaultServiceWeightService),
			weightHeadWait:  floatOr(cfg.WeightServiceHeadWait, defaultServiceWeightHeadWait),
			decayFactor:     floatOr(cfg.ServiceDecayFactor, defaultServiceDecayFactor),
			halfLifeSeconds: floatOr(cfg.ServiceHalfLifeSeconds, 0),
		}, nil
	case "rr":
		return &RRStrategy{
			deferCursor: boolOr(cfg.DeferRRCursor, false),
		}, nil
	default:
		return nil, fmt.Errorf("unknown scoring strategy %q: valid values are \"drr\", \"las\", \"rr\"", cfg.Strategy)
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

// boolOr returns *p if non-nil, otherwise the default.
func boolOr(p *bool, def bool) bool {
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
// Weights and quantum are configurable via the plugin config; defaults are 0.8/0.2/1000.
type DRRStrategy struct {
	weightDeficit  float64
	weightHeadWait float64
	quantumTokens  int64
	addQuantum     atomic.Bool
	seen           sync.Map
}

// Name returns "drr".
func (s *DRRStrategy) Name() string { return "drr" }

// Pick selects the queue with the highest deficit-weighted score.
//
// Quantum allocation is cycle-aware: the first Pick() in a dispatch cycle
// allocates quantum to all non-empty queues; subsequent Pick() calls in the
// same cycle only allocate to programs not yet seen. addQuantum is cleared
// at the end of Pick(); OnPrerequest() resets it for the next cycle.
func (s *DRRStrategy) Pick(queues map[string]QueueInfo) (flowcontrol.FlowQueueAccessor, map[string]float64) {
	type entry struct {
		deficit    float64
		headWaitMs float64
	}

	// Pass 1: bookkeeping + collect raw values for non-empty queues.
	entries := make(map[string]entry)
	minDeficit, maxDeficit := 0.0, 0.0
	minWait, maxWait := 0.0, 0.0
	first := true

	for id, qi := range queues {
		if qi.Metrics == nil {
			continue
		}
		// Allocate quantum to all queues (including empty) so idle programs
		// accumulate deficit credit and aren't penalised when they return.
		if s.addQuantum.Load() {
			qi.Metrics.AddDeficit(s.quantumTokens)
			s.seen.Store(id, true)
		} else {
			if _, ok := s.seen.Load(id); !ok {
				s.seen.Store(id, true)
				qi.Metrics.AddDeficit(s.quantumTokens)
			}
		}

		// Only non-empty queues participate in scoring.
		if qi.Len == 0 {
			continue
		}

		deficit := float64(qi.Metrics.Deficit())
		var headWaitMs float64
		if head := qi.Queue.PeekHead(); head != nil {
			headWaitMs = float64(time.Since(head.EnqueueTime()).Milliseconds())
		}

		entries[id] = entry{deficit: deficit, headWaitMs: headWaitMs}

		if first {
			minDeficit, maxDeficit = deficit, deficit
			minWait, maxWait = headWaitMs, headWaitMs
			first = false
		} else {
			if deficit < minDeficit {
				minDeficit = deficit
			}
			if deficit > maxDeficit {
				maxDeficit = deficit
			}
			if headWaitMs < minWait {
				minWait = headWaitMs
			}
			if headWaitMs > maxWait {
				maxWait = headWaitMs
			}
		}
	}

	if len(entries) == 0 {
		return nil, nil
	}

	// Pass 2: normalize, score, select.
	scores := make(map[string]float64, len(entries))
	var best flowcontrol.FlowQueueAccessor
	bestScore := math.Inf(-1)

	for id, e := range entries {
		normDeficit := rangeNormalize(e.deficit, minDeficit, maxDeficit)
		normWait := rangeNormalize(e.headWaitMs, minWait, maxWait)
		score := s.weightDeficit*normDeficit + s.weightHeadWait*normWait
		scores[id] = score
		if score > bestScore {
			bestScore = score
			best = queues[id].Queue
		}
	}

	s.addQuantum.Store(false)
	return best, scores
}

// OnPreRequest resets the quantum flag so the next Pick() allocates quantum.
func (s *DRRStrategy) OnPreRequest(_ *ProgramMetrics, _ *scheduling.LLMRequest) {
	s.addQuantum.Store(true)
}

// OnCompleted deducts actual token usage from the deficit counter.
func (s *DRRStrategy) OnCompleted(metrics *ProgramMetrics, _ *scheduling.LLMRequest, response *requestcontrol.Response) {
	if metrics == nil || response == nil {
		return
	}
	promptTokens := int64(response.Usage.PromptTokens)
	completionTokens := int64(response.Usage.CompletionTokens)
	metrics.DeductTokens(weightInputToken*promptTokens + weightOutputToken*completionTokens)
}


// =============================================================================
// LAS (Least Attained Service) Strategy
// =============================================================================

// Default LAS strategy values.
const (
	defaultServiceWeightService  float64 = 0.8
	defaultServiceWeightHeadWait float64 = 0.2
	defaultServiceDecayFactor    float64 = 0.995
)

// LASStrategy scores queues by equalizing attained service (weighted tokens
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
type LASStrategy struct {
	weightService   float64
	weightHeadWait  float64
	decayFactor     float64
	halfLifeSeconds float64 // if > 0, use time-based decay instead of per-cycle decayFactor
}

// Name returns "service".
func (s *LASStrategy) Name() string { return "las" }

// Pick selects the queue with the lowest attained service (highest need).
//
// First decays every queue's attained service, then uses two-pass adaptive
// normalization across non-empty queues. The service dimension is inverted
// so that lower attained service maps to a higher score.
func (s *LASStrategy) Pick(queues map[string]QueueInfo) (flowcontrol.FlowQueueAccessor, map[string]float64) {
	type entry struct {
		service    float64
		headWaitMs float64
	}

	// Pass 1: decay service for all queues, collect raw values for non-empty.
	entries := make(map[string]entry)
	minService, maxService := 0.0, 0.0
	minWait, maxWait := 0.0, 0.0
	first := true
	now := time.Now()

	for id, qi := range queues {
		if qi.Metrics == nil {
			continue
		}
		// Decay runs for every queue, including empty ones.
		if s.halfLifeSeconds > 0 {
			qi.Metrics.DecayServiceTimed(s.halfLifeSeconds, now)
		} else {
			qi.Metrics.DecayService(s.decayFactor)
		}

		if qi.Len == 0 {
			continue
		}

		service := qi.Metrics.AttainedService()
		var headWaitMs float64
		if head := qi.Queue.PeekHead(); head != nil {
			headWaitMs = float64(time.Since(head.EnqueueTime()).Milliseconds())
		}

		entries[id] = entry{service: service, headWaitMs: headWaitMs}

		if first {
			minService, maxService = service, service
			minWait, maxWait = headWaitMs, headWaitMs
			first = false
		} else {
			if service < minService {
				minService = service
			}
			if service > maxService {
				maxService = service
			}
			if headWaitMs < minWait {
				minWait = headWaitMs
			}
			if headWaitMs > maxWait {
				maxWait = headWaitMs
			}
		}
	}

	if len(entries) == 0 {
		return nil, nil
	}

	// Pass 2: normalize (invert service), score, select.
	scores := make(map[string]float64, len(entries))
	var best flowcontrol.FlowQueueAccessor
	bestScore := math.Inf(-1)

	for id, e := range entries {
		// Invert service: lower attained service → higher normalized score.
		normService := 1 - rangeNormalize(e.service, minService, maxService)
		normWait := rangeNormalize(e.headWaitMs, minWait, maxWait)
		score := s.weightService*normService + s.weightHeadWait*normWait
		scores[id] = score
		if score > bestScore {
			bestScore = score
			best = queues[id].Queue
		}
	}

	return best, scores
}

// OnPreRequest is a no-op for LAS.
func (s *LASStrategy) OnPreRequest(_ *ProgramMetrics, _ *scheduling.LLMRequest) {}

// OnCompleted accumulates the weighted token cost into the program's attained service.
func (s *LASStrategy) OnCompleted(metrics *ProgramMetrics, _ *scheduling.LLMRequest, response *requestcontrol.Response) {
	if metrics == nil || response == nil {
		return
	}
	promptTokens := int64(response.Usage.PromptTokens)
	completionTokens := int64(response.Usage.CompletionTokens)
	cost := float64(weightInputToken*promptTokens + weightOutputToken*completionTokens)
	metrics.AddService(cost)
}

// =============================================================================
// RR (Round-Robin) Strategy
// =============================================================================

// RRStrategy implements a simple round-robin scheduling strategy that matches
// the upstream gateway-api-inference-extension round-robin fairness policy.
//
// It maintains a cursor (lastSelected) that tracks which program was last
// dispatched. On each Pick() cycle, programs are sorted deterministically
// and the one immediately after the cursor gets the highest score.
// Empty queues are naturally skipped because only non-empty queues are scored.
type RRStrategy struct {
	mu            sync.Mutex
	lastSelected  string // program ID last picked (committed cursor)
	pendingCursor string // set by Pick when deferCursor=true, committed by OnPreRequest
	deferCursor   bool   // when true, cursor advances in OnPreRequest instead of Pick
}

// Name returns "rr".
func (s *RRStrategy) Name() string { return "rr" }

// Pick selects the next non-empty queue in deterministic round-robin order.
// Walks forward from the cursor and returns the first non-empty queue found.
func (s *RRStrategy) Pick(queues map[string]QueueInfo) (flowcontrol.FlowQueueAccessor, map[string]float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Sort all program IDs for deterministic ordering.
	allKeys := make([]string, 0, len(queues))
	for id := range queues {
		allKeys = append(allKeys, id)
	}
	slices.Sort(allKeys)

	n := len(allKeys)
	if n == 0 {
		return nil, nil
	}

	// Find the start index (next after lastSelected).
	start := 0
	if s.lastSelected != "" {
		if idx := slices.Index(allKeys, s.lastSelected); idx != -1 {
			start = (idx + 1) % n
		}
	}

	// Walk forward from start, pick the first non-empty queue.
	for i := range n {
		id := allKeys[(start+i)%n]
		if queues[id].Len > 0 {
			if s.deferCursor {
				s.pendingCursor = id
			} else {
				s.lastSelected = id
			}
			return queues[id].Queue, nil
		}
	}

	s.lastSelected = ""
	s.pendingCursor = ""
	return nil, nil
}

// OnPreRequest commits the deferred cursor when deferCursor is enabled.
func (s *RRStrategy) OnPreRequest(_ *ProgramMetrics, _ *scheduling.LLMRequest) {
	if !s.deferCursor {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingCursor != "" {
		s.lastSelected = s.pendingCursor
		s.pendingCursor = ""
	}
}

// OnCompleted is a no-op for round-robin (no token tracking needed).
func (s *RRStrategy) OnCompleted(_ *ProgramMetrics, _ *scheduling.LLMRequest, _ *requestcontrol.Response) {}
