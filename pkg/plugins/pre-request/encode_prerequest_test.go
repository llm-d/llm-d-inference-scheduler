package prerequest

import (
	"encoding/json"
	"net"
	"strings"
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
	encodeTestAddr     = "10.0.0.5"
	encodeTestPort     = "8000"
	encodeTestIPv6Addr = "fd00::1"
)

func makeEncodeEndpoint(addr string) scheduling.Endpoint {
	return scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Namespace: "default", Name: "encode-pod"},
			Address:        addr,
			Port:           encodeTestPort,
		},
		&fwkdl.Metrics{},
		nil,
	)
}

func TestEncodeHeaderHandlerFactory(t *testing.T) {
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
			expectProfile: "encode",
			expectName:    "my-handler",
		},
		{
			name:          "custom encode profile",
			pluginName:    "custom-handler",
			rawParams:     `{"encodeProfile": "my-encode"}`,
			expectErr:     false,
			expectProfile: "my-encode",
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

			p, err := EncodeHeaderHandlerFactory(tt.pluginName, raw, nil)
			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, p)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, p)

			handler, ok := p.(*EncodeHeaderHandler)
			require.True(t, ok)
			assert.Equal(t, tt.expectName, handler.TypedName().Name)
			assert.Equal(t, EncodeHeaderHandlerType, handler.TypedName().Type)
			assert.Equal(t, tt.expectProfile, handler.encodeProfile)
		})
	}
}

func TestPreRequestEncodeNilRequest(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewEncodeHeaderHandler("encode").WithName("test")

	result := &scheduling.SchedulingResult{
		ProfileResults: map[string]*scheduling.ProfileRunResult{},
	}

	// Should not panic
	assert.NotPanics(t, func() {
		handler.PreRequest(ctx, nil, result)
	})
}

func TestPreRequestEncodeNilSchedulingResult(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewEncodeHeaderHandler("encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}

	// Should not panic
	assert.NotPanics(t, func() {
		handler.PreRequest(ctx, request, nil)
	})
}

func TestPreRequestEncodeProfileExists(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewEncodeHeaderHandler("encode").WithName("test")

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
					makeEncodeEndpoint(encodeTestAddr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(encodeTestAddr, encodeTestPort), request.Headers[common.EncoderEndpointsHeader])
}

func TestPreRequestEncodeProfileNotExists(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewEncodeHeaderHandler("encode").WithName("test")

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
	handler := NewEncodeHeaderHandler("encode").WithName("test")

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
					makeEncodeEndpoint(encodeTestAddr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(encodeTestAddr, encodeTestPort), request.Headers[common.EncoderEndpointsHeader])
}

func TestPreRequestEncodeClearsHeaderWhenNoEncodeResult(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewEncodeHeaderHandler("encode").WithName("test")

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
	handler := NewEncodeHeaderHandler("my-custom-encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"my-custom-encode": {
				TargetEndpoints: []scheduling.Endpoint{
					makeEncodeEndpoint(encodeTestAddr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(encodeTestAddr, encodeTestPort), request.Headers[common.EncoderEndpointsHeader])
}

func TestPreRequestEncodeIPv6Address(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewEncodeHeaderHandler("encode").WithName("test")

	request := &scheduling.LLMRequest{
		RequestId: "req-123",
		Headers:   map[string]string{},
	}

	result := &scheduling.SchedulingResult{
		PrimaryProfileName: "decode",
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			"encode": {
				TargetEndpoints: []scheduling.Endpoint{
					makeEncodeEndpoint(encodeTestIPv6Addr),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	assert.Equal(t, net.JoinHostPort(encodeTestIPv6Addr, encodeTestPort), request.Headers[common.EncoderEndpointsHeader])
}

func TestPreRequestEncodeMultipleEndpoints(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handler := NewEncodeHeaderHandler("encode").WithName("test")

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
					makeEncodeEndpoint(encodeTestAddr),
					makeEncodeEndpoint(addr2),
				},
			},
		},
	}

	handler.PreRequest(ctx, request, result)

	expected := strings.Join([]string{
		net.JoinHostPort(encodeTestAddr, encodeTestPort),
		net.JoinHostPort(addr2, encodeTestPort),
	}, ",")
	assert.Equal(t, expected, request.Headers[common.EncoderEndpointsHeader])
}
