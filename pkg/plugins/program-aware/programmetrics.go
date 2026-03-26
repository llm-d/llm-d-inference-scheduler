package programaware

import (
	"sync"
	"sync/atomic"
)

const ewmaAlpha = 0.5

// Token cost weights: output tokens are ~2× more expensive than input tokens
const (
	weightInputToken  = 1
	weightOutputToken = 2
)

// ProgramMetrics holds aggregated metrics for a single program (identified by its fairness ID).
// All methods are goroutine-safe.
type ProgramMetrics struct {
	mu                sync.Mutex
	averageWaitTime   float64 // EWMA in milliseconds
	accumulatedWaitMs float64 // total accumulated wait time in milliseconds
	waitCount         int64   // number of wait time observations
	hasWaitData       bool

	averageTokens float64 // EWMA of per-request token usage (input+output)

	// Per-request throughput tracking: EWMA of tokens/sec per completed request.
	averageThroughput float64 // EWMA of per-request tokens/sec
	hasThroughputData bool

	totalRequests     atomic.Int64
	dispatchedCount   atomic.Int64
	totalInputTokens  atomic.Int64
	totalOutputTokens atomic.Int64

	// deficitTokens is the DRR deficit counter: positive means the program is owed
	// service; negative means it has been overserved relative to its quantum.
	// Only used by DRRStrategy; ignored by EWMAStrategy.
	deficitTokens atomic.Int64
}

// IncrementRequests atomically increments the total request counter.
func (m *ProgramMetrics) IncrementRequests() {
	m.totalRequests.Add(1)
}

// IncrementDispatched atomically increments the dispatched counter.
func (m *ProgramMetrics) IncrementDispatched() {
	m.dispatchedCount.Add(1)
}

// RecordWaitTime updates the EWMA of wait time with a new observation
// and accumulates the total wait time for lifetime average computation.
func (m *ProgramMetrics) RecordWaitTime(waitMs float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasWaitData {
		m.averageWaitTime = waitMs
		m.hasWaitData = true
	} else {
		m.averageWaitTime = ewmaAlpha*waitMs + (1-ewmaAlpha)*m.averageWaitTime
	}
	m.accumulatedWaitMs += waitMs
	m.waitCount++
}

// HasWaitData returns true if at least one wait time observation has been recorded.
func (m *ProgramMetrics) HasWaitData() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hasWaitData
}

// AverageWaitTime returns the current EWMA of wait time in milliseconds.
func (m *ProgramMetrics) AverageWaitTime() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.averageWaitTime
}

// TotalAverageWaitTime returns the lifetime average wait time in milliseconds
// (accumulated wait time / total observations). Returns 0 if no data.
func (m *ProgramMetrics) TotalAverageWaitTime() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.waitCount == 0 {
		return 0
	}
	return m.accumulatedWaitMs / float64(m.waitCount)
}

// RecordTokens adds token counts from a completed request and updates the
// EWMA of per-request token usage so recent heavy/light requests carry more weight.
func (m *ProgramMetrics) RecordTokens(input, output int64) {
	m.totalInputTokens.Add(input)
	m.totalOutputTokens.Add(output)

	cost := weightInputToken*float64(input) + weightOutputToken*float64(output)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.averageTokens == 0 {
		m.averageTokens = cost
	} else {
		m.averageTokens = ewmaAlpha*cost + (1-ewmaAlpha)*m.averageTokens
	}
}

// AverageTokens returns the EWMA of per-request token usage.
func (m *ProgramMetrics) AverageTokens() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.averageTokens
}

// RecordThroughput records a per-request throughput observation (tokens/sec) using EWMA.
func (m *ProgramMetrics) RecordThroughput(tokensPerSec float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasThroughputData {
		m.averageThroughput = tokensPerSec
		m.hasThroughputData = true
	} else {
		m.averageThroughput = ewmaAlpha*tokensPerSec + (1-ewmaAlpha)*m.averageThroughput
	}
}

// AverageThroughput returns the EWMA of per-request throughput (tokens/sec).
// Returns 0 if no data.
func (m *ProgramMetrics) AverageThroughput() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.averageThroughput
}

// TotalRequests returns the total number of requests seen for this program.
func (m *ProgramMetrics) TotalRequests() int64 {
	return m.totalRequests.Load()
}

// DispatchedCount returns the total number of dispatched requests for this program.
func (m *ProgramMetrics) DispatchedCount() int64 {
	return m.dispatchedCount.Load()
}

// TotalInputTokens returns the total number of input tokens across all requests.
func (m *ProgramMetrics) TotalInputTokens() int64 {
	return m.totalInputTokens.Load()
}

// TotalOutputTokens returns the total number of output tokens across all requests.
func (m *ProgramMetrics) TotalOutputTokens() int64 {
	return m.totalOutputTokens.Load()
}

// --- DRR deficit counter ---

// AddDeficit increases the deficit counter by n tokens (quantum allocation).
func (m *ProgramMetrics) AddDeficit(n int64) {
	m.deficitTokens.Add(n)
}

// DeductTokens decreases the deficit counter by n tokens (actual cost deduction).
func (m *ProgramMetrics) DeductTokens(n int64) {
	m.deficitTokens.Add(-n)
}

// ResetDeficit sets the deficit counter to zero (called when the queue drains).
func (m *ProgramMetrics) ResetDeficit() {
	m.deficitTokens.Store(0)
}

// Deficit returns the current deficit counter value in tokens.
func (m *ProgramMetrics) Deficit() int64 {
	return m.deficitTokens.Load()
}
