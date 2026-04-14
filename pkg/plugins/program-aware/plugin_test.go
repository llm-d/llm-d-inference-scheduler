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

func TestProgramMetrics_TotalAverageWaitTime(t *testing.T) {
	m := &ProgramMetrics{}

	assert.Equal(t, 0.0, m.TotalAverageWaitTime(), "no data → 0")

	m.RecordWaitTime(100)
	assert.InDelta(t, 100.0, m.TotalAverageWaitTime(), 0.01)

	m.RecordWaitTime(200)
	// total = 300, count = 2 → 150
	assert.InDelta(t, 150.0, m.TotalAverageWaitTime(), 0.01)

	m.RecordWaitTime(50)
	// total = 350, count = 3 → 116.67
	assert.InDelta(t, 116.67, m.TotalAverageWaitTime(), 0.01)
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
	p.requestTimestamps.Store("req-1", time.Now().Add(-100*time.Millisecond))

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

	// EWMA token cost should be recorded: 100*1 + 50*2 = 200 weighted tokens.
	assert.Greater(t, metrics.AverageTokens(), 0.0, "token usage should be recorded")
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

// --- rangeNormalize tests ---

func TestRangeNormalize(t *testing.T) {
	assert.InDelta(t, 0.0, rangeNormalize(0, 0, 100), 0.001)
	assert.InDelta(t, 0.5, rangeNormalize(50, 0, 100), 0.001)
	assert.InDelta(t, 1.0, rangeNormalize(100, 0, 100), 0.001)
	assert.InDelta(t, 0.5, rangeNormalize(42, 42, 42), 0.001, "min==max returns 0.5")
	assert.InDelta(t, 0.5, rangeNormalize(-10, -20, 0), 0.001, "works with negative range")
	assert.InDelta(t, 0.0, rangeNormalize(-20, -20, 0), 0.001, "min of negative range")
	assert.InDelta(t, 1.0, rangeNormalize(0, -20, 0), 0.001, "max of negative range")
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

// --- fairness index tests (attained-service-based) ---

func TestComputeFairnessIndex_EqualServiceRate(t *testing.T) {
	p := &ProgramAwarePlugin{}

	now := time.Now()
	mA := &ProgramMetrics{}
	mA.RecordServiceRate(1000.0, now)                  // first call sets baseline
	mA.RecordServiceRate(1000.0, now.Add(time.Second)) // rate = 1000 tok/s
	p.programMetrics.Store("prog-a", mA)

	mB := &ProgramMetrics{}
	mB.RecordServiceRate(1000.0, now)
	mB.RecordServiceRate(1000.0, now.Add(time.Second))
	p.programMetrics.Store("prog-b", mB)

	assert.InDelta(t, 1.0, p.computeFairnessIndex(), 0.001, "equal service rate → perfect fairness")
}

func TestComputeFairnessIndex_SkewedServiceRate(t *testing.T) {
	p := &ProgramAwarePlugin{}

	now := time.Now()
	mA := &ProgramMetrics{}
	mA.RecordServiceRate(10000.0, now)
	mA.RecordServiceRate(10000.0, now.Add(time.Second)) // rate = 10000
	p.programMetrics.Store("prog-a", mA)

	mB := &ProgramMetrics{}
	mB.RecordServiceRate(1000.0, now)
	mB.RecordServiceRate(1000.0, now.Add(time.Second)) // rate = 1000
	p.programMetrics.Store("prog-b", mB)

	idx := p.computeFairnessIndex()
	assert.Less(t, idx, 1.0, "skewed rate should produce index < 1")
	// J = (10000+1000)^2 / (2 * (10000^2 + 1000^2)) ≈ 0.599
	assert.InDelta(t, 0.599, idx, 0.01)
}

func TestComputeFairnessIndex_SingleProgram(t *testing.T) {
	p := &ProgramAwarePlugin{}

	now := time.Now()
	m := &ProgramMetrics{}
	m.RecordServiceRate(5000.0, now)
	m.RecordServiceRate(5000.0, now.Add(time.Second))
	p.programMetrics.Store("prog-a", m)

	assert.InDelta(t, 1.0, p.computeFairnessIndex(), 0.001, "single program → trivially fair")
}

func TestComputeFairnessIndex_NoServiceData(t *testing.T) {
	p := &ProgramAwarePlugin{}

	// Programs exist but have no rate data yet.
	p.programMetrics.Store("prog-a", &ProgramMetrics{})
	p.programMetrics.Store("prog-b", &ProgramMetrics{})

	assert.InDelta(t, 1.0, p.computeFairnessIndex(), 0.001, "no service data → 1.0")
}

// --- Two-pass scoring tests ---

func TestPick_AllIdenticalMetrics(t *testing.T) {
	// When all queues have identical metrics, Pick should still return a valid queue.
	p := &ProgramAwarePlugin{}

	now := time.Now()
	queueA := &fcmocks.MockFlowQueueAccessor{
		LenV:     1,
		FlowKeyV: flowcontrol.FlowKey{ID: "prog-a"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV:     now,
			OriginalRequestV: &fcmocks.MockFlowControlRequest{IDV: "a-req"},
		},
	}
	queueB := &fcmocks.MockFlowQueueAccessor{
		LenV:     1,
		FlowKeyV: flowcontrol.FlowKey{ID: "prog-b"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV:     now,
			OriginalRequestV: &fcmocks.MockFlowControlRequest{IDV: "b-req"},
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
	assert.NotNil(t, queue, "should select a queue even when all metrics are identical")
}
