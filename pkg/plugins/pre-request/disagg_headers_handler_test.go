package prerequest

import (
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	giePlugin "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

func TestMain(m *testing.M) {
	giePlugin.Register(DisaggHeadersHandlerType, DisaggHeadersHandlerFactory)
	giePlugin.Register(PrefillHeaderHandlerType, DisaggHeadersHandlerFactory) //nolint:staticcheck
	os.Exit(m.Run())
}

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

func makeEncodeEndpoint(addr string) scheduling.Endpoint {
	return scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Namespace: "default", Name: "encode-pod"},
			Address:        addr,
			Port:           testPort,
		},
		&fwkdl.Metrics{},
		nil,
	)
}

func TestDisaggHeadersHandlerFactory(t *testing.T) {
	tests := []struct {
		name                 string
		pluginName           string
		rawParams            string
		expectErr            bool
		expectPrefillProfile string
		expectEncodeProfile  string
		expectName           string
	}{
		{
			name:                 "default parameters",
			pluginName:           "my-handler",
			rawParams:            "",
			expectErr:            false,
			expectPrefillProfile: "prefill",
			expectEncodeProfile:  "encode",
			expectName:           "my-handler",
		},
		{
			name:                 "custom prefill profile",
			pluginName:           "custom-handler",
			rawParams:            `{"prefillProfile": "my-prefill"}`,
			expectErr:            false,
			expectPrefillProfile: "my-prefill",
			expectEncodeProfile:  "encode",
			expectName:           "custom-handler",
		},
		{
			name:                 "custom encode profile",
			pluginName:           "custom-handler",
			rawParams:            `{"encodeProfile": "my-encode"}`,
			expectErr:            false,
			expectPrefillProfile: "prefill",
			expectEncodeProfile:  "my-encode",
			expectName:           "custom-handler",
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

			p, err := DisaggHeadersHandlerFactory(tt.pluginName, raw, nil)
			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, p)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, p)

			handler, ok := p.(*DisaggHeadersHandler)
			require.True(t, ok)
			assert.Equal(t, tt.expectName, handler.TypedName().Name)
			assert.Equal(t, DisaggHeadersHandlerType, handler.TypedName().Type)
			assert.Equal(t, tt.expectPrefillProfile, handler.prefillProfile)
			assert.Equal(t, tt.expectEncodeProfile, handler.encodeProfile)
		})
	}
}

func TestPreRequestNilRequest(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	result := &scheduling.SchedulingResult{
		ProfileResults: map[string]*scheduling.ProfileRunResult{},
	}

	assert.NotPanics(t, func() {
		handler.PreRequest(ctx, nil, result)
	})
}

func TestPreRequestNilSchedulingResult(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}

	assert.NotPanics(t, func() {
		handler.PreRequest(ctx, request, nil)
	})
}

// ----- Backward compatibility -----

func TestPrefillHeaderHandlerBackwardCompat(t *testing.T) {
	// Simulate what the config loader does when it reads type: prefill-header-handler from YAML
	factory, ok := giePlugin.Registry[PrefillHeaderHandlerType]
	require.True(t, ok, "prefill-header-handler must be in the registry")

	raw := json.RawMessage(`{"prefillProfile": "prefill"}`)
	p, err := factory("compat-handler", raw, nil)
	require.NoError(t, err)
	require.NotNil(t, p)

	handler, ok := p.(*DisaggHeadersHandler)
	require.True(t, ok)
	assert.Equal(t, "prefill", handler.prefillProfile)
	assert.Equal(t, defaultEncodeProfile, handler.encodeProfile)

	// Verify it correctly handles a PD-only scheduling result
	ctx := utils.NewTestContext(t)
	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}
	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"prefill": {TargetEndpoints: []scheduling.Endpoint{makeEndpoint(testAddr)}},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(testAddr, testPort), request.Headers[common.PrefillEndpointHeader])
	_, encodeSet := request.Headers[common.EncoderEndpointsHeader]
	assert.False(t, encodeSet, "encode header must not be set in PD-only scenario")
}

// ----- Prefill tests -----

func TestPreRequestPrefillProfileExists(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

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
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

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

func TestPreRequestClearsExistingPrefillHeader(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

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
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

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
	handler := NewDisaggHeadersHandler("my-custom-prefill", "encode").WithName("test")

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

func TestPreRequestPrefillProfileNilResult(t *testing.T) {
	// disagg_profile_handler sets the prefill profile result to nil when the
	// decider decides not to prefill. Verify PreRequest handles this gracefully.
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"prefill": nil,
		},
	}

	assert.NotPanics(t, func() {
		handler.PreRequest(ctx, request, result)
	})
	_, exists := request.Headers[common.PrefillEndpointHeader]
	assert.False(t, exists)
}

func TestPreRequestPrefillEmptyTargetEndpoints(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"prefill": {TargetEndpoints: []scheduling.Endpoint{}},
		},
	}

	assert.NotPanics(t, func() {
		handler.PreRequest(ctx, request, result)
	})
	_, exists := request.Headers[common.PrefillEndpointHeader]
	assert.False(t, exists)
}

func TestPreRequestPrefillIPv6Address(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

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

// ----- Encode tests -----

func TestPreRequestEncodeProfileExists(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	request := &scheduling.LLMRequest{
		TargetModel: "test-model",
		RequestId:   "req-123",
		Headers:     map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"encode": {
				TargetEndpoints: []scheduling.Endpoint{
					makeEncodeEndpoint(testAddr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(testAddr, testPort), request.Headers[common.EncoderEndpointsHeader])
}

func TestPreRequestEncodeProfileNotExists(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults:     map[string]*scheduling.ProfileRunResult{},
	}

	handler.PreRequest(ctx, request, result)

	_, exists := request.Headers[common.EncoderEndpointsHeader]
	assert.False(t, exists)
}

func TestPreRequestEncodeClearsExistingHeader(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers: map[string]string{
			common.EncoderEndpointsHeader: "old-host:9999",
		},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"encode": {
				TargetEndpoints: []scheduling.Endpoint{
					makeEncodeEndpoint(testAddr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(testAddr, testPort), request.Headers[common.EncoderEndpointsHeader])
}

func TestPreRequestEncodeClearsHeaderWhenNoEncodeResult(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers: map[string]string{
			common.EncoderEndpointsHeader: "stale-host:9999",
		},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults:     map[string]*scheduling.ProfileRunResult{},
	}

	handler.PreRequest(ctx, request, result)

	val := request.Headers[common.EncoderEndpointsHeader]
	assert.Equal(t, "", val)
}

func TestPreRequestEncodeCustomProfile(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "my-custom-encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"my-custom-encode": {
				TargetEndpoints: []scheduling.Endpoint{
					makeEncodeEndpoint(testAddr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(testAddr, testPort), request.Headers[common.EncoderEndpointsHeader])
}

func TestPreRequestEncodeIPv6Address(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"encode": {
				TargetEndpoints: []scheduling.Endpoint{
					makeEncodeEndpoint(testIPv6Addr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(testIPv6Addr, testPort), request.Headers[common.EncoderEndpointsHeader])
}

func TestPreRequestEncodeProfileNilResult(t *testing.T) {
	// disagg_profile_handler sets the encode profile result to nil when the
	// decider decides not to encode. Verify PreRequest handles this gracefully.
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"encode": nil,
		},
	}

	assert.NotPanics(t, func() {
		handler.PreRequest(ctx, request, result)
	})
	_, exists := request.Headers[common.EncoderEndpointsHeader]
	assert.False(t, exists)
}

func TestPreRequestEncodeEmptyTargetEndpoints(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers: map[string]string{
			common.EncoderEndpointsHeader: "stale-host:9999",
		},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"encode": {TargetEndpoints: []scheduling.Endpoint{}},
		},
	}

	assert.NotPanics(t, func() {
		handler.PreRequest(ctx, request, result)
	})
	val := request.Headers[common.EncoderEndpointsHeader]
	assert.Equal(t, "", val)
}

func TestPreRequestEncodeMultipleEndpoints(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewDisaggHeadersHandler("prefill", "encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}

	addr2 := "10.0.0.6"
	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"encode": {
				TargetEndpoints: []scheduling.Endpoint{
					makeEncodeEndpoint(testAddr),
					makeEncodeEndpoint(addr2),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	expected := strings.Join([]string{
		net.JoinHostPort(testAddr, testPort),
		net.JoinHostPort(addr2, testPort),
	}, ",")
	assert.Equal(t, expected, request.Headers[common.EncoderEndpointsHeader])
}
