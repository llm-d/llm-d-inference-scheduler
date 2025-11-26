// Package profile provides profile handler plugin for the epp.
package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/metrics"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
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
	PrimaryPort      int    `json:"primaryPort"`
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
		HashBlockSize:    prefix.DefaultBlockSize,
		PrimaryPort:      0,
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' profile handler - %w", PdProfileHandlerType, err)
		}
	}

	if parameters.Threshold < 0 {
		return nil, fmt.Errorf("invalid threshold: must be >= 0, got %d", parameters.Threshold)
	}

	if parameters.HashBlockSize <= 0 {
		return nil, fmt.Errorf("invalid hashBlockSize: must be > 0, got %d", parameters.HashBlockSize)
	}

	if parameters.PrimaryPort != 0 {
		if parameters.PrimaryPort < 1 || parameters.PrimaryPort > 65535 {
			return nil, fmt.Errorf("invalid primaryPort: must be between 1 and 65535, got %d", parameters.PrimaryPort)
		}
	}

	return NewPdProfileHandler(parameters.PrefillProfile, parameters.DecodeProfile, parameters.PrefixPluginName,
		parameters.Threshold, parameters.HashBlockSize, parameters.PrimaryPort).WithName(name), nil
}

// NewPdProfileHandler initializes a new PdProfileHandler and returns its pointer.
func NewPdProfileHandler(prefillProfile string, decodeProfile string, prefixPluginName string, pdThreshold int, hashBlockSize int, primaryPort int) *PdProfileHandler {
	result := &PdProfileHandler{
		typedName:             plugins.TypedName{Type: PdProfileHandlerType},
		prefixPluginTypedName: plugins.TypedName{Type: prefix.PrefixCachePluginType, Name: prefixPluginName},
		decodeProfile:         decodeProfile,
		prefillProfile:        prefillProfile,
		pdThreshold:           pdThreshold,
		hashBlockSize:         hashBlockSize,
	}
	if primaryPort != 0 {
		result.primaryPort = strconv.Itoa(primaryPort)
	}

	return result
}

// PdProfileHandler handles scheduler profiles for PD.
type PdProfileHandler struct {
	typedName             plugins.TypedName
	prefixPluginTypedName plugins.TypedName
	decodeProfile         string
	prefillProfile        string
	pdThreshold           int
	hashBlockSize         int
	primaryPort           string
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
		userInput, err := getUserInputBytes(request)
		if err != nil {
			log.FromContext(ctx).V(logutil.DEBUG).Error(err, "Failed to get user input bytes")
			return nil
		}

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
			hitPercentagePrefix = float64(hitPrefix*h.hashBlockSize) / float64(len(userInput))
			log.FromContext(ctx).V(logutil.DEBUG).Info("Computed hit percentage for prefix cache", "hitPercentage", hitPercentagePrefix,
				"promptLength", len(userInput))
		}

		if (1.0-hitPercentagePrefix)*float64(len(userInput)) < float64(h.pdThreshold) {
			log.FromContext(ctx).Info("Non-cached suffix is smaller than threshold, using decode profile only", "hitPercentage", hitPercentagePrefix)
			metrics.RecordPDDecision(metrics.DecisionTypeDecodeOnly)
			return map[string]*framework.SchedulerProfile{} // do not run prefill
		}
	}

	metrics.RecordPDDecision(metrics.DecisionTypePrefillDecode)
	// run the prefill profile
	return map[string]*framework.SchedulerProfile{
		h.prefillProfile: profiles[h.prefillProfile],
	}
}

// ProcessResults handles the outcome of the profile runs after the selected profiles ran.
// In case of an error in any of the profiles, the matching entry in the profileResults will contain nil, to indicate there was
// an error while running the profile.
func (h *PdProfileHandler) ProcessResults(_ context.Context, _ *types.CycleState, request *types.LLMRequest,
	profileResults map[string]*types.ProfileRunResult) (*types.SchedulingResult, error) {
	decodeRunResults := profileResults[h.decodeProfile]
	if decodeRunResults == nil { // if decode profile failed to run, we should fail
		return nil, errors.New("failed to find available decode workers")
	}
	// otherwise, decode ran successfully

	updatedResults := map[string]*types.ProfileRunResult{}

	// Add decode profile to result
	if h.primaryPort != "" {
		// Data Parallel is active

		targetPod := decodeRunResults.TargetPods[0].GetPod()
		request.Headers[common.DataParallelPodHeader] = net.JoinHostPort(targetPod.Address, targetPod.Port)

		updatedResult := types.ProfileRunResult{
			TargetPods: []types.Pod{},
		}

		for _, target := range decodeRunResults.TargetPods {
			updatedPodInfo := target.GetPod().Clone()
			updatedPodInfo.Port = h.primaryPort
			targetPod := &types.PodMetrics{Pod: updatedPodInfo, MetricsState: target.GetMetrics().Clone()}
			updatedResult.TargetPods = append(updatedResult.TargetPods, targetPod)
		}
		updatedResults[h.decodeProfile] = &updatedResult
	} else {
		updatedResults[h.decodeProfile] = decodeRunResults
	}

	// if both prefill and decode ran successfully
	if prefillRunResult, exists := profileResults[h.prefillProfile]; exists && prefillRunResult != nil {
		// Add the prefill profile to the results
		updatedResults[h.prefillProfile] = prefillRunResult
	}

	return &types.SchedulingResult{
		PrimaryProfileName: h.decodeProfile,
		ProfileResults:     updatedResults,
	}, nil
}

func getUserInputBytes(request *types.LLMRequest) ([]byte, error) {
	if request.Body.Completions != nil { // assumed to be valid if not nil
		return []byte(request.Body.Completions.Prompt), nil
	}

	// must be chat-completions request at this point, return bytes of entire messages
	return json.Marshal(request.Body.ChatCompletions.Messages)
}
