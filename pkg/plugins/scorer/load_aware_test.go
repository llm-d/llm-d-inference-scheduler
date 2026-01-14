package scorer_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer" // Import config for thresholds
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

func TestLoadBasedScorer(t *testing.T) {
	podA := &types.EndpointMetrics{
		EndpointMetadata: &datalayer.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		Metrics: &datalayer.Metrics{
			WaitingQueueSize: 2,
		},
	}
	podB := &types.EndpointMetrics{
		EndpointMetadata: &datalayer.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-b"}},
		Metrics: &datalayer.Metrics{
			WaitingQueueSize: 0,
		},
	}
	podC := &types.EndpointMetrics{
		EndpointMetadata: &datalayer.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: "pod-c"}},
		Metrics: &datalayer.Metrics{
			WaitingQueueSize: 15,
		},
	}

	tests := []struct {
		name       string
		scorer     framework.Scorer
		req        *types.LLMRequest
		input      []types.Endpoint
		wantScores map[types.Endpoint]float64
	}{
		{
			name:   "load based scorer",
			scorer: scorer.NewLoadAware(utils.NewTestContext(t), 10),
			req: &types.LLMRequest{
				TargetModel: "critical",
			},
			input: []types.Endpoint{
				podA, podB, podC,
			},
			wantScores: map[types.Endpoint]float64{
				podA: 0.4,
				podB: 0.5,
				podC: 0,
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
