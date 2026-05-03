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

package file

import (
	"encoding/json"
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

// fakeNotifier records Upsert/Delete calls.
type fakeNotifier struct {
	upserted []*fwkdl.EndpointMetadata
	deleted  []types.NamespacedName
}

func (f *fakeNotifier) Upsert(ep *fwkdl.EndpointMetadata) { f.upserted = append(f.upserted, ep) }
func (f *fakeNotifier) Delete(id types.NamespacedName)     { f.deleted = append(f.deleted, id) }

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "endpoints-*.yaml")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

// ---- Factory tests -------------------------------------------------------

func TestFactory_MissingPath(t *testing.T) {
	_, err := Factory("test", json.RawMessage(`{}`), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "'path'")
}

func TestFactory_InvalidJSON(t *testing.T) {
	_, err := Factory("test", json.RawMessage(`{bad json`), nil)
	assert.Error(t, err)
}

func TestFactory_ValidParams(t *testing.T) {
	p, err := Factory("my-plugin", json.RawMessage(`{"path":"/tmp/eps.yaml","watchFile":true}`), nil)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, fwkplugin.TypedName{Type: PluginType, Name: "my-plugin"}, p.TypedName())
}

func TestFactory_DefaultName(t *testing.T) {
	p, err := Factory("", json.RawMessage(`{"path":"/tmp/eps.yaml"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, PluginType, p.TypedName().Name)
}

// ---- load() tests -------------------------------------------------------

func TestLoad_BasicEndpoints(t *testing.T) {
	path := writeTempFile(t, `
endpoints:
  - name: ep-0
    address: 10.0.0.1
    port: "8080"
  - name: ep-1
    address: 10.0.0.2
    port: "8080"
`)
	fd := &FileDiscovery{
		path:    path,
		current: make(map[types.NamespacedName]struct{}),
	}
	n := &fakeNotifier{}
	require.NoError(t, fd.load(n))

	require.Len(t, n.upserted, 2)
	assert.Equal(t, "ep-0", n.upserted[0].NamespacedName.Name)
	assert.Equal(t, "ep-1", n.upserted[1].NamespacedName.Name)
	assert.Equal(t, "10.0.0.1", n.upserted[0].Address)
	assert.Empty(t, n.deleted)
}

func TestLoad_DefaultNamespace(t *testing.T) {
	path := writeTempFile(t, `
endpoints:
  - name: ep-0
    address: 10.0.0.1
    port: "8080"
`)
	fd := &FileDiscovery{path: path, current: make(map[types.NamespacedName]struct{})}
	n := &fakeNotifier{}
	require.NoError(t, fd.load(n))

	assert.Equal(t, "default", n.upserted[0].NamespacedName.Namespace)
}

func TestLoad_ExplicitNamespace(t *testing.T) {
	path := writeTempFile(t, `
endpoints:
  - name: ep-0
    namespace: prod
    address: 10.0.0.1
    port: "8080"
`)
	fd := &FileDiscovery{path: path, current: make(map[types.NamespacedName]struct{})}
	n := &fakeNotifier{}
	require.NoError(t, fd.load(n))

	assert.Equal(t, "prod", n.upserted[0].NamespacedName.Namespace)
}

func TestLoad_MetricsHostDerived(t *testing.T) {
	path := writeTempFile(t, `
endpoints:
  - name: ep-0
    address: 10.0.0.1
    port: "8080"
`)
	fd := &FileDiscovery{path: path, current: make(map[types.NamespacedName]struct{})}
	n := &fakeNotifier{}
	require.NoError(t, fd.load(n))

	assert.Equal(t, "10.0.0.1:8080", n.upserted[0].MetricsHost)
}

func TestLoad_MetricsHostExplicit(t *testing.T) {
	path := writeTempFile(t, `
endpoints:
  - name: ep-0
    address: 10.0.0.1
    port: "8080"
    metricsHost: 10.0.0.1:9090
`)
	fd := &FileDiscovery{path: path, current: make(map[types.NamespacedName]struct{})}
	n := &fakeNotifier{}
	require.NoError(t, fd.load(n))

	assert.Equal(t, "10.0.0.1:9090", n.upserted[0].MetricsHost)
}

func TestLoad_DeletesRemovedEndpoints(t *testing.T) {
	path := writeTempFile(t, `
endpoints:
  - name: ep-0
    address: 10.0.0.1
    port: "8080"
  - name: ep-1
    address: 10.0.0.2
    port: "8080"
`)
	fd := &FileDiscovery{path: path, current: make(map[types.NamespacedName]struct{})}
	n := &fakeNotifier{}
	require.NoError(t, fd.load(n))
	assert.Len(t, n.upserted, 2)

	// overwrite file with only ep-0
	require.NoError(t, os.WriteFile(path, []byte(`
endpoints:
  - name: ep-0
    address: 10.0.0.1
    port: "8080"
`), 0600))

	n2 := &fakeNotifier{}
	require.NoError(t, fd.load(n2))

	assert.Len(t, n2.upserted, 1)
	assert.Equal(t, "ep-0", n2.upserted[0].NamespacedName.Name)
	assert.Len(t, n2.deleted, 1)
	assert.Equal(t, "ep-1", n2.deleted[0].Name)
}

func TestLoad_EmptyFile(t *testing.T) {
	path := writeTempFile(t, `endpoints: []`)
	fd := &FileDiscovery{path: path, current: make(map[types.NamespacedName]struct{})}
	n := &fakeNotifier{}
	require.NoError(t, fd.load(n))
	assert.Empty(t, n.upserted)
	assert.Empty(t, n.deleted)
}

func TestLoad_FileNotFound(t *testing.T) {
	fd := &FileDiscovery{path: "/nonexistent/path.yaml", current: make(map[types.NamespacedName]struct{})}
	n := &fakeNotifier{}
	err := fd.load(n)
	assert.Error(t, err)
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeTempFile(t, `{not valid yaml: [`)
	fd := &FileDiscovery{path: path, current: make(map[types.NamespacedName]struct{})}
	n := &fakeNotifier{}
	err := fd.load(n)
	assert.Error(t, err)
}

func TestLoad_JSONFormat(t *testing.T) {
	path := writeTempFile(t, `{"endpoints":[{"name":"ep-0","address":"10.0.0.1","port":"8080"}]}`)
	path = path[:len(path)] // rename not needed, yaml.YAMLToJSON handles JSON
	// rename to .json to make intent clear -- but the plugin doesn't care about extension
	fd := &FileDiscovery{path: path, current: make(map[types.NamespacedName]struct{})}
	n := &fakeNotifier{}
	require.NoError(t, fd.load(n))
	require.Len(t, n.upserted, 1)
	assert.Equal(t, "ep-0", n.upserted[0].NamespacedName.Name)
}

func TestLoad_LabelsPreserved(t *testing.T) {
	path := writeTempFile(t, `
endpoints:
  - name: ep-0
    address: 10.0.0.1
    port: "8080"
    labels:
      model: llama-3
      gpu: h100
`)
	fd := &FileDiscovery{path: path, current: make(map[types.NamespacedName]struct{})}
	n := &fakeNotifier{}
	require.NoError(t, fd.load(n))

	assert.Equal(t, map[string]string{"model": "llama-3", "gpu": "h100"}, n.upserted[0].Labels)
}

func TestLoad_UpsertDeleteOrder(t *testing.T) {
	// first load: ep-0 and ep-1
	path := writeTempFile(t, `
endpoints:
  - name: ep-0
    address: 10.0.0.1
    port: "8080"
  - name: ep-1
    address: 10.0.0.2
    port: "8080"
`)
	fd := &FileDiscovery{path: path, current: make(map[types.NamespacedName]struct{})}
	n := &fakeNotifier{}
	require.NoError(t, fd.load(n))

	// second load: only ep-2 -- ep-0 and ep-1 should be deleted
	require.NoError(t, os.WriteFile(path, []byte(`
endpoints:
  - name: ep-2
    address: 10.0.0.3
    port: "8080"
`), 0600))

	type op struct {
		kind string
		name string
	}
	var ops []op
	tracking := &trackingNotifier{
		onUpsert: func(ep *fwkdl.EndpointMetadata) { ops = append(ops, op{"upsert", ep.NamespacedName.Name}) },
		onDelete: func(id types.NamespacedName) { ops = append(ops, op{"delete", id.Name}) },
	}
	require.NoError(t, fd.load(tracking))

	// all upserts must come before deletes (ordering contract)
	lastUpsertIdx := -1
	firstDeleteIdx := len(ops)
	for i, o := range ops {
		if o.kind == "upsert" {
			lastUpsertIdx = i
		}
		if o.kind == "delete" && i < firstDeleteIdx {
			firstDeleteIdx = i
		}
	}
	assert.Less(t, lastUpsertIdx, firstDeleteIdx, "all upserts must precede all deletes")
}

type trackingNotifier struct {
	onUpsert func(*fwkdl.EndpointMetadata)
	onDelete func(types.NamespacedName)
}

func (t *trackingNotifier) Upsert(ep *fwkdl.EndpointMetadata) { t.onUpsert(ep) }
func (t *trackingNotifier) Delete(id types.NamespacedName)     { t.onDelete(id) }
