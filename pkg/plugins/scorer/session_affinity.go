package scorer

import (
	"context"
	"encoding/base64"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	// SessionAffinityScorerType is the type of the SessionAffinityScorer
	SessionAffinityScorerType = "session-affinity-scorer"

	sessionKeepAliveTime           = 60 * time.Minute  // How long should an idle session be kept alive
	sessionKeepAliveCheckFrequency = 15 * time.Minute  // How often to check for overly idle sessions
	sessionTokenHeader             = "x-session-token" // name of the session header in request
)

// compile-time type assertion
var _ framework.Scorer = &SessionAffinity{}
var _ requestcontrol.PostResponse = &SessionAffinity{}

// NewSessionAffinity returns a scorer
func NewSessionAffinity() *SessionAffinity {
	return &SessionAffinity{
		name: SessionAffinityScorerType,
	}
}

// SessionAffinity is a routing scorer that routes subsequent
// requests in a session to the same pod as the first request in the
// session was sent to, by giving that pod the specified weight and assigning
// zero score to the rest of the targets
type SessionAffinity struct {
	name string
}

// Type returns the type of the scorer.
func (s *SessionAffinity) Type() string {
	return SessionAffinityScorerType
}

// Name returns the name of the instance of the filter.
func (s *SessionAffinity) Name() string {
	return s.name
}

// WithName sets the name of the filter.
func (s *SessionAffinity) WithName(name string) *SessionAffinity {
	s.name = name
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
		log.FromContext(ctx).V(logutil.DEBUG).Info("Session affinity scorer - skip post response because one of ctx.Resp, pod, pod.GetPod is nil", "req id", reqID)
		return
	}

	if response.Headers == nil { // TODO should always be populated?
		response.Headers = make(map[string]string)
	}

	response.Headers[sessionTokenHeader] = base64.StdEncoding.EncodeToString([]byte(targetPod.NamespacedName.String()))
}
