package scorer_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	k8stypes "k8s.io/apimachinery/pkg/types" // Import config for thresholds
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

func TestLoadBasedScorer(t *testing.T) {
	endpointA := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		&datalayer.Metrics{
			WaitingQueueSize: 2,
		},
		nil,
	)
	endpointB := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-b"}},
		&datalayer.Metrics{
			WaitingQueueSize: 0,
		},
		nil,
	)
	endpointC := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-c"}},
		&datalayer.Metrics{
			WaitingQueueSize: 15,
		},
		nil,
	)

	tests := []struct {
		name       string
		scorer     scheduling.Scorer
		req        *scheduling.LLMRequest
		input      []scheduling.Endpoint
		wantScores map[scheduling.Endpoint]float64
	}{
		{
			name:   "load based scorer",
			scorer: scorer.NewLoadAware(utils.NewTestContext(t), 10),
			req: &scheduling.LLMRequest{
				TargetModel: "critical",
			},
			input: []scheduling.Endpoint{
				endpointA, endpointB, endpointC,
			},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 0.4,
				endpointB: 0.5,
				endpointC: 0,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.scorer.Score(context.Background(), nil, nil, test.input)

			if diff := cmp.Diff(test.wantScores, got); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}
