/*
Copyright 2025 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/metrics"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

const (
	// PdSLOProfileHandlerType is the type of the PdSLOProfileHandler
	PdSLOProfileHandlerType = "pd-slo-profile-handler"

	defaultDecodeProfileSLO  = "decode"
	defaultPrefillProfileSLO = "prefill"

	// Cycle state keys for selected pod pair
	selectedPrefillPodKey = "pd-slo-selected-prefill-pod"
	selectedDecodePodKey  = "pd-slo-selected-decode-pod"
)

type pdSLOProfileHandlerParameters struct {
	DecodeProfile      string  `json:"decodeProfile"`
	PrefillProfile     string  `json:"prefillProfile"`
	PrefixPluginName   string  `json:"prefixPluginName"`
	HashBlockSize      int     `json:"hashBlockSize"`
	TransferOverheadMs float64 `json:"transferOverheadMs"`
	PdThreshold        int     `json:"pdThreshold"` // Threshold for non-SLO fallback (0 = always PD)
}

// compile-time type assertion
var _ framework.ProfileHandler = &PdSLOProfileHandler{}

// PdSLOProfileHandlerFactory defines the factory function for the PdSLOProfileHandler
func PdSLOProfileHandlerFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	parameters := pdSLOProfileHandlerParameters{
		DecodeProfile:      defaultDecodeProfileSLO,
		PrefillProfile:     defaultPrefillProfileSLO,
		PrefixPluginName:   "prefix-cache-scorer",
		HashBlockSize:      16,
		TransferOverheadMs: 5.0,
		PdThreshold:        0, // Default: always use PD disaggregation
	}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' profile handler - %w", PdSLOProfileHandlerType, err)
		}
	}

	if parameters.HashBlockSize <= 0 {
		return nil, fmt.Errorf("invalid hashBlockSize: must be > 0, got %d", parameters.HashBlockSize)
	}

	if parameters.TransferOverheadMs < 0 {
		return nil, fmt.Errorf("invalid transferOverheadMs: must be >= 0, got %f", parameters.TransferOverheadMs)
	}

	if parameters.PdThreshold < 0 {
		return nil, fmt.Errorf("invalid pdThreshold: must be >= 0, got %d", parameters.PdThreshold)
	}

	return NewPdSLOProfileHandler(
		parameters.PrefillProfile,
		parameters.DecodeProfile,
		parameters.PrefixPluginName,
		parameters.HashBlockSize,
		parameters.TransferOverheadMs,
		parameters.PdThreshold,
	).WithName(name), nil
}

// NewPdSLOProfileHandler initializes a new PdSLOProfileHandler and returns its pointer.
func NewPdSLOProfileHandler(
	prefillProfile string,
	decodeProfile string,
	prefixPluginName string,
	hashBlockSize int,
	transferOverheadMs float64,
	pdThreshold int,
) *PdSLOProfileHandler {
	return &PdSLOProfileHandler{
		typedName:             plugins.TypedName{Type: PdSLOProfileHandlerType},
		prefixPluginTypedName: plugins.TypedName{Type: prefix.PrefixCachePluginType, Name: prefixPluginName},
		decodeProfile:         decodeProfile,
		prefillProfile:        prefillProfile,
		hashBlockSize:         hashBlockSize,
		transferOverheadMs:    transferOverheadMs,
		pdThreshold:           pdThreshold,
	}
}

// PdSLOProfileHandler handles scheduler profiles for PD disaggregation with SLO awareness.
// It coordinates the execution of both prefill and decode profiles and uses the PD-SLO
// pair optimizer to select optimal (prefill, decode) pod pairs when SLO headers are present.
// When SLO headers are absent, it falls back to threshold-based PD scheduling.
type PdSLOProfileHandler struct {
	typedName             plugins.TypedName
	prefixPluginTypedName plugins.TypedName
	decodeProfile         string
	prefillProfile        string
	hashBlockSize         int
	transferOverheadMs    float64
	pdThreshold           int
}

// TypedName returns the typed name of the plugin.
func (h *PdSLOProfileHandler) TypedName() plugins.TypedName {
	return h.typedName
}

// WithName sets the name of the plugin.
func (h *PdSLOProfileHandler) WithName(name string) *PdSLOProfileHandler {
	h.typedName.Name = name
	return h
}

// Pick selects the SchedulingProfiles to run from the list of candidate profiles.
// Applies threshold-based logic to determine if prefill is needed (for both SLO and non-SLO requests).
// If prefill is needed and SLO headers are present, enables joint optimization via pair optimizer.
func (h *PdSLOProfileHandler) Pick(
	ctx context.Context,
	cycleState *types.CycleState,
	request *types.LLMRequest,
	profiles map[string]*framework.SchedulerProfile,
	profileResults map[string]*types.ProfileRunResult,
) map[string]*framework.SchedulerProfile {
	logger := log.FromContext(ctx)

	// Always run decode profile first (both SLO and non-SLO paths need it)
	if _, executed := profileResults[h.decodeProfile]; !executed {
		logger.V(logutil.DEBUG).Info("Running decode profile")
		return map[string]*framework.SchedulerProfile{
			h.decodeProfile: profiles[h.decodeProfile],
		}
	}

	// Check if decode profile succeeded
	if profileResults[h.decodeProfile] == nil {
		logger.V(logutil.DEBUG).Info("Decode profile failed, not running prefill profile")
		return map[string]*framework.SchedulerProfile{}
	}

	// Check if all configured profiles have been executed
	if len(profiles) == len(profileResults) {
		return map[string]*framework.SchedulerProfile{}
	}

	// Apply threshold-based logic to decide if prefill is needed
	// This applies to BOTH SLO and non-SLO requests
	if h.pdThreshold > 0 {
		userInput, err := getUserInputBytes(request)
		if err != nil {
			logger.V(logutil.DEBUG).Error(err, "Failed to get user input bytes")
			return map[string]*framework.SchedulerProfile{}
		}

		// Calculate prefix cache hit percentage
		hitPercentagePrefix := 0.0 // default to 0, meaning no prefix cache hit
		prefixState, err := types.ReadCycleStateKey[*prefix.SchedulingContextState](cycleState, plugins.StateKey(h.prefixPluginTypedName.String()))
		if err != nil {
			logger.Error(err, "unable to read prefix state")
		} else {
			decodePod := profileResults[h.decodeProfile].TargetPods[0].GetPod().NamespacedName
			hitPrefix := max(prefixState.PrefixCacheServers[prefix.ServerID(decodePod)]-1, 0) // The first hit is always the model name
			hitPercentagePrefix = float64(hitPrefix*h.hashBlockSize) / float64(len(userInput))
			logger.V(logutil.DEBUG).Info("Computed hit percentage for prefix cache", "hitPercentage", hitPercentagePrefix,
				"promptLength", len(userInput))
		}

		// If non-cached suffix is smaller than threshold, use decode-only
		if (1.0-hitPercentagePrefix)*float64(len(userInput)) < float64(h.pdThreshold) {
			logger.Info("Non-cached suffix is smaller than threshold, using decode profile only", "hitPercentage", hitPercentagePrefix)
			metrics.RecordPDDecision(metrics.DecisionTypeDecodeOnly)
			return map[string]*framework.SchedulerProfile{} // do not run prefill
		}
	}

	// Prefill is needed - run prefill profile for PD disaggregation
	metrics.RecordPDDecision(metrics.DecisionTypePrefillDecode)

	// Check if SLO headers are present (determines optimization strategy in ProcessResults)
	hasSLO := h.hasSLOHeaders(request)
	if hasSLO {
		logger.V(logutil.DEBUG).Info("Running prefill profile for PD-SLO scheduling (joint optimization)")
	} else {
		logger.V(logutil.DEBUG).Info("Running prefill profile for threshold-based PD scheduling")
	}

	if _, executed := profileResults[h.prefillProfile]; !executed {
		return map[string]*framework.SchedulerProfile{
			h.prefillProfile: profiles[h.prefillProfile],
		}
	}

	return map[string]*framework.SchedulerProfile{}
}

// ProcessResults handles the outcome of the profile runs after the selected profiles ran.
// For SLO requests: retrieves the selected pod pair from cycle state (set by the PD-SLO pair optimizer)
// For non-SLO requests: uses the pods selected by the profiles directly
func (h *PdSLOProfileHandler) ProcessResults(
	ctx context.Context,
	cycleState *types.CycleState,
	request *types.LLMRequest,
	profileResults map[string]*types.ProfileRunResult,
) (*types.SchedulingResult, error) {
	logger := log.FromContext(ctx)

	decodeRunResults := profileResults[h.decodeProfile]
	if decodeRunResults == nil {
		return nil, errors.New("failed to find available decode workers")
	}

	updatedResults := map[string]*types.ProfileRunResult{}

	// Check if this is an SLO request
	hasSLO := h.hasSLOHeaders(request)

	if hasSLO {
		// SLO path: Use pod pair selected by PD-SLO pair optimizer
		prefillRunResults := profileResults[h.prefillProfile]
		if prefillRunResults == nil {
			return nil, errors.New("failed to find available prefill workers")
		}

		// Get selected pod pair from cycle state (set by PD-SLO pair optimizer)
		prefillPodName, decodePodName, err := h.getSelectedPairFromState(cycleState)
		if err != nil {
			logger.V(logutil.DEBUG).Error(err, "Failed to get selected pair from cycle state")
			// Fallback: use first pods from each profile
			if len(prefillRunResults.TargetPods) > 0 && len(decodeRunResults.TargetPods) > 0 {
				prefillPodName = prefillRunResults.TargetPods[0].GetPod().String()
				decodePodName = decodeRunResults.TargetPods[0].GetPod().String()
			} else {
				return nil, errors.New("no pods available in profile results")
			}
		}

		// Find the actual pod objects
		var selectedPrefillPod, selectedDecodePod types.Pod
		for _, pod := range prefillRunResults.TargetPods {
			if pod.GetPod().String() == prefillPodName {
				selectedPrefillPod = pod
				break
			}
		}

		for _, pod := range decodeRunResults.TargetPods {
			if pod.GetPod().String() == decodePodName {
				selectedDecodePod = pod
				break
			}
		}

		if selectedPrefillPod == nil || selectedDecodePod == nil {
			return nil, fmt.Errorf("failed to find selected pods: prefill=%s, decode=%s", prefillPodName, decodePodName)
		}

		// Set prefiller header for the decode pod to use
		prefillPodInfo := selectedPrefillPod.GetPod()
		prefillAddr := net.JoinHostPort(prefillPodInfo.Address, prefillPodInfo.Port)
		request.Headers[common.PrefillPodHeader] = prefillAddr

		logger.Info("Set prefiller header for PD-SLO scheduling",
			"prefillPod", prefillPodName,
			"decodePod", decodePodName,
			"prefillAddr", prefillAddr)

		// Build scheduling result with both profiles
		updatedResults[h.decodeProfile] = &types.ProfileRunResult{
			TargetPods: []types.Pod{selectedDecodePod},
		}
		updatedResults[h.prefillProfile] = &types.ProfileRunResult{
			TargetPods: []types.Pod{selectedPrefillPod},
		}
	} else {
		// Non-SLO path: Use threshold-based PD logic
		updatedResults[h.decodeProfile] = decodeRunResults

		// If both prefill and decode ran successfully (threshold decided to use PD)
		if prefillRunResult, exists := profileResults[h.prefillProfile]; exists && prefillRunResult != nil {
			// Set prefiller header for the decode pod to use
			prefillPodInfo := prefillRunResult.TargetPods[0].GetPod()
			prefillAddr := net.JoinHostPort(prefillPodInfo.Address, prefillPodInfo.Port)
			request.Headers[common.PrefillPodHeader] = prefillAddr

			logger.Info("Set prefiller header for threshold-based PD scheduling",
				"prefillPod", prefillPodInfo.String(),
				"decodePod", decodeRunResults.TargetPods[0].GetPod().String(),
				"prefillAddr", prefillAddr)

			// Add the prefill profile to the results
			updatedResults[h.prefillProfile] = prefillRunResult
		}
	}

	return &types.SchedulingResult{
		PrimaryProfileName: h.decodeProfile,
		ProfileResults:     updatedResults,
	}, nil
}

// Helper methods

func (h *PdSLOProfileHandler) hasSLOHeaders(request *types.LLMRequest) bool {
	_, hasTTFT := request.Headers["x-slo-ttft-ms"]
	_, hasTPOT := request.Headers["x-slo-tpot-ms"]
	return hasTTFT || hasTPOT
}

func (h *PdSLOProfileHandler) getSelectedPairFromState(cycleState *types.CycleState) (prefillPodName, decodePodName string, err error) {
	prefillData, err := cycleState.Read(plugins.StateKey(selectedPrefillPodKey))
	if err != nil {
		return "", "", fmt.Errorf("failed to read selected prefill pod from cycle state: %w", err)
	}

	decodeData, err := cycleState.Read(plugins.StateKey(selectedDecodePodKey))
	if err != nil {
		return "", "", fmt.Errorf("failed to read selected decode pod from cycle state: %w", err)
	}

	prefillStringData, ok := prefillData.(*scorer.StringStateData)
	if !ok {
		return "", "", fmt.Errorf("selected prefill pod is not StringStateData")
	}

	decodeStringData, ok := decodeData.(*scorer.StringStateData)
	if !ok {
		return "", "", fmt.Errorf("selected decode pod is not StringStateData")
	}

	return prefillStringData.Value, decodeStringData.Value, nil
}
