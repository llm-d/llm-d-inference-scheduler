/*
Copyright 2026 The Kubernetes Authors.

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

package datalayer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/source/mocks"
)

func TestRuntimeConfigureWithNilExtractor(t *testing.T) {
	logger := newTestLogger(t)
	r := NewRuntime(1)

	cfg := &Config{
		Sources: []DataSourceConfig{
			{
				Plugin: &mocks.MetricsDataSource{},
				// all extractor slices nil — allowed.
			},
		},
	}

	err := r.Configure(cfg, false, "", logger)
	assert.NoError(t, err, "Configure should succeed with no extractors")
}

func TestRuntimeConfigureDuplicateGVKFails(t *testing.T) {
	logger := newTestLogger(t)
	r := NewRuntime(1)

	gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
	src1 := mocks.NewNotificationSource("test", "source1", gvk)
	src2 := mocks.NewNotificationSource("test", "source2", gvk)

	cfg := &Config{
		Sources: []DataSourceConfig{
			{Plugin: src1},
			{Plugin: src2},
		},
	}

	err := r.Configure(cfg, false, "", logger)
	assert.Error(t, err, "Configure should fail with duplicate GVK")
	assert.Contains(t, err.Error(), "duplicate", "Error should mention duplicate GVK")
}
