// Package profile provides profile handler plugin for the epp.
package profile

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/datastore"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

const (
	name    = "pd-profile-handler"
	decode  = "decode"
	prefill = "prefill"

	prefillPodHeader = "x-prefiller-url" // PrefillPodHeader is the HTTP header name used to indicate Prefill worker
)

// compile-time type assertion
var _ framework.ProfileHandler = &PdProfileHandler{}

// NewPdProfileHandler initializes a new PdProfileHandler and returns its pointer.
func NewPdProfileHandler(pdThreshold int, prefixScorer *scorer.PrefixAwareScorer, datastore datastore.Datastore) *PdProfileHandler {
	return &PdProfileHandler{
		pdThreshold:  pdThreshold,
		prefixScorer: prefixScorer,
		datastore:    datastore,
	}
}

// PdProfileHandler handles scheduler profiles for PD.
type PdProfileHandler struct {
	pdThreshold  int
	prefixScorer *scorer.PrefixAwareScorer
	datastore    datastore.Datastore
}

// Name returns the name of the Profiles Picker.
func (h *PdProfileHandler) Name() string {
	return name
}

// Pick selects the SchedulingProfiles to run from the list of candidate profiles, while taking into consideration the request properties and the
// previously executed cycles along with their results.
func (h *PdProfileHandler) Pick(ctx context.Context, request *types.LLMRequest, profiles map[string]*framework.SchedulerProfile,
	profileResults map[string]*types.ProfileRunResult) map[string]*framework.SchedulerProfile {
	if _, executed := profileResults[decode]; !executed {
		// if decode profile was not executed yet, first let the scheduler run the decode profile
		return map[string]*framework.SchedulerProfile{
			decode: profiles[decode],
		}
	}
	// otherwise, decode was already executed.

	if len(profiles) == len(profileResults) { // if all configured profiles have been executed, we're done running profiles.
		return map[string]*framework.SchedulerProfile{}
	}

	// if we're here that means decode profile already ran, and we have additional profile configured that didn't run yet,
	// which means PD is enabled (otherwise, prefil profile is not configured at all).

	// inspect decode execution result to decide if prefil should run or not.
	// if the request is short enough, use decode results only and don't run the prefill profile.
	hitPercentage := h.prefixScorer.GetCachedPercentage(profileResults[decode].TargetPod.GetPod().NamespacedName.String(), request.Prompt)
	if (1.0-hitPercentage)*float64(len(request.Prompt)) < float64(h.pdThreshold) {
		log.FromContext(ctx).Info("Non-cached suffix is smaller than threshold, using decode profile only", "hitPercentage", hitPercentage)
		return map[string]*framework.SchedulerProfile{} // do not run prefill
	}

	// run the prefill profile
	return map[string]*framework.SchedulerProfile{
		prefill: profiles[prefill],
	}
}

// ProcessResults handles the outcome of the profile runs after both prefill and decode profiles ran succuessfully.
// In case of an error in one of them, ProcessResults will not be called by the scheduler and an appropriate error
// is returned by the scheduler.
func (h *PdProfileHandler) ProcessResults(_ context.Context, request *types.LLMRequest, profileResults map[string]*types.ProfileRunResult) *types.SchedulingResult {
	if pool, err := h.datastore.PoolGet(); err == nil {
		// TODO: should the scheme be conifgurable (e.g., https://)?
		prefillURL := fmt.Sprintf("http://%s:%d", profileResults[prefill].TargetPod.GetPod().Address, pool.Spec.TargetPortNumber)
		if request.Headers == nil { // TODO should always be populated?
			request.Headers = make(map[string]string)
		}
		request.Headers[prefillPodHeader] = prefillURL
	}

	// we don't return prefill result to the caller. its result is added in a header instead.
	// TODO should be changed once PreRequest plugin is added in request control (should move there)
	return &types.SchedulingResult{
		PrimaryProfileName: decode,
		ProfileResults: map[string]*types.ProfileRunResult{
			decode: profileResults[decode],
		},
	}
}
