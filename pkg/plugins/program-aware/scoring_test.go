package programaware

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol"
	fcmocks "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol/mocks"
)

// --- EWMA Convergence ---

func TestEWMA_ConvergesToSteadyState(t *testing.T) {
	m := &ProgramMetrics{}

	// Feed 50 identical observations of 200ms.
	// EWMA with alpha=0.2 should converge to 200.
	for i := range 50 {
		m.RecordWaitTime(200)
		if i > 30 {
			assert.InDelta(t, 200.0, m.AverageWaitTime(), 1.0,
				"EWMA should converge to steady state after many identical observations")
		}
	}
}

func TestEWMA_DecaysAfterRecovery(t *testing.T) {
	m := &ProgramMetrics{}

	// Phase 1: 10 observations of high wait time (1000ms).
	for range 10 {
		m.RecordWaitTime(1000)
	}
	highPhaseAvg := m.AverageWaitTime()
	assert.InDelta(t, 1000.0, highPhaseAvg, 5.0, "should converge near 1000 after 10 observations")

	// Phase 2: 10 observations of low wait time (10ms).
	// EWMA should decay toward 10, but slowly due to alpha=0.2.
	for range 10 {
		m.RecordWaitTime(10)
	}
	lowPhaseAvg := m.AverageWaitTime()
	assert.Less(t, lowPhaseAvg, highPhaseAvg, "EWMA should drop after low wait observations")
	assert.Greater(t, lowPhaseAvg, 10.0, "EWMA should not immediately reach 10 due to smoothing")

	// Phase 3: 40 more low observations — should be very close to 10.
	for range 40 {
		m.RecordWaitTime(10)
	}
	assert.InDelta(t, 10.0, m.AverageWaitTime(), 5.0, "should converge near 10 after many low observations")
}

func TestEWMA_TracksStepChange(t *testing.T) {
	m := &ProgramMetrics{}

	// Start at 100ms, then jump to 500ms.
	for range 20 {
		m.RecordWaitTime(100)
	}
	assert.InDelta(t, 100.0, m.AverageWaitTime(), 2.0)

	// After 5 observations at 500ms, EWMA should be noticeably higher than 100
	// but not yet at 500 (smoothing effect).
	for range 5 {
		m.RecordWaitTime(500)
	}
	avg := m.AverageWaitTime()
	assert.Greater(t, avg, 150.0, "EWMA should respond to step increase")
	assert.Less(t, avg, 500.0, "EWMA should lag behind the new value due to smoothing")
}

// --- Score Calculation Matrix ---

func TestScoreQueue_Matrix(t *testing.T) {
	// score = 0.4 * normalize(avgWait, 5000) + 0.3 * normalize(queueLen, 100) - 0.3 * normalize(dispatched, 1000)
	tests := []struct {
		name       string
		avgWaitMs  float64
		queueLen   int
		dispatched int
		wantScore  float64
	}{
		{
			name:       "zero everything",
			avgWaitMs:  0, queueLen: 0, dispatched: 0,
			wantScore:  0.0,
		},
		{
			name:       "max wait only",
			avgWaitMs:  5000, queueLen: 0, dispatched: 0,
			wantScore:  0.4, // 0.4*1 + 0.3*0 - 0.3*0
		},
		{
			name:       "max queue only",
			avgWaitMs:  0, queueLen: 100, dispatched: 0,
			wantScore:  0.3, // 0.4*0 + 0.3*1 - 0.3*0
		},
		{
			name:       "max dispatch penalty only",
			avgWaitMs:  0, queueLen: 0, dispatched: 1000,
			wantScore:  -0.3, // 0.4*0 + 0.3*0 - 0.3*1
		},
		{
			name:       "all at 50%",
			avgWaitMs:  2500, queueLen: 50, dispatched: 500,
			wantScore:  0.2, // 0.4*0.5 + 0.3*0.5 - 0.3*0.5 = 0.2 + 0.15 - 0.15
		},
		{
			name:       "high wait high dispatch — wait dominates",
			avgWaitMs:  5000, queueLen: 0, dispatched: 1000,
			wantScore:  0.1, // 0.4*1 + 0 - 0.3*1 = 0.1
		},
		{
			name:       "saturation — all signals capped",
			avgWaitMs:  10000, queueLen: 200, dispatched: 2000,
			wantScore:  0.4, // 0.4*1 + 0.3*1 - 0.3*1 = 0.4
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &ProgramAwarePlugin{}

			metrics := &ProgramMetrics{}
			// Set EWMA to exact value by recording once (first observation = the value).
			if tt.avgWaitMs > 0 {
				metrics.RecordWaitTime(tt.avgWaitMs)
			}
			for range tt.dispatched {
				metrics.IncrementDispatched()
			}
			p.programMetrics.Store("test-prog", metrics)

			queue := &fcmocks.MockFlowQueueAccessor{
				LenV:     tt.queueLen,
				FlowKeyV: flowcontrol.FlowKey{ID: "test-prog"},
				PeekHeadV: &fcmocks.MockQueueItemAccessor{
					EnqueueTimeV: time.Now(),
				},
			}

			score := p.scoreQueue(queue)
			assert.InDelta(t, tt.wantScore, score, 0.01, "score mismatch for case: %s", tt.name)
		})
	}
}

// --- Multi-Round Pick Simulation ---

func TestPick_MultiRoundFairness(t *testing.T) {
	p := &ProgramAwarePlugin{}

	// Two programs: alpha (high wait history) and beta (low wait history).
	// Over multiple rounds, we simulate Pick() and feed wait times back.
	metricsAlpha := &ProgramMetrics{}
	metricsAlpha.RecordWaitTime(500) // alpha has experienced high wait
	p.programMetrics.Store("alpha", metricsAlpha)

	metricsBeta := &ProgramMetrics{}
	metricsBeta.RecordWaitTime(10) // beta has had fast service
	p.programMetrics.Store("beta", metricsBeta)

	pickCounts := map[string]int{"alpha": 0, "beta": 0}

	for round := range 20 {
		now := time.Now()
		queueAlpha := &fcmocks.MockFlowQueueAccessor{
			LenV:     5,
			FlowKeyV: flowcontrol.FlowKey{ID: "alpha"},
			PeekHeadV: &fcmocks.MockQueueItemAccessor{
				EnqueueTimeV: now,
			},
		}
		queueBeta := &fcmocks.MockFlowQueueAccessor{
			LenV:     5,
			FlowKeyV: flowcontrol.FlowKey{ID: "beta"},
			PeekHeadV: &fcmocks.MockQueueItemAccessor{
				EnqueueTimeV: now,
			},
		}

		band := &fcmocks.MockPriorityBandAccessor{
			IterateQueuesFunc: func(cb func(flowcontrol.FlowQueueAccessor) bool) {
				cb(queueAlpha)
				cb(queueBeta)
			},
		}

		queue, err := p.Pick(context.Background(), band)
		assert.NoError(t, err)

		winner := queue.FlowKey().ID
		pickCounts[winner]++

		// Simulate: the picked program gets served (low wait next time),
		// the other program's wait increases.
		if winner == "alpha" {
			metricsAlpha.RecordWaitTime(5)  // fast service
			metricsAlpha.IncrementDispatched()
			metricsBeta.RecordWaitTime(metricsBeta.AverageWaitTime() + 50) // waiting longer
		} else {
			metricsBeta.RecordWaitTime(5)
			metricsBeta.IncrementDispatched()
			metricsAlpha.RecordWaitTime(metricsAlpha.AverageWaitTime() + 50)
		}

		scoreAlpha := p.scoreQueue(queueAlpha)
		scoreBeta := p.scoreQueue(queueBeta)
		t.Logf("Round %2d: winner=%-5s  alpha[score=%.4f avgWait=%.1f dispatched=%d]  beta[score=%.4f avgWait=%.1f dispatched=%d]",
			round, winner,
			scoreAlpha, metricsAlpha.AverageWaitTime(), metricsAlpha.DispatchedCount(),
			scoreBeta, metricsBeta.AverageWaitTime(), metricsBeta.DispatchedCount())
	}

	// Alpha starts with high wait, so it should be picked first.
	// Over time, as alpha gets served and beta's wait rises, picks should alternate.
	t.Logf("Final pick counts: alpha=%d, beta=%d", pickCounts["alpha"], pickCounts["beta"])
	assert.Greater(t, pickCounts["alpha"], 0, "alpha should be picked at least once")
	assert.Greater(t, pickCounts["beta"], 0, "beta should be picked at least once")
}

// --- New Program vs Established ---

func TestPick_NewProgramVsEstablished(t *testing.T) {
	p := &ProgramAwarePlugin{}

	// Established program: has been running, accumulated 200 dispatches, moderate EWMA.
	established := &ProgramMetrics{}
	established.RecordWaitTime(100) // moderate wait
	for range 200 {
		established.IncrementDispatched()
	}
	p.programMetrics.Store("established", established)

	// New program: no metrics at all (first request).
	// scoreQueue will find no metrics → avgWait=0, dispatched=0.

	now := time.Now()
	queueEstablished := &fcmocks.MockFlowQueueAccessor{
		LenV:     3,
		FlowKeyV: flowcontrol.FlowKey{ID: "established"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV: now,
		},
	}
	queueNew := &fcmocks.MockFlowQueueAccessor{
		LenV:     3,
		FlowKeyV: flowcontrol.FlowKey{ID: "new-prog"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV: now,
		},
	}

	band := &fcmocks.MockPriorityBandAccessor{
		IterateQueuesFunc: func(cb func(flowcontrol.FlowQueueAccessor) bool) {
			cb(queueEstablished)
			cb(queueNew)
		},
	}

	queue, err := p.Pick(context.Background(), band)
	assert.NoError(t, err)

	// Established program has:
	//   0.4 * (100/5000) + 0.3 * (3/100) - 0.3 * (200/1000)
	//   = 0.4*0.02 + 0.3*0.03 - 0.3*0.2
	//   = 0.008 + 0.009 - 0.06
	//   = -0.043
	//
	// New program has:
	//   0.4 * (0/5000) + 0.3 * (3/100) - 0.3 * (0/1000)
	//   = 0 + 0.009 - 0
	//   = 0.009
	//
	// New program wins because established has dispatch penalty.
	assert.Equal(t, "new-prog", queue.FlowKey().ID,
		"new program should be preferred over established program with high dispatch count")

	scoreEstablished := p.scoreQueue(queueEstablished)
	scoreNew := p.scoreQueue(queueNew)
	t.Logf("Established score: %.4f, New score: %.4f", scoreEstablished, scoreNew)
}

// --- Starvation Prevention ---

func TestPick_StarvationPrevention(t *testing.T) {
	p := &ProgramAwarePlugin{}

	// Scenario: program "hog" has been heavily dispatched (800 requests).
	// Program "starved" has only 10 dispatches but high accumulated wait time.
	hog := &ProgramMetrics{}
	hog.RecordWaitTime(10) // low wait — it gets served fast
	for range 800 {
		hog.IncrementDispatched()
	}
	p.programMetrics.Store("hog", hog)

	starved := &ProgramMetrics{}
	starved.RecordWaitTime(3000) // high wait — it's been waiting
	for range 10 {
		starved.IncrementDispatched()
	}
	p.programMetrics.Store("starved", starved)

	now := time.Now()
	queueHog := &fcmocks.MockFlowQueueAccessor{
		LenV:     5,
		FlowKeyV: flowcontrol.FlowKey{ID: "hog"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV: now,
		},
	}
	queueStarved := &fcmocks.MockFlowQueueAccessor{
		LenV:     5,
		FlowKeyV: flowcontrol.FlowKey{ID: "starved"},
		PeekHeadV: &fcmocks.MockQueueItemAccessor{
			EnqueueTimeV: now,
		},
	}

	band := &fcmocks.MockPriorityBandAccessor{
		IterateQueuesFunc: func(cb func(flowcontrol.FlowQueueAccessor) bool) {
			cb(queueHog)
			cb(queueStarved)
		},
	}

	queue, err := p.Pick(context.Background(), band)
	assert.NoError(t, err)

	// Hog:    0.4*(10/5000) + 0.3*(5/100) - 0.3*(800/1000) = 0.0008 + 0.015 - 0.24 = -0.2242
	// Starved: 0.4*(3000/5000) + 0.3*(5/100) - 0.3*(10/1000) = 0.24 + 0.015 - 0.003 = 0.252
	scoreHog := p.scoreQueue(queueHog)
	scoreStarved := p.scoreQueue(queueStarved)
	t.Logf("Hog score: %.4f, Starved score: %.4f", scoreHog, scoreStarved)

	assert.Equal(t, "starved", queue.FlowKey().ID,
		"starved program must be picked over the hog")
	assert.Greater(t, scoreStarved, scoreHog,
		"starved score (%.4f) should be much higher than hog score (%.4f)",
		scoreStarved, scoreHog)
}

// --- Three-Program Fairness ---

func TestPick_ThreeProgramRotation(t *testing.T) {
	p := &ProgramAwarePlugin{}

	programs := []string{"A", "B", "C"}
	metricsMap := map[string]*ProgramMetrics{}
	for _, prog := range programs {
		metricsMap[prog] = &ProgramMetrics{}
		p.programMetrics.Store(prog, metricsMap[prog])
	}

	// Give each program different initial wait times.
	metricsMap["A"].RecordWaitTime(300)
	metricsMap["B"].RecordWaitTime(200)
	metricsMap["C"].RecordWaitTime(100)

	pickCounts := map[string]int{}
	for round := range 30 {
		now := time.Now()
		queues := make([]*fcmocks.MockFlowQueueAccessor, len(programs))
		for i, prog := range programs {
			queues[i] = &fcmocks.MockFlowQueueAccessor{
				LenV:     3,
				FlowKeyV: flowcontrol.FlowKey{ID: prog},
				PeekHeadV: &fcmocks.MockQueueItemAccessor{
					EnqueueTimeV: now,
				},
			}
		}

		band := &fcmocks.MockPriorityBandAccessor{
			IterateQueuesFunc: func(cb func(flowcontrol.FlowQueueAccessor) bool) {
				for _, q := range queues {
					if !cb(q) {
						return
					}
				}
			},
		}

		queue, err := p.Pick(context.Background(), band)
		assert.NoError(t, err)
		winner := queue.FlowKey().ID
		pickCounts[winner]++

		// Served program gets low wait, others accumulate wait.
		for _, prog := range programs {
			if prog == winner {
				metricsMap[prog].RecordWaitTime(5)
				metricsMap[prog].IncrementDispatched()
			} else {
				metricsMap[prog].RecordWaitTime(metricsMap[prog].AverageWaitTime() + 30)
			}
		}

		t.Logf("Round %2d: winner=%s  A[score=%.4f avgWait=%.1f disp=%d]  B[score=%.4f avgWait=%.1f disp=%d]  C[score=%.4f avgWait=%.1f disp=%d]",
			round, winner,
			p.scoreQueue(queues[0]), metricsMap["A"].AverageWaitTime(), metricsMap["A"].DispatchedCount(),
			p.scoreQueue(queues[1]), metricsMap["B"].AverageWaitTime(), metricsMap["B"].DispatchedCount(),
			p.scoreQueue(queues[2]), metricsMap["C"].AverageWaitTime(), metricsMap["C"].DispatchedCount())
	}

	t.Logf("Pick counts over 30 rounds: %v", pickCounts)

	// All three programs should get served — no starvation.
	for _, prog := range programs {
		assert.Greater(t, pickCounts[prog], 0,
			fmt.Sprintf("program %s should be picked at least once in 30 rounds", prog))
	}

	// With equal queue lengths and no initial dispatch history differences,
	// picks should be roughly balanced (not perfect, due to EWMA smoothing).
	for _, prog := range programs {
		assert.Greater(t, pickCounts[prog], 5,
			fmt.Sprintf("program %s should get a fair share (>5 of 30 rounds)", prog))
	}
}
