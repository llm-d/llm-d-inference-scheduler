package scorer_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics" // Import config for thresholds
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/picker"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

func TestLoadBasedScorer(t *testing.T) {
	tests := []struct {
		name    string
		scorer  framework.Scorer
		req     *types.LLMRequest
		input   []types.Pod
		wantRes *types.ProfileRunResult
		err     bool
	}{
		{
			name:   "load based scorer",
			scorer: scorer.NewLoadAwareScorer(context.Background(), 10),

			req: &types.LLMRequest{
				TargetModel: "critical",
			},
			// pod2 will be picked because it has the shortest queue
			input: []types.Pod{
				&types.PodMetrics{
					Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod1"}},
					MetricsState: &backendmetrics.MetricsState{
						WaitingQueueSize:    2,
						KVCacheUsagePercent: 0.2,
						MaxActiveModels:     2,
						ActiveModels: map[string]int{
							"foo": 1,
							"bar": 1,
						},
					},
				},

				&types.PodMetrics{
					Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod2"}},
					MetricsState: &backendmetrics.MetricsState{
						WaitingQueueSize:    0,
						KVCacheUsagePercent: 0.2,
						MaxActiveModels:     2,
						ActiveModels: map[string]int{
							"foo": 1,
							"bar": 1,
						},
					},
				},
				&types.PodMetrics{
					Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod3"}},
					MetricsState: &backendmetrics.MetricsState{
						WaitingQueueSize:    5,
						KVCacheUsagePercent: 0.2,
						MaxActiveModels:     2,
						ActiveModels: map[string]int{
							"foo": 1,
							"bar": 1,
						},
					},
				},
			},
			wantRes: &types.ProfileRunResult{
				TargetPods: []types.Pod{&types.ScoredPod{
					Pod: &types.PodMetrics{
						Pod: &backend.Pod{
							NamespacedName: k8stypes.NamespacedName{Name: "pod2"},
						},
						MetricsState: &backendmetrics.MetricsState{
							WaitingQueueSize:    0,
							KVCacheUsagePercent: 0.2,
							MaxActiveModels:     2,
							ActiveModels: map[string]int{
								"foo": 1,
								"bar": 1,
							},
						},
					},
					Score: 0.5,
				},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schedulerProfile := framework.NewSchedulerProfile().
				WithScorers(framework.NewWeightedScorer(test.scorer, 1)).
				WithPicker(picker.NewMaxScorePicker(picker.DefaultMaxNumOfEndpoints))

			got, err := schedulerProfile.Run(context.Background(), test.req, nil, test.input)

			if test.err != (err != nil) {
				t.Errorf("Unexpected error, got %v, want %v", err, test.err)
			}

			if diff := cmp.Diff(test.wantRes, got); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}
