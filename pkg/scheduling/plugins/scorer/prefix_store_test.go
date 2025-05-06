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

	"github.com/neuralmagic/llm-d-inference-scheduler/pkg/scheduling/plugins/scorer"
)

// TestBasicPrefixOperations tests the basic functionality of adding and finding prefixes
func TestBasicPrefixOperations(t *testing.T) {
	ctx := context.TODO()
	_ = log.IntoContext(ctx, logr.New(log.NullLogSink{}))

	config := scorer.DefaultPrefixStoreConfig()
	config.BlockSize = 5 // set small chunking for testing
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
