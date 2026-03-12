package programaware

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol"
	fcmocks "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol/mocks"
	requestcontrol "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	scheduling "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

// --- Factory tests ---

func TestFactory(t *testing.T) {
	p, err := ProgramAwarePluginFactory("test-instance", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, p)

	assert.Equal(t, ProgramAwarePluginType, p.TypedName().Type)
	assert.Equal(t, "test-instance", p.TypedName().Name)
}

// --- ProgramMetrics tests ---

func TestProgramMetrics_EWMA(t *testing.T) {
	m := &ProgramMetrics{}

	// First observation initializes EWMA directly.
	m.RecordWaitTime(100)
	assert.InDelta(t, 100.0, m.AverageWaitTime(), 0.01)

	// Second observation: EWMA = 0.2*200 + 0.8*100 = 120
	m.RecordWaitTime(200)
	assert.InDelta(t, 120.0, m.AverageWaitTime(), 0.01)

	// Third observation: EWMA = 0.2*50 + 0.8*120 = 106
	m.RecordWaitTime(50)
	assert.InDelta(t, 106.0, m.AverageWaitTime(), 0.01)
}

func TestProgramMetrics_Counters(t *testing.T) {
	m := &ProgramMetrics{}

	m.IncrementRequests()
	m.IncrementRequests()
	m.IncrementDispatched()
	m.RecordTokens(100, 50)
	m.RecordTokens(200, 75)

	assert.Equal(t, int64(2), m.TotalRequests())
	assert.Equal(t, int64(1), m.DispatchedCount())
	assert.Equal(t, int64(300), m.TotalInputTokens())
	assert.Equal(t, int64(125), m.TotalOutputTokens())
}

// --- Pick tests ---

func TestPick_NilBand(t *testing.T) {
	p := &ProgramAwarePlugin{}
	queue, err := p.Pick(context.Background(), nil)
	assert.NoError(t, err)
	assert.Nil(t, queue)
}

func TestPick_AllQueuesEmpty(t *testing.T) {
	p := &ProgramAwarePlugin{}

	band := &fcmocks.MockPriorityBandAccessor{
		IterateQueuesFunc: func(cb func(flowcontrol.FlowQueueAccessor) bool) {
			cb(&fcmocks.MockFlowQueueAccessor{LenV: 0, FlowKeyV: flowcontrol.FlowKey{ID: "prog-a"}})
			cb(&fcmocks.MockFlowQueueAccessor{LenV: 0, FlowKeyV: flowcontrol.FlowKey{ID: "prog-b"}})
		},
	}

	queue, err := p.Pick(context.Background(), band)
	assert.NoError(t, err)
	assert.Nil(t, queue)
}

func TestPick_SingleNonEmptyQueue(t *testing.T) {
	p := &ProgramAwarePlugin{}

	queueA := &fcmocks.MockFlowQueueAccessor{
		LenV:     3,
		FlowKeyV: flowcontrol.FlowKey{ID: "prog-a"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV: time.Now().Add(-2 * time.Second),
		},
	}

	band := &fcmocks.MockPriorityBandAccessor{
		IterateQueuesFunc: func(cb func(flowcontrol.FlowQueueAccessor) bool) {
			cb(queueA)
			cb(&fcmocks.MockFlowQueueAccessor{LenV: 0, FlowKeyV: flowcontrol.FlowKey{ID: "prog-b"}})
		},
	}

	queue, err := p.Pick(context.Background(), band)
	assert.NoError(t, err)
	assert.Equal(t, queueA, queue)
}

func TestPick_RecordsEnqueueTime(t *testing.T) {
	p := &ProgramAwarePlugin{}

	enqueueTime := time.Now().Add(-500 * time.Millisecond)
	queueA := &fcmocks.MockFlowQueueAccessor{
		LenV:     1,
		FlowKeyV: flowcontrol.FlowKey{ID: "prog-a"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV: enqueueTime,
			OriginalRequestV: &fcmocks.MockFlowControlRequest{
				IDV: "req-123",
			},
		},
	}

	band := &fcmocks.MockPriorityBandAccessor{
		IterateQueuesFunc: func(cb func(flowcontrol.FlowQueueAccessor) bool) {
			cb(queueA)
		},
	}

	queue, err := p.Pick(context.Background(), band)
	assert.NoError(t, err)
	assert.Equal(t, queueA, queue)

	// Verify Pick() stored the enqueue time for the dispatched request.
	storedTimeRaw, ok := p.requestTimestamps.Load("req-123")
	require.True(t, ok, "Pick should store enqueue time for selected request")
	storedTime := storedTimeRaw.(time.Time)
	assert.Equal(t, enqueueTime, storedTime, "stored time should be the item's enqueue time")
}

func TestPick_PrefersHigherAvgWaitTime(t *testing.T) {
	p := &ProgramAwarePlugin{}

	// prog-a has a high EWMA wait time (4000ms), prog-b has a low one (100ms).
	// Both have the same queue length and no prior dispatch history.
	metricsA := &ProgramMetrics{}
	metricsA.RecordWaitTime(4000)
	p.programMetrics.Store("prog-a", metricsA)

	metricsB := &ProgramMetrics{}
	metricsB.RecordWaitTime(100)
	p.programMetrics.Store("prog-b", metricsB)

	now := time.Now()
	queueA := &fcmocks.MockFlowQueueAccessor{
		LenV:     1,
		FlowKeyV: flowcontrol.FlowKey{ID: "prog-a"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV: now,
		},
	}
	queueB := &fcmocks.MockFlowQueueAccessor{
		LenV:     1,
		FlowKeyV: flowcontrol.FlowKey{ID: "prog-b"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV: now,
		},
	}

	band := &fcmocks.MockPriorityBandAccessor{
		IterateQueuesFunc: func(cb func(flowcontrol.FlowQueueAccessor) bool) {
			cb(queueA)
			cb(queueB)
		},
	}

	queue, err := p.Pick(context.Background(), band)
	assert.NoError(t, err)
	assert.Equal(t, queueA, queue, "should prefer the program with higher average wait time")
}

func TestPick_PenalizesHighDispatchCount(t *testing.T) {
	p := &ProgramAwarePlugin{}

	// prog-a has dispatched 500 requests, prog-b has dispatched 0.
	// Give them identical queue state so only the dispatch penalty differs.
	metricsA := &ProgramMetrics{}
	for range 500 {
		metricsA.IncrementDispatched()
	}
	p.programMetrics.Store("prog-a", metricsA)

	now := time.Now()
	queueA := &fcmocks.MockFlowQueueAccessor{
		LenV:     1,
		FlowKeyV: flowcontrol.FlowKey{ID: "prog-a"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV: now.Add(-1 * time.Second),
		},
	}
	queueB := &fcmocks.MockFlowQueueAccessor{
		LenV:     1,
		FlowKeyV: flowcontrol.FlowKey{ID: "prog-b"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV: now.Add(-1 * time.Second),
		},
	}

	band := &fcmocks.MockPriorityBandAccessor{
		IterateQueuesFunc: func(cb func(flowcontrol.FlowQueueAccessor) bool) {
			cb(queueA)
			cb(queueB)
		},
	}

	queue, err := p.Pick(context.Background(), band)
	assert.NoError(t, err)
	assert.Equal(t, queueB, queue, "should prefer the queue with lower dispatch count")
}

// --- PrepareRequestData tests ---

func TestPrepareRequestData_UpdatesMetrics(t *testing.T) {
	p := &ProgramAwarePlugin{}

	request := &scheduling.LLMRequest{
		RequestId: "req-1",
		Headers:   map[string]string{fairnessIDHeader: "prog-a"},
	}

	err := p.PrepareRequestData(context.Background(), request, nil)
	assert.NoError(t, err)

	// Check metrics were created and incremented.
	metricsRaw, ok := p.programMetrics.Load("prog-a")
	require.True(t, ok)
	metrics := metricsRaw.(*ProgramMetrics)
	assert.Equal(t, int64(1), metrics.TotalRequests())

	// PrepareData does NOT store timestamps — Pick() does that.
	_, ok = p.requestTimestamps.Load("req-1")
	assert.False(t, ok)
}

func TestPrepareRequestData_NoFairnessHeader(t *testing.T) {
	p := &ProgramAwarePlugin{}

	request := &scheduling.LLMRequest{
		RequestId: "req-1",
		Headers:   map[string]string{},
	}

	err := p.PrepareRequestData(context.Background(), request, nil)
	assert.NoError(t, err)

	// No metrics should be created.
	_, ok := p.programMetrics.Load("")
	assert.False(t, ok)

	// No timestamp either.
	_, ok = p.requestTimestamps.Load("req-1")
	assert.False(t, ok)
}

// --- PreRequest tests ---

func TestPreRequest_RecordsWaitTime(t *testing.T) {
	p := &ProgramAwarePlugin{}

	// Simulate Pick() having stored the enqueue time 50ms ago.
	p.requestTimestamps.Store("req-1", time.Now().Add(-50*time.Millisecond))
	p.programMetrics.Store("prog-a", &ProgramMetrics{})

	request := &scheduling.LLMRequest{
		RequestId: "req-1",
		Headers:   map[string]string{fairnessIDHeader: "prog-a"},
	}

	p.PreRequest(context.Background(), request, nil)

	metricsRaw, _ := p.programMetrics.Load("prog-a")
	metrics := metricsRaw.(*ProgramMetrics)
	assert.Equal(t, int64(1), metrics.DispatchedCount())
	assert.Greater(t, metrics.AverageWaitTime(), 0.0)
}

// --- ResponseComplete tests ---

func TestResponseComplete_RecordsTokensAndCleanup(t *testing.T) {
	p := &ProgramAwarePlugin{}
	p.programMetrics.Store("prog-a", &ProgramMetrics{})
	p.requestTimestamps.Store("req-1", time.Now())

	request := &scheduling.LLMRequest{
		RequestId: "req-1",
		Headers:   map[string]string{fairnessIDHeader: "prog-a"},
	}
	response := &requestcontrol.Response{
		Usage: requestcontrol.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
		},
	}

	p.ResponseComplete(context.Background(), request, response, &datalayer.EndpointMetadata{})

	metricsRaw, _ := p.programMetrics.Load("prog-a")
	metrics := metricsRaw.(*ProgramMetrics)
	assert.Equal(t, int64(100), metrics.TotalInputTokens())
	assert.Equal(t, int64(50), metrics.TotalOutputTokens())

	// Timestamp should be cleaned up.
	_, ok := p.requestTimestamps.Load("req-1")
	assert.False(t, ok)
}

func TestResponseComplete_NoFairnessHeader_StillCleansTimestamp(t *testing.T) {
	p := &ProgramAwarePlugin{}
	p.requestTimestamps.Store("req-1", time.Now())

	request := &scheduling.LLMRequest{
		RequestId: "req-1",
		Headers:   map[string]string{},
	}

	p.ResponseComplete(context.Background(), request, nil, nil)

	_, ok := p.requestTimestamps.Load("req-1")
	assert.False(t, ok, "timestamp should be cleaned up even without fairness header")
}

// --- normalize tests ---

func TestNormalize(t *testing.T) {
	assert.InDelta(t, 0.0, normalize(0, 100), 0.001)
	assert.InDelta(t, 0.5, normalize(50, 100), 0.001)
	assert.InDelta(t, 1.0, normalize(100, 100), 0.001)
	assert.InDelta(t, 1.0, normalize(200, 100), 0.001, "should clamp to 1")
	assert.InDelta(t, 0.0, normalize(-10, 100), 0.001, "should clamp to 0")
	assert.InDelta(t, 0.0, normalize(50, 0), 0.001, "zero cap returns 0")
}

// --- Produces / Consumes tests ---

func TestProducesConsumes(t *testing.T) {
	p := &ProgramAwarePlugin{}
	assert.Empty(t, p.Produces())
	assert.Empty(t, p.Consumes())
}

// --- Full lifecycle integration test ---

func TestFullLifecycle(t *testing.T) {
	p := &ProgramAwarePlugin{name: "test"}

	programID := "prog-integration"
	request := &scheduling.LLMRequest{
		RequestId: "req-lifecycle",
		Headers:   map[string]string{fairnessIDHeader: programID},
	}

	// 0. Simulate Pick() recording the enqueue time (flow control layer).
	//    In production, this happens when the request is dispatched from the queue.
	enqueueTime := time.Now().Add(-20 * time.Millisecond) // enqueued 20ms ago
	p.requestTimestamps.Store(request.RequestId, enqueueTime)

	// 1. PrepareData (runs after flow control dispatch)
	err := p.PrepareRequestData(context.Background(), request, nil)
	require.NoError(t, err)

	// Verify metrics created.
	metricsRaw, ok := p.programMetrics.Load(programID)
	require.True(t, ok)
	metrics := metricsRaw.(*ProgramMetrics)
	assert.Equal(t, int64(1), metrics.TotalRequests())
	assert.Equal(t, int64(0), metrics.DispatchedCount())

	// 2. PreRequest — computes wait time from enqueue time
	p.PreRequest(context.Background(), request, nil)
	assert.Equal(t, int64(1), metrics.DispatchedCount())
	assert.Greater(t, metrics.AverageWaitTime(), 0.0, "wait time should reflect queue residence time")

	// 3. ResponseComplete
	response := &requestcontrol.Response{Headers: map[string]string{}}
	response.Usage = requestcontrol.Usage{PromptTokens: 42, CompletionTokens: 17}
	p.ResponseComplete(context.Background(), request, response, &datalayer.EndpointMetadata{})
	assert.Equal(t, int64(42), metrics.TotalInputTokens())
	assert.Equal(t, int64(17), metrics.TotalOutputTokens())

	// Verify cleanup.
	_, ok = p.requestTimestamps.Load("req-lifecycle")
	assert.False(t, ok)
}

// --- scoreQueue tests ---

func TestScoreQueue(t *testing.T) {
	p := &ProgramAwarePlugin{}

	// Program with EWMA wait time 2500ms, queue length 50, no dispatch history.
	metrics := &ProgramMetrics{}
	metrics.RecordWaitTime(2500)
	p.programMetrics.Store("test-prog", metrics)

	queue := &fcmocks.MockFlowQueueAccessor{
		LenV:     50,
		FlowKeyV: flowcontrol.FlowKey{ID: "test-prog"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV: time.Now(),
		},
	}

	score := p.scoreQueue(queue)

	// headWaitMs ≈ 0 (EnqueueTimeV: time.Now())
	// Expected: 0.5 * (0/5000) + 0.3 * (2500/5000) - 0.2 * (0/1000)
	//         = 0 + 0.3 * 0.5 - 0
	//         = 0.15
	assert.InDelta(t, 0.15, score, 0.01)

	// Now add dispatch history and verify penalty.
	for range 500 {
		metrics.IncrementDispatched()
	}

	scoreWithDispatch := p.scoreQueue(queue)
	// Penalty: 0.2 * (500/1000) = 0.10 → new score = 0.15 - 0.10 = 0.05
	assert.True(t, scoreWithDispatch < score,
		"score with high dispatch count (%f) should be lower than without (%f)",
		scoreWithDispatch, score)
}
