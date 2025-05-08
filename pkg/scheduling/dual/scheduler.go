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
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/picker"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/config"
	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/scheduling/plugins/filter"
	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/scheduling/plugins/scorer"
)

// Scheduler implements the dual scheduler concept, along with a threshold
// determining when each is invoked.
type Scheduler struct {
	threshold int
	store     datastore.Datastore
	primary   requestcontrol.Scheduler
	secondary requestcontrol.Scheduler
}

// NewScheduler create a new scheduler with the given datastore and threshold
func NewScheduler(schedCfg *config.Config, datastore datastore.Datastore) *Scheduler {
	scheduler := &Scheduler{
		threshold: schedCfg.PDThreshold,
		store:     datastore,
	}

	scheduler.primary = scheduling.NewSchedulerWithConfig(datastore, scheduling.NewSchedulerConfig(
		[]plugins.PreSchedule{},
		[]plugins.Filter{
			&filter.Passthrough{},
		},
		map[plugins.Scorer]int{
			&scorer.Passthrough{}: 10,
		},
		&picker.MaxScorePicker{},
		[]plugins.PostSchedule{},
	))
	scheduler.secondary = scheduling.NewSchedulerWithConfig(datastore, scheduling.NewSchedulerConfig(
		[]plugins.PreSchedule{},
		[]plugins.Filter{
			&filter.Random{},
		},
		map[plugins.Scorer]int{
			&scorer.Random{}: 10,
		},
		&picker.RandomPicker{},
		[]plugins.PostSchedule{},
	))

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

	if rand.Int() > s.threshold { // choose a primary only
		return s.primary.Schedule(ctx, req)
	}

	primary, err := s.primary.Schedule(ctx, req)
	if err != nil {
		return nil, err
	}
	debugLog.Info(fmt.Sprintf("Primary scheduler selected %+v", primary))

	// TODO: this is demo behavior we need to replace once we know what we want.
	if rand.Int() < s.threshold { // choose a secondary as well
		secondary, err := s.secondary.Schedule(ctx, req)
		if err != nil {
			debugLog.Info(fmt.Sprintf("Secondary scheduler failed %+v, returning primary", err))
		}
		debugLog.Info(fmt.Sprintf("Secondary scheduler selected %+v", secondary))
		if rand.Int() < s.threshold { // lucky again: return the secondary
			return secondary, nil
		}
	}
	return primary, nil
}
