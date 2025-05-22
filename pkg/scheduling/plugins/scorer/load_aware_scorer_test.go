package scorer_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics" // Import config for thresholds
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/picker"
	k8sscorer "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins/scorer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/plugins/scorer"
)

func TestLoadBasedScorer(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		scorer  plugins.Scorer
		req     *types.LLMRequest
		input   []*backendmetrics.FakePodMetrics
		wantRes *types.Result
		err     bool
	}{
		{
			name:   "load based scorer",
			scorer: scorer.NewLoadAwareScorer(ctx),
			req: &types.LLMRequest{
				TargetModel: "critical",
				Critical:    true,
			},
			// pod2 will be picked because it has the shortest queue
			input: []*backendmetrics.FakePodMetrics{
				{
					Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod1"}},
					Metrics: &backendmetrics.MetricsState{
						WaitingQueueSize:    2,
						KVCacheUsagePercent: 0.2,
						MaxActiveModels:     2,
						ActiveModels: map[string]int{
							"foo": 1,
							"bar": 1,
						},
					},
				},
				{
					Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod2"}},
					Metrics: &backendmetrics.MetricsState{
						WaitingQueueSize:    0,
						KVCacheUsagePercent: 0.2,
						MaxActiveModels:     2,
						ActiveModels: map[string]int{
							"foo": 1,
							"bar": 1,
						},
					},
				},
				{
					Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod3"}},
					Metrics: &backendmetrics.MetricsState{
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
			wantRes: &types.Result{
				TargetPod: &types.ScoredPod{
					Pod: &types.PodMetrics{
						Pod: &backend.Pod{
							NamespacedName: k8stypes.NamespacedName{Name: "pod2"},
							Labels:         map[string]string{},
						},
						MetricsState: &backendmetrics.MetricsState{
							WaitingQueueSize:    0,
							KVCacheUsagePercent: 0.2,
							MaxActiveModels:     2,
							ActiveModels: map[string]int{
								"foo": 1,
								"bar": 1,
							},
							WaitingModels: map[string]int{},
						},
					},
					Score: 0.5,
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			datastore := &fakeDataStore{pods: test.input}

			scheduler := scheduling.NewSchedulerWithConfig(datastore, scheduling.NewSchedulerConfig().
				WithScorers(k8sscorer.NewWeightedScorer(test.scorer, 1)).
				WithPicker(picker.NewMaxScorePicker()))

			got, err := scheduler.Schedule(context.Background(), test.req)
			if test.err != (err != nil) {
				t.Errorf("Unexpected error, got %v, want %v", err, test.err)
			}

			opt := cmp.AllowUnexported(types.PodMetrics{})
			if diff := cmp.Diff(test.wantRes, got, opt); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}

// TODO: this is probably better in upstream (e.g., epp/scheduling or epp/scheduling/plugins)
type fakeDataStore struct {
	pods []*backendmetrics.FakePodMetrics
}

// PodGetAll returns all pods in the store
func (fds *fakeDataStore) PodGetAll() []backendmetrics.PodMetrics {
	pm := make([]backendmetrics.PodMetrics, 0, len(fds.pods))
	for _, pod := range fds.pods {
		pm = append(pm, pod)
	}
	return pm
}
