package scorer_test

import (
	"context"
	"math/rand"
	"strconv"
	"testing"
	"time"

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
				pod1: 1.0, // Pod1 matches with weight
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

			s := scorer.NewPrefixAwareScorer(ctx, config)

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
				Prompt:      tt.prompt,
				TargetModel: tt.modelName,
			}, nil, []types.Pod{})

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

func TestPrefixAwareScorerProfiling(t *testing.T) {
	const testName = "profiling_test"
	const modelName = "test1" // store contains single cache for this model
	const nPodsTotal = 200
	const nPodsInStore = 100 // number of chunks stored for pod is proportional to the pod number

	ctx := context.Background()
	logger := log.FromContext(ctx)
	ctx = log.IntoContext(ctx, logger)

	name2Pod := createPods(nPodsTotal)
	config := scorer.DefaultPrefixStoreConfig()
	text := generateNonRepeatingText(config.BlockSize * nPodsInStore)
	t.Run(testName, func(t *testing.T) {
		start := time.Now() // record start time
		config := scorer.DefaultPrefixStoreConfig()
		s := scorer.NewPrefixAwareScorer(ctx, config)
		for i := range nPodsInStore {
			prompt := text[0 : (i+1)*config.BlockSize-1]
			err := s.GetPrefixStore().AddEntry(modelName, prompt, &name2Pod["pod"+strconv.Itoa(i)].NamespacedName)
			if err != nil {
				t.Errorf("Failed to add entry to prefix store: %v", err)
			}
		}
		sCtx := types.NewSchedulingContext(ctx, &types.LLMRequest{
			Prompt:      text,
			TargetModel: modelName,
		}, nil, []types.Pod{})

		// Score pods
		pods := make([]types.Pod, 0, len(name2Pod))
		for _, v := range name2Pod {
			pods = append(pods, v)
		}

		scores := s.Score(sCtx, pods)

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
			Metrics: &backendmetrics.Metrics{},
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
