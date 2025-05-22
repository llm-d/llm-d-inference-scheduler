// Package dual provides a sample Scheduler that internally uses
// a dual scheduler construct (primary and secondary).
package dual

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/picker"
	k8sscorer "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/scorer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/plugins/filter"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/plugins/scorer"
)

// Scheduler implements the dual scheduler concept, along with a threshold
// determining when each is invoked.
type Scheduler struct {
	threshold float32
	store     datastore.Datastore
	primary   requestcontrol.Scheduler
	secondary requestcontrol.Scheduler
}

// NewScheduler create a new scheduler with the given datastore and threshold
func NewScheduler(threshold float32, datastore datastore.Datastore) *Scheduler {
	scheduler := &Scheduler{
		threshold: threshold,
		store:     datastore,
	}

	scheduler.primary = scheduling.NewSchedulerWithConfig(datastore, scheduling.NewSchedulerConfig().
		WithFilters(&filter.Passthrough{}).
		WithScorers(k8sscorer.NewWeightedScorer(&scorer.Passthrough{}, 10)).
		WithPicker(picker.NewMaxScorePicker()))

	scheduler.secondary = scheduling.NewSchedulerWithConfig(datastore, scheduling.NewSchedulerConfig().
		WithFilters(&filter.Random{}).
		WithScorers(k8sscorer.NewWeightedScorer(&scorer.Random{}, 10)).
		WithPicker(picker.NewRandomPicker()))

	return scheduler
}

// Schedule selects a Pod for the given request and context
func (s *Scheduler) Schedule(ctx context.Context, req *types.LLMRequest) (*types.Result, error) {
	logger := log.FromContext(ctx).WithName("PD-scheduler").WithValues("request", req)
	debugLog := logger.V(logutil.DEBUG)

	scheduleStart := time.Now()
	defer func() {
		metrics.RecordSchedulerE2ELatency(time.Since(scheduleStart))
	}()

	if rand.Float32() > s.threshold { // choose a primary only
		return s.primary.Schedule(ctx, req)
	}

	primary, err := s.primary.Schedule(ctx, req)
	if err != nil {
		return nil, err
	}
	debugLog.Info(fmt.Sprintf("Primary scheduler selected %+v", primary))

	// TODO: this is demo behavior we need to replace once we know what we want.
	if rand.Float32() < s.threshold { // choose a secondary as well
		secondary, err := s.secondary.Schedule(ctx, req)
		if err != nil {
			debugLog.Info(fmt.Sprintf("Secondary scheduler failed %+v, returning primary", err))
		}
		debugLog.Info(fmt.Sprintf("Secondary scheduler selected %+v", secondary))
		if rand.Float32() < s.threshold { // lucky again: return the secondary
			return secondary, nil
		}
	}
	return primary, nil
}
