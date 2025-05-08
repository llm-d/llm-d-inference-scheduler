package pd_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics" // Import config for thresholds
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/config"
	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/scheduling/pd"
	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/scheduling/plugins/filter"
)

// Tests the default scheduler configuration and expected behavior.
func TestPDSchedule(t *testing.T) {
	pod1 := &backendmetrics.FakePodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod1"},
			Address:        "1.2.3.4",
			Labels:         map[string]string{filter.RoleLabel: filter.RolePrefill},
		},
		Metrics: &backendmetrics.Metrics{},
	}
	pod2 := &backendmetrics.FakePodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod2"},
			Address:        "5.6.7.8",
			Labels:         map[string]string{filter.RoleLabel: filter.RoleDecode},
		},
		Metrics: &backendmetrics.Metrics{},
	}
	wantPod2 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod2"},
			Address:        "5.6.7.8",
			Labels:         map[string]string{filter.RoleLabel: filter.RoleDecode},
		},
		Metrics: &backendmetrics.Metrics{
			ActiveModels:  map[string]int{},
			WaitingModels: map[string]int{},
		},
	}

	tests := []struct {
		name    string
		req     *types.LLMRequest
		input   []*backendmetrics.FakePodMetrics
		wantRes *types.Result
		err     bool
	}{
		{
			name: "no pods in datastore",
			req: &types.LLMRequest{
				Model:               "any-model",
				ResolvedTargetModel: "any-model",
				Critical:            true,
				Prompt:              "12345678901",
			},
			input: []*backendmetrics.FakePodMetrics{},
			err:   true,
		},
		{
			name: "one pod, short prompt",
			req: &types.LLMRequest{
				Model:               "critical",
				ResolvedTargetModel: "critical",
				Critical:            true,
				Prompt:              "123",
			},
			// pod2 will be picked because it is the only pod with Decode role
			input: []*backendmetrics.FakePodMetrics{pod1, pod2},
			wantRes: &types.Result{
				TargetPod: &types.ScoredPod{
					Pod: wantPod2,
				},
			},
		},
		{
			name: "1P1D",
			req: &types.LLMRequest{
				Model:               "critical",
				ResolvedTargetModel: "critical",
				Critical:            true,
				Prompt:              "12345678901",
			},
			// pod2 will be picked because it is the decode pod, pod1 IP will be in header
			input: []*backendmetrics.FakePodMetrics{pod1, pod2},
			wantRes: &types.Result{
				TargetPod: &types.ScoredPod{
					Pod:   wantPod2,
					Score: 0.0,
				},
				//				MutatedHeaders: map[string]string{"x-prefiller-url": "http://1.2.3.4:80"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			logger := log.FromContext(ctx)

			schedCfg := config.NewConfig(logger)
			// TODO - unsure that default config is ok here (no scorers)
			scheduler, _ := pd.NewScheduler(ctx, schedCfg, &fakeDataStore{pods: test.input})
			got, err := scheduler.Schedule(ctx, test.req)

			fmt.Printf("Test %s:\n", test.name)
			fmt.Printf("Result: %#v\n", got)
			fmt.Printf("Expected: %#v\n", test.wantRes)

			if test.err != (err != nil) {
				t.Errorf("Unexpected error, got %v, want %v", err, test.err)
			}

			if diff := cmp.Diff(test.wantRes, got); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}

// TODO: this is probably better in upstream (e.g., epp/scheduling or epp/scheduling/plugins)
// currently duplicated from pkg/scheduling/plugins/
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

func (fds *fakeDataStore) PoolGet() (*v1alpha2.InferencePool, error) {
	return &v1alpha2.InferencePool{
		Spec: v1alpha2.InferencePoolSpec{
			TargetPortNumber: 80,
		},
	}, nil
}
