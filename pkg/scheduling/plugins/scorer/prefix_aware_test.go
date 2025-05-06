/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scorer_test

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/scheduling/plugins/scorer"
)

func TestPrefixAwareScorer(t *testing.T) {
	// Create test pods
	pod1 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{
				Name:      "pod1",
				Namespace: "default",
			},
		},
		Metrics: &backendmetrics.Metrics{},
	}
	pod2 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{
				Name:      "pod2",
				Namespace: "default",
			},
		},
		Metrics: &backendmetrics.Metrics{},
	}

	tests := []struct {
		name           string
		weight         float64
		prompt         string
		modelName      string
		prefixToAdd    string
		podToAdd       k8stypes.NamespacedName
		prefixModel    string // Model name to use when adding the prefix
		expectedScores map[types.Pod]float64
	}{
		{
			name:           "no prompt",
			weight:         1.0,
			prompt:         "",
			modelName:      "model1",
			prefixToAdd:    "hello",
			podToAdd:       pod1.NamespacedName,
			prefixModel:    "model1",
			expectedScores: map[types.Pod]float64{}, // No prompt means zero scores
		},
		{
			name:        "exact prefix match",
			weight:      1.0,
			prompt:      "hello world",
			modelName:   "model1",
			prefixToAdd: "hello",
			podToAdd:    pod1.NamespacedName,
			prefixModel: "model1",
			expectedScores: map[types.Pod]float64{
				pod1: 1.0,
				pod2: 0.0,
			}, // pod1 matches, pod2 doesn't
		},
		{
			name:           "no prefix match",
			weight:         1.0,
			prompt:         "goodbye",
			modelName:      "model1",
			prefixToAdd:    "hello",
			podToAdd:       pod1.NamespacedName,
			prefixModel:    "model1",
			expectedScores: map[types.Pod]float64{}, // No matching prefix
		},
		{
			name:           "different model name",
			weight:         1.0,
			prompt:         "hello world",
			modelName:      "model2", // Try to find with model2
			prefixToAdd:    "hello",
			podToAdd:       pod1.NamespacedName,
			prefixModel:    "model1",                // But prefix was added with model1
			expectedScores: map[types.Pod]float64{}, // Model name mismatch should result in no match
		},
		{
			name:        "custom weight",
			weight:      0.5,
			prompt:      "hello world",
			modelName:   "model1",
			prefixToAdd: "hello",
			podToAdd:    pod1.NamespacedName,
			prefixModel: "model1",
			expectedScores: map[types.Pod]float64{
				pod1: 0.5, // Pod1 matches with weight
				pod2: 0.0, // Pod2 doesn't match
			}, // Weight affects score
		},
	}

	ctx := context.TODO()
	_ = log.IntoContext(ctx, logr.New(log.NullLogSink{}))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset prefix store for each test
			config := scorer.DefaultPrefixStoreConfig()
			config.BlockSize = 5 // set small chunking for testing

			s := scorer.NewPrefixAwareScorer(config)

			// Add prefix if specified
			if tt.prefixToAdd != "" {
				err := s.GetPrefixStore().AddEntry(tt.prefixModel,
					tt.prefixToAdd, &tt.podToAdd)
				if err != nil {
					t.Fatalf("Failed to add prefix: %v", err)
				}
			}

			// Create test context
			sCtx := types.NewSchedulingContext(ctx, &types.LLMRequest{
				Prompt:              tt.prompt,
				ResolvedTargetModel: tt.modelName,
			}, []types.Pod{})

			// Score pods
			pods := []types.Pod{pod1, pod2}
			scores := s.Score(sCtx, pods)

			for p, score := range scores {
				if score != tt.expectedScores[p] {
					t.Errorf("Pod %v: expected score %v, got %v", p, tt.expectedScores[p], score)
				}
			}
		})
	}
}
