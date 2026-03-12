package profile

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer/plugins/approximateprefix"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

const (
	testEndpointAddr = "10.0.0.1"
	testEndpointPort = "8000"
)

// notPrefixCacheMatchInfo is a Cloneable type that is not *PrefixCacheMatchInfo, used to test type assertion failure.
type notPrefixCacheMatchInfo struct{}

func (n *notPrefixCacheMatchInfo) Clone() fwkdl.Cloneable { return &notPrefixCacheMatchInfo{} }

const (
	testTotalTokens = 10
	testBlockSize   = 1
)

func makeTestEndpoint(cachedTokens int) scheduling.Endpoint {
	ep := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Namespace: "default", Name: "test-pod"},
			Address:        testEndpointAddr,
			Port:           testEndpointPort,
		},
		nil,
		fwkdl.NewAttributes(),
	)
	ep.Put(approximateprefix.PrefixCacheMatchInfoKey,
		approximateprefix.NewPrefixCacheMatchInfo(cachedTokens, testTotalTokens, testBlockSize))
	return ep
}

func TestPrefixBasedPDDeciderConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		config    PrefixBasedPDDeciderConfig
		expectErr bool
	}{
		{
			name:      "zero is valid",
			config:    PrefixBasedPDDeciderConfig{NonCachedTokens: 0},
			expectErr: false,
		},
		{
			name:      "positive is valid",
			config:    PrefixBasedPDDeciderConfig{NonCachedTokens: 100},
			expectErr: false,
		},
		{
			name:      "negative is invalid",
			config:    PrefixBasedPDDeciderConfig{NonCachedTokens: -1},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewPrefixBasedPDDecider(tt.config)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestPrefixBasedPDDeciderFactory(t *testing.T) {
	tests := []struct {
		name             string
		pluginName       string
		rawParams        string
		expectErr        bool
		expectNonCached  int
		expectPluginName string
	}{
		{
			name:             "default parameters (nil)",
			pluginName:       "my-decider",
			rawParams:        "",
			expectErr:        false,
			expectNonCached:  0,
			expectPluginName: "my-decider",
		},
		{
			name:             "custom nonCachedTokens",
			pluginName:       "custom-decider",
			rawParams:        `{"nonCachedTokens": 50}`,
			expectErr:        false,
			expectNonCached:  50,
			expectPluginName: "custom-decider",
		},
		{
			name:       "negative nonCachedTokens",
			pluginName: "bad-decider",
			rawParams:  `{"nonCachedTokens": -5}`,
			expectErr:  true,
		},
		{
			name:       "invalid json",
			pluginName: "bad-json",
			rawParams:  `{invalid}`,
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw json.RawMessage
			if tt.rawParams != "" {
				raw = json.RawMessage(tt.rawParams)
			}

			p, err := PrefixBasedPDDeciderPluginFactory(tt.pluginName, raw, nil)
			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, p)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, p)

			decider, ok := p.(*PrefixBasedPDDecider)
			require.True(t, ok)
			assert.Equal(t, tt.expectPluginName, decider.TypedName().Name)
			assert.Equal(t, tt.expectNonCached, decider.config.NonCachedTokens)
		})
	}
}

func TestDisaggregate(t *testing.T) {
	ctx := utils.NewTestContext(t)

	tests := []struct {
		name            string
		nonCachedTokens int
		inputTokens     int
		endpoint        scheduling.Endpoint
		expected        bool
	}{
		{
			name:            "threshold zero always disaggregates",
			nonCachedTokens: 0,
			inputTokens:     10,
			endpoint:        makeTestEndpoint(5),
			expected:        true,
		},
		{
			name:            "threshold zero with nil endpoint still disaggregates",
			nonCachedTokens: 0,
			inputTokens:     10,
			endpoint:        nil,
			expected:        true,
		},
		{
			name:            "nil endpoint returns false",
			nonCachedTokens: 5,
			inputTokens:     10,
			endpoint:        nil,
			expected:        false,
		},
		{
			name:            "input shorter than threshold",
			nonCachedTokens: 20,
			inputTokens:     10,
			endpoint:        makeTestEndpoint(0),
			expected:        false,
		},
		{
			name:            "non-cached suffix below threshold",
			nonCachedTokens: 5,
			inputTokens:     10,
			endpoint:        makeTestEndpoint(8),
			expected:        false,
		},
		{
			name:            "non-cached suffix equals threshold",
			nonCachedTokens: 5,
			inputTokens:     10,
			endpoint:        makeTestEndpoint(5),
			expected:        true,
		},
		{
			name:            "non-cached suffix above threshold",
			nonCachedTokens: 3,
			inputTokens:     10,
			endpoint:        makeTestEndpoint(2),
			expected:        true,
		},
		{
			name:            "fully cached prompt",
			nonCachedTokens: 1,
			inputTokens:     10,
			endpoint:        makeTestEndpoint(10),
			expected:        false,
		},
		{
			name:            "no cache hit at all",
			nonCachedTokens: 5,
			inputTokens:     10,
			endpoint:        makeTestEndpoint(0),
			expected:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decider, err := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{NonCachedTokens: tt.nonCachedTokens})
			require.NoError(t, err)

			result := decider.disaggregate(ctx, tt.inputTokens, tt.endpoint)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDisaggregateNoPrefixInfo(t *testing.T) {
	ctx := utils.NewTestContext(t)

	ep := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Namespace: "default", Name: "no-cache-pod"},
			Address:        testEndpointAddr,
			Port:           testEndpointPort,
		},
		nil,
		fwkdl.NewAttributes(),
	)

	decider, err := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{NonCachedTokens: 5})
	require.NoError(t, err)

	assert.False(t, decider.disaggregate(ctx, 100, ep))
}

func TestDisaggregateWrongPrefixInfoType(t *testing.T) {
	ctx := utils.NewTestContext(t)

	ep := scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Namespace: "default", Name: "wrong-type-pod"},
			Address:        testEndpointAddr,
			Port:           testEndpointPort,
		},
		nil,
		fwkdl.NewAttributes(),
	)
	ep.Put(approximateprefix.PrefixCacheMatchInfoKey, &notPrefixCacheMatchInfo{})

	decider, err := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{NonCachedTokens: 5})
	require.NoError(t, err)

	assert.False(t, decider.disaggregate(ctx, 100, ep))
}

func TestConsumes(t *testing.T) {
	decider, err := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{NonCachedTokens: 0})
	require.NoError(t, err)

	consumed := decider.Consumes()
	assert.Contains(t, consumed, approximateprefix.PrefixCacheMatchInfoKey)
}

func TestWithName(t *testing.T) {
	decider, err := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{NonCachedTokens: 0})
	require.NoError(t, err)

	decider.WithName("my-decider")
	assert.Equal(t, "my-decider", decider.TypedName().Name)

	decider.WithName("renamed")
	assert.Equal(t, "renamed", decider.TypedName().Name)
}
