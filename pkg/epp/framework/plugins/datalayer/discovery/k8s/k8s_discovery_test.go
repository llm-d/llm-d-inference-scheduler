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

package k8s

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryclient "k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

// ---- baseK8sDiscovery tests -----------------------------------------------

func TestBaseK8sDiscovery_SetDatastore(t *testing.T) {
	b := &baseK8sDiscovery{}
	assert.Nil(t, b.ds)
	// SetDatastore accepts any datastore.Datastore; we test it doesn't panic
	// and sets the field -- use nil safely since we're just testing the setter.
	b.SetDatastore(nil)
	assert.Nil(t, b.ds)
}

func TestBaseK8sDiscovery_BindDatalayer(t *testing.T) {
	b := &baseK8sDiscovery{}
	assert.Nil(t, b.dlRuntime)
	rt := datalayer.NewRuntime(0)
	b.BindDatalayer(rt)
	assert.Equal(t, rt, b.dlRuntime)
}

// ---- InferencePoolFactory tests -------------------------------------------

func TestInferencePoolFactory_DefaultName(t *testing.T) {
	p, err := InferencePoolFactory("", json.RawMessage(`{"poolName":"my-pool","poolNamespace":"default"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, InferencePoolPluginType, p.TypedName().Name)
	assert.Equal(t, InferencePoolPluginType, p.TypedName().Type)
}

func TestInferencePoolFactory_CustomName(t *testing.T) {
	p, err := InferencePoolFactory("my-inference-pool", json.RawMessage(`{"poolName":"p"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "my-inference-pool", p.TypedName().Name)
}

func TestInferencePoolFactory_DefaultPoolGroup(t *testing.T) {
	p, err := InferencePoolFactory("", json.RawMessage(`{"poolName":"p"}`), nil)
	require.NoError(t, err)
	ip := p.(*InferencePoolDiscoveryPlugin)
	assert.Equal(t, "inference.networking.k8s.io", ip.PoolGroup)
}

func TestInferencePoolFactory_AllParams(t *testing.T) {
	params := `{"poolName":"my-pool","poolNamespace":"prod","poolGroup":"custom.io","leaderElection":true}`
	p, err := InferencePoolFactory("", json.RawMessage(params), nil)
	require.NoError(t, err)
	ip := p.(*InferencePoolDiscoveryPlugin)
	assert.Equal(t, "my-pool", ip.PoolName)
	assert.Equal(t, "prod", ip.PoolNamespace)
	assert.Equal(t, "custom.io", ip.PoolGroup)
	assert.True(t, ip.LeaderElect)
}

func TestInferencePoolFactory_InvalidJSON(t *testing.T) {
	_, err := InferencePoolFactory("", json.RawMessage(`{bad`), nil)
	assert.Error(t, err)
}

func TestInferencePoolFactory_ImplementsInterfaces(t *testing.T) {
	p, err := InferencePoolFactory("", json.RawMessage(`{"poolName":"p"}`), nil)
	require.NoError(t, err)
	_, ok := p.(DatastoreProvider)
	assert.True(t, ok, "should implement DatastoreProvider")
	_, ok = p.(DatalayerBinder)
	assert.True(t, ok, "should implement DatalayerBinder")
}

// ---- NewInferencePoolDiscoveryPlugin tests --------------------------------

func TestNewInferencePoolDiscoveryPlugin(t *testing.T) {
	p := NewInferencePoolDiscoveryPlugin("pool", "ns", "group.io", true)
	assert.Equal(t, "pool", p.PoolName)
	assert.Equal(t, "ns", p.PoolNamespace)
	assert.Equal(t, "group.io", p.PoolGroup)
	assert.True(t, p.LeaderElect)
	assert.Equal(t, fwkplugin.TypedName{Type: InferencePoolPluginType, Name: InferencePoolPluginType}, p.TypedName())
}

// ---- StaticSelectorFactory tests ------------------------------------------

func TestStaticSelectorFactory_MissingSelector(t *testing.T) {
	_, err := StaticSelectorFactory("", json.RawMessage(`{"endpointTargetPorts":[8080]}`), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "endpointSelector")
}

func TestStaticSelectorFactory_MissingPorts(t *testing.T) {
	_, err := StaticSelectorFactory("", json.RawMessage(`{"endpointSelector":"app=vllm"}`), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "endpointTargetPorts")
}

func TestStaticSelectorFactory_ValidParams(t *testing.T) {
	params := `{"endpointSelector":"app=vllm","endpointTargetPorts":[8080,8081],"namespace":"prod"}`
	p, err := StaticSelectorFactory("", json.RawMessage(params), nil)
	require.NoError(t, err)
	ss := p.(*StaticSelectorDiscoveryPlugin)
	assert.Equal(t, "app=vllm", ss.selector)
	assert.Equal(t, []int{8080, 8081}, ss.targetPorts)
	assert.Equal(t, "prod", ss.namespace)
}

func TestStaticSelectorFactory_DefaultNamespace(t *testing.T) {
	params := `{"endpointSelector":"app=vllm","endpointTargetPorts":[8080]}`
	p, err := StaticSelectorFactory("", json.RawMessage(params), nil)
	require.NoError(t, err)
	ss := p.(*StaticSelectorDiscoveryPlugin)
	assert.Equal(t, "default", ss.namespace)
}

func TestStaticSelectorFactory_InvalidJSON(t *testing.T) {
	_, err := StaticSelectorFactory("", json.RawMessage(`{bad`), nil)
	assert.Error(t, err)
}

func TestStaticSelectorFactory_ImplementsInterfaces(t *testing.T) {
	params := `{"endpointSelector":"app=vllm","endpointTargetPorts":[8080]}`
	p, err := StaticSelectorFactory("", json.RawMessage(params), nil)
	require.NoError(t, err)
	_, ok := p.(DatastoreProvider)
	assert.True(t, ok, "should implement DatastoreProvider")
	_, ok = p.(DatalayerBinder)
	assert.True(t, ok, "should implement DatalayerBinder")
}

// ---- NewStaticSelectorDiscoveryPlugin tests -------------------------------

func TestNewStaticSelectorDiscoveryPlugin(t *testing.T) {
	p := NewStaticSelectorDiscoveryPlugin("app=vllm", "ns", []int{8080, 8081})
	assert.Equal(t, "app=vllm", p.selector)
	assert.Equal(t, "ns", p.namespace)
	assert.Equal(t, []int{8080, 8081}, p.targetPorts)
	assert.Equal(t, fwkplugin.TypedName{Type: StaticSelectorPluginType, Name: StaticSelectorPluginType}, p.TypedName())
}

// ---- gvkInstalled tests ---------------------------------------------------

func makeDiscoveryClient(resources map[string][]string) discoveryclient.DiscoveryInterface {
	cs := fakeclientset.NewSimpleClientset()
	fd := cs.Discovery().(*fakediscovery.FakeDiscovery)
	var resourceList []*metav1.APIResourceList
	for groupVersion, kinds := range resources {
		apiResources := make([]metav1.APIResource, len(kinds))
		for i, k := range kinds {
			apiResources[i] = metav1.APIResource{Kind: k}
		}
		resourceList = append(resourceList, &metav1.APIResourceList{
			GroupVersion: groupVersion,
			APIResources: apiResources,
		})
	}
	fd.Resources = resourceList
	return fd
}

func TestGVKInstalled_Present(t *testing.T) {
	dc := makeDiscoveryClient(map[string][]string{
		"inference.networking.x-k8s.io/v1alpha2": {"InferenceObjective", "InferenceModelRewrite"},
	})
	gvk := schema.GroupVersion{Group: "inference.networking.x-k8s.io", Version: "v1alpha2"}
	assert.True(t, gvkInstalled(dc, gvk.Group, gvk.Version, "InferenceObjective"))
	assert.True(t, gvkInstalled(dc, gvk.Group, gvk.Version, "InferenceModelRewrite"))
}

func TestGVKInstalled_Absent(t *testing.T) {
	dc := makeDiscoveryClient(map[string][]string{
		"inference.networking.x-k8s.io/v1alpha2": {"InferenceObjective"},
	})
	assert.False(t, gvkInstalled(dc, "inference.networking.x-k8s.io", "v1alpha2", "InferenceModelRewrite"))
}

func TestGVKInstalled_MissingGroup(t *testing.T) {
	dc := makeDiscoveryClient(map[string][]string{})
	assert.False(t, gvkInstalled(dc, "nonexistent.group", "v1", "SomeKind"))
}
