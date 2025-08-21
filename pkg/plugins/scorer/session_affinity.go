package scorer

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	// SessionAffinityType is the type of the SessionAffinity scorer.
	SessionAffinityType = "session-affinity-scorer"

	sessionTokenHeader = "x-session-token" // name of the session header in request
)

// compile-time type assertion
var _ framework.Scorer = &SessionAffinity{}
var _ requestcontrol.PostResponse = &SessionAffinity{}

// SessionAffinityFactory defines the factory function for SessionAffinity scorer.
func SessionAffinityFactory(name string, _ json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	return NewSessionAffinity().WithName(name), nil
}

// NewSessionAffinity returns a scorer
func NewSessionAffinity() *SessionAffinity {
	return &SessionAffinity{
		typedName: plugins.TypedName{Type: SessionAffinityType},
	}
}

// SessionAffinity is a routing scorer that routes subsequent
// requests in a session to the same pod as the first request in the
// session was sent to, by giving that pod the specified weight and assigning
// zero score to the rest of the targets
type SessionAffinity struct {
	typedName plugins.TypedName
}

// TypedName returns the typed name of the plugin.
func (s *SessionAffinity) TypedName() plugins.TypedName {
	return s.typedName
}

// WithName sets the name of the plugin.
func (s *SessionAffinity) WithName(name string) *SessionAffinity {
	s.typedName.Name = name
	return s
}

// Score assign a high score to the pod used in previous requests and zero to others
func (s *SessionAffinity) Score(ctx context.Context, _ *types.CycleState, request *types.LLMRequest, pods []types.Pod) map[types.Pod]float64 {
	scoredPods := make(map[types.Pod]float64)
	sessionToken := request.Headers[sessionTokenHeader]
	podName := ""

	if sessionToken != "" {
		decodedBytes, err := base64.StdEncoding.DecodeString(sessionToken)
		if err != nil {
			log.FromContext(ctx).Error(err, "Error decoding session header")
		} else {
			podName = string(decodedBytes)
		}
	}
	for _, pod := range pods {
		scoredPods[pod] = 0.0 // initial value
		if pod.GetPod().NamespacedName.String() == podName {
			scoredPods[pod] = 1.0
		}
	}

	return scoredPods
}

// PostResponse sets the session header on the response sent to the client
// TODO: this should be using a cookie and ensure not overriding any other
// cookie values if present.
// Tracked in https://github.com/llm-d/llm-d-inference-scheduler/issues/28
func (s *SessionAffinity) PostResponse(ctx context.Context, _ *types.LLMRequest, response *requestcontrol.Response, targetPod *backend.Pod) {
	if response == nil || targetPod == nil {
		reqID := "undefined"
		if response != nil {
			reqID = response.RequestId
		}
		log.FromContext(ctx).V(logutil.DEBUG).Info("Session affinity scorer - skip post response because one of response, targetPod is nil", "req id", reqID)
		return
	}

	if response.Headers == nil { // TODO should always be populated?
		response.Headers = make(map[string]string)
	}

	response.Headers[sessionTokenHeader] = base64.StdEncoding.EncodeToString([]byte(targetPod.NamespacedName.String()))
}
