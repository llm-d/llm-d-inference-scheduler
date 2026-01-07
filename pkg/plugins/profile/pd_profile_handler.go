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
	defaultPrefixPluginType = prefix.PrefixCachePluginType
	defaultDeciderName      = alwaysDeciderName

	// AverageCharactersPerToken is an estimated average characters per token, used since the request we cached is not tokenized.
	AverageCharactersPerToken = 4
)

type pdProfileHandlerParameters struct {
	DecodeProfile    string           `json:"decodeProfile"`
	PrefillProfile   string           `json:"prefillProfile"`
	PrefixPluginType string           `json:"prefixPluginType"`
	PrefixPluginName string           `json:"prefixPluginName"`
	PrimaryPort      int              `json:"primaryPort"`
	Decider          *pdDeciderParams `json:"decider"`
}

// compile-time type assertion
var _ framework.ProfileHandler = &PdProfileHandler{}

// PdProfileHandlerFactory defines the factory function for the PdProfileHandler
func PdProfileHandlerFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	parameters := pdProfileHandlerParameters{
		DecodeProfile:    defaultDecodeProfile,
		PrefillProfile:   defaultPrefillProfile,
		PrefixPluginType: defaultPrefixPluginType,
		PrimaryPort:      0,
		Decider:          &pdDeciderParams{Name: defaultDeciderName},
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' profile handler - %w", PdProfileHandlerType, err)
		}
	}

	if parameters.PrefixPluginName == "" {
		parameters.PrefixPluginName = parameters.PrefixPluginType
	}

	if parameters.PrimaryPort != 0 {
		if parameters.PrimaryPort < 1 || parameters.PrimaryPort > 65535 {
			return nil, fmt.Errorf("invalid primaryPort: must be between 1 and 65535, got %d", parameters.PrimaryPort)
		}
	}

	handler, err := NewPdProfileHandler(parameters.PrefillProfile, parameters.DecodeProfile, parameters.PrefixPluginType, parameters.PrefixPluginName,
		parameters.PrimaryPort, parameters.Decider.Name, parameters.Decider.Parameters)

	if err != nil {
		return nil, err
	}

	return handler.WithName(name), nil

}

// NewPdProfileHandler initializes a new PdProfileHandler and returns its pointer.
func NewPdProfileHandler(prefillProfile, decodeProfile, prefixPluginType, prefixPluginName string,
	primaryPort int, deciderName string, deciderParams json.RawMessage) (*PdProfileHandler, error) {
	var decider pdDecider
	var err error

	switch deciderName {
	case alwaysDeciderName:
		decider, err = newAlwaysDisaggregationDecider(deciderParams)
	case PrefixDeciderName:
		decider, err = newPrefixDisaggregationDecider(deciderParams)
	default:
		return nil, fmt.Errorf("invalid decider type '%s'", deciderName)
	}

	if err != nil {
		return nil, err
	}

	result := &PdProfileHandler{
		typedName:             plugins.TypedName{Type: PdProfileHandlerType},
		prefixPluginTypedName: plugins.TypedName{Type: prefixPluginType, Name: prefixPluginName},
		decodeProfile:         decodeProfile,
		prefillProfile:        prefillProfile,
		decider:               decider,
	}
	if primaryPort != 0 {
		result.primaryPort = strconv.Itoa(primaryPort)
	}

	return result, nil
}

// PdProfileHandler handles scheduler profiles for PD.
type PdProfileHandler struct {
	typedName             plugins.TypedName
	prefixPluginTypedName plugins.TypedName
	decodeProfile         string
	prefillProfile        string
	primaryPort           string
	decider               pdDecider
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
func (h *PdProfileHandler) Pick(ctx context.Context, _ *types.CycleState, request *types.LLMRequest, profiles map[string]*framework.SchedulerProfile,
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

	inputTokens, err := getUserInputLenInTokens(request)
	if err != nil {
		log.FromContext(ctx).V(logutil.DEBUG).Error(err, "Failed to get user input")
		return nil
	}

	if h.decider != nil && h.decider.isDisaggregationRequired(ctx, inputTokens, profileResults[h.decodeProfile].TargetPods[0]) {
		metrics.RecordPDDecision(request.TargetModel, metrics.DecisionTypePrefillDecode)
		// run the prefill profile
		return map[string]*framework.SchedulerProfile{
			h.prefillProfile: profiles[h.prefillProfile],
		}
	}

	metrics.RecordPDDecision(request.TargetModel, metrics.DecisionTypeDecodeOnly)
	return map[string]*framework.SchedulerProfile{} // do not run prefill
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

func getUserInputLenInTokens(request *types.LLMRequest) (int, error) {
	if request.Body.Completions != nil { // assumed to be valid if not nil
		return len([]byte(request.Body.Completions.Prompt)) / AverageCharactersPerToken, nil
	}

	// must be chat-completions request at this point, return bytes of entire messages
	prompt, err := json.Marshal(request.Body.ChatCompletions.Messages)

	if err != nil {
		return 0, err
	}

	return len(prompt) / AverageCharactersPerToken, nil
}
