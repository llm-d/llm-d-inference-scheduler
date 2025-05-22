package pd_test

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/config"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/pd"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/plugins/filter"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	k8stypes "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics" // Import config for thresholds
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// Tests the default scheduler configuration and expected behavior.
func TestPDSchedule(t *testing.T) {
	RegisterFailHandler(Fail)

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
		name        string
		req         *types.LLMRequest
		input       []*backendmetrics.FakePodMetrics
		wantRes     *types.Result
		wantHeaders map[string]string
		err         bool
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
			name: "one decode pod, short prompt",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Critical:    true,
				Prompt:      "123",
			},
			// pod2 will be picked because it is the only pod with Decode role
			input: []*backendmetrics.FakePodMetrics{pod1, pod2},
			wantRes: &types.Result{
				TargetPod: &types.ScoredPod{
					Pod: wantPod2,
				},
			},
			// no headers - only decode is used
			wantHeaders: map[string]string{},
		},
		{
			name: "one decode pod, long prompt",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Critical:    true,
				Prompt:      "1234567890",
			},
			// pod2 will be picked because it is the only pod with Decode role
			input: []*backendmetrics.FakePodMetrics{pod2},
			wantRes: &types.Result{
				TargetPod: &types.ScoredPod{
					Pod: wantPod2,
				},
			},
			// empty headers since there is no prefill pod
			wantHeaders: map[string]string{},
		},
		{
			name: "1P1D-short",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Critical:    true,
				Prompt:      "12",
			},
			// pod2 will be picked because it is the decode pod, pod1 IP will be in header
			input: []*backendmetrics.FakePodMetrics{pod1, pod2},
			wantRes: &types.Result{
				TargetPod: &types.ScoredPod{
					Pod:   wantPod2,
					Score: 0.0,
				},
			},
			// empty headers - prompt is short ebought to be processed on decode only
			wantHeaders: map[string]string{},
		},
		{
			name: "1P1D",
			req: &types.LLMRequest{
				TargetModel: "critical",
				Critical:    true,
				Prompt:      "1234567890",
			},
			// pod2 will be picked because it is the decode pod, pod1 IP will be in header
			input: []*backendmetrics.FakePodMetrics{pod1, pod2},
			wantRes: &types.Result{
				TargetPod: &types.ScoredPod{
					Pod:   wantPod2,
					Score: 0.0,
				},
			},
			wantHeaders: map[string]string{"x-prefiller-url": "http://1.2.3.4:80"},
		},
	}

	ctx := context.Background()
	logger := testr.New(t)
	ctx = ctrl.IntoContext(ctx, logger)

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

			reqHeaders := test.req.Headers
			if reqHeaders == nil {
				reqHeaders = map[string]string{}
			}
			wantHeaders := test.wantHeaders
			if wantHeaders == nil {
				wantHeaders = map[string]string{}
			}

			Expect(reqHeaders).To(Equal(wantHeaders))
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
