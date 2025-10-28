package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// DataParallelProfileHandlerType is the type of the DataParallelProfileHandler
	DataParallelProfileHandlerType = "data-parallel-profile-handler"
)

type dataParallelProfileHandlerParameters struct {
	TargetPort int `json:"targetPort"`
}

// compile-time type assertion
var _ framework.ProfileHandler = &DataParallelProfileHandler{}

// DataParallelProfileHandlerFactory defines the factory function for the DataParallelProfileHandler
func DataParallelProfileHandlerFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	parameters := dataParallelProfileHandlerParameters{
		TargetPort: 8000,
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' profile handler - %w", PdProfileHandlerType, err)
		}
	}

	return NewDataParallelProfileHandler(parameters.TargetPort).WithName(name), nil
}

// NewDataParallelProfileHandler initializes a new PdProfileHandler and returns its pointer.
func NewDataParallelProfileHandler(targetPort int) *DataParallelProfileHandler {
	return &DataParallelProfileHandler{
		typedName:  plugins.TypedName{Type: DataParallelProfileHandlerType},
		targetPort: strconv.Itoa(targetPort),
	}
}

// PdProfileHandler handles scheduler profiles for PD.
type DataParallelProfileHandler struct {
	typedName  plugins.TypedName
	targetPort string
}

// TypedName returns the typed name of the plugin.
func (h *DataParallelProfileHandler) TypedName() plugins.TypedName {
	return h.typedName
}

// WithName sets the name of the plugin.
func (h *DataParallelProfileHandler) WithName(name string) *DataParallelProfileHandler {
	h.typedName.Name = name
	return h
}

// Pick selects the SchedulingProfiles to run from the list of candidate profiles, while taking into consideration the request properties and the
// previously executed cycles along with their results.
func (h *DataParallelProfileHandler) Pick(_ context.Context, cycleState *types.CycleState, request *types.LLMRequest, profiles map[string]*framework.SchedulerProfile,
	profileResults map[string]*types.ProfileRunResult) map[string]*framework.SchedulerProfile {
	if len(profiles) == len(profileResults) { // all profiles have been executed already in previous call
		return map[string]*framework.SchedulerProfile{}
	}
	// return all profiles
	return profiles
}

// ProcessResults handles the outcome of the profile runs after all profiles ran.
// It may aggregate results, log test profile outputs, or apply custom logic. It specifies in the SchedulingResult the
// key of the primary profile that should be used to get the request selected destination.
// When a profile run fails, its result in the profileResults map is nil.
func (h *DataParallelProfileHandler) ProcessResults(_ context.Context, _ *types.CycleState, request *types.LLMRequest,
	profileResults map[string]*types.ProfileRunResult) (*types.SchedulingResult, error) {
	if len(profileResults) != 1 {
		return nil, errors.New("data parallel profile handler is intended to be used with a single profile, failed to process multiple profiles")
	}

	var singleProfileName string
	for profileName := range profileResults {
		singleProfileName = profileName
		break
	}

	profileResult := profileResults[singleProfileName]
	if profileResult == nil { // there was an error while running the profile
		return nil, fmt.Errorf("failed to run scheduler profile '%s'", singleProfileName)
	}

	newResult := types.ProfileRunResult{
		TargetPods: []types.Pod{},
	}

	targetPod := profileResult.TargetPods[0].GetPod()

	request.Headers[common.DataParallelPodHeader] = net.JoinHostPort(targetPod.Address, targetPod.Port)

	for _, target := range profileResult.TargetPods {
		newPodInfo := target.GetPod().Clone()
		newPodInfo.Port = h.targetPort
		targetPod := &types.PodMetrics{Pod: newPodInfo, MetricsState: target.GetMetrics().Clone()}
		newResult.TargetPods = append(newResult.TargetPods, targetPod)
	}
	modifiedResults := map[string]*types.ProfileRunResult{singleProfileName: &newResult}

	return &types.SchedulingResult{
		ProfileResults:     modifiedResults,
		PrimaryProfileName: singleProfileName,
	}, nil
}
