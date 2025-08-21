package scorer_test

import (
	"context"
	"errors"

	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// mockPodScorer is a mock implementation of the scorer.PodScorer interface for testing.
type mockPodScorer struct {
	scores map[string]int
	err    error
}

func (m *mockPodScorer) GetPodScores(_ context.Context, _, _ string, _ []string) (map[string]int, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.scores, nil
}

func TestPrefixCacheTracking_Score(t *testing.T) {
	testcases := []struct {
		name                string
		pods                []types.Pod
		request             *types.LLMRequest
		mockScores          map[string]int
		mockError           error
		wantScoresByAddress map[string]float64 // Use address as key instead of Pod objects
	}{
		{
			name: "test normalized scores",
			pods: []types.Pod{
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
					MetricsState: &backendmetrics.MetricsState{
						WaitingQueueSize: 0,
					},
				},
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-b"},
						Address:        "10.0.0.2:8080",
					},
					MetricsState: &backendmetrics.MetricsState{
						WaitingQueueSize: 1,
					},
				},
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-c"},
						Address:        "10.0.0.3:8080",
					},
					MetricsState: &backendmetrics.MetricsState{
						WaitingQueueSize: 2,
					},
				},
			},
			request: &types.LLMRequest{
				TargetModel: "gpt-4",
				Prompt:      "what is meaning of life?",
			},
			mockScores: map[string]int{
				"10.0.0.1:8080": 10,
				"10.0.0.2:8080": 20,
				"10.0.0.3:8080": 30,
			},
			wantScoresByAddress: map[string]float64{
				"10.0.0.1:8080": 0.0, // (10-10)/(30-10) = 0.0
				"10.0.0.2:8080": 0.5, // (20-10)/(30-10) = 0.5
				"10.0.0.3:8080": 1.0, // (30-10)/(30-10) = 1.0
			},
		},
		{
			name: "test nil request",
			pods: []types.Pod{
				&types.PodMetrics{
					Pod: &backend.Pod{NamespacedName: k8stypes.NamespacedName{Name: "pod-a"}},
				},
			},
			request:             nil,
			wantScoresByAddress: make(map[string]float64), // empty map instead of nil
		},
		{
			name: "test pod scorer error",
			pods: []types.Pod{
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
				},
			},
			request: &types.LLMRequest{
				TargetModel: "gpt-4",
				Prompt:      "test prompt",
			},
			mockError:           errors.New("test error"),
			wantScoresByAddress: make(map[string]float64), // empty map instead of nil
		},
	}

	for _, tt := range testcases {
		t.Run(tt.name, func(t *testing.T) {
			mockScorer := &mockPodScorer{
				scores: tt.mockScores,
				err:    tt.mockError,
			}
			prefixCacheScorer := scorer.NewWithPodScorer(mockScorer)
			require.NotNil(t, prefixCacheScorer)
			got := prefixCacheScorer.Score(context.Background(), nil, tt.request, tt.pods)
			// Convert the result to address-based map for easier comparison
			gotByAddress := make(map[string]float64)
			for pod, score := range got {
				if podMetrics, ok := pod.(*types.PodMetrics); ok && podMetrics.GetPod() != nil {
					gotByAddress[podMetrics.GetPod().Address] = score
				}
			}
			if diff := cmp.Diff(tt.wantScoresByAddress, gotByAddress); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}
		})
	}
}
