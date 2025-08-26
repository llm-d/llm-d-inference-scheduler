package filter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
)

func TestByLabelSelectorFactoryWithJSON(t *testing.T) {
	tests := []struct {
		testName   string
		pluginName string
		jsonParams string
		expectErr  bool
	}{
		{
			testName:   "simple matchLabels selector",
			pluginName: "nginx-selector",
			jsonParams: `{
				"matchLabels": {
					"app": "nginx",
					"version": "v1.0"
				}
			}`,
			expectErr: false,
		},
		{
			testName:   "complex selector with matchExpressions",
			pluginName: "complex-selector",
			jsonParams: `{
				"matchLabels": {
					"tier": "frontend"
				},
				"matchExpressions": [
					{
						"key": "environment",
						"operator": "In",
						"values": ["production", "staging"]
					},
					{
						"key": "deprecated",
						"operator": "DoesNotExist"
					}
				]
			}`,
			expectErr: false,
		},
		{
			testName:   "empty selector",
			pluginName: "empty-selector",
			jsonParams: `{}`,
			expectErr:  false,
		},
		{
			testName:   "matchExpressions only",
			pluginName: "expressions-only",
			jsonParams: `{
				"matchExpressions": [
					{
						"key": "component",
						"operator": "NotIn",
						"values": ["test", "debug"]
					}
				]
			}`,
			expectErr: false,
		},
		{
			testName:   "exists operator",
			pluginName: "exists-selector",
			jsonParams: `{
				"matchExpressions": [
					{
						"key": "release",
						"operator": "Exists"
					}
				]
			}`,
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			rawParams := json.RawMessage(tt.jsonParams)

			plugin, err := filter.ByLabelSelectorFactory(tt.pluginName, rawParams, nil)

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, plugin)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, plugin)
			}
		})
	}
}

func TestByLabelSelectorFactoryWithInvalidJSON(t *testing.T) {
	invalidTests := []struct {
		testName   string
		pluginName string
		jsonParams string
	}{
		{
			testName:   "invalid json syntax",
			pluginName: "invalid-json",
			jsonParams: `{"matchLabels": {"app": "nginx"`,
		},
		{
			testName:   "invalid operator",
			pluginName: "invalid-operator",
			jsonParams: `{
				"matchExpressions": [
					{
						"key": "app",
						"operator": "InvalidOperator",
						"values": ["nginx"]
					}
				]
			}`,
		},
	}

	for _, tt := range invalidTests {
		t.Run(tt.testName, func(t *testing.T) {
			rawParams := json.RawMessage(tt.jsonParams)

			plugin, err := filter.ByLabelSelectorFactory(tt.pluginName, rawParams, nil)

			assert.Error(t, err)
			assert.Nil(t, plugin)
		})
	}
}

func TestByLabelSelectorFiltering(t *testing.T) {
	pods := []types.Pod{
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "nginx-1"},
			"10.0.0.1",
			map[string]string{
				"app":     "nginx",
				"version": "v1.0",
				"tier":    "frontend",
			}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "nginx-2"},
			"10.0.0.2",
			map[string]string{
				"app":     "nginx",
				"version": "v1.1",
				"tier":    "frontend",
			}),
		createPod(k8stypes.NamespacedName{Namespace: "kube-system", Name: "coredns-1"},
			"10.0.0.3",
			map[string]string{
				"app":  "coredns",
				"tier": "system",
			}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "redis-1"},
			"10.0.0.4",
			map[string]string{
				"app":        "redis",
				"tier":       "backend",
				"deprecated": "true",
			}),
		createPod(k8stypes.NamespacedName{Namespace: "default", Name: "web-1"},
			"10.0.0.5",
			map[string]string{
				"app":         "web",
				"tier":        "frontend",
				"environment": "production",
			}),
	}

	tests := []struct {
		testName     string
		selectorJSON string
		expectedPods []string // pod names that should match
	}{
		{
			testName: "matchLabels - app nginx",
			selectorJSON: `{
				"matchLabels": {
					"app": "nginx"
				}
			}`,
			expectedPods: []string{"nginx-1", "nginx-2"},
		},
		{
			testName: "matchLabels - exact match",
			selectorJSON: `{
				"matchLabels": {
					"app": "nginx",
					"version": "v1.0"
				}
			}`,
			expectedPods: []string{"nginx-1"},
		},
		{
			testName: "matchExpressions - In operator",
			selectorJSON: `{
				"matchExpressions": [
					{
						"key": "tier",
						"operator": "In",
						"values": ["frontend", "backend"]
					}
				]
			}`,
			expectedPods: []string{"nginx-1", "nginx-2", "redis-1", "web-1"},
		},
		{
			testName: "matchExpressions - NotIn operator",
			selectorJSON: `{
				"matchExpressions": [
					{
						"key": "tier",
						"operator": "NotIn",
						"values": ["system"]
					}
				]
			}`,
			expectedPods: []string{"nginx-1", "nginx-2", "redis-1", "web-1"},
		},
		{
			testName: "matchExpressions - Exists operator",
			selectorJSON: `{
				"matchExpressions": [
					{
						"key": "deprecated",
						"operator": "Exists"
					}
				]
			}`,
			expectedPods: []string{"redis-1"},
		},
		{
			testName: "matchExpressions - DoesNotExist operator",
			selectorJSON: `{
				"matchExpressions": [
					{
						"key": "deprecated",
						"operator": "DoesNotExist"
					}
				]
			}`,
			expectedPods: []string{"nginx-1", "nginx-2", "coredns-1", "web-1"},
		},
		{
			testName: "combined matchLabels and matchExpressions",
			selectorJSON: `{
				"matchLabels": {
					"tier": "frontend"
				},
				"matchExpressions": [
					{
						"key": "environment",
						"operator": "Exists"
					}
				]
			}`,
			expectedPods: []string{"web-1"},
		},
		{
			testName:     "empty selector - matches all",
			selectorJSON: `{}`,
			expectedPods: []string{"nginx-1", "nginx-2", "coredns-1", "redis-1", "web-1"},
		},
		{
			testName: "no matches",
			selectorJSON: `{
				"matchLabels": {
					"app": "nonexistent"
				}
			}`,
			expectedPods: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			rawParams := json.RawMessage(tt.selectorJSON)
			plugin, err := filter.ByLabelSelectorFactory("test-selector", rawParams, nil)
			require.NoError(t, err)
			require.NotNil(t, plugin)

			blf, ok := plugin.(*filter.ByLabelSelector)
			require.True(t, ok, "plugin should be of type *ByLabelSelector")

			ctx := context.Background()
			filteredPods := blf.Filter(ctx, nil, nil, pods)

			var actualPodNames []string
			for _, pod := range filteredPods {
				actualPodNames = append(actualPodNames, pod.GetPod().NamespacedName.Name)
			}

			assert.ElementsMatch(t, tt.expectedPods, actualPodNames,
				"filtered pods should match expected pods")
			assert.Len(t, filteredPods, len(tt.expectedPods),
				"filtered pods count should match expected count")
		})
	}
}

func TestByLabelSelectorFilterEdgeCases(t *testing.T) {
	rawParams := json.RawMessage(`{"matchLabels": {"app": "test"}}`)
	plugin, err := filter.ByLabelSelectorFactory("test-selector", rawParams, nil)
	require.NoError(t, err)

	blf, ok := plugin.(*filter.ByLabelSelector)
	require.True(t, ok)

	ctx := context.Background()

	t.Run("empty pods slice", func(t *testing.T) {
		result := blf.Filter(ctx, nil, nil, []types.Pod{})
		assert.Empty(t, result)
	})

	t.Run("nil pods slice", func(t *testing.T) {
		result := blf.Filter(ctx, nil, nil, nil)
		assert.Empty(t, result)
	})

	t.Run("pods with nil labels", func(t *testing.T) {
		pods := []types.Pod{createPod(k8stypes.NamespacedName{Name: "pod-1"}, "10.0.0.1", nil)}
		result := blf.Filter(ctx, nil, nil, pods)
		assert.Empty(t, result, "pod with nil labels should not match")
	})

	t.Run("pods with empty labels", func(t *testing.T) {
		pods := []types.Pod{createPod(k8stypes.NamespacedName{Name: "pod-1"}, "10.0.0.1", map[string]string{})}
		result := blf.Filter(ctx, nil, nil, pods)
		assert.Empty(t, result, "pod with empty labels should not match")
	})
}

// Example for setting Prefill/Decode roles using a LabelSelector filter.
// Definition of labels is based on https://github.com/llm-d/llm-d-inference-scheduler/issues/220.
func ExamplePrefillDecodeRolesInLWS() {
	decodeLeaderJSON := json.RawMessage(`{ "matchLabels": { "leaderworkerset.sigs.k8s.io/worker-index": "0" } }`)
	plugin, _ := filter.ByLabelSelectorFactory("decode-role", decodeLeaderJSON, nil)
	decodeLeader, _ := plugin.(*filter.ByLabelSelector)

	decodeFollowerJSON := json.RawMessage(`{"matchExpressions": [{ 
		"key": "leaderworkerset.sigs.k8s.io/worker-index",
      	"operator": "NotIn",
      	"values": ["0"]
    }]}`)
	plugin, _ = filter.ByLabelSelectorFactory("ignore-decode-workers", decodeFollowerJSON, nil)
	decodeFollower, _ := plugin.(*filter.ByLabelSelector)

	prefillWorkerJSON := json.RawMessage(`{"matchExpressions": [{
    	"key": "leaderworkerset.sigs.k8s.io/worker-index",
      	"operator": "DoesNotExist"
    }]}`)
	plugin, _ = filter.ByLabelSelectorFactory("prefill-role", prefillWorkerJSON, nil)
	prefillworker, _ := plugin.(*filter.ByLabelSelector)

	pods := []types.Pod{createPod(k8stypes.NamespacedName{Namespace: "default", Name: "vllm"},
		"10.0.0.1",
		map[string]string{
			"app.kubernetes.io/component":              "vllm-worker",
			"app.kubernetes.io/name":                   "some-model",
			"leaderworkerset.sigs.k8s.io/worker-index": "0",
		}),
	}

	name := ""

	for _, blf := range []*filter.ByLabelSelector{decodeLeader, decodeFollower, prefillworker} {
		filtered := PrefillDecodeRolesInLWS(blf, pods)
		if len(filtered) > 0 {
			name = blf.TypedName().Name
		}
	}
	if name != "" {
		fmt.Println("pod accepted by", name)
		// Output: pod accepted by decode-role
	}
}

// Helper functions
func createPod(nsn k8stypes.NamespacedName, ipaddr string, labels map[string]string) types.Pod {
	return &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: nsn,
			Address:        ipaddr,
			Labels:         labels,
		},
		MetricsState: &backendmetrics.MetricsState{},
	}
}

func PrefillDecodeRolesInLWS(blf *filter.ByLabelSelector, pods []types.Pod) []types.Pod {
	return blf.Filter(context.Background(), nil, nil, pods)
}
