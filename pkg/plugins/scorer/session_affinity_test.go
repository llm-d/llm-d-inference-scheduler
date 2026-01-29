package scorer_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/google/go-cmp/cmp"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

func TestSessionAffinity_Score(t *testing.T) {
	endpointA := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		&datalayer.Metrics{},
		nil,
	)
	endpointB := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-b"}},
		&datalayer.Metrics{},
		nil,
	)

	inputEndpoints := []scheduling.Endpoint{endpointA, endpointB}

	// valid session token for endpointB
	validSessionTokenForEndpointB := base64.StdEncoding.EncodeToString([]byte(endpointB.GetMetadata().NamespacedName.String()))

	sessionAffinityScorer := scorer.NewSessionAffinity()

	tests := []struct {
		name       string
		req        *scheduling.LLMRequest
		input      []scheduling.Endpoint
		wantScores map[scheduling.Endpoint]float64
	}{
		{
			name: "selects correct endpoint : endpointB",
			req: &scheduling.LLMRequest{
				Headers: map[string]string{"x-session-token": validSessionTokenForEndpointB},
			},
			input: inputEndpoints,
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 0.0,
				endpointB: 1.0,
			},
		},
		{
			name: "no session token",
			req: &scheduling.LLMRequest{
				Headers: map[string]string{},
			},
			// both endpoints get score 0.0
			input: inputEndpoints,
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 0.0,
				endpointB: 0.0,
			},
		},
		{
			name: "invalid session token",
			req: &scheduling.LLMRequest{
				Headers: map[string]string{"x-session-token": "garbage-token"},
			},
			// expect same behavior as no session token
			input: inputEndpoints,
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 0.0,
				endpointB: 0.0,
			},
		},
		{
			name:  "no endpoints available",
			req:   &scheduling.LLMRequest{},
			input: []scheduling.Endpoint{},
			// returns empty score map
			wantScores: map[scheduling.Endpoint]float64{},
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

func TestSessionAffinity_ResponseComplete(t *testing.T) {

	targetEndpoint := &fwkdl.EndpointMetadata{
		NamespacedName: k8stypes.NamespacedName{Name: "pod1"},
		Address:        "1.2.3.4",
	}

	// expected token to be set in response header
	wantToken := base64.StdEncoding.EncodeToString([]byte(targetEndpoint.NamespacedName.String()))

	tests := []struct {
		name            string
		initialResponse *requestcontrol.Response
		targetPod       *fwkdl.EndpointMetadata
		wantHeaders     map[string]string
	}{
		{
			name:            "standard case with existing headers map",
			initialResponse: &requestcontrol.Response{RequestId: "req-1", Headers: make(map[string]string)},
			targetPod:       targetEndpoint,
			wantHeaders:     map[string]string{"x-session-token": wantToken},
		},
		{
			name:            "response with nil headers map",
			initialResponse: &requestcontrol.Response{RequestId: "req-2", Headers: nil},
			targetPod:       targetEndpoint,
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
	ctx := utils.NewTestContext(t)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s.ResponseComplete(ctx, nil, test.initialResponse, test.targetPod)

			if diff := cmp.Diff(test.wantHeaders, test.initialResponse.Headers); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}
