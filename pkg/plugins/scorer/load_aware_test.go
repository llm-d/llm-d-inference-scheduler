package scorer_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics" // Import config for thresholds
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

func TestLoadBasedScorer(t *testing.T) {
	podA := &types.PodMetrics{
		Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
		MetricsState: &backendmetrics.MetricsState{
			WaitingQueueSize: 2,
		},
	}
	podB := &types.PodMetrics{
		Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-b"}},
		MetricsState: &backendmetrics.MetricsState{
			WaitingQueueSize: 0,
		},
	}
	podC := &types.PodMetrics{
		Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-c"}},
		MetricsState: &backendmetrics.MetricsState{
			WaitingQueueSize: 15,
		},
	}

	tests := []struct {
		name       string
		scorer     framework.Scorer
		req        *types.LLMRequest
		input      []types.Pod
		wantScores map[types.Pod]float64
	}{
		{
			name:   "load based scorer",
			scorer: scorer.NewLoadAware(context.Background(), 10),
			req: &types.LLMRequest{
				TargetModel: "critical",
			},
			input: []types.Pod{
				podA, podB, podC,
			},
			wantScores: map[types.Pod]float64{
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
