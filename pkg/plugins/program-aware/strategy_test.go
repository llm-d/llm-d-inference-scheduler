package programaware

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol"
	fcmocks "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol/mocks"
	requestcontrol "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
)

// testDRR returns a DRRStrategy with default weights for tests.
func testDRR() *DRRStrategy {
	s := &DRRStrategy{
		weightDeficit:  defaultDRRWeightDeficit,
		weightHeadWait: defaultDRRWeightHeadWait,
		quantumTokens:  defaultDRRQuantumTokens,
	}
	s.addQuantum.Store(true)
	return s
}

func TestNewStrategy_Valid(t *testing.T) {
	s, err := newStrategy(Config{Strategy: "drr"})
	require.NoError(t, err)
	assert.Equal(t, "drr", s.Name())

	s, err = newStrategy(Config{Strategy: "las"})
	require.NoError(t, err)
	assert.Equal(t, "las", s.Name())

	s, err = newStrategy(Config{Strategy: ""})
	require.NoError(t, err)
	assert.Equal(t, "las", s.Name())
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
	assert.Equal(t, "las", plugin.strategy.Name())
}

func TestFactory_InvalidStrategy(t *testing.T) {
	_, err := ProgramAwarePluginFactory("test", []byte(`{"strategy":"wfq"}`), nil)
	assert.Error(t, err)
}

// =============================================================================
// DRR Strategy tests
// =============================================================================

// makeQueueInfo builds a QueueInfo with a mock queue for testing.
func makeQueueInfo(id string, queueLen int, metrics *ProgramMetrics, enqueueTime time.Time) QueueInfo {
	return QueueInfo{
		Queue: &fcmocks.MockFlowQueueAccessor{
			LenV:     queueLen,
			FlowKeyV: flowcontrol.FlowKey{ID: id},
			PeekHeadV: &fcmocks.MockQueueItemAccessor{
				EnqueueTimeV:     enqueueTime,
				OriginalRequestV: &fcmocks.MockFlowControlRequest{IDV: id + "-req"},
			},
		},
		Metrics: metrics,
		Len:     queueLen,
	}
}

// makeEmptyQueueInfo builds a QueueInfo for an empty queue.
func makeEmptyQueueInfo(id string, metrics *ProgramMetrics) QueueInfo {
	return QueueInfo{
		Queue: &fcmocks.MockFlowQueueAccessor{
			LenV:     0,
			FlowKeyV: flowcontrol.FlowKey{ID: id},
		},
		Metrics: metrics,
		Len:     0,
	}
}

func TestDRRStrategy_Pick_AllocatesQuantum(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}
	now := time.Now()

	queues := map[string]QueueInfo{"prog": makeQueueInfo("prog", 3, m, now)}
	s.Pick(queues)

	assert.Equal(t, defaultDRRQuantumTokens, m.Deficit(), "non-empty queue should receive quantum")
}

func TestDRRStrategy_Pick_QuantumAccumulates(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}
	now := time.Now()

	for range 5 {
		queues := map[string]QueueInfo{"prog": makeQueueInfo("prog", 1, m, now)}
		s.Pick(queues)
		s.OnPreRequest(nil, nil) // reset for next dispatch cycle
	}
	assert.Equal(t, defaultDRRQuantumTokens*5, m.Deficit(), "deficit should accumulate across rounds")
}

func TestDRRStrategy_Pick_IdleAccumulatesDeficit(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}
	now := time.Now()

	// Accumulate 3 rounds of quantum.
	for range 3 {
		queues := map[string]QueueInfo{"prog": makeQueueInfo("prog", 2, m, now)}
		s.Pick(queues)
		s.OnPreRequest(nil, nil)
	}
	assert.Equal(t, defaultDRRQuantumTokens*3, m.Deficit())

	// Queue drains — deficit should still accumulate (quantum allocated to idle queues).
	queues := map[string]QueueInfo{"prog": makeEmptyQueueInfo("prog", m)}
	s.Pick(queues)
	assert.Equal(t, defaultDRRQuantumTokens*4, m.Deficit(), "idle queues still receive quantum")
}

func TestDRRStrategy_OnCompleted_DeductsTokens(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}
	m.AddDeficit(defaultDRRQuantumTokens) // one round of quantum

	resp := &requestcontrol.Response{Usage: requestcontrol.Usage{PromptTokens: 700, CompletionTokens: 300}}
	s.OnCompleted(m, nil, resp) // weighted cost: 700*1 + 300*2 = 1300
	assert.Equal(t, int64(-300), m.Deficit(), "weighted 1300-token cost against 1000 quantum")
}

func TestDRRStrategy_OnCompleted_GoesNegativeOnOveruse(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}
	m.AddDeficit(defaultDRRQuantumTokens) // 1000 tokens

	resp := &requestcontrol.Response{Usage: requestcontrol.Usage{PromptTokens: 1500, CompletionTokens: 500}}
	s.OnCompleted(m, nil, resp) // weighted cost: 1500*1 + 500*2 = 2500
	assert.Equal(t, int64(-1500), m.Deficit(), "deficit should be negative after overuse")
}

func TestDRRStrategy_Pick_PreferHighDeficit(t *testing.T) {
	s := testDRR()
	now := time.Now()

	mHigh := &ProgramMetrics{}
	mHigh.AddDeficit(20000)

	mLow := &ProgramMetrics{}
	mLow.DeductTokens(20000)

	queues := map[string]QueueInfo{
		"high": makeQueueInfo("high", 1, mHigh, now),
		"low":  makeQueueInfo("low", 1, mLow, now),
	}

	selected, scores := s.Pick(queues)
	require.NotNil(t, selected)
	assert.Equal(t, "high", selected.FlowKey().ID)
	assert.Greater(t, scores["high"], scores["low"],
		"high-deficit queue should outscore overserved queue")
}

func TestDRRStrategy_Pick_QuantumOncePerCycle(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}
	now := time.Now()

	// First Pick allocates quantum.
	queues := map[string]QueueInfo{"prog": makeQueueInfo("prog", 3, m, now)}
	s.Pick(queues)
	assert.Equal(t, defaultDRRQuantumTokens, m.Deficit(), "first Pick should allocate quantum")

	// Second Pick without OnPrerequest — same program already seen, no extra quantum.
	s.Pick(queues)
	assert.Equal(t, defaultDRRQuantumTokens, m.Deficit(), "second Pick without OnPrerequest should not allocate again")
}

func TestDRRStrategy_OnPrerequest_ResetsQuantum(t *testing.T) {
	s := testDRR()
	m := &ProgramMetrics{}
	now := time.Now()

	queues := map[string]QueueInfo{"prog": makeQueueInfo("prog", 3, m, now)}
	s.Pick(queues)
	assert.Equal(t, defaultDRRQuantumTokens, m.Deficit())

	// OnPrerequest resets the cycle — next Pick should allocate again.
	s.OnPreRequest(nil, nil)
	s.Pick(queues)
	assert.Equal(t, defaultDRRQuantumTokens*2, m.Deficit(), "Pick after OnPrerequest should allocate quantum again")
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

// =============================================================================
// LAS Strategy tests
// =============================================================================

// testService returns a LASStrategy with default weights for tests.
func testService() *LASStrategy {
	return &LASStrategy{
		weightService:  defaultServiceWeightService,
		weightHeadWait: defaultServiceWeightHeadWait,
		decayFactor:    defaultServiceDecayFactor,
	}
}

func TestLASStrategy_Name(t *testing.T) {
	s := testService()
	assert.Equal(t, "las", s.Name())
}

func TestLASStrategy_Pick_DecaysService(t *testing.T) {
	s := testService()
	m := &ProgramMetrics{}
	m.AddService(1000.0)
	now := time.Now()

	queues := map[string]QueueInfo{"prog": makeQueueInfo("prog", 5, m, now)}
	s.Pick(queues)

	assert.InDelta(t, 1000.0*defaultServiceDecayFactor, m.AttainedService(), 0.01,
		"Pick should decay attained service")
}

func TestLASStrategy_OnCompleted_AddsService(t *testing.T) {
	s := testService()
	m := &ProgramMetrics{}

	// 100 input + 50 output → weighted: 100*1 + 50*2 = 200
	resp := &requestcontrol.Response{Usage: requestcontrol.Usage{PromptTokens: 100, CompletionTokens: 50}}
	s.OnCompleted(m, nil, resp)
	assert.InDelta(t, 200.0, m.AttainedService(), 0.01,
		"OnCompleted should add weighted token cost to attained service")
}

func TestLASStrategy_Pick_PreferLowService(t *testing.T) {
	s := testService()
	now := time.Now()

	mLow := &ProgramMetrics{}
	mLow.AddService(100.0)

	mHigh := &ProgramMetrics{}
	mHigh.AddService(10000.0)

	queues := map[string]QueueInfo{
		"low":  makeQueueInfo("low", 1, mLow, now),
		"high": makeQueueInfo("high", 1, mHigh, now),
	}

	selected, scores := s.Pick(queues)
	require.NotNil(t, selected)
	assert.Equal(t, "low", selected.FlowKey().ID)
	assert.Greater(t, scores["low"], scores["high"],
		"underserved queue should outscore overserved queue")
}

func TestLASStrategy_Pick_ColdStartUsesHeadWait(t *testing.T) {
	s := testService()

	mOld := &ProgramMetrics{}
	mNew := &ProgramMetrics{}

	queues := map[string]QueueInfo{
		"old": makeQueueInfo("old", 1, mOld, time.Now().Add(-500*time.Millisecond)),
		"new": makeQueueInfo("new", 1, mNew, time.Now()),
	}

	selected, scores := s.Pick(queues)
	require.NotNil(t, selected)
	assert.Equal(t, "old", selected.FlowKey().ID)
	assert.Greater(t, scores["old"], scores["new"],
		"with zero service, longer-waiting queue should outscore newer queue")
}

func TestLASStrategy_DecayForgetsOldService(t *testing.T) {
	s := testService()
	m := &ProgramMetrics{}
	m.AddService(1000.0)
	now := time.Now()

	// After many decay cycles, service should approach 0.
	for range 1000 {
		queues := map[string]QueueInfo{"prog": makeQueueInfo("prog", 1, m, now)}
		s.Pick(queues)
	}
	// 1000 * 0.995^1000 ≈ 6.7 — verify significant decay occurred.
	assert.Less(t, m.AttainedService(), 10.0,
		"after 1000 decay cycles, attained service should be nearly forgotten")
}

func TestNewStrategy_LAS(t *testing.T) {
	s, err := newStrategy(Config{Strategy: "las"})
	require.NoError(t, err)
	assert.Equal(t, "las", s.Name())
}

func TestFactory_LASStrategy(t *testing.T) {
	p, err := ProgramAwarePluginFactory("test", []byte(`{"strategy":"las"}`), nil)
	require.NoError(t, err)
	plugin := p.(*ProgramAwarePlugin)
	assert.Equal(t, "las", plugin.strategy.Name())
}

func testServiceTimed(halfLife float64) *LASStrategy {
	return &LASStrategy{
		weightService:   defaultServiceWeightService,
		weightHeadWait:  defaultServiceWeightHeadWait,
		halfLifeSeconds: halfLife,
	}
}

func TestLASStrategy_TimedDecay_FirstCallNoDecay(t *testing.T) {
	m := &ProgramMetrics{}
	m.AddService(1000.0)

	// First call should set lastDecayTime but not decay.
	m.DecayServiceTimed(30.0, time.Now())
	assert.InDelta(t, 1000.0, m.AttainedService(), 0.01,
		"first timed decay call should not reduce service")
}

func TestLASStrategy_TimedDecay_HalvesAtHalfLife(t *testing.T) {
	m := &ProgramMetrics{}
	m.AddService(1000.0)

	now := time.Now()
	m.DecayServiceTimed(30.0, now)                     // initialize lastDecayTime
	m.DecayServiceTimed(30.0, now.Add(30*time.Second)) // exactly one half-life later

	assert.InDelta(t, 500.0, m.AttainedService(), 1.0,
		"service should halve after exactly one half-life")
}

func TestLASStrategy_TimedDecay_ConsistentWindow(t *testing.T) {
	// Whether we call DecayServiceTimed once after 30s or 100 times over 30s,
	// the result should be the same.
	m1 := &ProgramMetrics{}
	m1.AddService(1000.0)
	now := time.Now()
	m1.DecayServiceTimed(30.0, now)
	m1.DecayServiceTimed(30.0, now.Add(30*time.Second))
	singleCall := m1.AttainedService()

	m2 := &ProgramMetrics{}
	m2.AddService(1000.0)
	m2.DecayServiceTimed(30.0, now)
	for i := 1; i <= 100; i++ {
		m2.DecayServiceTimed(30.0, now.Add(time.Duration(i)*300*time.Millisecond))
	}
	manyCalls := m2.AttainedService()

	assert.InDelta(t, singleCall, manyCalls, 1.0,
		"time-based decay should produce same result regardless of call frequency")
}

func TestLASStrategy_Pick_UsesTimedDecay(t *testing.T) {
	s := testServiceTimed(30.0)
	m := &ProgramMetrics{}
	m.AddService(1000.0)
	now := time.Now()

	// First Pick initializes timer.
	queues := map[string]QueueInfo{"prog": makeQueueInfo("prog", 1, m, now)}
	s.Pick(queues)
	assert.InDelta(t, 1000.0, m.AttainedService(), 0.01)
}

func TestFactory_ServiceHalfLifeSeconds(t *testing.T) {
	p, err := ProgramAwarePluginFactory("test", []byte(`{"strategy":"las","serviceHalfLifeSeconds":30}`), nil)
	require.NoError(t, err)
	plugin := p.(*ProgramAwarePlugin)
	svc := plugin.strategy.(*LASStrategy)
	assert.Equal(t, 30.0, svc.halfLifeSeconds)
}

// =============================================================================
// RR (Round-Robin) Strategy tests
// =============================================================================

func TestNewStrategy_RR(t *testing.T) {
	s, err := newStrategy(Config{Strategy: "rr"})
	require.NoError(t, err)
	assert.Equal(t, "rr", s.Name())
}

func TestFactory_RRStrategy(t *testing.T) {
	p, err := ProgramAwarePluginFactory("test", []byte(`{"strategy":"rr"}`), nil)
	require.NoError(t, err)
	plugin := p.(*ProgramAwarePlugin)
	assert.Equal(t, "rr", plugin.strategy.Name())
}

// simulateRRCycle runs one Pick() cycle on the RRStrategy directly.
// Returns the selected program ID (or "" if none).
func simulateRRCycle(s *RRStrategy, allIDs []string, nonEmptyIDs []string) string {
	now := time.Now()

	// Build QueueInfo map: empty queues for IDs not in nonEmptyIDs.
	nonEmptySet := make(map[string]bool, len(nonEmptyIDs))
	for _, id := range nonEmptyIDs {
		nonEmptySet[id] = true
	}

	queues := make(map[string]QueueInfo, len(allIDs))
	for _, id := range allIDs {
		if nonEmptySet[id] {
			queues[id] = makeQueueInfo(id, 1, nil, now)
		} else {
			queues[id] = makeEmptyQueueInfo(id, nil)
		}
	}

	selected, _ := s.Pick(queues)
	if selected == nil {
		return ""
	}
	return selected.FlowKey().ID
}

func TestRRStrategy_BasicCycle(t *testing.T) {
	s := &RRStrategy{}
	ids := []string{"alpha", "beta", "gamma"}

	// Sorted order: alpha, beta, gamma.
	// With no lastSelected, startIndex=0 → alpha first.
	picked := simulateRRCycle(s, ids, ids)
	assert.Equal(t, "alpha", picked, "first cycle should pick alpha (first in sorted order)")

	// After alpha, cursor advances → beta.
	picked = simulateRRCycle(s, ids, ids)
	assert.Equal(t, "beta", picked, "second cycle should pick beta")

	// After beta → gamma.
	picked = simulateRRCycle(s, ids, ids)
	assert.Equal(t, "gamma", picked, "third cycle should pick gamma")

	// After gamma → wraps to alpha.
	picked = simulateRRCycle(s, ids, ids)
	assert.Equal(t, "alpha", picked, "fourth cycle should wrap back to alpha")
}

func TestRRStrategy_SkipsEmptyQueues(t *testing.T) {
	s := &RRStrategy{}
	allIDs := []string{"alpha", "beta", "gamma"}
	nonEmpty := []string{"alpha", "gamma"} // beta is empty

	// Sorted: alpha, beta, gamma. startIndex=0 → alpha wins.
	picked := simulateRRCycle(s, allIDs, nonEmpty)
	assert.Equal(t, "alpha", picked)

	// After alpha, startIndex=1 (beta). beta is empty.
	// gamma: distance = (2-1+3)%3 = 1, score = 3-1 = 2
	// alpha: distance = (0-1+3)%3 = 2, score = 3-2 = 1
	// gamma wins.
	picked = simulateRRCycle(s, allIDs, nonEmpty)
	assert.Equal(t, "gamma", picked, "should skip empty beta and pick gamma")

	// After gamma, startIndex=0 (wraps). alpha wins again.
	picked = simulateRRCycle(s, allIDs, nonEmpty)
	assert.Equal(t, "alpha", picked, "should wrap back to alpha")
}

func TestRRStrategy_WrapAround(t *testing.T) {
	s := &RRStrategy{}

	// Set cursor to "c" (last in sorted order) by running a pick that selects "c".
	s.mu.Lock()
	s.lastSelected = "c"
	s.mu.Unlock()

	ids := []string{"a", "b", "c"}
	// Next cycle: startIndex = (index_of_c + 1) % 3 = 0 → "a" should win.
	picked := simulateRRCycle(s, ids, ids)
	assert.Equal(t, "a", picked, "should wrap from c to a")
}

func TestRRStrategy_SingleQueue(t *testing.T) {
	s := &RRStrategy{}
	ids := []string{"solo"}

	for i := range 5 {
		picked := simulateRRCycle(s, ids, ids)
		assert.Equal(t, "solo", picked, "single queue should always be picked (iteration %d)", i)
	}
}

func TestRRStrategy_Pick_UpdatesCursor(t *testing.T) {
	s := &RRStrategy{}

	queues := map[string]QueueInfo{
		"alpha": makeQueueInfo("alpha", 1, nil, time.Now()),
		"beta":  makeQueueInfo("beta", 1, nil, time.Now()),
	}

	selected, _ := s.Pick(queues)
	require.NotNil(t, selected)

	s.mu.Lock()
	assert.Equal(t, selected.FlowKey().ID, s.lastSelected, "lastSelected should be updated after Pick")
	s.mu.Unlock()
}

func TestRRStrategy_NoQueues(t *testing.T) {
	s := &RRStrategy{}

	// Empty cycle — no queues at all.
	picked := simulateRRCycle(s, nil, nil)
	assert.Equal(t, "", picked, "no queues should yield empty pick")
}

func TestFactory_RRDeferCursor(t *testing.T) {
	p, err := ProgramAwarePluginFactory("test", []byte(`{"strategy":"rr","deferRRCursor":true}`), nil)
	require.NoError(t, err)
	plugin := p.(*ProgramAwarePlugin)
	rr := plugin.strategy.(*RRStrategy)
	assert.True(t, rr.deferCursor)
}

func TestRRStrategy_DeferCursor_PickDoesNotAdvance(t *testing.T) {
	s := &RRStrategy{deferCursor: true}
	s.moveCursor.Store(true)
	ids := []string{"alpha", "beta", "gamma"}

	picked := simulateRRCycle(s, ids, ids)
	assert.Equal(t, "alpha", picked)

	s.mu.Lock()
	assert.Equal(t, "alpha", s.lastSelected, "first Pick advances lastSelected")
	s.mu.Unlock()

	// Second Pick without OnPreRequest returns same queue.
	picked = simulateRRCycle(s, ids, ids)
	assert.Equal(t, "alpha", picked, "repeated Pick without OnPreRequest returns same queue")
}

func TestRRStrategy_DeferCursor_OnPreRequestCommits(t *testing.T) {
	s := &RRStrategy{deferCursor: true}
	s.moveCursor.Store(true)
	ids := []string{"alpha", "beta", "gamma"}

	simulateRRCycle(s, ids, ids) // picks alpha, sets moveCursor=false
	s.OnPreRequest(nil, nil)     // resets moveCursor=true

	s.mu.Lock()
	assert.Equal(t, "alpha", s.lastSelected)
	s.mu.Unlock()

	// Next pick should advance past alpha.
	picked := simulateRRCycle(s, ids, ids)
	assert.Equal(t, "beta", picked)
}

func TestRRStrategy_DeferCursor_RepeatedPickWithoutDispatch(t *testing.T) {
	s := &RRStrategy{deferCursor: true}
	s.moveCursor.Store(true)
	ids := []string{"alpha", "beta", "gamma"}

	// Pick three times without OnPreRequest — cursor never advances.
	for range 3 {
		picked := simulateRRCycle(s, ids, ids)
		assert.Equal(t, "alpha", picked, "without OnPreRequest, alpha is always picked")
	}
}

func TestRRStrategy_DeferCursor_FullCycle(t *testing.T) {
	s := &RRStrategy{deferCursor: true}
	s.moveCursor.Store(true)
	ids := []string{"alpha", "beta", "gamma"}

	expected := []string{"alpha", "beta", "gamma", "alpha"}
	for _, want := range expected {
		picked := simulateRRCycle(s, ids, ids)
		assert.Equal(t, want, picked)
		s.OnPreRequest(nil, nil) // commit after dispatch
	}
}

func TestRRStrategy_DeferCursor_AllEmpty(t *testing.T) {
	s := &RRStrategy{deferCursor: true}
	s.moveCursor.Store(true)
	allIDs := []string{"alpha", "beta"}

	picked := simulateRRCycle(s, allIDs, nil) // all empty
	assert.Equal(t, "", picked)

	s.mu.Lock()
	assert.Equal(t, "", s.lastSelected)
	s.mu.Unlock()
}

func TestRR_Pick_CyclesThroughPrograms(t *testing.T) {
	p := &ProgramAwarePlugin{strategy: &RRStrategy{}}

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
			cb(makeQueue("gamma"))
		},
	}

	// Three picks should cycle alpha → beta → gamma.
	expected := []string{"alpha", "beta", "gamma"}
	for i, want := range expected {
		queue, err := p.Pick(context.Background(), band)
		require.NoError(t, err)
		assert.Equal(t, want, queue.FlowKey().ID,
			"Pick #%d should select %s", i+1, want)
	}

	// Fourth pick wraps to alpha.
	queue, err := p.Pick(context.Background(), band)
	require.NoError(t, err)
	assert.Equal(t, "alpha", queue.FlowKey().ID, "Pick #4 should wrap to alpha")
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

	// Deficit after Pick() = quantumTokens (added by strategy.Pick(), not yet deducted)
	assert.Equal(t, defaultDRRQuantumTokens, alphaMetrics.(*ProgramMetrics).Deficit())
	assert.Equal(t, defaultDRRQuantumTokens, betaMetrics.(*ProgramMetrics).Deficit())
}
