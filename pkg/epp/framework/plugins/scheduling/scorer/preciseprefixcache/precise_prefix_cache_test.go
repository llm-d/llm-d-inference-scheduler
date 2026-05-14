/*
Copyright 2026 The llm-d Authors.

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

package preciseprefixcache

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requestcontrol"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	attrprefix "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/attribute/prefix"
	preciseproducer "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/requestcontrol/dataproducer/preciseprefixcache"
	igwtestutils "github.com/llm-d/llm-d-inference-scheduler/test/utils/igw"
)

func newEndpoint(name, addr string) scheduling.Endpoint {
	return scheduling.NewEndpoint(&fwkdl.EndpointMetadata{
		NamespacedName: k8stypes.NamespacedName{Name: name},
		Address:        addr,
		Port:           "8080",
	}, nil, nil)
}

func TestScorer_ReadsMatchRatio(t *testing.T) {
	ctx := context.Background()

	epA := newEndpoint("pod-a", "10.0.0.1")
	epA.Put(preciseproducer.MatchInfoAttributeKey, attrprefix.NewPrefixCacheMatchInfo(3, 4, 16))

	epB := newEndpoint("pod-b", "10.0.0.2")
	epB.Put(preciseproducer.MatchInfoAttributeKey, attrprefix.NewPrefixCacheMatchInfo(1, 4, 16))

	epC := newEndpoint("pod-c", "10.0.0.3")
	epC.Put(preciseproducer.MatchInfoAttributeKey, attrprefix.NewPrefixCacheMatchInfo(0, 4, 16))

	got := New().Score(ctx, scheduling.NewCycleState(),
		&scheduling.InferenceRequest{RequestID: "r"},
		[]scheduling.Endpoint{epA, epB, epC})

	assert.InDelta(t, 0.75, got[epA], 1e-9)
	assert.InDelta(t, 0.25, got[epB], 1e-9)
	assert.InDelta(t, 0.0, got[epC], 1e-9)
}

func TestScorer_MissingAttributeIsZero(t *testing.T) {
	ep := newEndpoint("pod-a", "10.0.0.1")
	got := New().Score(context.Background(), scheduling.NewCycleState(),
		&scheduling.InferenceRequest{RequestID: "r"},
		[]scheduling.Endpoint{ep})
	assert.Equal(t, 0.0, got[ep])
}

// Divide-by-zero guard: totalBlocks=0 must produce 0, not NaN.
func TestScorer_ZeroTotalIsZero(t *testing.T) {
	ep := newEndpoint("pod-a", "10.0.0.1")
	ep.Put(preciseproducer.MatchInfoAttributeKey, attrprefix.NewPrefixCacheMatchInfo(0, 0, 16))

	got := New().Score(context.Background(), scheduling.NewCycleState(),
		&scheduling.InferenceRequest{RequestID: "r"},
		[]scheduling.Endpoint{ep})
	assert.Equal(t, 0.0, got[ep])
}

func TestScorer_ConsumesPreciseKey(t *testing.T) {
	_, ok := New().Consumes()[preciseproducer.MatchInfoAttributeKey]
	require.True(t, ok)
}

// No params → slim Scorer.
func TestPluginFactory_SplitMode_NoParams(t *testing.T) {
	handle := igwtestutils.NewTestHandle(context.Background())

	for _, raw := range []json.RawMessage{nil, []byte(""), []byte("null"), []byte("{}"), []byte("   ")} {
		got, err := PluginFactory("precise-prefix-cache-scorer", raw, handle)
		require.NoError(t, err)
		_, isSlim := got.(*Scorer)
		assert.True(t, isSlim, "got %T", got)
	}
}

// Non-empty params → legacy wrapper fulfilling every hat the monolith did.
func TestPluginFactory_LegacyMode_WithParams(t *testing.T) {
	handle := igwtestutils.NewTestHandle(context.Background())
	raw := json.RawMessage(`{"tokenProcessorConfig":{"blockSize":16}}`)

	got, err := PluginFactory("precise-prefix-cache-scorer", raw, handle)
	require.NoError(t, err)

	wrapper, ok := got.(*legacyScorer)
	require.True(t, ok, "got %T", got)

	assert.Equal(t, PrecisePrefixCachePluginType, wrapper.TypedName().Type)
	assert.Equal(t, "precise-prefix-cache-scorer", wrapper.TypedName().Name)

	var _ scheduling.Scorer = wrapper
	var _ requestcontrol.DataProducer = wrapper
	var _ requestcontrol.PreRequest = wrapper
	var _ fwkdl.EndpointExtractor = wrapper

	_, ok = wrapper.Produces()[attrprefix.PrefixCacheMatchInfoKey]
	assert.True(t, ok)
	_, ok = wrapper.Consumes()["TokenizedPrompt"]
	assert.True(t, ok)
	require.NotNil(t, wrapper.producer)
}
