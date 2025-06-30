package pd_test

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics" // Import config for thresholds
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/config"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/pd"
)

// Tests the default scheduler configuration and expected behavior.
func TestPDSchedule(t *testing.T) {
	pod1 := &backendmetrics.FakePodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod1"},
			Address:        "1.2.3.4",
			Labels:         map[string]string{filter.RoleLabel: filter.RolePrefill},
		},
		Metrics: &backendmetrics.MetricsState{},
	}
	pod2 := &backendmetrics.FakePodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod2"},
			Address:        "5.6.7.8",
			Labels:         map[string]string{filter.RoleLabel: filter.RoleDecode},
		},
		Metrics: &backendmetrics.MetricsState{},
	}
	wantPod1 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod1"},
			Address:        "1.2.3.4",
			Labels:         map[string]string{filter.RoleLabel: filter.RolePrefill},
		},
		MetricsState: &backendmetrics.MetricsState{
			ActiveModels:  map[string]int{},
			WaitingModels: map[string]int{},
		},
	}
	wantPod2 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod2"},
			Address:        "5.6.7.8",
			Labels:         map[string]string{filter.RoleLabel: filter.RoleDecode},
		},
		MetricsState: &backendmetrics.MetricsState{
			ActiveModels:  map[string]int{},
			WaitingModels: map[string]int{},
		},
	}
	noRolePod1 := &backendmetrics.FakePodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "noRolePod1"},
			Address:        "1.1.1.1",
		},
		Metrics: &backendmetrics.MetricsState{},
	}
	noRolePod2 := &backendmetrics.FakePodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "noRolePod2"},
			Address:        "2.2.2.2",
		},
		Metrics: &backendmetrics.MetricsState{},
	}

	tests := []struct {
		name            string
		req             *types.LLMRequest
		input           []backendmetrics.PodMetrics
		wantRes         *types.SchedulingResult
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
			input: []backendmetrics.PodMetrics{},
			err:   true,
		},
		{
			name: "one decode pod, long prompt",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Prompt:      "12345678901",
			},
			// pod2 will be picked because it is the only pod with Decode role
			input: []backendmetrics.PodMetrics{pod2},
			wantRes: &types.SchedulingResult{
				ProfileResults: map[string]*types.ProfileRunResult{
					"decode": {
						TargetPod: &types.ScoredPod{
							Pod: wantPod2,
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
			input: []backendmetrics.PodMetrics{pod1},
			err:   true,
		},
		{
			name: "1P1D",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Prompt:      "12345678901",
			},
			// pod2 will be picked because it is the decode pod, pod1 IP will be in the header
			input: []backendmetrics.PodMetrics{pod1, pod2},
			wantRes: &types.SchedulingResult{
				ProfileResults: map[string]*types.ProfileRunResult{
					"decode": {
						TargetPod: &types.ScoredPod{
							Pod:   wantPod2,
							Score: 0.0,
						},
					},
					"prefill": {
						TargetPod: &types.ScoredPod{
							Pod:   wantPod1,
							Score: 0.0,
						},
					},
				},
				PrimaryProfileName: "decode",
			},
		},
		{
			name: "1P1Dshort",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Prompt:      "123",
			},
			// pod2 will be picked because it is the decode pod, pod1 IP should no be in the header,
			// because the prompt is too short
			input: []backendmetrics.PodMetrics{pod1, pod2},
			wantRes: &types.SchedulingResult{
				ProfileResults: map[string]*types.ProfileRunResult{
					"decode": {
						TargetPod: &types.ScoredPod{
							Pod:   wantPod2,
							Score: 0.0,
						},
					},
				},
				PrimaryProfileName: "decode",
			},
		},
		{
			name: "TestRoles",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Prompt:      "12345678901",
			},
			input:          []backendmetrics.PodMetrics{pod1, noRolePod1, noRolePod2},
			wantRes:        nil, // doesn't mater which pod was selected
			unwantedPodIDs: []string{pod1.GetPod().NamespacedName.String()},
		},
	}

	ctx := context.Background()
	logger := testr.New(t)
	ctx = log.IntoContext(ctx, logger)

	schedulderConfig := &config.Config{
		DecodeSchedulerPlugins:  map[string]int{},
		PrefillSchedulerPlugins: map[string]int{},
		PDEnabled:               true,
		PDThreshold:             5,
		PrefixCacheBlockSize:    256,
		PrefixCacheCapacity:     50000,
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prefixConfig := scorer.DefaultPrefixStoreConfig()
			prefixConfig.CacheBlockSize = schedulderConfig.PrefixCacheBlockSize
			prefixConfig.CacheCapacity = schedulderConfig.PrefixCacheCapacity
			prefixScorer := scorer.NewPrefixAwareScorer(ctx, prefixConfig)

			schedulderConfig, err := pd.CreatePDSchedulerConfig(ctx, schedulderConfig, prefixScorer)
			if err != nil {
				t.Errorf("Unexpected error, got %v", err)
			}

			datastore := &fakeDataStore{pods: test.input}
			scheduler := scheduling.NewSchedulerWithConfig(schedulderConfig)
			candidatePods := types.ToSchedulerPodMetrics(datastore.PodGetAll())
			got, err := scheduler.Schedule(ctx, test.req, candidatePods)

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
		})
	}
}

// TODO: this is probably better in upstream (e.g., epp/scheduling or epp/scheduling/plugins)
// currently duplicated from pkg/scheduling/plugins/
type fakeDataStore struct {
	pods []backendmetrics.PodMetrics
}

// PodGetAll returns all pods in the store
func (fds *fakeDataStore) PodGetAll() []backendmetrics.PodMetrics {
	return fds.pods
}
