package scorer

import (
	"encoding/base64"
	"time"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	sessionKeepAliveTime           = 60 * time.Minute  // How long should an idle session be kept alive
	sessionKeepAliveCheckFrequency = 15 * time.Minute  // How often to check for overly idle sessions
	sessionTokenHeader             = "x-session-token" // name of the session header in request
)

// SessionAffinity is a routing scorer that routes subsequent
// requests in a session to the same pod as the first request in the
// session was sent to, by giving that pod the specified weight and assigning
// zero score to the rest of the targets
type SessionAffinity struct {
}

var _ plugins.Scorer = &SessionAffinity{}       // validate interface conformance
var _ plugins.PostResponse = &SessionAffinity{} // validate interface conformance

// NewSessionAffinity returns a scorer
func NewSessionAffinity() *SessionAffinity {
	return &SessionAffinity{}
}

// Name returns the scorer's name
func (s *SessionAffinity) Name() string {
	return "session-affinity-scorer"
}

// Score assign a high score to the pod used in previous requests and zero to others
func (s *SessionAffinity) Score(ctx *types.SchedulingContext, pods []types.Pod) map[types.Pod]float64 {
	scoredPods := make(map[types.Pod]float64)
	sessionToken := ctx.Req.Headers[sessionTokenHeader]
	podName := ""

	if sessionToken != "" {
		decodedBytes, err := base64.StdEncoding.DecodeString(sessionToken)
		if err != nil {
			ctx.Logger.Error(err, "Error decoding session header")
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
func (s *SessionAffinity) PostResponse(ctx *types.SchedulingContext, pod types.Pod) {
	if ctx.Resp == nil || pod == nil || pod.GetPod() == nil {
		return
	}

	if ctx.Resp.Headers == nil { // TODO should always be populated?
		ctx.Resp.Headers = make(map[string]string)
	}

	ctx.Resp.Headers[sessionTokenHeader] = base64.StdEncoding.EncodeToString([]byte(pod.GetPod().NamespacedName.String()))
}
