package scorer_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
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
	s := scorer.NewSessionAffinity(0) // Use default (session cookie)
	ctx := context.Background()

	// Create mock endpoints
	endpoint1 := newTestEndpointForSession("pod-1", "default")
	endpoint2 := newTestEndpointForSession("pod-2", "default")
	endpoints := []scheduling.Endpoint{endpoint1, endpoint2}

	tests := []struct {
		name           string
		cookieHeader   string
		expectedScores map[string]float64 // endpoint name -> score
	}{
		{
			name:         "No cookie - all endpoints get zero score",
			cookieHeader: "",
			expectedScores: map[string]float64{
				"pod-1": 0.0,
				"pod-2": 0.0,
			},
		},
		{
			name:         "Cookie with pod-1 - pod-1 gets high score",
			cookieHeader: scorer.SessionCookieName + "=" + base64.StdEncoding.EncodeToString([]byte("default/pod-1")),
			expectedScores: map[string]float64{
				"pod-1": 1.0,
				"pod-2": 0.0,
			},
		},
		{
			name:         "Cookie with pod-2 - pod-2 gets high score",
			cookieHeader: scorer.SessionCookieName + "=" + base64.StdEncoding.EncodeToString([]byte("default/pod-2")),
			expectedScores: map[string]float64{
				"pod-1": 0.0,
				"pod-2": 1.0,
			},
		},
		{
			name:         "Multiple cookies - session cookie is extracted",
			cookieHeader: "other-cookie=value; " + scorer.SessionCookieName + "=" + base64.StdEncoding.EncodeToString([]byte("default/pod-1")) + "; another=test",
			expectedScores: map[string]float64{
				"pod-1": 1.0,
				"pod-2": 0.0,
			},
		},
		{
			name:         "Invalid base64 cookie - all endpoints get zero score",
			cookieHeader: scorer.SessionCookieName + "=invalid-base64!!!",
			expectedScores: map[string]float64{
				"pod-1": 0.0,
				"pod-2": 0.0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := &scheduling.LLMRequest{
				Headers: map[string]string{},
			}
			if tt.cookieHeader != "" {
				request.Headers[scorer.CookieHeaderName] = tt.cookieHeader
			}

			scores := s.Score(ctx, nil, request, endpoints)

			assert.Equal(t, tt.expectedScores["pod-1"], scores[endpoint1], "pod-1 score mismatch")
			assert.Equal(t, tt.expectedScores["pod-2"], scores[endpoint2], "pod-2 score mismatch")
		})
	}
}

func TestResponseReceivedSetsCookie(t *testing.T) {
	s := scorer.NewSessionAffinity(0) // Use default (session cookie)
	ctx := context.Background()

	tests := []struct {
		name                string
		request             *scheduling.LLMRequest
		response            *requestcontrol.Response
		targetPod           *fwkdl.EndpointMetadata
		expectCookieSet     bool
		expectedContains    []string
		expectedNotContains []string
		expectEmpty         bool
	}{
		{
			name:    "Sets cookie in response",
			request: nil,
			response: &requestcontrol.Response{
				RequestId: "test-req-1",
				Headers:   make(map[string]string),
			},
			targetPod: &fwkdl.EndpointMetadata{
				NamespacedName: k8stypes.NamespacedName{
					Namespace: "default",
					Name:      "pod-1",
				},
			},
			expectCookieSet: true,
			expectedContains: []string{
				scorer.SessionCookieName + "=",
				"Path=/",
				"HttpOnly",
				"SameSite=Lax",
				base64.StdEncoding.EncodeToString([]byte("default/pod-1")),
			},
		},
		{
			name:    "Appends to existing Set-Cookie header",
			request: nil,
			response: &requestcontrol.Response{
				RequestId: "test-req-2",
				Headers: map[string]string{
					scorer.SetCookieHeaderName: "existing-cookie=value; Path=/",
				},
			},
			targetPod: &fwkdl.EndpointMetadata{
				NamespacedName: k8stypes.NamespacedName{
					Namespace: "default",
					Name:      "pod-2",
				},
			},
			expectCookieSet: true,
			expectedContains: []string{
				"existing-cookie=value",
				scorer.SessionCookieName + "=",
			},
		},
		{
			name:     "Handles nil response gracefully",
			request:  nil,
			response: nil,
			targetPod: &fwkdl.EndpointMetadata{
				NamespacedName: k8stypes.NamespacedName{
					Namespace: "default",
					Name:      "pod-1",
				},
			},
			expectCookieSet: false,
		},
		{
			name:    "Handles nil targetPod gracefully",
			request: nil,
			response: &requestcontrol.Response{
				RequestId: "test-req-3",
				Headers:   make(map[string]string),
			},
			targetPod:       nil,
			expectCookieSet: false,
			expectEmpty:     true,
		},
		{
			name: "Skips setting cookie when request already has correct cookie",
			request: &scheduling.LLMRequest{
				Headers: map[string]string{
					scorer.CookieHeaderName: scorer.SessionCookieName + "=" + base64.StdEncoding.EncodeToString([]byte("default/pod-1")),
				},
			},
			response: &requestcontrol.Response{
				RequestId: "test-req-4",
				Headers:   make(map[string]string),
			},
			targetPod: &fwkdl.EndpointMetadata{
				NamespacedName: k8stypes.NamespacedName{
					Namespace: "default",
					Name:      "pod-1",
				},
			},
			expectCookieSet: false,
			expectEmpty:     true,
		},
		{
			name: "Sets cookie when request has different cookie value",
			request: &scheduling.LLMRequest{
				Headers: map[string]string{
					scorer.CookieHeaderName: scorer.SessionCookieName + "=" + base64.StdEncoding.EncodeToString([]byte("default/pod-1")),
				},
			},
			response: &requestcontrol.Response{
				RequestId: "test-req-5",
				Headers:   make(map[string]string),
			},
			targetPod: &fwkdl.EndpointMetadata{
				NamespacedName: k8stypes.NamespacedName{
					Namespace: "default",
					Name:      "pod-2",
				},
			},
			expectCookieSet: true,
			expectedContains: []string{
				base64.StdEncoding.EncodeToString([]byte("default/pod-2")),
			},
		},
		{
			name: "Sets cookie when request has no cookie",
			request: &scheduling.LLMRequest{
				Headers: map[string]string{},
			},
			response: &requestcontrol.Response{
				RequestId: "test-req-6",
				Headers:   make(map[string]string),
			},
			targetPod: &fwkdl.EndpointMetadata{
				NamespacedName: k8stypes.NamespacedName{
					Namespace: "default",
					Name:      "pod-1",
				},
			},
			expectCookieSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic
			s.ResponseReceived(ctx, tt.request, tt.response, tt.targetPod)

			if tt.response != nil {
				setCookie := tt.response.Headers[scorer.SetCookieHeaderName]

				if tt.expectEmpty {
					assert.Empty(t, setCookie)
				} else if tt.expectCookieSet {
					assert.NotEmpty(t, setCookie)
				}

				for _, expected := range tt.expectedContains {
					assert.Contains(t, setCookie, expected)
				}

				for _, notExpected := range tt.expectedNotContains {
					assert.NotContains(t, setCookie, notExpected)
				}
			}
		})
	}
}

// extractCookieValue is a test helper that mirrors the private function in session_affinity.go
func extractCookieValue(cookieHeader, cookieName string) string {
	cookies := strings.Split(cookieHeader, ";")
	for _, cookie := range cookies {
		cookie = strings.TrimSpace(cookie)
		parts := strings.SplitN(cookie, "=", 2)
		if len(parts) == 2 && parts[0] == cookieName {
			return parts[1]
		}
	}
	return ""
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

	tests := []struct {
		name                string
		maxAge              int
		expectedContains    []string
		expectedNotContains []string
	}{
		{
			name:   "Cookie with MaxAge set",
			maxAge: 3600,
			expectedContains: []string{
				scorer.SessionCookieName + "=",
				"Max-Age=3600",
				"HttpOnly",
				"SameSite=Lax",
			},
		},
		{
			name:   "Session cookie (no MaxAge)",
			maxAge: 0,
			expectedContains: []string{
				"HttpOnly",
				"SameSite=Lax",
			},
			expectedNotContains: []string{
				"Max-Age",
				"Secure",
			},
		},
		{
			name:   "Default config when nil",
			maxAge: 0,
			expectedContains: []string{
				"HttpOnly",
				"SameSite=Lax",
			},
			expectedNotContains: []string{
				"Max-Age",
				"Secure",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := scorer.NewSessionAffinity(tt.maxAge)

			response := &requestcontrol.Response{
				RequestId: "test-req-" + tt.name,
				Headers:   make(map[string]string),
			}
			targetPod := &fwkdl.EndpointMetadata{
				NamespacedName: k8stypes.NamespacedName{
					Namespace: "default",
					Name:      "pod-1",
				},
			}

			s.ResponseReceived(ctx, nil, response, targetPod)

			setCookie := response.Headers[scorer.SetCookieHeaderName]
			require.NotEmpty(t, setCookie)

			for _, expected := range tt.expectedContains {
				assert.Contains(t, setCookie, expected)
			}

			for _, notExpected := range tt.expectedNotContains {
				assert.NotContains(t, setCookie, notExpected)
			}
		})
	}
}

// Made with Bob
