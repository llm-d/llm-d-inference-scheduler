package programaware

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"time"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/flowcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	requestcontrol "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
)

const (
	// ProgramAwarePluginType is the registered type name for this plugin.
	ProgramAwarePluginType = "program-aware-fairness"

	// fairnessIDHeader is the standard header used to identify the program.
	fairnessIDHeader = "x-gateway-inference-fairness-id"

	// Default scoring weights for Pick().
	defaultWeightHeadWait       = 0.4
	defaultWeightQueueLength    = 0.3
	defaultWeightTotalDispatched = 0.3

	// Normalization caps for scoring.
	capHeadWaitMs       = 5000.0
	capQueueLength      = 100.0
	capTotalDispatched  = 1000.0
)

// Compile-time interface assertions.
var (
	_ flowcontrol.FairnessPolicy       = &ProgramAwarePlugin{}
	_ requestcontrol.PrepareDataPlugin = &ProgramAwarePlugin{}
	_ requestcontrol.PreRequest        = &ProgramAwarePlugin{}
	_ requestcontrol.ResponseReceived  = &ProgramAwarePlugin{}
	_ requestcontrol.ResponseComplete  = &ProgramAwarePlugin{}
)

// ProgramAwarePluginFactory creates a new ProgramAwarePlugin instance.
func ProgramAwarePluginFactory(name string, _ json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	return &ProgramAwarePlugin{
		name: name,
	}, nil
}

// ProgramAwarePlugin implements a FairnessPolicy that selects which program's
// queue to service next based on accumulated per-program metrics, and request
// lifecycle hooks that track those metrics.
//
// Program identity comes from the standard x-gateway-inference-fairness-id header.
type ProgramAwarePlugin struct {
	name string

	// programMetrics stores aggregated metrics per program. Key: program ID (string), Value: *ProgramMetrics.
	programMetrics sync.Map

	// requestTimestamps tracks when PrepareData ran for each request, used to compute wait time in PreRequest.
	// Key: request ID (string), Value: time.Time.
	requestTimestamps sync.Map
}

// TypedName returns the plugin type and instance name.
func (p *ProgramAwarePlugin) TypedName() plugin.TypedName {
	return plugin.TypedName{
		Type: ProgramAwarePluginType,
		Name: p.name,
	}
}

// --- FairnessPolicy interface ---

// NewState creates per-PriorityBand state. This plugin uses its own shared sync.Map
// for metrics, so no per-band state is needed.
func (p *ProgramAwarePlugin) NewState(_ context.Context) any {
	return nil
}

// Pick selects which program queue to service next based on a scoring function
// that considers live queue data and accumulated program metrics.
func (p *ProgramAwarePlugin) Pick(_ context.Context, band flowcontrol.PriorityBandAccessor) (flowcontrol.FlowQueueAccessor, error) {
	if band == nil {
		return nil, nil
	}

	var bestQueue flowcontrol.FlowQueueAccessor
	bestScore := -1.0

	band.IterateQueues(func(queue flowcontrol.FlowQueueAccessor) (keepIterating bool) {
		if queue == nil || queue.Len() == 0 {
			return true
		}

		score := p.scoreQueue(queue)
		if score > bestScore {
			bestScore = score
			bestQueue = queue
		}
		return true
	})

	// Record the selected item's enqueue time so PreRequest can compute
	// the actual flow control queue wait time (enqueue → dispatch).
	if bestQueue != nil {
		if head := bestQueue.PeekHead(); head != nil {
			reqID := head.OriginalRequest().ID()
			p.requestTimestamps.Store(reqID, head.EnqueueTime())
		}
	}

	return bestQueue, nil
}

// scoreQueue computes a priority score for a program's queue.
// Higher scores mean the queue should be serviced sooner.
func (p *ProgramAwarePlugin) scoreQueue(queue flowcontrol.FlowQueueAccessor) float64 {
	programID := queue.FlowKey().ID

	// Live signal: how long has the head item been waiting?
	headWaitMs := 0.0
	if head := queue.PeekHead(); head != nil {
		headWaitMs = float64(time.Since(head.EnqueueTime()).Milliseconds())
	}

	// Live signal: queue depth.
	queueLen := float64(queue.Len())

	// Accumulated signal: total dispatched requests for this program.
	totalDispatched := 0.0
	if metricsRaw, ok := p.programMetrics.Load(programID); ok {
		metrics := metricsRaw.(*ProgramMetrics)
		totalDispatched = float64(metrics.DispatchedCount())
	}

	return defaultWeightHeadWait*normalize(headWaitMs, capHeadWaitMs) +
		defaultWeightQueueLength*normalize(queueLen, capQueueLength) -
		defaultWeightTotalDispatched*normalize(totalDispatched, capTotalDispatched)
}

// getOrCreateMetrics returns the ProgramMetrics for the given program ID, creating it if needed.
func (p *ProgramAwarePlugin) getOrCreateMetrics(programID string) *ProgramMetrics {
	if metricsRaw, ok := p.programMetrics.Load(programID); ok {
		return metricsRaw.(*ProgramMetrics)
	}
	m := &ProgramMetrics{}
	actual, _ := p.programMetrics.LoadOrStore(programID, m)
	return actual.(*ProgramMetrics)
}

// normalize clamps v/cap to [0, 1].
func normalize(v, cap float64) float64 {
	if cap <= 0 {
		return 0
	}
	return math.Min(math.Max(v/cap, 0), 1)
}
