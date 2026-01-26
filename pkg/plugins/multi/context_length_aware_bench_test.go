package multi

import (
	"context"
	"fmt"
	"testing"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// BenchmarkContextLengthAwareFilter benchmarks the filter operation
func BenchmarkContextLengthAwareFilter(b *testing.B) {
	ctx := context.Background()
	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: true,
	}
	plugin := NewContextLengthAware("bench-filter", params)
	request := &types.LLMRequest{RequestId: "bench-request"}

	// Create a realistic set of pods with various range configurations
	pods := []types.Pod{
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod-1"}, "10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "0-2048"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod-2"}, "10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "2048-8192"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod-3"}, "10.0.0.3",
			map[string]string{DefaultContextLengthLabel: "8192-32768"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod-4"}, "10.0.0.4",
			map[string]string{DefaultContextLengthLabel: "0-1024,4096-16384"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod-5"}, "10.0.0.5",
			map[string]string{}),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = plugin.Filter(ctx, nil, request, pods)
	}
}

// BenchmarkContextLengthAwareScore benchmarks the score operation
func BenchmarkContextLengthAwareScore(b *testing.B) {
	ctx := context.Background()
	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: false,
	}
	plugin := NewContextLengthAware("bench-scorer", params)
	request := &types.LLMRequest{RequestId: "bench-request"}

	pods := []types.Pod{
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod-1"}, "10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "0-2048"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod-2"}, "10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "2048-8192"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod-3"}, "10.0.0.3",
			map[string]string{DefaultContextLengthLabel: "8192-32768"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod-4"}, "10.0.0.4",
			map[string]string{DefaultContextLengthLabel: "0-1024,4096-16384"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "pod-5"}, "10.0.0.5",
			map[string]string{}),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = plugin.Score(ctx, nil, request, pods)
	}
}

// BenchmarkContextLengthAwareScore_LargePodSet benchmarks scoring with many pods
func BenchmarkContextLengthAwareScore_LargePodSet(b *testing.B) {
	ctx := context.Background()
	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: false,
	}
	plugin := NewContextLengthAware("bench-scorer", params)
	request := &types.LLMRequest{RequestId: "bench-request"}

	// Create 100 pods with various configurations
	pods := make([]types.Pod, 100)
	for i := 0; i < 100; i++ {
		var label map[string]string
		switch i % 5 {
		case 0:
			label = map[string]string{DefaultContextLengthLabel: "0-2048"}
		case 1:
			label = map[string]string{DefaultContextLengthLabel: "2048-8192"}
		case 2:
			label = map[string]string{DefaultContextLengthLabel: "8192-32768"}
		case 3:
			label = map[string]string{DefaultContextLengthLabel: "0-1024,4096-16384"}
		case 4:
			label = map[string]string{} // no label
		}
		pods[i] = &types.PodMetrics{
			Pod: &backend.Pod{
				NamespacedName: k8stypes.NamespacedName{
					Namespace: "default",
					Name:      fmt.Sprintf("pod-%d", i),
				},
				Address: "10.0.0.1",
				Labels:  label,
			},
			MetricsState: &backendmetrics.MetricsState{},
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = plugin.Score(ctx, nil, request, pods)
	}
}

// BenchmarkParseContextRanges benchmarks range parsing
func BenchmarkParseContextRanges(b *testing.B) {
	testCases := []struct {
		name     string
		rangeStr string
	}{
		{"single_range", "0-2048"},
		{"two_ranges", "0-2048,8192-16384"},
		{"four_ranges", "0-1024,1024-2048,2048-8192,8192-32768"},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = parseContextRanges(tc.rangeStr)
			}
		})
	}
}

// BenchmarkCalculateRangeScore benchmarks score calculation
func BenchmarkCalculateRangeScore(b *testing.B) {
	testCases := []struct {
		name   string
		ranges []contextRange
	}{
		{
			name:   "single_range",
			ranges: []contextRange{{min: 0, max: 2048}},
		},
		{
			name: "two_ranges",
			ranges: []contextRange{
				{min: 0, max: 2048},
				{min: 8192, max: 16384},
			},
		},
		{
			name: "four_ranges",
			ranges: []contextRange{
				{min: 0, max: 1024},
				{min: 1024, max: 2048},
				{min: 2048, max: 8192},
				{min: 8192, max: 32768},
			},
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			contextLength := 100 // test with a typical context length
			for i := 0; i < b.N; i++ {
				_ = calculateRangeScore(contextLength, tc.ranges)
			}
		})
	}
}

// BenchmarkEstimateContextLength benchmarks context length estimation
func BenchmarkEstimateContextLength(b *testing.B) {
	request := &types.LLMRequest{RequestId: "bench-request"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = estimateContextLength(request)
	}
}

// BenchmarkMatchesAnyRange benchmarks range matching
func BenchmarkMatchesAnyRange(b *testing.B) {
	ranges := []contextRange{
		{min: 0, max: 2048},
		{min: 2048, max: 8192},
		{min: 8192, max: 32768},
	}
	contextLength := 100

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = matchesAnyRange(contextLength, ranges)
	}
}

// BenchmarkFilterWithInvalidLabels benchmarks filtering with some invalid labels
func BenchmarkFilterWithInvalidLabels(b *testing.B) {
	ctx := context.Background()
	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: true,
	}
	plugin := NewContextLengthAware("bench-filter", params)
	request := &types.LLMRequest{RequestId: "bench-request"}

	pods := []types.Pod{
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "valid-1"}, "10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "0-2048"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "invalid-1"}, "10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "invalid-format"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "valid-2"}, "10.0.0.3",
			map[string]string{DefaultContextLengthLabel: "2048-8192"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "invalid-2"}, "10.0.0.4",
			map[string]string{DefaultContextLengthLabel: "not-a-range"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "valid-3"}, "10.0.0.5",
			map[string]string{DefaultContextLengthLabel: "8192-32768"}),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = plugin.Filter(ctx, nil, request, pods)
	}
}

// BenchmarkScoreWithInvalidLabels benchmarks scoring with some invalid labels
func BenchmarkScoreWithInvalidLabels(b *testing.B) {
	ctx := context.Background()
	params := &contextLengthAwareParams{
		Label:           DefaultContextLengthLabel,
		EnableFiltering: false,
	}
	plugin := NewContextLengthAware("bench-scorer", params)
	request := &types.LLMRequest{RequestId: "bench-request"}

	pods := []types.Pod{
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "valid-1"}, "10.0.0.1",
			map[string]string{DefaultContextLengthLabel: "0-2048"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "invalid-1"}, "10.0.0.2",
			map[string]string{DefaultContextLengthLabel: "invalid-format"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "valid-2"}, "10.0.0.3",
			map[string]string{DefaultContextLengthLabel: "2048-8192"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "invalid-2"}, "10.0.0.4",
			map[string]string{DefaultContextLengthLabel: "not-a-range"}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "valid-3"}, "10.0.0.5",
			map[string]string{DefaultContextLengthLabel: "8192-32768"}),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = plugin.Score(ctx, nil, request, pods)
	}
}
