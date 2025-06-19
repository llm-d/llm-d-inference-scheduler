package scorer_test

import (
	"testing"

	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

// TestBasicPrefixOperations tests the basic functionality of adding and finding prefixes
func TestBasicPrefixOperations(t *testing.T) {
	config := scorer.DefaultPrefixStoreConfig()
	config.CacheBlockSize = 5 // set small chunking for testing
	store := scorer.NewPrefixStore(config)

	podName := k8stypes.NamespacedName{
		Name:      "pod1",
		Namespace: "default",
	}

	// Test adding a prefix
	err := store.AddEntry("model1", "hello", &podName)
	if err != nil {
		t.Errorf("Failed to add prefix: %v", err)
	}

	// Test finding the exact prefix
	scores := store.FindMatchingPods("hello", "model1")
	if _, ok := scores[podName.String()]; !ok {
		t.Errorf("Expected pod %v, scores %v", podName, scores)
	}

	// Test finding with a longer prefix
	scores = store.FindMatchingPods("hello world", "model1")
	if _, ok := scores[podName.String()]; !ok {
		t.Errorf("Expected pod %v, scores %v", podName, scores)
	}
}
