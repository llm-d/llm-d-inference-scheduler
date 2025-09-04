// Package profile provides profile handler plugin for the epp.
package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	// PdProfileHandlerType is the type of the PdProfileHandler
	PdProfileHandlerType = "pd-profile-handler"

	defaultDecodeProfile    = "decode"
	defaultPrefillProfile   = "prefill"
	defaultPrefixPluginName = prefix.PrefixCachePluginType
)

type pdProfileHandlerParameters struct {
	Threshold        int    `json:"threshold"`
	DecodeProfile    string `json:"decodeProfile"`
	PrefillProfile   string `json:"prefillProfile"`
	PrefixPluginName string `json:"prefixPluginName"`
	HashBlockSize    int    `json:"hashBlockSize"`
}

// compile-time type assertion
var _ framework.ProfileHandler = &PdProfileHandler{}

// PdProfileHandlerFactory defines the factory function for the PdProfileHandler
func PdProfileHandlerFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	parameters := pdProfileHandlerParameters{
		Threshold:        0,
		DecodeProfile:    defaultDecodeProfile,
		PrefillProfile:   defaultPrefillProfile,
		PrefixPluginName: defaultPrefixPluginName,
		HashBlockSize:    prefix.DefaultHashBlockSize,
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' profile handler - %w", PdProfileHandlerType, err)
		}
	}

	return NewPdProfileHandler(parameters.PrefillProfile, parameters.DecodeProfile, parameters.PrefixPluginName,
		parameters.Threshold, parameters.HashBlockSize).WithName(name), nil
}

// NewPdProfileHandler initializes a new PdProfileHandler and returns its pointer.
func NewPdProfileHandler(prefillProfile string, decodeProfile string, prefixPluginName string, pdThreshold int, hashBlockSize int) *PdProfileHandler {
	return &PdProfileHandler{
		typedName:             plugins.TypedName{Type: PdProfileHandlerType},
		prefixPluginTypedName: plugins.TypedName{Type: prefix.PrefixCachePluginType, Name: prefixPluginName},
		decodeProfile:         decodeProfile,
		prefillProfile:        prefillProfile,
		pdThreshold:           pdThreshold,
		hashBlockSize:         hashBlockSize,
	}
}

// PdProfileHandler handles scheduler profiles for PD.
type PdProfileHandler struct {
	typedName             plugins.TypedName
	prefixPluginTypedName plugins.TypedName
	decodeProfile         string
	prefillProfile        string
	pdThreshold           int
	hashBlockSize         int
}

// TypedName returns the typed name of the plugin.
func (h *PdProfileHandler) TypedName() plugins.TypedName {
	return h.typedName
}

// WithName sets the name of the plugin.
func (h *PdProfileHandler) WithName(name string) *PdProfileHandler {
	h.typedName.Name = name
	return h
}

// Pick selects the SchedulingProfiles to run from the list of candidate profiles, while taking into consideration the request properties and the
// previously executed cycles along with their results.
func (h *PdProfileHandler) Pick(ctx context.Context, cycleState *types.CycleState, request *types.LLMRequest, profiles map[string]*framework.SchedulerProfile,
	profileResults map[string]*types.ProfileRunResult) map[string]*framework.SchedulerProfile {
	if _, executed := profileResults[h.decodeProfile]; !executed {
		// if decode profile was not executed yet, first let the scheduler run the decode profile
		return map[string]*framework.SchedulerProfile{
			h.decodeProfile: profiles[h.decodeProfile],
		}
	}
	// otherwise, decode was already executed.

	// when a profile run fails its result value is nil. we need to check decode result before continuing to prefill
	// check if all configured profiles have been executed, or if decode failed, no need to run more profiles.
	if len(profiles) == len(profileResults) || profileResults[h.decodeProfile] == nil {
		return map[string]*framework.SchedulerProfile{}
	}

	if h.pdThreshold > 0 {
		// if we're here that means decode profile ran successfully, and we have additional profile configured that didn't run yet,
		// which means PD is enabled (otherwise, prefill profile is not configured at all and this profile handler is not used).
		// inspect decode execution result to decide if prefill should run or not.
		// if the request is short enough, use decode results only and don't run the prefill profile.
		hitPercentagePrefix := 0.0 // default to 0, meaning no prefix cache hit
		prefixState, err := types.ReadCycleStateKey[*prefix.SchedulingContextState](cycleState, plugins.StateKey(h.prefixPluginTypedName.String()))
		if err != nil {
			log.FromContext(ctx).Error(err, "unable to read prefix state")
		} else {
			decodePod := profileResults[h.decodeProfile].TargetPods[0].GetPod().NamespacedName
			hitPrefix := max(prefixState.PrefixCacheServers[prefix.ServerID(decodePod)]-1, 0) // The first hit is always the model name
			hitPercentagePrefix = float64(hitPrefix*h.hashBlockSize) / float64(len(request.Prompt))
			log.FromContext(ctx).V(logutil.DEBUG).Info("Computed hit percentage for prefix cache", "hitPercentage", hitPercentagePrefix,
				"promptLength", len(request.Prompt))
		}

		if (1.0-hitPercentagePrefix)*float64(len(request.Prompt)) < float64(h.pdThreshold) {
			log.FromContext(ctx).Info("Non-cached suffix is smaller than threshold, using decode profile only", "hitPercentage", hitPercentagePrefix)
			return map[string]*framework.SchedulerProfile{} // do not run prefill
		}
	}

	// run the prefill profile
	return map[string]*framework.SchedulerProfile{
		h.prefillProfile: profiles[h.prefillProfile],
	}
}

// ProcessResults handles the outcome of the profile runs after the selected profiles ran.
// In case of an error in any of the profiles, the matching entry in the profileResults will contain nil, to indicate there was
// an error while running the profile.
func (h *PdProfileHandler) ProcessResults(_ context.Context, _ *types.CycleState, _ *types.LLMRequest,
	profileResults map[string]*types.ProfileRunResult) (*types.SchedulingResult, error) {
	if profileResults[h.decodeProfile] == nil { // if decode profile failed to run, we should fail
		return nil, errors.New("failed to find available decode workers")
	}
	// otherwise, decode ran successfully

	// if both prefill and decode ran successfully
	if prefillRunResult, exists := profileResults[h.prefillProfile]; exists && prefillRunResult != nil {
		return &types.SchedulingResult{
			PrimaryProfileName: h.decodeProfile,
			ProfileResults:     profileResults,
		}, nil
	}

	// otherwise, decode ran successfully and prefill failed. filter out prefill from the returned results.
	return &types.SchedulingResult{
		PrimaryProfileName: h.decodeProfile,
		ProfileResults: map[string]*types.ProfileRunResult{
			h.decodeProfile: profileResults[h.decodeProfile], // return decode only
		},
	}, nil
}
