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

package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	configapi "github.com/llm-d/llm-d-inference-scheduler/apix/config/v1alpha1"
	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	k8sdiscovery "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/discovery/k8s"
	runserver "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/server"
)

// stubDiscovery is a minimal EndpointDiscovery implementation for testing.
type stubDiscovery struct{}

func (s *stubDiscovery) TypedName() fwkplugin.TypedName {
	return fwkplugin.TypedName{Type: "stub", Name: "stub"}
}

func (s *stubDiscovery) Start(_ context.Context, _ fwkdl.DiscoveryNotifier) error {
	return nil
}

// notADiscovery satisfies fwkplugin.Plugin but does NOT implement EndpointDiscovery.
type notADiscovery struct{}

func (n *notADiscovery) TypedName() fwkplugin.TypedName {
	return fwkplugin.TypedName{Type: "not-discovery", Name: "not-discovery"}
}

func newTestRunner(plugins map[string]fwkplugin.Plugin) *Runner {
	handle := fwkplugin.NewEppHandle(context.Background(), func() []types.NamespacedName { return nil })
	for name, p := range plugins {
		handle.AddPlugin(name, p)
	}
	return &Runner{handle: handle}
}

func defaultOpts(poolName, namespace string) *runserver.Options {
	opts := runserver.NewOptions()
	opts.PoolName = poolName
	opts.PoolNamespace = namespace
	return opts
}

// --- no DataLayer / no Discovery: falls back to CLI flags ---

func TestResolveDiscovery_NilDataLayer_UsesPoolName(t *testing.T) {
	r := newTestRunner(nil)
	cfg := &configapi.EndpointPickerConfig{}

	disc, err := r.resolveDiscovery(cfg, defaultOpts("my-pool", "default"), "default", nil)

	require.NoError(t, err)
	ip, ok := disc.(*k8sdiscovery.InferencePoolDiscoveryPlugin)
	require.True(t, ok, "expected InferencePoolDiscoveryPlugin")
	assert.Equal(t, "my-pool", ip.GetPoolName())
	assert.Equal(t, "default", ip.GetPoolNamespace())
}

func TestResolveDiscovery_NilDataLayer_NoPoolName_ReturnsError(t *testing.T) {
	r := newTestRunner(nil)
	cfg := &configapi.EndpointPickerConfig{}

	_, err := r.resolveDiscovery(cfg, defaultOpts("", "default"), "default", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--pool-name is required")
}

func TestResolveDiscovery_DataLayerNilDiscovery_UsesPoolName(t *testing.T) {
	r := newTestRunner(nil)
	cfg := &configapi.EndpointPickerConfig{
		DataLayer: &configapi.DataLayerConfig{}, // Discovery is nil
	}

	disc, err := r.resolveDiscovery(cfg, defaultOpts("my-pool", "prod"), "prod", nil)

	require.NoError(t, err)
	ip, ok := disc.(*k8sdiscovery.InferencePoolDiscoveryPlugin)
	require.True(t, ok)
	assert.Equal(t, "my-pool", ip.GetPoolName())
	assert.Equal(t, "prod", ip.GetPoolNamespace())
}

// --- EndpointSelector fallback ---

func TestResolveDiscovery_NilDataLayer_EndpointSelectorUsed(t *testing.T) {
	r := newTestRunner(nil)
	cfg := &configapi.EndpointPickerConfig{}
	opts := defaultOpts("", "default")
	opts.EndpointSelector = "app=vllm"

	disc, err := r.resolveDiscovery(cfg, opts, "default", nil)

	require.NoError(t, err)
	_, ok := disc.(*k8sdiscovery.StaticSelectorDiscoveryPlugin)
	assert.True(t, ok, "expected StaticSelectorDiscoveryPlugin")
}

// --- Discovery configured via DataLayer ---

func TestResolveDiscovery_PluginRefNotFound_ReturnsError(t *testing.T) {
	r := newTestRunner(nil) // no plugins registered
	cfg := &configapi.EndpointPickerConfig{
		DataLayer: &configapi.DataLayerConfig{
			Discovery: &configapi.DiscoveryConfig{PluginRef: "missing-plugin"},
		},
	}

	_, err := r.resolveDiscovery(cfg, defaultOpts("", ""), "", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-plugin")
	assert.Contains(t, err.Error(), "not found")
}

func TestResolveDiscovery_PluginDoesNotImplementEndpointDiscovery_ReturnsError(t *testing.T) {
	r := newTestRunner(map[string]fwkplugin.Plugin{
		"my-plugin": &notADiscovery{},
	})
	cfg := &configapi.EndpointPickerConfig{
		DataLayer: &configapi.DataLayerConfig{
			Discovery: &configapi.DiscoveryConfig{PluginRef: "my-plugin"},
		},
	}

	_, err := r.resolveDiscovery(cfg, defaultOpts("", ""), "", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not implement EndpointDiscovery")
}

func TestResolveDiscovery_ValidPlugin_ReturnsPlugin(t *testing.T) {
	stub := &stubDiscovery{}
	r := newTestRunner(map[string]fwkplugin.Plugin{"my-discovery": stub})
	cfg := &configapi.EndpointPickerConfig{
		DataLayer: &configapi.DataLayerConfig{
			Discovery: &configapi.DiscoveryConfig{PluginRef: "my-discovery"},
		},
	}

	disc, err := r.resolveDiscovery(cfg, defaultOpts("", ""), "", nil)

	require.NoError(t, err)
	assert.Equal(t, stub, disc)
}

// --- InferencePoolDiscoveryPlugin injection via DataLayer.Discovery ---

func TestResolveDiscovery_InferencePoolPlugin_InjectsPoolNameAndNamespace(t *testing.T) {
	ip := k8sdiscovery.NewInferencePoolDiscoveryPlugin("", "", "", false)
	r := newTestRunner(map[string]fwkplugin.Plugin{"pool-plugin": ip})
	cfg := &configapi.EndpointPickerConfig{
		DataLayer: &configapi.DataLayerConfig{
			Discovery: &configapi.DiscoveryConfig{PluginRef: "pool-plugin"},
		},
	}

	disc, err := r.resolveDiscovery(cfg, defaultOpts("injected-pool", "injected-ns"), "injected-ns", nil)

	require.NoError(t, err)
	result, ok := disc.(*k8sdiscovery.InferencePoolDiscoveryPlugin)
	require.True(t, ok)
	assert.Equal(t, "injected-pool", result.GetPoolName())
	assert.Equal(t, "injected-ns", result.GetPoolNamespace())
}

func TestResolveDiscovery_InferencePoolPlugin_DoesNotOverrideExistingValues(t *testing.T) {
	ip := k8sdiscovery.NewInferencePoolDiscoveryPlugin("existing-pool", "existing-ns", "", false)
	r := newTestRunner(map[string]fwkplugin.Plugin{"pool-plugin": ip})
	cfg := &configapi.EndpointPickerConfig{
		DataLayer: &configapi.DataLayerConfig{
			Discovery: &configapi.DiscoveryConfig{PluginRef: "pool-plugin"},
		},
	}

	disc, err := r.resolveDiscovery(cfg, defaultOpts("other-pool", "other-ns"), "other-ns", nil)

	require.NoError(t, err)
	result, ok := disc.(*k8sdiscovery.InferencePoolDiscoveryPlugin)
	require.True(t, ok)
	assert.Equal(t, "existing-pool", result.GetPoolName())
	assert.Equal(t, "existing-ns", result.GetPoolNamespace())
}
