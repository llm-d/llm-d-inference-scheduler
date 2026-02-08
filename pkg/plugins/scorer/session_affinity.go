package scorer

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

	sessionCookieName   = "llm-d-session" // name of the session cookie
	setCookieHeaderName = "Set-Cookie"    // HTTP Set-Cookie header name (canonical case)
)

// compile-time type assertion
var _ scheduling.Scorer = &SessionAffinity{}
var _ requestcontrol.ResponseReceived = &SessionAffinity{}

// SessionAffinityConfig holds configuration for the SessionAffinity scorer.
type SessionAffinityConfig struct {
	// MaxAge specifies the cookie's Max-Age attribute in seconds.
	// If 0 (default), the cookie is a session cookie (expires when browser closes).
	// If negative, the cookie is deleted immediately.
	MaxAge int `json:"maxAge,omitempty"`

	// Secure specifies whether the cookie should only be sent over HTTPS.
	// Should be true in production environments with HTTPS.
	Secure bool `json:"secure,omitempty"`

	// SameSite specifies the SameSite attribute for the cookie.
	// Valid values: "Lax" (default), "Strict", "None"
	// "Lax" provides good CSRF protection while allowing normal navigation.
	SameSite string `json:"sameSite,omitempty"`
}

// SessionAffinityFactory defines the factory function for SessionAffinity scorer.
func SessionAffinityFactory(name string, rawConfig json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	config := &SessionAffinityConfig{
		SameSite: "Lax", // default
	}

	if len(rawConfig) > 0 {
		if err := json.Unmarshal(rawConfig, config); err != nil {
			return nil, err
		}
	}

	return NewSessionAffinity(config).WithName(name), nil
}

// NewSessionAffinity returns a scorer with the given configuration
func NewSessionAffinity(config *SessionAffinityConfig) *SessionAffinity {
	if config == nil {
		config = &SessionAffinityConfig{
			SameSite: "Lax",
		}
	}
	return &SessionAffinity{
		typedName: plugin.TypedName{Type: SessionAffinityType},
		config:    config,
	}
}

// SessionAffinity is a routing scorer that routes subsequent
// requests in a session to the same pod as the first request in the
// session was sent to, by giving that pod the specified weight and assigning
// zero score to the rest of the targets
type SessionAffinity struct {
	typedName plugin.TypedName
	config    *SessionAffinityConfig
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
	podName := ""

	// Extract session cookie from Cookie header
	cookieHeader := request.Headers["cookie"]
	if cookieHeader != "" {
		sessionToken := extractCookieValue(cookieHeader, sessionCookieName)
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
		if endpoint.GetMetadata().NamespacedName.String() == podName {
			scoredEndpoints[endpoint] = 1.0
		}
	}

	return scoredEndpoints
}

// ResponseReceived sets the session cookie on the response sent to the client
func (s *SessionAffinity) ResponseReceived(ctx context.Context, _ *scheduling.LLMRequest, response *requestcontrol.Response, targetPod *datalayer.EndpointMetadata) {

	if response == nil || targetPod == nil {
		reqID := "undefined"
		if response != nil {
			reqID = response.RequestId
		}
		log.FromContext(ctx).V(logutil.DEBUG).Info("Session affinity scorer - skip post response because one of response, targetPod is nil", "req id", reqID)
		return
	}

	if response.Headers == nil {
		response.Headers = make(map[string]string)
	}

	// Create session cookie with encoded pod name
	sessionToken := base64.StdEncoding.EncodeToString([]byte(targetPod.NamespacedName.String()))
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.config.Secure,
		SameSite: parseSameSite(s.config.SameSite),
	}

	// Set MaxAge if configured (0 means session cookie)
	if s.config.MaxAge != 0 {
		cookie.MaxAge = s.config.MaxAge
	}
	cookieStr := cookie.String()

	// Append to existing Set-Cookie headers if any
	existingSetCookie := response.Headers[setCookieHeaderName]
	if existingSetCookie != "" {
		response.Headers[setCookieHeaderName] = existingSetCookie + "\n" + cookieStr
	} else {
		response.Headers[setCookieHeaderName] = cookieStr
	}
}

// parseSameSite converts string to http.SameSite value
func parseSameSite(sameSite string) http.SameSite {
	switch strings.ToLower(sameSite) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	case "lax", "":
		return http.SameSiteLaxMode
	default:
		return http.SameSiteLaxMode
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
