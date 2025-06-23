package scorer_test

import (
	"context"
	"math/rand"
	"strconv"
	"testing"
	"time"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
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
		MetricsState: &backendmetrics.MetricsState{},
	}
	pod2 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{
				Name:      "pod2",
				Namespace: "default",
			},
		},
		MetricsState: &backendmetrics.MetricsState{},
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
				pod1: 1.0, // Pod1 matches with weight
				pod2: 0.0, // Pod2 doesn't match
			}, // Weight affects score
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Reset prefix store for each test
			config := scorer.DefaultPrefixStoreConfig()
			config.CacheBlockSize = 5 // set small chunking for testing

			s := scorer.NewPrefixAwareScorer(context.Background(), config)

			// Add prefix if specified
			if test.prefixToAdd != "" {
				err := s.GetPrefixStore().AddEntry(test.prefixModel, test.prefixToAdd, &test.podToAdd)
				if err != nil {
					t.Fatalf("Failed to add prefix: %v", err)
				}
			}

			request := &types.LLMRequest{
				Prompt:      test.prompt,
				TargetModel: test.modelName,
			}

			// Score pods
			pods := []types.Pod{pod1, pod2}
			scores := s.Score(context.Background(), nil, request, pods)

			for p, score := range scores {
				if score != test.expectedScores[p] {
					t.Errorf("Pod %v: expected score %v, got %v", p, test.expectedScores[p], score)
				}
			}
		})
	}
}

func TestPrefixAwareScorerProfiling(t *testing.T) {
	const testName = "profiling_test"
	const modelName = "test1" // store contains single cache for this model
	const nPodsTotal = 200
	const nPodsInStore = 100 // number of chunks stored for pod is proportional to the pod number

	name2Pod := createPods(nPodsTotal)
	config := scorer.DefaultPrefixStoreConfig()
	text := generateNonRepeatingText(config.CacheBlockSize * nPodsInStore)
	t.Run(testName, func(t *testing.T) {
		start := time.Now() // record start time
		config := scorer.DefaultPrefixStoreConfig()
		s := scorer.NewPrefixAwareScorer(context.Background(), config)
		for i := range nPodsInStore {
			prompt := text[0 : (i+1)*config.CacheBlockSize-1]
			err := s.GetPrefixStore().AddEntry(modelName, prompt, &name2Pod["pod"+strconv.Itoa(i)].NamespacedName)
			if err != nil {
				t.Errorf("Failed to add entry to prefix store: %v", err)
			}
		}
		request := &types.LLMRequest{
			Prompt:      text,
			TargetModel: modelName,
		}
		// Score pods
		pods := make([]types.Pod, 0, len(name2Pod))
		for _, v := range name2Pod {
			pods = append(pods, v)
		}

		scores := s.Score(context.Background(), nil, request, pods)

		highestScore := scores[name2Pod["pod"+strconv.Itoa(nPodsInStore-1)]]
		if highestScore < 0.99 {
			t.Error("Failed to calculate scores")
		}

		// use 'elapsed' time when built-in profiler is not suitable because of short time periods
		elapsed := time.Since(start) // calculate duration
		t.Log("Time spent in microsec: " + strconv.FormatInt(elapsed.Microseconds(), 10))
	})

}

func createPods(nPods int) map[string]*types.PodMetrics {
	res := map[string]*types.PodMetrics{}
	for i := range nPods {
		pShortName := "pod" + strconv.Itoa(i)
		pod := &types.PodMetrics{
			Pod: &backend.Pod{
				NamespacedName: k8stypes.NamespacedName{
					Name:      pShortName,
					Namespace: "default",
				},
			},
			MetricsState: &backendmetrics.MetricsState{},
		}
		res[pShortName] = pod
	}
	return res
}

func generateNonRepeatingText(length int) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	chars := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 .,!?;:-_[]{}()<>|@#$%^&*+=")

	result := make([]rune, length)
	for i := range result {
		result[i] = chars[r.Intn(len(chars))]
	}
	return string(result)
}
