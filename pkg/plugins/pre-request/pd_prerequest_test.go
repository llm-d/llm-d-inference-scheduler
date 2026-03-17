package prerequest

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

const (
	testAddr     = "10.0.0.5"
	testPort     = "8000"
	testIPv6Addr = "fd00::1"
)

func makeEndpoint(addr string) scheduling.Endpoint {
	return scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Namespace: "default", Name: "prefill-pod"},
			Address:        addr,
			Port:           testPort,
		},
		&fwkdl.Metrics{},
		nil,
	)
}

func TestPrefillHeaderHandlerFactory(t *testing.T) {
	tests := []struct {
		name          string
		pluginName    string
		rawParams     string
		expectErr     bool
		expectProfile string
		expectName    string
	}{
		{
			name:          "default parameters",
			pluginName:    "my-handler",
			rawParams:     "",
			expectErr:     false,
			expectProfile: "prefill",
			expectName:    "my-handler",
		},
		{
			name:          "custom prefill profile",
			pluginName:    "custom-handler",
			rawParams:     `{"prefillProfile": "my-prefill"}`,
			expectErr:     false,
			expectProfile: "my-prefill",
			expectName:    "custom-handler",
		},
		{
			name:       "invalid json",
			pluginName: "bad-handler",
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

			p, err := PrefillHeaderHandlerFactory(tt.pluginName, raw, nil)
			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, p)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, p)

			handler, ok := p.(*PrefillHeaderHandler)
			require.True(t, ok)
			assert.Equal(t, tt.expectName, handler.TypedName().Name)
			assert.Equal(t, PrefillHeaderHandlerType, handler.TypedName().Type)
			assert.Equal(t, tt.expectProfile, handler.prefillProfile)
		})
	}
}

func TestPreRequestPrefillProfileExists(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewPrefillHeaderHandler("prefill").WithName("test")

	request := &scheduling.LLMRequest{
		TargetModel: "test-model",
		RequestId:   "req-123",
		Headers:     map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"prefill": {
				TargetEndpoints: []scheduling.Endpoint{
					makeEndpoint(testAddr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(testAddr, testPort), request.Headers[common.PrefillEndpointHeader])
}

func TestPreRequestPrefillProfileNotExists(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewPrefillHeaderHandler("prefill").WithName("test")

	request := &scheduling.LLMRequest{
		Headers: map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults:     map[string]*scheduling.ProfileRunResult{},
	}

	handler.PreRequest(ctx, request, result)

	_, exists := request.Headers[common.PrefillEndpointHeader]
	assert.False(t, exists)
}

func TestPreRequestClearsExistingHeader(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewPrefillHeaderHandler("prefill").WithName("test")

	request := &scheduling.LLMRequest{
		Headers: map[string]string{
			common.PrefillEndpointHeader: "old-host:9999",
		},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"prefill": {
				TargetEndpoints: []scheduling.Endpoint{
					makeEndpoint(testAddr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(testAddr, testPort), request.Headers[common.PrefillEndpointHeader])
}

func TestPreRequestClearsHeaderWhenNoPrefillResult(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewPrefillHeaderHandler("prefill").WithName("test")

	request := &scheduling.LLMRequest{
		Headers: map[string]string{
			common.PrefillEndpointHeader: "stale-host:9999",
		},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults:     map[string]*scheduling.ProfileRunResult{},
	}

	handler.PreRequest(ctx, request, result)

	val := request.Headers[common.PrefillEndpointHeader]
	assert.Equal(t, "", val)
}

func TestPreRequestCustomPrefillProfile(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewPrefillHeaderHandler("my-custom-prefill").WithName("test")

	request := &scheduling.LLMRequest{
		Headers: map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"my-custom-prefill": {
				TargetEndpoints: []scheduling.Endpoint{
					makeEndpoint(testAddr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(testAddr, testPort), request.Headers[common.PrefillEndpointHeader])
}

func TestPreRequestIPv6Address(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewPrefillHeaderHandler("prefill").WithName("test")

	request := &scheduling.LLMRequest{
		Headers: map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"prefill": {
				TargetEndpoints: []scheduling.Endpoint{
					makeEndpoint(testIPv6Addr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(testIPv6Addr, testPort), request.Headers[common.PrefillEndpointHeader])
}
