package scorer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

const (
	// SessionAffinityType is the type of the SessionAffinity scorer.
	SessionAffinityType = "session-affinity-scorer"

	// SessionCookieName - the name of the session cookie
	SessionCookieName = "llm-d-session"
	// CookieHeaderName - the HTTP Cookie header name
	CookieHeaderName = "cookie"
	// SetCookieHeaderName - the HTTP Set-Cookie header name
	SetCookieHeaderName = "set-cookie"

	// defaultMaxAge is the default cookie Max-Age in seconds.
	// 0 means session cookie (expires when browser closes).
	defaultMaxAge = 0
)

// compile-time type assertion
var _ scheduling.Scorer = &SessionAffinity{}
var _ requestcontrol.ResponseReceived = &SessionAffinity{}

type sessionAffinityConfig struct {
	MaxAge int `json:"maxAge"` // Cookie Max-Age in seconds (0 = session cookie)
}

// SessionAffinityFactory defines the factory function for SessionAffinity scorer.
func SessionAffinityFactory(name string, rawConfig json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	config := sessionAffinityConfig{
		MaxAge: defaultMaxAge, // Set default
	}

	if len(rawConfig) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(rawConfig))
		decoder.DisallowUnknownFields() // Reject unknown fields
		if err := decoder.Decode(&config); err != nil {
			return nil, fmt.Errorf("invalid session affinity config: %w", err)
		}
	}

	return NewSessionAffinity(config.MaxAge).WithName(name), nil
}

// NewSessionAffinity returns a scorer with the given maxAge configuration.
// maxAge specifies the cookie's Max-Age attribute in seconds.
// If 0 (default), the cookie is a session cookie (expires when browser closes).
func NewSessionAffinity(maxAge int) *SessionAffinity {
	return &SessionAffinity{
		typedName: plugin.TypedName{Type: SessionAffinityType},
		maxAge:    maxAge,
	}
}

// SessionAffinity is a routing scorer that routes subsequent
// requests in a session to the same pod as the first request in the
// session was sent to, by giving that pod the specified weight and assigning
// zero score to the rest of the targets
type SessionAffinity struct {
	typedName plugin.TypedName
	maxAge    int
}

// TypedName returns the typed name of the plugin.
func (s *SessionAffinity) TypedName() plugin.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *SessionAffinity) WithName(name string) *SessionAffinity {
	s.typedName.Name = name
	return s
}

// Category returns the preference the scorer applies when scoring candidate endpoints.
func (s *SessionAffinity) Category() scheduling.ScorerCategory {
	return scheduling.Affinity
}

// Score assign a high score to the pod used in previous requests and zero to others
func (s *SessionAffinity) Score(ctx context.Context, _ *scheduling.CycleState, request *scheduling.LLMRequest, endpoints []scheduling.Endpoint) map[scheduling.Endpoint]float64 {
	scoredEndpoints := make(map[scheduling.Endpoint]float64)
	target := ""

	// Extract session cookie from Cookie header
	cookieHeader := request.Headers[CookieHeaderName]
	if cookieHeader != "" {
		sessionToken := extractCookieValue(cookieHeader, SessionCookieName)
		if sessionToken != "" {
			decodedBytes, err := base64.StdEncoding.DecodeString(sessionToken)
			if err != nil {
				log.FromContext(ctx).Error(err, "Error decoding session cookie")
			} else {
				target = string(decodedBytes)
			}
		}
	}

	for _, endpoint := range endpoints {
		scoredEndpoints[endpoint] = 0.0 // initial value
		if endpoint.GetMetadata().NamespacedName.String() == target {
			scoredEndpoints[endpoint] = 1.0
		}
	}

	return scoredEndpoints
}

// ResponseReceived sets the session cookie on the response sent to the client
// only if the cookie doesn't exist or its value is different
func (s *SessionAffinity) ResponseReceived(ctx context.Context, request *scheduling.LLMRequest, response *requestcontrol.Response, targetEndpoint *datalayer.EndpointMetadata) {
	logger := log.FromContext(ctx)

	if response == nil || targetEndpoint == nil {
		reqID := "undefined"
		if response != nil {
			reqID = response.RequestId
		}
		logger.V(logutil.DEBUG).Info("Session affinity scorer - skip post response because one of response, targetEndpoint is nil", "req id", reqID)
		return
	}

	// Create session token for the target endpoint
	expectedSessionToken := base64.StdEncoding.EncodeToString([]byte(targetEndpoint.NamespacedName.String()))

	// Check if client already has the correct cookie
	if request != nil && request.Headers != nil {
		cookieHeader := request.Headers[CookieHeaderName]
		if cookieHeader != "" {
			existingSessionToken := extractCookieValue(cookieHeader, SessionCookieName)
			if existingSessionToken == expectedSessionToken {
				// Cookie already exists with correct value, no need to set it again
				return
			}
		}
	}

	// Cookie doesn't exist or has different value, set it
	if response.Headers == nil {
		response.Headers = make(map[string]string)
	}

	cookie := &http.Cookie{
		Name:     SessionCookieName,
		Value:    expectedSessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   s.maxAge, // 0 means session cookie
	}
	cookieStr := cookie.String()

	logger.V(logutil.DEBUG).Info("Setting session cookie",
		"sessionToken", expectedSessionToken,
		"targetEndpoint", targetEndpoint.NamespacedName.String())

	// Append to existing Set-Cookie headers if any
	existingSetCookie := response.Headers[SetCookieHeaderName]
	if existingSetCookie != "" {
		response.Headers[SetCookieHeaderName] = existingSetCookie + "\n" + cookieStr
	} else {
		response.Headers[SetCookieHeaderName] = cookieStr
	}
}

// extractCookieValue extracts the value of a specific cookie from the Cookie header
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
