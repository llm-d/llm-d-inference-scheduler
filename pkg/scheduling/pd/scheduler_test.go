package pd_test

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"

	"github.com/google/go-cmp/cmp"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics" // Import config for thresholds
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/config"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/pd"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/plugins/filter"
	"sigs.k8s.io/controller-runtime/pkg/log"
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

	tests := []struct {
		name            string
		req             *types.LLMRequest
		input           []*backendmetrics.FakePodMetrics
		wantRes         *types.Result
		wantHeaders     map[string]string
		unwantedHeaders []string
		err             bool
	}{
		{
			name: "no pods in datastore",
			req: &types.LLMRequest{
				TargetModel: "any-model",
				Critical:    true,
				Prompt:      "12345678901",
			},
			input: []*backendmetrics.FakePodMetrics{},
			err:   true,
		},
		{
			name: "one decode pod, long prompt",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Critical:    true,
				Prompt:      "12345678901",
			},
			// pod2 will be picked because it is the only pod with Decode role
			input: []*backendmetrics.FakePodMetrics{pod2},
			wantRes: &types.Result{
				TargetPod: &types.ScoredPod{
					Pod: wantPod2,
				},
			},
			unwantedHeaders: []string{"x-prefiller-url"},
		},
		{
			name: "one prefill pod, long prompt",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Critical:    true,
				Prompt:      "12345678901",
			},
			// no Decode pod
			input: []*backendmetrics.FakePodMetrics{pod1},
			err:   true,
		},
		{
			name: "1P1D",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Critical:    true,
				Prompt:      "12345678901",
			},
			// pod2 will be picked because it is the decode pod, pod1 IP will be in the header
			input: []*backendmetrics.FakePodMetrics{pod1, pod2},
			wantRes: &types.Result{
				TargetPod: &types.ScoredPod{
					Pod:   wantPod2,
					Score: 0.0,
				},
			},
			wantHeaders: map[string]string{"x-prefiller-url": "http://1.2.3.4:80"},
		},
		{
			name: "1P1Dshort",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Critical:    true,
				Prompt:      "123",
			},
			// pod2 will be picked because it is the decode pod, pod1 IP should no be in the header,
			// because the prompt is too short
			input: []*backendmetrics.FakePodMetrics{pod1, pod2},
			wantRes: &types.Result{
				TargetPod: &types.ScoredPod{
					Pod:   wantPod2,
					Score: 0.0,
				},
			},
			unwantedHeaders: []string{"x-prefiller-url"},
		},
	}

	ctx := context.Background()
	logger := testr.New(t)
	ctx = log.IntoContext(ctx, logger)

	schedCfg := config.NewConfig(logger)
	schedCfg.PDEnabled = true
	schedCfg.PDThreshold = 5

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scheduler, _ := pd.NewScheduler(ctx, schedCfg, &fakeDataStore{pods: test.input})
			got, err := scheduler.Schedule(ctx, test.req)

			if test.err != (err != nil) {
				t.Errorf("Unexpected error, got %v, want %v", err, test.err)
			}

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
