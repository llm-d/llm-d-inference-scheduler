package scorer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"github.com/llm-d/llm-d-kv-cache-manager/pkg/kvcache"
	"github.com/llm-d/llm-d-kv-cache-manager/pkg/kvcache/kvblock"
	"github.com/llm-d/llm-d-kv-cache-manager/pkg/kvcache/kvevents"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

func TestPrefixCacheTracking_Score(t *testing.T) {
	testcases := []struct {
		name                string
		pods                []types.Pod
		request             *types.LLMRequest
		kvBlockData         map[kvblock.Key][]kvblock.PodEntry // KV-blocks to populate in the index
		wantScoresByAddress map[string]float64                 // Use address as key instead of Pod objects
	}{
		{
			name: "nil request",
			pods: []types.Pod{
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
				},
			},
			request:             nil,
			kvBlockData:         nil,
			wantScoresByAddress: make(map[string]float64), // empty map instead of nil
		},
		{
			name: "empty request body",
			pods: []types.Pod{
				&types.PodMetrics{
					Pod: &backend.Pod{
						NamespacedName: k8stypes.NamespacedName{Name: "pod-a"},
						Address:        "10.0.0.1:8080",
					},
				},
			},
			request: &types.LLMRequest{
				RequestId:   "test-request-1",
				TargetModel: "test-model",
				Body:        nil,
			},
			kvBlockData:         nil,                      // no kv-blocks in index
			wantScoresByAddress: make(map[string]float64), // empty map instead of nil
		},
		{
			name: "test normalized scores with different kv-block hits",
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
				RequestId:   "test-request-1",
				TargetModel: "test-model",
				Body: &types.LLMRequestBody{
					Completions: &types.CompletionsRequest{
						Prompt: "hello world",
					},
				},
			},
			// Populate kvblock.Index with blocks such that:
			// - block1 exists on pod-a only (10 hits for pod-a)
			// - block2 exists on pod-a and pod-b (10 more hits for pod-a, 10 for pod-b)
			// - block3 exists on all pods (10 more hits each)
			// Total: pod-a=30, pod-b=20, pod-c=10 -> normalized to 1.0, 0.5, 0.0
			kvBlockData: map[kvblock.Key][]kvblock.PodEntry{
				{ModelName: "test-model", ChunkHash: 1}: {
					{PodIdentifier: "10.0.0.1:8080"},
				},
				{ModelName: "test-model", ChunkHash: 2}: {
					{PodIdentifier: "10.0.0.1:8080"},
					{PodIdentifier: "10.0.0.2:8080"},
				},
				{ModelName: "test-model", ChunkHash: 3}: {
					{PodIdentifier: "10.0.0.1:8080"},
					{PodIdentifier: "10.0.0.2:8080"},
					{PodIdentifier: "10.0.0.3:8080"},
				},
			},
			wantScoresByAddress: map[string]float64{
				"10.0.0.1:8080": 1.0, // 30 hits -> (30-10)/(30-10) = 1.0
				"10.0.0.2:8080": 0.5, // 20 hits -> (20-10)/(30-10) = 0.5
				"10.0.0.3:8080": 0.0, // 10 hits -> (10-10)/(30-10) = 0.0
			},
		},
	}

	for _, tt := range testcases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			d, _ := os.Getwd()
			modelDir := filepath.Join(d, "/testdata")

			kvcacheConfig, err := kvcache.NewDefaultConfig()
			kvcacheConfig.TokenizersPoolConfig.WorkersCount = 1
			kvcacheConfig.TokenizersPoolConfig.LocalTokenizerConfig.AutoDiscoveryDir = modelDir
			kvcacheConfig.TokenizersPoolConfig.HFTokenizerConfig.Enabled = false
			kvcacheConfig.TokenizersPoolConfig.HFTokenizerConfig.TokenizersCacheDir = "./build/tokenizers"
			require.NoError(t, err)

			prefixCacheScorer, err := scorer.New(ctx, scorer.PrecisePrefixCachePluginConfig{
				IndexerConfig:  kvcacheConfig,
				KVEventsConfig: kvevents.DefaultConfig(),
			})
			require.NoError(t, err)
			require.NotNil(t, prefixCacheScorer)

			// Populate the kvblock.Index with test data
			if tt.kvBlockData != nil {
				kvBlockIndex := prefixCacheScorer.KVBlockIndex()
				for key, entries := range tt.kvBlockData {
					keys := []kvblock.Key{key}
					err := kvBlockIndex.Add(ctx, keys, entries)
					require.NoError(t, err)
				}
			}

			got := prefixCacheScorer.Score(ctx, nil, tt.request, tt.pods)

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
