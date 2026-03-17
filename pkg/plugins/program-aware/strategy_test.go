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

func TestNewStrategy_Valid(t *testing.T) {
	s, err := newStrategy("ewma")
	require.NoError(t, err)
	assert.Equal(t, "ewma", s.Name())

	s, err = newStrategy("drr")
	require.NoError(t, err)
	assert.Equal(t, "drr", s.Name())

	s, err = newStrategy("")
	require.NoError(t, err)
	assert.Equal(t, "ewma", s.Name())
}

func TestNewStrategy_Invalid(t *testing.T) {
	_, err := newStrategy("unknown")
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

func TestEWMAStrategy_OnPickStart_IsNoop(t *testing.T) {
	s := &EWMAStrategy{}
	m := &ProgramMetrics{}
	m.AddDeficit(500)

	s.OnPickStart("prog", 5, m)
	assert.Equal(t, int64(500), m.Deficit(), "EWMA OnPickStart must not modify deficit")
}

func TestEWMAStrategy_OnCompleted_IsNoop(t *testing.T) {
	s := &EWMAStrategy{}
	m := &ProgramMetrics{}

	s.OnCompleted(m, 100, 50)
	assert.Equal(t, int64(0), m.Deficit(), "EWMA OnCompleted must not modify deficit")
}

func TestDRRStrategy_OnPickStart_AllocatesQuantum(t *testing.T) {
	s := &DRRStrategy{}
	m := &ProgramMetrics{}

	s.OnPickStart("prog", 3, m) // non-empty queue
	assert.Equal(t, drrQuantumTokens, m.Deficit(), "non-empty queue should receive quantum")
}

func TestDRRStrategy_OnPickStart_QuantumAccumulates(t *testing.T) {
	s := &DRRStrategy{}
	m := &ProgramMetrics{}

	for range 5 {
		s.OnPickStart("prog", 1, m)
	}
	assert.Equal(t, drrQuantumTokens*5, m.Deficit(), "deficit should accumulate across rounds")
}

func TestDRRStrategy_OnPickStart_ResetsOnIdle(t *testing.T) {
	s := &DRRStrategy{}
	m := &ProgramMetrics{}

	// Accumulate 3 rounds of quantum.
	for range 3 {
		s.OnPickStart("prog", 2, m)
	}
	assert.Equal(t, drrQuantumTokens*3, m.Deficit())

	// Queue drains.
	s.OnPickStart("prog", 0, m)
	assert.Equal(t, int64(0), m.Deficit(), "deficit must reset to 0 when queue drains")
}

func TestDRRStrategy_OnCompleted_DeductsTokens(t *testing.T) {
	s := &DRRStrategy{}
	m := &ProgramMetrics{}
	m.AddDeficit(drrQuantumTokens) // one round of quantum

	s.OnCompleted(m, 700, 300) // 1000 tokens total
	assert.Equal(t, int64(0), m.Deficit(), "1000-token request should consume full quantum")
}

func TestDRRStrategy_OnCompleted_GoesNegativeOnOveruse(t *testing.T) {
	s := &DRRStrategy{}
	m := &ProgramMetrics{}
	m.AddDeficit(drrQuantumTokens) // 1000 tokens

	s.OnCompleted(m, 1500, 500) // 2000 tokens — overserved
	assert.Equal(t, int64(-1000), m.Deficit(), "deficit should be negative after overuse")
}

func TestDRRStrategy_ScoreQueue_PreferHighDeficit(t *testing.T) {
	s := &DRRStrategy{}
	now := time.Now()

	// Two programs with equal head wait; one has much higher deficit.
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

	scoreHigh := s.ScoreQueue(queueHigh, mHigh)
	scoreLow := s.ScoreQueue(queueLow, mLow)

	assert.Greater(t, scoreHigh, scoreLow,
		"high-deficit queue (score=%.4f) should outscore overserved queue (score=%.4f)",
		scoreHigh, scoreLow)
}

func TestDRRStrategy_ScoreQueue_NilMetrics(t *testing.T) {
	s := &DRRStrategy{}
	now := time.Now()
	queue := &fcmocks.MockFlowQueueAccessor{
		FlowKeyV:  flowcontrol.FlowKey{ID: "prog"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{EnqueueTimeV: now},
	}

	score := s.ScoreQueue(queue, nil)
	assert.InDelta(t, 0.35, score, 0.01, "nil metrics should score at deficit midpoint")
}

func TestDRR_Pick_TokenHeavyProgramDeprioritized(t *testing.T) {
	p := &ProgramAwarePlugin{strategy: &DRRStrategy{}}

	// "heavy" has consumed many tokens relative to its quantum allocation.
	mHeavy := p.getOrCreateMetrics("heavy")
	mHeavy.AddDeficit(drrQuantumTokens * 2)
	mHeavy.DeductTokens(drrQuantumTokens * 10)

	// "light" has only consumed its fair share.
	mLight := p.getOrCreateMetrics("light")
	mLight.AddDeficit(drrQuantumTokens * 2)
	mLight.DeductTokens(drrQuantumTokens * 1)

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
	p := &ProgramAwarePlugin{strategy: &DRRStrategy{}}

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
	assert.Equal(t, drrQuantumTokens, alphaMetrics.(*ProgramMetrics).Deficit())
	assert.Equal(t, drrQuantumTokens, betaMetrics.(*ProgramMetrics).Deficit())
}
