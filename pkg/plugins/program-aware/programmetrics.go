package programaware

import (
	"sync"
	"sync/atomic"
)

const ewmaAlpha = 0.2

// ProgramMetrics holds aggregated metrics for a single program (identified by its fairness ID).
// All methods are goroutine-safe.
type ProgramMetrics struct {
	mu              sync.Mutex
	averageWaitTime float64 // EWMA in milliseconds
	hasWaitData     bool

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

// RecordWaitTime updates the EWMA of wait time with a new observation.
func (m *ProgramMetrics) RecordWaitTime(waitMs float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasWaitData {
		m.averageWaitTime = waitMs
		m.hasWaitData = true
	} else {
		m.averageWaitTime = ewmaAlpha*waitMs + (1-ewmaAlpha)*m.averageWaitTime
	}
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

// RecordTokens adds token counts from a completed request.
func (m *ProgramMetrics) RecordTokens(input, output int64) {
	m.totalInputTokens.Add(input)
	m.totalOutputTokens.Add(output)
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
