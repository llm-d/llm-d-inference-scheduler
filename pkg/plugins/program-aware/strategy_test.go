package programaware

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol"
	fcmocks "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol/mocks"
)

// testEWMA returns an EWMAStrategy with default weights for tests.
func testEWMA() *EWMAStrategy {
	return &EWMAStrategy{
		weightHeadWait:        defaultEWMAWeightHeadWait,
		weightAvgWait:         defaultEWMAWeightAvgWait,
		weightTotalDispatched: defaultEWMAWeightTotalDispatched,
	}
}

// testDRR returns a DRRStrategy with default weights for tests.
func testDRR() *DRRStrategy {
	return &DRRStrategy{
		weightDeficit:  defaultDRRWeightDeficit,
		weightHeadWait: defaultDRRWeightHeadWait,
		quantumTokens:  defaultDRRQuantumTokens,
	}
}

func TestNewStrategy_Valid(t *testing.T) {
	s, err := newStrategy(Config{Strategy: "ewma"})
	require.NoError(t, err)
	assert.Equal(t, "ewma", s.Name())

	s, err = newStrategy(Config{Strategy: "drr"})
	require.NoError(t, err)
	assert.Equal(t, "drr", s.Name())

	s, err = newStrategy(Config{Strategy: ""})
	require.NoError(t, err)
	assert.Equal(t, "ewma", s.Name())
}

func TestNewStrategy_Invalid(t *testing.T) {
	_, err := newStrategy(Config{Strategy: "unknown"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown scoring strategy")
}

func TestFactory_StrategyConfig(t *testing.T) {
	p, err := ProgramAwarePluginFactory("test", []byte(`{"strategy":"drr"}`), nil)
	require.NoError(t, err)
	plugin := p.(*ProgramAwarePlugin)
	assert.Equal(t, "drr", plugin.strategy.Name())
}

func TestFactory_DefaultStrategy(t *testing.T) {
	p, err := ProgramAwarePluginFactory("test", nil, nil)
	require.NoError(t, err)
	plugin := p.(*ProgramAwarePlugin)
	assert.Equal(t, "ewma", plugin.strategy.Name())
}

func TestFactory_InvalidStrategy(t *testing.T) {
	_, err := ProgramAwarePluginFactory("test", []byte(`{"strategy":"wfq"}`), nil)
	assert.Error(t, err)
}

// =============================================================================
// EWMA Strategy tests
// =============================================================================

func TestEWMAStrategy_OnPickStart_IsNoop(t *testing.T) {
	s := testEWMA()
	m := &ProgramMetrics{}
	m.AddDeficit(500)

	s.OnPickStart("prog", 5, m)
	assert.Equal(t, int64(500), m.Deficit(), "EWMA OnPickStart must not modify deficit")
}

func TestEWMAStrategy_OnCompleted_IsNoop(t *testing.T) {
	s := testEWMA()
	m := &ProgramMetrics{}

	s.OnCompleted(m, 100, 50)
	assert.Equal(t, int64(0), m.Deficit(), "EWMA OnCompleted must not modify deficit")
}

func TestEWMAStrategy_NumDimensions(t *testing.T) {
	s := testEWMA()
	assert.Equal(t, 3, s.NumDimensions())
}

func TestEWMAStrategy_CollectRaw(t *testing.T) {
	s := testEWMA()

	m := &ProgramMetrics{}
	m.RecordWaitTime(500)
	for range 10 {
		m.IncrementDispatched()
	}

	enqueueTime := time.Now().Add(-200 * time.Millisecond)
	queue := &fcmocks.MockFlowQueueAccessor{
		FlowKeyV:  flowcontrol.FlowKey{ID: "prog"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{EnqueueTimeV: enqueueTime},
	}

	raw := s.CollectRaw(queue, m)
	require.Len(t, raw, 3)
	assert.Greater(t, raw[ewmaDimHeadWait], 190.0, "headWaitMs should reflect enqueue age")
	assert.InDelta(t, 500.0, raw[ewmaDimAvgWait], 0.01)
	assert.InDelta(t, 10.0, raw[ewmaDimTotalDispatched], 0.01)
}

func TestEWMAStrategy_CollectRaw_NilMetrics(t *testing.T) {
	s := testEWMA()

	enqueueTime := time.Now().Add(-100 * time.Millisecond)
	queue := &fcmocks.MockFlowQueueAccessor{
		FlowKeyV:  flowcontrol.FlowKey{ID: "prog"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{EnqueueTimeV: enqueueTime},
	}

	raw := s.CollectRaw(queue, nil)
	require.Len(t, raw, 3)
	assert.Greater(t, raw[ewmaDimHeadWait], 90.0)
	assert.InDelta(t, 0.0, raw[ewmaDimAvgWait], 0.01)
	assert.InDelta(t, 0.0, raw[ewmaDimTotalDispatched], 0.01)
}

func TestEWMAStrategy_NormalizeDimension(t *testing.T) {
	s := testEWMA()

	// Standard range normalization.
	assert.InDelta(t, 0.0, s.NormalizeDimension(0, 0, 0, 100), 0.001)
	assert.InDelta(t, 0.5, s.NormalizeDimension(0, 50, 0, 100), 0.001)
	assert.InDelta(t, 1.0, s.NormalizeDimension(0, 100, 0, 100), 0.001)

	// min == max → 0.5.
	assert.InDelta(t, 0.5, s.NormalizeDimension(0, 42, 42, 42), 0.001)
}

func TestEWMAStrategy_Score(t *testing.T) {
	s := testEWMA()

	// Both high: max(0.5*1.0, 0.3*1.0) - 0 = 0.5 (no double-counting)
	score := s.Score([]float64{1.0, 1.0, 0.0})
	assert.InDelta(t, 0.5, score, 0.001)

	// Both zero, high dispatched: max(0, 0) - 0.2 = -0.2
	score = s.Score([]float64{0.0, 0.0, 1.0})
	assert.InDelta(t, -0.2, score, 0.001)

	// New flow (headWait only, no history): max(0.5*1.0, 0.3*0.0) = 0.5
	// Same as existing starved flow above — no cold-start penalty.
	score = s.Score([]float64{1.0, 0.0, 0.0})
	assert.InDelta(t, 0.5, score, 0.001)

	// Low headWait but high historical debt: max(0.5*0.2, 0.3*0.8) = max(0.1, 0.24) = 0.24
	score = s.Score([]float64{0.2, 0.8, 0.0})
	assert.InDelta(t, 0.24, score, 0.001)
}

// =============================================================================
// DRR Strategy tests
// =============================================================================

func TestDRRStrategy_OnPickStart_AllocatesQuantum(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}

	s.OnPickStart("prog", 3, m) // non-empty queue
	assert.Equal(t, defaultDRRQuantumTokens, m.Deficit(), "non-empty queue should receive quantum")
}

func TestDRRStrategy_OnPickStart_QuantumAccumulates(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}

	for range 5 {
		s.OnPickStart("prog", 1, m)
	}
	assert.Equal(t, defaultDRRQuantumTokens*5, m.Deficit(), "deficit should accumulate across rounds")
}

func TestDRRStrategy_OnPickStart_ResetsOnIdle(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}

	// Accumulate 3 rounds of quantum.
	for range 3 {
		s.OnPickStart("prog", 2, m)
	}
	assert.Equal(t, defaultDRRQuantumTokens*3, m.Deficit())

	// Queue drains.
	s.OnPickStart("prog", 0, m)
	assert.Equal(t, int64(0), m.Deficit(), "deficit must reset to 0 when queue drains")
}

func TestDRRStrategy_OnCompleted_DeductsTokens(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}
	m.AddDeficit(defaultDRRQuantumTokens) // one round of quantum

	s.OnCompleted(m, 700, 300) // 1000 tokens total
	assert.Equal(t, int64(0), m.Deficit(), "1000-token request should consume full quantum")
}

func TestDRRStrategy_OnCompleted_GoesNegativeOnOveruse(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}
	m.AddDeficit(defaultDRRQuantumTokens) // 1000 tokens

	s.OnCompleted(m, 1500, 500) // 2000 tokens — overserved
	assert.Equal(t, int64(-1000), m.Deficit(), "deficit should be negative after overuse")
}

func TestDRRStrategy_NumDimensions(t *testing.T) {
	s := testDRR()
	assert.Equal(t, 2, s.NumDimensions())
}

func TestDRRStrategy_CollectRaw(t *testing.T) {
	s := testDRR()

	m := &ProgramMetrics{}
	m.AddDeficit(5000)

	enqueueTime := time.Now().Add(-300 * time.Millisecond)
	queue := &fcmocks.MockFlowQueueAccessor{
		FlowKeyV:  flowcontrol.FlowKey{ID: "prog"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{EnqueueTimeV: enqueueTime},
	}

	raw := s.CollectRaw(queue, m)
	require.Len(t, raw, 2)
	assert.InDelta(t, 5000.0, raw[drrDimDeficit], 0.01)
	assert.Greater(t, raw[drrDimHeadWait], 290.0)
}

func TestDRRStrategy_CollectRaw_NilMetrics(t *testing.T) {
	s := testDRR()

	queue := &fcmocks.MockFlowQueueAccessor{
		FlowKeyV:  flowcontrol.FlowKey{ID: "prog"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{EnqueueTimeV: time.Now()},
	}

	raw := s.CollectRaw(queue, nil)
	require.Len(t, raw, 2)
	assert.InDelta(t, 0.0, raw[drrDimDeficit], 0.01)
}

func TestDRRStrategy_NormalizeDimension(t *testing.T) {
	s := testDRR()

	// Deficit range [-5000, +3000]: value -5000 → 0.0, +3000 → 1.0.
	assert.InDelta(t, 0.0, s.NormalizeDimension(drrDimDeficit, -5000, -5000, 3000), 0.001)
	assert.InDelta(t, 1.0, s.NormalizeDimension(drrDimDeficit, 3000, -5000, 3000), 0.001)

	// min == max → 0.5.
	assert.InDelta(t, 0.5, s.NormalizeDimension(drrDimDeficit, 100, 100, 100), 0.001)
}

func TestDRRStrategy_Score(t *testing.T) {
	s := testDRR()

	// deficit=1.0, headWait=0.0 → 0.7 + 0 = 0.7
	score := s.Score([]float64{1.0, 0.0})
	assert.InDelta(t, 0.7, score, 0.001)

	// deficit=0.0, headWait=1.0 → 0 + 0.3 = 0.3
	score = s.Score([]float64{0.0, 1.0})
	assert.InDelta(t, 0.3, score, 0.001)
}

func TestDRRStrategy_PreferHighDeficit(t *testing.T) {
	// End-to-end: two queues with different deficits, verify via CollectRaw + Normalize + Score.
	s := testDRR()
	now := time.Now()

	mHigh := &ProgramMetrics{}
	mHigh.AddDeficit(20000)

	mLow := &ProgramMetrics{}
	mLow.DeductTokens(20000)

	queueHigh := &fcmocks.MockFlowQueueAccessor{
		FlowKeyV:  flowcontrol.FlowKey{ID: "high"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{EnqueueTimeV: now},
	}
	queueLow := &fcmocks.MockFlowQueueAccessor{
		FlowKeyV:  flowcontrol.FlowKey{ID: "low"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{EnqueueTimeV: now},
	}

	rawHigh := s.CollectRaw(queueHigh, mHigh)
	rawLow := s.CollectRaw(queueLow, mLow)

	// Compute min/max across both.
	numDims := s.NumDimensions()
	dimMin := make([]float64, numDims)
	dimMax := make([]float64, numDims)
	for d := range numDims {
		dimMin[d] = min(rawHigh[d], rawLow[d])
		dimMax[d] = max(rawHigh[d], rawLow[d])
	}

	normHigh := make([]float64, numDims)
	normLow := make([]float64, numDims)
	for d := range numDims {
		normHigh[d] = s.NormalizeDimension(d, rawHigh[d], dimMin[d], dimMax[d])
		normLow[d] = s.NormalizeDimension(d, rawLow[d], dimMin[d], dimMax[d])
	}

	scoreHigh := s.Score(normHigh)
	scoreLow := s.Score(normLow)

	assert.Greater(t, scoreHigh, scoreLow,
		"high-deficit queue (score=%.4f) should outscore overserved queue (score=%.4f)",
		scoreHigh, scoreLow)
}

// =============================================================================
// DRR Pick integration tests
// =============================================================================

func TestDRR_Pick_TokenHeavyProgramDeprioritized(t *testing.T) {
	p := &ProgramAwarePlugin{strategy: testDRR()}

	// "heavy" has consumed many tokens relative to its quantum allocation.
	mHeavy := p.getOrCreateMetrics("heavy")
	mHeavy.AddDeficit(defaultDRRQuantumTokens * 2)
	mHeavy.DeductTokens(defaultDRRQuantumTokens * 10)

	// "light" has only consumed its fair share.
	mLight := p.getOrCreateMetrics("light")
	mLight.AddDeficit(defaultDRRQuantumTokens * 2)
	mLight.DeductTokens(defaultDRRQuantumTokens * 1)

	now := time.Now()
	queueHeavy := &fcmocks.MockFlowQueueAccessor{
		LenV:     5,
		FlowKeyV: flowcontrol.FlowKey{ID: "heavy"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV:     now,
			OriginalRequestV: &fcmocks.MockFlowControlRequest{IDV: "heavy-req-1"},
		},
	}
	queueLight := &fcmocks.MockFlowQueueAccessor{
		LenV:     1,
		FlowKeyV: flowcontrol.FlowKey{ID: "light"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV:     now,
			OriginalRequestV: &fcmocks.MockFlowControlRequest{IDV: "light-req-1"},
		},
	}

	band := &fcmocks.MockPriorityBandAccessor{
		IterateQueuesFunc: func(cb func(flowcontrol.FlowQueueAccessor) bool) {
			cb(queueHeavy)
			cb(queueLight)
		},
	}

	queue, err := p.Pick(context.Background(), band)
	require.NoError(t, err)
	assert.Equal(t, "light", queue.FlowKey().ID,
		"light program (fair deficit) should be preferred over token-heavy program")
}

func TestDRR_Pick_QuantumAllocatedDuringPick(t *testing.T) {
	p := &ProgramAwarePlugin{strategy: testDRR()}

	// Two fresh programs with no prior state.
	_ = p.getOrCreateMetrics("alpha")
	_ = p.getOrCreateMetrics("beta")

	now := time.Now()
	makeQueue := func(id string) *fcmocks.MockFlowQueueAccessor {
		return &fcmocks.MockFlowQueueAccessor{
			LenV:     1,
			FlowKeyV: flowcontrol.FlowKey{ID: id},
			PeekHeadV: &fcmocks.MockQueueItemAccessor{
				EnqueueTimeV:     now,
				OriginalRequestV: &fcmocks.MockFlowControlRequest{IDV: id + "-req"},
			},
		}
	}

	band := &fcmocks.MockPriorityBandAccessor{
		IterateQueuesFunc: func(cb func(flowcontrol.FlowQueueAccessor) bool) {
			cb(makeQueue("alpha"))
			cb(makeQueue("beta"))
		},
	}

	_, err := p.Pick(context.Background(), band)
	require.NoError(t, err)

	// Both queues should have received a quantum during Pick().
	alphaMetrics, _ := p.programMetrics.Load("alpha")
	betaMetrics, _ := p.programMetrics.Load("beta")

	// Deficit after Pick() = quantumTokens (added by OnPickStart, not yet deducted)
	assert.Equal(t, defaultDRRQuantumTokens, alphaMetrics.(*ProgramMetrics).Deficit())
	assert.Equal(t, defaultDRRQuantumTokens, betaMetrics.(*ProgramMetrics).Deficit())
}
