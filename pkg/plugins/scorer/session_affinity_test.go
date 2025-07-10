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
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

func TestSessionAffinity_Score(t *testing.T) {
	podA := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		MetricsState: &backendmetrics.MetricsState{},
	}
	podB := &types.PodMetrics{
		Pod:          &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-b"}},
		MetricsState: &backendmetrics.MetricsState{},
	}

	inputPods := []types.Pod{podA, podB}

	// valid session token for podB
	validSessionTokenForPodB := base64.StdEncoding.EncodeToString([]byte(podB.GetPod().NamespacedName.String()))

	sessionAffinityScorer := scorer.NewSessionAffinity()

	tests := []struct {
		name       string
		req        *types.LLMRequest
		input      []types.Pod
		wantScores map[types.Pod]float64
	}{
		{
			name: "selects correct pod : podB",
			req: &types.LLMRequest{
				Headers: map[string]string{"x-session-token": validSessionTokenForPodB},
			},
			input: inputPods,
			wantScores: map[types.Pod]float64{
				podA: 0.0,
				podB: 1.0,
			},
		},
		{
			name: "no session token",
			req: &types.LLMRequest{
				Headers: map[string]string{},
			},
			// both pods get score 0.0
			input: inputPods,
			wantScores: map[types.Pod]float64{
				podA: 0.0,
				podB: 0.0,
			},
		},
		{
			name: "invalid session token",
			req: &types.LLMRequest{
				Headers: map[string]string{"x-session-token": "garbage-token"},
			},
			// expect same behavior as no session token
			input: inputPods,
			wantScores: map[types.Pod]float64{
				podA: 0.0,
				podB: 0.0,
			},
		},
		{
			name:  "no pods available",
			req:   &types.LLMRequest{},
			input: []types.Pod{},
			// returns empty score map
			wantScores: map[types.Pod]float64{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotScores := sessionAffinityScorer.Score(context.Background(), nil, test.req, test.input)

			if diff := cmp.Diff(test.wantScores, gotScores); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
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
	}{
		{
			name:            "standard case with existing headers map",
			initialResponse: &requestcontrol.Response{RequestId: "req-1", Headers: make(map[string]string)},
			targetPod:       targetPod,
			wantHeaders:     map[string]string{"x-session-token": wantToken},
		},
		{
			name:            "response with nil headers map",
			initialResponse: &requestcontrol.Response{RequestId: "req-2", Headers: nil},
			targetPod:       targetPod,
			wantHeaders:     map[string]string{"x-session-token": wantToken},
		},
		{
			name:            "nil targetPod should do nothing",
			initialResponse: &requestcontrol.Response{RequestId: "req-3", Headers: make(map[string]string)},
			targetPod:       nil,
			wantHeaders:     map[string]string{},
		},
	}

	s := scorer.NewSessionAffinity()
	ctx := context.Background()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s.PostResponse(ctx, nil, test.initialResponse, test.targetPod)

			if diff := cmp.Diff(test.wantHeaders, test.initialResponse.Headers); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}
