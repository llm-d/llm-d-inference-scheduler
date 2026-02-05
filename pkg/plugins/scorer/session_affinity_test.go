package scorer

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

func newTestEndpointForSession(name, namespace string) scheduling.Endpoint {
	return scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: name, Namespace: namespace},
		},
		&fwkdl.Metrics{},
		nil,
	)
}

func TestSessionAffinityWithCookies(t *testing.T) {
	scorer := NewSessionAffinity(nil) // Use default config
	ctx := context.Background()

	// Create mock endpoints
	endpoint1 := newTestEndpointForSession("pod-1", "default")
	endpoint2 := newTestEndpointForSession("pod-2", "default")
	endpoints := []scheduling.Endpoint{endpoint1, endpoint2}

	t.Run("No cookie - all endpoints get zero score", func(t *testing.T) {
		request := &scheduling.LLMRequest{
			Headers: map[string]string{},
		}

		scores := scorer.Score(ctx, nil, request, endpoints)
		assert.Equal(t, 0.0, scores[endpoint1])
		assert.Equal(t, 0.0, scores[endpoint2])
	})

	t.Run("Cookie with pod-1 - pod-1 gets high score", func(t *testing.T) {
		sessionToken := base64.StdEncoding.EncodeToString([]byte("default/pod-1"))
		request := &scheduling.LLMRequest{
			Headers: map[string]string{
				"cookie": sessionCookieName + "=" + sessionToken,
			},
		}

		scores := scorer.Score(ctx, nil, request, endpoints)
		assert.Equal(t, 1.0, scores[endpoint1])
		assert.Equal(t, 0.0, scores[endpoint2])
	})

	t.Run("Cookie with pod-2 - pod-2 gets high score", func(t *testing.T) {
		sessionToken := base64.StdEncoding.EncodeToString([]byte("default/pod-2"))
		request := &scheduling.LLMRequest{
			Headers: map[string]string{
				"cookie": sessionCookieName + "=" + sessionToken,
			},
		}

		scores := scorer.Score(ctx, nil, request, endpoints)
		assert.Equal(t, 0.0, scores[endpoint1])
		assert.Equal(t, 1.0, scores[endpoint2])
	})

	t.Run("Multiple cookies - session cookie is extracted", func(t *testing.T) {
		sessionToken := base64.StdEncoding.EncodeToString([]byte("default/pod-1"))
		request := &scheduling.LLMRequest{
			Headers: map[string]string{
				"cookie": "other-cookie=value; " + sessionCookieName + "=" + sessionToken + "; another=test",
			},
		}

		scores := scorer.Score(ctx, nil, request, endpoints)
		assert.Equal(t, 1.0, scores[endpoint1])
		assert.Equal(t, 0.0, scores[endpoint2])
	})

	t.Run("Invalid base64 cookie - all endpoints get zero score", func(t *testing.T) {
		request := &scheduling.LLMRequest{
			Headers: map[string]string{
				"cookie": sessionCookieName + "=invalid-base64!!!",
			},
		}

		scores := scorer.Score(ctx, nil, request, endpoints)
		assert.Equal(t, 0.0, scores[endpoint1])
		assert.Equal(t, 0.0, scores[endpoint2])
	})
}

func TestResponseReceivedSetsCookie(t *testing.T) {
	scorer := NewSessionAffinity(nil) // Use default config
	ctx := context.Background()

	t.Run("Sets cookie in response", func(t *testing.T) {
		response := &requestcontrol.Response{
			RequestId: "test-req-1",
			Headers:   make(map[string]string),
		}
		targetPod := &fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{
				Namespace: "default",
				Name:      "pod-1",
			},
		}

		scorer.ResponseReceived(ctx, nil, response, targetPod)

		setCookie := response.Headers[setCookieHeaderName]
		assert.NotEmpty(t, setCookie)
		assert.Contains(t, setCookie, sessionCookieName+"=")
		assert.Contains(t, setCookie, "Path=/")
		assert.Contains(t, setCookie, "HttpOnly")
		assert.Contains(t, setCookie, "SameSite=Lax")

		// Verify the cookie value is correctly encoded
		expectedToken := base64.StdEncoding.EncodeToString([]byte("default/pod-1"))
		assert.Contains(t, setCookie, expectedToken)
	})

	t.Run("Appends to existing Set-Cookie header", func(t *testing.T) {
		response := &requestcontrol.Response{
			RequestId: "test-req-2",
			Headers: map[string]string{
				setCookieHeaderName: "existing-cookie=value; Path=/",
			},
		}
		targetPod := &fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{
				Namespace: "default",
				Name:      "pod-2",
			},
		}

		scorer.ResponseReceived(ctx, nil, response, targetPod)

		setCookie := response.Headers[setCookieHeaderName]
		assert.Contains(t, setCookie, "existing-cookie=value")
		assert.Contains(t, setCookie, sessionCookieName+"=")
	})

	t.Run("Handles nil response gracefully", func(t *testing.T) {
		targetPod := &fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{
				Namespace: "default",
				Name:      "pod-1",
			},
		}

		// Should not panic
		scorer.ResponseReceived(ctx, nil, nil, targetPod)
	})

	t.Run("Handles nil targetPod gracefully", func(t *testing.T) {
		response := &requestcontrol.Response{
			RequestId: "test-req-3",
			Headers:   make(map[string]string),
		}

		// Should not panic
		scorer.ResponseReceived(ctx, nil, response, nil)
		assert.Empty(t, response.Headers[setCookieHeaderName])
	})
}

func TestExtractCookieValue(t *testing.T) {
	tests := []struct {
		name         string
		cookieHeader string
		cookieName   string
		expected     string
	}{
		{
			name:         "Single cookie",
			cookieHeader: "session=abc123",
			cookieName:   "session",
			expected:     "abc123",
		},
		{
			name:         "Multiple cookies",
			cookieHeader: "cookie1=value1; session=abc123; cookie2=value2",
			cookieName:   "session",
			expected:     "abc123",
		},
		{
			name:         "Cookie not found",
			cookieHeader: "cookie1=value1; cookie2=value2",
			cookieName:   "session",
			expected:     "",
		},
		{
			name:         "Empty cookie header",
			cookieHeader: "",
			cookieName:   "session",
			expected:     "",
		},
		{
			name:         "Cookie with spaces",
			cookieHeader: "cookie1=value1;  session=abc123  ; cookie2=value2",
			cookieName:   "session",
			expected:     "abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractCookieValue(tt.cookieHeader, tt.cookieName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSessionAffinityWithConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("Cookie with MaxAge set", func(t *testing.T) {
		config := &SessionAffinityConfig{
			MaxAge:   3600, // 1 hour
			Secure:   true,
			SameSite: "Strict",
		}
		scorer := NewSessionAffinity(config)

		response := &requestcontrol.Response{
			RequestId: "test-req-config",
			Headers:   make(map[string]string),
		}
		targetPod := &fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{
				Namespace: "default",
				Name:      "pod-1",
			},
		}

		scorer.ResponseReceived(ctx, nil, response, targetPod)

		setCookie := response.Headers[setCookieHeaderName]
		assert.NotEmpty(t, setCookie)
		assert.Contains(t, setCookie, sessionCookieName+"=")
		assert.Contains(t, setCookie, "Max-Age=3600")
		assert.Contains(t, setCookie, "Secure")
		assert.Contains(t, setCookie, "SameSite=Strict")
	})

	t.Run("Session cookie (no MaxAge)", func(t *testing.T) {
		config := &SessionAffinityConfig{
			MaxAge:   0, // Session cookie
			Secure:   false,
			SameSite: "Lax",
		}
		scorer := NewSessionAffinity(config)

		response := &requestcontrol.Response{
			RequestId: "test-req-session",
			Headers:   make(map[string]string),
		}
		targetPod := &fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{
				Namespace: "default",
				Name:      "pod-1",
			},
		}

		scorer.ResponseReceived(ctx, nil, response, targetPod)

		setCookie := response.Headers[setCookieHeaderName]
		assert.NotEmpty(t, setCookie)
		assert.NotContains(t, setCookie, "Max-Age") // Session cookie has no Max-Age
		assert.NotContains(t, setCookie, "Secure")  // Secure not set
		assert.Contains(t, setCookie, "SameSite=Lax")
	})

	t.Run("Default config when nil", func(t *testing.T) {
		scorer := NewSessionAffinity(nil)

		response := &requestcontrol.Response{
			RequestId: "test-req-default",
			Headers:   make(map[string]string),
		}
		targetPod := &fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{
				Namespace: "default",
				Name:      "pod-1",
			},
		}

		scorer.ResponseReceived(ctx, nil, response, targetPod)

		setCookie := response.Headers[setCookieHeaderName]
		assert.NotEmpty(t, setCookie)
		assert.Contains(t, setCookie, "SameSite=Lax") // Default SameSite
		assert.NotContains(t, setCookie, "Secure")    // Default not secure
	})
}

func TestParseSameSite(t *testing.T) {
	tests := []struct {
		input    string
		expected http.SameSite
	}{
		{"Strict", http.SameSiteStrictMode},
		{"strict", http.SameSiteStrictMode},
		{"Lax", http.SameSiteLaxMode},
		{"lax", http.SameSiteLaxMode},
		{"None", http.SameSiteNoneMode},
		{"none", http.SameSiteNoneMode},
		{"", http.SameSiteLaxMode},        // Default
		{"invalid", http.SameSiteLaxMode}, // Default for invalid
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseSameSite(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
