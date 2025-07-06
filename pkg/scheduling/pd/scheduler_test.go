package pd_test

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/cmd/epp/runner"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics" // Import config for thresholds
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/common/config/loader"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	"sigs.k8s.io/gateway-api-inference-extension/test/utils"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
)

// Tests the default scheduler configuration and expected behavior.
func TestPDSchedule(t *testing.T) {
	pod1 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod1"},
			Address:        "1.2.3.4",
			Labels:         map[string]string{filter.RoleLabel: filter.RolePrefill},
		},
		MetricsState: backendmetrics.NewMetricsState(),
	}
	pod2 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod2"},
			Address:        "5.6.7.8",
			Labels:         map[string]string{filter.RoleLabel: filter.RoleDecode},
		},
		MetricsState: backendmetrics.NewMetricsState(),
	}
	noRolePod1 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "noRolePod1"},
			Address:        "1.1.1.1",
		},
		MetricsState: backendmetrics.NewMetricsState(),
	}
	noRolePod2 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "noRolePod2"},
			Address:        "2.2.2.2",
		},
		MetricsState: backendmetrics.NewMetricsState(),
	}

	prefillDecodeResult := &types.SchedulingResult{
		ProfileResults: map[string]*types.ProfileRunResult{
			"decode": {
				TargetPod: &types.ScoredPod{
					Pod:   pod2,
					Score: 0.0,
				},
			},
			"prefill": {
				TargetPod: &types.ScoredPod{
					Pod:   pod1,
					Score: 0.0,
				},
			},
		},
		PrimaryProfileName: "decode",
	}

	decodeResult := &types.SchedulingResult{
		ProfileResults: map[string]*types.ProfileRunResult{
			"decode": {
				TargetPod: &types.ScoredPod{
					Pod:   pod2,
					Score: 0.0,
				},
			},
		},
		PrimaryProfileName: "decode",
	}

	tests := []struct {
		name            string
		req             *types.LLMRequest
		input           []types.Pod
		wantRes         *types.SchedulingResult
		wantRes2        *types.SchedulingResult
		wantHeaders     map[string]string
		unwantedHeaders []string
		unwantedPodIDs  []string
		err             bool
	}{
		{
			name: "no pods in datastore",
			req: &types.LLMRequest{
				TargetModel: "any-model",
				Prompt:      "12345678901",
			},
			input: []types.Pod{},
			err:   true,
		},
		{
			name: "one decode pod, long prompt",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Prompt:      "12345678901",
			},
			// pod2 will be picked because it is the only pod with Decode role
			input: []types.Pod{pod2},
			wantRes: &types.SchedulingResult{
				ProfileResults: map[string]*types.ProfileRunResult{
					"decode": {
						TargetPod: &types.ScoredPod{
							Pod: pod2,
						},
					},
				},
				PrimaryProfileName: "decode",
			},
		},
		{
			name: "one prefill pod, long prompt",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Prompt:      "12345678901",
			},
			// no Decode pod
			input: []types.Pod{pod1},
			err:   true,
		},
		{
			name: "1P1D - long prompt",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Prompt:      "12345678906",
			},
			// pod2 will be picked because it is the decode pod, pod1 IP will be in the header
			input:    []types.Pod{pod1, pod2},
			wantRes:  prefillDecodeResult,
			wantRes2: decodeResult,
		},
		{
			name: "1P1Dshort",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Prompt:      "12345",
			},
			// pod2 will be picked because it is the decode pod, pod1 IP should no be in the header,
			// because the prompt is too short
			input:    []types.Pod{pod1, pod2},
			wantRes:  decodeResult,
			wantRes2: decodeResult,
		},
		{
			name: "TestRoles",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Prompt:      "12345678901",
			},
			input:          []types.Pod{pod1, noRolePod1, noRolePod2},
			wantRes:        nil, // doesn't mater which pod was selected
			unwantedPodIDs: []string{pod1.GetPod().NamespacedName.String()},
		},
	}

	runner.RegisterAllPlugins()
	plugins.RegisterAllPlugins()

	ctx := context.Background()
	logger := testr.New(t)
	ctx = log.IntoContext(ctx, logger)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handle := utils.NewTestHandle(ctx)

			eppConfig, err := loader.LoadConfig([]byte(pdSchedulerConfigYaml), "")
			if err != nil {
				t.Errorf("Unexpected error, got %v", err)
			}

			err = loader.LoadPluginReferences(eppConfig.Plugins, handle)
			if err != nil {
				t.Errorf("Unexpected error, got %v", err)
			}

			schedulderConfig, err := loader.LoadSchedulerConfig(eppConfig.SchedulingProfiles, handle)
			if err != nil {
				t.Errorf("Unexpected error, got %v", err)
			}

			scheduler := scheduling.NewSchedulerWithConfig(schedulderConfig)
			got, err := scheduler.Schedule(ctx, test.req, test.input)

			if test.err != (err != nil) {
				t.Errorf("Unexpected error, got %v, want %v", err, test.err)
			}

			if test.wantRes != nil {
				if diff := cmp.Diff(test.wantRes, got); diff != "" {
					t.Errorf("Unexpected output (-want +got): %v", diff)
				}

				for header, value := range test.wantHeaders {
					gotValue, ok := test.req.Headers[header]
					if !ok {
						t.Errorf("Missing header: %s", header)
					} else if gotValue != value {
						t.Errorf("Wrong header value for %s: want %s got %s)", header, value, gotValue)
					}
				}

				for _, header := range test.unwantedHeaders {
					if _, exists := test.req.Headers[header]; exists {
						t.Errorf("Unwanted header %s exists", header)
					}
				}
			}

			if len(test.unwantedPodIDs) > 0 {
				// ensure that target pod is not one of the unwanted
				profileRes, found := got.ProfileResults[got.PrimaryProfileName]
				if found {
					for _, podID := range test.unwantedPodIDs {
						if podID == profileRes.TargetPod.GetPod().NamespacedName.String() {
							t.Errorf("Unwanted pod was selected: %s", podID)
						}
					}
				}
			}

			if test.wantRes2 != nil { // Checking the prefix match in the decode pod.
				got, err = scheduler.Schedule(ctx, test.req, test.input)
				if test.err != (err != nil) {
					t.Errorf("Unexpected error, got %v, want %v", err, test.err)
				}

				if diff := cmp.Diff(test.wantRes2, got); diff != "" {
					t.Errorf("Unexpected output (-want +got): %v", diff)
				}
			}

		})
	}
}

const pdSchedulerConfigYaml = `
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: prefill-header
- name: prefixScorer 
  type: prefix-cache
  parameters:
    hashBlockSize: 5
    maxPrefixBlocksToMatch: 256
    lruCapacityPerServer: 31250
- name: prefillFilter
  type: prefill-filter
- name: decodeFilter
  type: decode-filter
- type: max-score
- type: pd-profile-handler
  parameters:
    hashBlockSize: 5
    maxPrefixBlocksToMatch: 256
    lruCapacityPerServer: 31250
    threshold: 10
schedulingProfiles:
- name: prefill
  plugins:
  - pluginRef: prefillFilter
  - pluginRef: max-score
  - pluginRef: prefixScorer
    weight: 50
- name: decode
  plugins:
  - pluginRef: decodeFilter
  - pluginRef: max-score
  - pluginRef: prefixScorer
    weight: 0
`
