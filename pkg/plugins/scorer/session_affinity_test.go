package scorer_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/google/go-cmp/cmp"

	k8stypes "k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/picker"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

func TestSessionAffinity_Scorer(t *testing.T) {
	podA := &backendmetrics.FakePodMetrics{
		Pod:     &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		Metrics: &backendmetrics.MetricsState{},
	}
	podB := &backendmetrics.FakePodMetrics{
		Pod:     &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-b"}},
		Metrics: &backendmetrics.MetricsState{},
	}

	wantPodA := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
			Labels:         map[string]string{},
		},
		MetricsState: &backendmetrics.MetricsState{
			ActiveModels:  map[string]int{},
			WaitingModels: map[string]int{},
		},
	}

	wantPodB := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
			Labels:         map[string]string{},
		},
		MetricsState: &backendmetrics.MetricsState{
			ActiveModels:  map[string]int{},
			WaitingModels: map[string]int{},
		},
	}

	// valid session token for podB
	validSessionTokenForPodB := base64.StdEncoding.EncodeToString([]byte(podB.GetPod().NamespacedName.String()))

	tests := []struct {
		name       string
		scorer     framework.Scorer
		req        *types.LLMRequest
		input      []backendmetrics.PodMetrics
		wantRes    *types.ProfileRunResult
		isTieBreak bool // non-deterministic tie breaker cases
		err        bool
	}{
		{
			name:   "selects correct pod : podB",
			scorer: scorer.NewSessionAffinity(),
			req: &types.LLMRequest{
				Headers: map[string]string{"x-session-token": validSessionTokenForPodB},
			},
			input: []backendmetrics.PodMetrics{podA, podB},
			wantRes: &types.ProfileRunResult{
				TargetPod: &types.ScoredPod{
					Pod:   wantPodB,
					Score: 1.0,
				},
			},
		},
		{
			name:   "no session token",
			scorer: scorer.NewSessionAffinity(),
			req: &types.LLMRequest{
				Headers: map[string]string{},
			},
			// both pods get score 0, assumes picker selects random pod acc to tie breaker logic
			input:      []backendmetrics.PodMetrics{podA, podB},
			isTieBreak: true,
		},
		{
			name:   "invalid session token",
			scorer: scorer.NewSessionAffinity(),
			req: &types.LLMRequest{
				Headers: map[string]string{"x-session-token": "garbage-token"},
			},
			// expect same behavior as no session token: a tie breaker
			input:      []backendmetrics.PodMetrics{podA, podB},
			isTieBreak: true,
		},
		{
			name:   "no pods available returns error",
			scorer: scorer.NewSessionAffinity(),
			req:    &types.LLMRequest{},
			input:  []backendmetrics.PodMetrics{},
			err:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schedulerProfile := framework.NewSchedulerProfile().
				WithScorers(framework.NewWeightedScorer(test.scorer, 1)).
				WithPicker(picker.NewMaxScorePicker())

			got, err := schedulerProfile.Run(context.Background(), test.req, nil, types.ToSchedulerPodMetrics(test.input))

			if test.err != (err != nil) {
				t.Errorf("Unexpected error (-want +got): want %v, got %v", test.err, err)
			}

			if test.err {
				return
			}

			if got == nil || got.TargetPod == nil {
				t.Fatalf("Unexpected target pod in tie (-want +got): want non-nil, got nil")
			}

			gotScoredPod := got.TargetPod.(*types.ScoredPod)

			if test.isTieBreak {
				if gotScoredPod.Score != 0.0 {
					t.Errorf("Unexpected score in tie (-want +got): want %f, got %f", 0.0, gotScoredPod.Score)
				}

				chosenPodName := gotScoredPod.GetPod().NamespacedName.String()
				wantPodAName := wantPodA.NamespacedName.String()
				wantPodBName := wantPodB.NamespacedName.String()

				if chosenPodName != wantPodAName && chosenPodName != wantPodBName {
					t.Errorf("Unexpected chosen pod (-want one of +got): want [%s, %s], got %s", wantPodAName, wantPodBName, chosenPodName)
				}
			} else {
				// for deterministic cases, we can do a direct comparison
				if diff := cmp.Diff(test.wantRes, got); diff != "" {
					t.Errorf("Unexpected output (-want +got): %v", diff)
				}
			}

		})
	}

}

func TestSessionAffinity_PostResponse(t *testing.T) {

	targetPod := &backend.Pod{
		NamespacedName: k8stypes.NamespacedName{Name: "pod1"},
		Address:        "1.2.3.4",
	}

	// expected token to be set in response header
	wantToken := base64.StdEncoding.EncodeToString([]byte(targetPod.NamespacedName.String()))

	tests := []struct {
		name            string
		initialResponse *requestcontrol.Response
		targetPod       *backend.Pod
		wantHeaders     map[string]string
		shouldPanic     bool
	}{
		{
			name:            "standard case with existing headers map",
			initialResponse: &requestcontrol.Response{RequestId: "req-1", Headers: make(map[string]string)},
			targetPod:       targetPod,
			wantHeaders:     map[string]string{"x-session-token": wantToken},
			shouldPanic:     false,
		},
		{
			name:            "response with nil headers map",
			initialResponse: &requestcontrol.Response{RequestId: "req-2", Headers: nil},
			targetPod:       targetPod,
			wantHeaders:     map[string]string{"x-session-token": wantToken},
			shouldPanic:     false,
		},
		{
			name:            "nil targetPod should do nothing",
			initialResponse: &requestcontrol.Response{RequestId: "req-3", Headers: make(map[string]string)},
			targetPod:       nil,
			wantHeaders:     map[string]string{},
			shouldPanic:     false,
		},
		{
			name:            "nil response should do nothing",
			initialResponse: nil,
			targetPod:       targetPod,
			wantHeaders:     nil,
			shouldPanic:     false,
		},
	}

	s := scorer.NewSessionAffinity()
	ctx := context.Background()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			didPanic := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						didPanic = true
					}
				}()

				s.PostResponse(ctx, nil, test.initialResponse, test.targetPod)
			}()

			if didPanic != test.shouldPanic {
				t.Errorf("Unexpected panic status (-want +got): -%v +%v", test.shouldPanic, didPanic)
			}
			if test.shouldPanic {
				return
			}

			if test.initialResponse != nil {
				if diff := cmp.Diff(test.wantHeaders, test.initialResponse.Headers); diff != "" {
					t.Errorf("Unexpected output (-want +got): %v", diff)
				}
			}
		})
	}
}
