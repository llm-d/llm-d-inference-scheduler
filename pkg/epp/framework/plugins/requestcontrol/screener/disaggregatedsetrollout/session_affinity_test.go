/*
Copyright 2026 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package disaggregatedsetrollout

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	fwkrh "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/requesthandling"
	fwksched "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/scheduling"
	"github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/requestcontrol/requestheader/agentidentity"
)

// sessionAffinityConfig returns a local-mode config (single EPP, no
// coordination) with session affinity enabled, the deployment shape the
// feature targets first.
func sessionAffinityConfig() Config {
	config := validConfig()
	config.RevisionGating.DisableCoordination = true
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	return config
}

// sessionRequest builds a request carrying an agent-identity session attribute
// and a raw body whose leading bytes repeat the shared session prefix. A
// negative turn leaves the body empty; an empty sessionID omits the attribute.
func sessionRequest(sessionID string, turn int) *fwksched.InferenceRequest {
	request := &fwksched.InferenceRequest{
		Headers: map[string]string{},
		Body:    &fwkrh.InferenceRequestBody{},
	}
	if sessionID != "" {
		request.PutAttribute(agentidentity.AgentIdentityKey, sessionID)
	}
	if turn >= 0 {
		prompt := strings.Repeat("shared session prefix. ", 400) +
			" turn " + strconv.Itoa(turn)
		request.Body.RawBody = []byte(prompt)
	}
	return request
}

func servedRevision(endpoints []fwksched.Endpoint) string {
	if len(endpoints) == 0 {
		return ""
	}
	return endpoints[0].GetMetadata().Labels[testRevLabel]
}

func TestScreenerSessionAffinityZeroFlipsAcrossTurns(t *testing.T) {
	screener := newTestScreener(sessionAffinityConfig())
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	candidates := candidatePool(1, 1)

	first := servedRevision(screenCandidates(t, screener, sessionRequest("session", 0), candidates))
	if first == "" {
		t.Fatal("first turn of the session screened to no endpoint")
	}
	for turn := 1; turn < 12; turn++ {
		if got := servedRevision(screenCandidates(t, screener, sessionRequest("session", turn), candidates)); got != first {
			t.Fatalf("turn %d flipped revision: first=%s got=%s", turn, first, got)
		}
	}

	// A second session must be free to land on the other revision: stickiness
	// pins a session, not the fleet.
	other := servedRevision(screenCandidates(t, screener, sessionRequest("session-b", 0), candidates))
	if other == "" {
		t.Fatal("second session screened to no endpoint")
	}
}

func TestScreenerSessionAffinityPrefixHashKey(t *testing.T) {
	screener := newTestScreener(sessionAffinityConfig())
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	candidates := candidatePool(1, 1)

	// No session attribute: the leading RawBody bytes are the session key.
	first := servedRevision(screenCandidates(t, screener, sessionRequest("", 0), candidates))
	if first == "" {
		t.Fatal("first turn screened to no endpoint")
	}
	for turn := 1; turn < 12; turn++ {
		if got := servedRevision(screenCandidates(t, screener, sessionRequest("", turn), candidates)); got != first {
			t.Fatalf("turn %d flipped revision without a session attribute: first=%s got=%s", turn, first, got)
		}
	}
}

func TestScreenerSessionAffinityPrefixHashTruncation(t *testing.T) {
	screener := newTestScreener(sessionAffinityConfig())
	screener.sessionAffinity.PrefixBytes = intPtr(32)

	shared := strings.Repeat("a", 64)
	within := strings.Repeat("b", 64)

	sameA := sessionRequest("", -1)
	sameA.Body.RawBody = []byte(shared + "tail-one")
	sameB := sessionRequest("", -1)
	sameB.Body.RawBody = []byte(shared + "tail-two")
	if screener.sessionKey(sameA) == "" || screener.sessionKey(sameA) != screener.sessionKey(sameB) {
		t.Fatalf("bodies differing past prefixBytes must share a key: %q vs %q",
			screener.sessionKey(sameA), screener.sessionKey(sameB))
	}

	different := sessionRequest("", -1)
	different.Body.RawBody = []byte(within + "tail-one")
	if screener.sessionKey(sameA) == screener.sessionKey(different) {
		t.Fatal("bodies differing within prefixBytes must not share a key")
	}
}

func TestScreenerSessionAffinityPrefersAttributeOverPrefixHash(t *testing.T) {
	screener := newTestScreener(sessionAffinityConfig())

	request := sessionRequest("session", -1)
	request.Body.RawBody = []byte(strings.Repeat("a", 64))

	if screener.sessionKey(request) != "session" {
		t.Fatalf("session attribute must win over the prefix hash: %q", screener.sessionKey(request))
	}
}

func TestScreenerSessionAffinityPrefixHashDisabled(t *testing.T) {
	config := sessionAffinityConfig()
	config.RevisionGating.SessionAffinity.PrefixBytes = intPtr(0)
	screener := newTestScreener(config)

	request := sessionRequest("", -1)
	request.Body.RawBody = []byte(strings.Repeat("a", 64))
	if screener.sessionKey(request) != "" {
		t.Fatalf("prefixBytes=0 must disable the prefix-hash fallback: %q", screener.sessionKey(request))
	}
}

func TestScreenerSessionAffinityShortBodyGetsNoKey(t *testing.T) {
	screener := newTestScreener(sessionAffinityConfig())
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})

	// A body shorter than prefixBytes would change every turn as the
	// conversation grows; hashing it must not claim affinity.
	request := sessionRequest("", -1)
	request.Body.RawBody = []byte("short conversation turn")
	if key := screener.sessionKey(request); key != "" {
		t.Fatalf("bodies shorter than prefixBytes must not derive a key: %q", key)
	}
	if got := screenCandidates(t, screener, request, candidatePool(1, 1)); len(got) != 1 {
		t.Fatalf("short body screened to %v", got)
	}
	if items := screener.sessionDecisions.Items(); len(items) != 0 {
		t.Fatalf("short body must not store a session decision: %v", items)
	}
}

func TestScreenerSessionAffinityScopeSeparatesDeployments(t *testing.T) {
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}

	// Two deployments share a syncer, a plugin name, and the user's session
	// ID; only the rollout scope differs.
	screenerA := newTestScreener(config)
	configB := config
	configB.Scope = Scope{LabelSelector: "disaggregatedset.x-k8s.io/name=other-set"}
	screenerB := newTestScreener(configB)

	seedCounts(t, screenerA, map[string]map[string]int{"v1": {"prefill": 1, "decode": 1}})
	for _, role := range []string{"prefill", "decode"} {
		pod := readyPod("v2-"+role, "v2", role)
		pod.Labels["disaggregatedset.x-k8s.io/name"] = "other-set"
		seedPods(t, screenerB, pod)
	}
	if screenerA.sessionStateKey == screenerB.sessionStateKey {
		t.Fatal("screeners with different scopes must not share a session state key")
	}

	syncer := &decisionSyncer{}
	screenerA.handle.SetCrossReplicaSyncer(syncer)
	screenerB.handle.SetCrossReplicaSyncer(syncer)

	if got := servedRevision(screenCandidates(t, screenerA, sessionRequest("session", 0), candidatePool(1, 1))); got != "v1" {
		t.Fatalf("deployment A must pin v1: got %s", got)
	}
	if got := servedRevision(screenCandidates(t, screenerB, sessionRequest("session", 0), candidatePool(1, 1))); got != "v2" {
		t.Fatalf("deployment B must pin v2: got %s", got)
	}
	if stored, ok := syncer.stored(screenerA.sessionStateKey, "session"); !ok || stored != "v1" {
		t.Fatalf("deployment A's session decision must survive B's assignment: %v", stored)
	}
	if stored, ok := syncer.stored(screenerB.sessionStateKey, "session"); !ok || stored != "v2" {
		t.Fatalf("deployment B must hold its own decision: %v", stored)
	}
}

func TestScreenerSessionAffinityNamespaceSeparatesDeployments(t *testing.T) {
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}

	// Two deployments differ only by namespace: same plugin name, same
	// selector, one shared syncer, and the user's one session ID.
	config.Scope.Namespace = "prod"
	screenerProd := newTestScreener(config)
	configStaging := config
	configStaging.Scope = Scope{LabelSelector: config.Scope.LabelSelector, Namespace: "staging"}
	screenerStaging := newTestScreener(configStaging)

	podProd := readyPod("v1-prefill", "v1", "prefill")
	podProd.Namespace = "prod"
	podProdDecode := readyPod("v1-decode", "v1", "decode")
	podProdDecode.Namespace = "prod"
	seedPods(t, screenerProd, podProd, podProdDecode)

	podStaging := readyPod("v2-prefill", "v2", "prefill")
	podStaging.Namespace = "staging"
	podStagingDecode := readyPod("v2-decode", "v2", "decode")
	podStagingDecode.Namespace = "staging"
	seedPods(t, screenerStaging, podStaging, podStagingDecode)

	// The staging screener must ignore the prod namespace's pods even though
	// they match its selector.
	if _, blended := screenerStaging.distributionSnapshot().shares["v1"]; blended {
		t.Fatal("a namespaced scope must not track pods from other namespaces")
	}
	if screenerProd.sessionStateKey == screenerStaging.sessionStateKey {
		t.Fatal("screeners in different namespaces must not share a session state key")
	}

	syncer := &decisionSyncer{}
	screenerProd.handle.SetCrossReplicaSyncer(syncer)
	screenerStaging.handle.SetCrossReplicaSyncer(syncer)

	if got := servedRevision(screenCandidates(t, screenerProd, sessionRequest("session", 0), candidatePool(1, 1))); got != "v1" {
		t.Fatalf("prod must pin v1: got %s", got)
	}
	if got := servedRevision(screenCandidates(t, screenerStaging, sessionRequest("session", 0), candidatePool(1, 1))); got != "v2" {
		t.Fatalf("staging must pin v2 without invalidating prod's decision: got %s", got)
	}
	if stored, ok := syncer.stored(screenerProd.sessionStateKey, "session"); !ok || stored != "v1" {
		t.Fatalf("prod session decision must survive staging's assignment: %v", stored)
	}
	if stored, ok := syncer.stored(screenerStaging.sessionStateKey, "session"); !ok || stored != "v2" {
		t.Fatalf("staging must hold its own decision: %v", stored)
	}
}

func TestScreenerSessionAffinityFailsClosedOnSessionReadError(t *testing.T) {
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	syncer := &decisionSyncer{}
	syncer.seed(screener.sessionStateKey, "session", "v1")
	syncer.err = errors.New("redis unavailable")
	screener.handle.SetCrossReplicaSyncer(syncer)

	// The stored decision cannot be read: serving a fresh pick could flip the
	// session off v1, so the request must fail closed.
	got := screenCandidates(t, screener, sessionRequest("session", 0), candidatePool(1, 1))
	if len(got) != 0 {
		t.Fatalf("unreadable session decision must fail closed: %v", got)
	}
}

func TestScreenerSessionAffinityFailsClosedOnSessionWriteError(t *testing.T) {
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	syncer := &decisionSyncer{}
	syncer.seed(screener.sessionStateKey, "session", "v1")
	syncer.seed(screener.revisionDecisionStateKey, "decision-id", "v2")
	syncer.replaceErr = errors.New("redis unavailable")
	screener.handle.SetCrossReplicaSyncer(syncer)

	// The session holds v1 but request-level coordination decides v2. Serving
	// v2 while the store keeps v1 would flip the session back on its next
	// turn, so the request must fail closed.
	request := sessionRequest("session", 0)
	request.Headers[reqcommon.RevisionDecisionIDHeaderKey] = "decision-id"
	got := screenCandidates(t, screener, request, candidatePool(1, 1))
	if len(got) != 0 {
		t.Fatalf("unstorable coordinated revision must fail closed: %v", got)
	}
	// The store must still hold the session's actual decision.
	if stored, ok := syncer.stored(screener.sessionStateKey, "session"); !ok || stored != "v1" {
		t.Fatalf("failed write must not corrupt the stored decision: %v", stored)
	}
}

func intPtr(i int) *int { return &i }

func TestScreenerSessionAffinityInvalidatesWhenRevisionLosesCoverage(t *testing.T) {
	screener := newTestScreener(sessionAffinityConfig())
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	candidates := candidatePool(1, 1)

	pinned := servedRevision(screenCandidates(t, screener, sessionRequest("session", 0), candidates))
	if pinned == "" {
		t.Fatal("first turn screened to no endpoint")
	}

	// Every pod of the pinned revision goes NotReady: its share drops to zero.
	flipRevisionNotReady(t, screener, pinned)

	moved := servedRevision(screenCandidates(t, screener, sessionRequest("session", 1), candidates))
	if moved == "" || moved == pinned {
		t.Fatalf("session must leave a revision with zero share: pinned=%s got=%s", pinned, moved)
	}
	again := servedRevision(screenCandidates(t, screener, sessionRequest("session", 2), candidates))
	if again != moved {
		t.Fatalf("replacement decision must stick: moved=%s again=%s", moved, again)
	}
}

func TestScreenerSessionAffinityKeepsStickyWhenNewRevisionAppears(t *testing.T) {
	screener := newTestScreener(sessionAffinityConfig())
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	candidates := candidatePool(1, 1)
	candidates = append(candidates, endpoint("v3-0", revLabels("v3")))

	pinned := servedRevision(screenCandidates(t, screener, sessionRequest("session", 0), candidates))
	if pinned == "" {
		t.Fatal("first turn screened to no endpoint")
	}
	seedCounts(t, screener, map[string]map[string]int{"v3": {"prefill": 1, "decode": 1}})
	for turn := 1; turn < 6; turn++ {
		if got := servedRevision(screenCandidates(t, screener, sessionRequest("session", turn), candidates)); got != pinned {
			t.Fatalf("turn %d left its revision after a new revision appeared: pinned=%s got=%s", turn, pinned, got)
		}
	}
}

func TestScreenerSessionAffinityStrictHeaderStillAuthoritative(t *testing.T) {
	screener := newTestScreener(sessionAffinityConfig())
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})

	request := sessionRequest("session", 0)
	request.Headers["x-llm-d-disagg-revision"] = "v2"
	got := screenCandidates(t, screener, request, candidatePool(1, 1))
	if servedRevision(got) != "v2" {
		t.Fatalf("strict revision header must win over session affinity: %v", got)
	}
	// The strict path must not have recorded a session decision.
	if sticky, ok := screener.lookupStickyRevision("session"); ok && sticky != "" {
		t.Fatalf("strict path stored a session decision: %q", sticky)
	}
}

func TestScreenerSessionAffinityUsesSyncerWhenCoordinationEnabled(t *testing.T) {
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	syncer := &decisionSyncer{actual: "v2"}
	screener.handle.SetCrossReplicaSyncer(syncer)

	got := screenCandidates(t, screener, sessionRequest("session", 0), candidatePool(1, 1))
	if servedRevision(got) != "v2" {
		t.Fatalf("synced session decision did not pin revision v2: %v", got)
	}
	if syncer.calls != 1 || len(syncer.ids) != 1 || syncer.ids[0] != "session" {
		t.Fatalf("GetOrSet calls = %d, ids = %v", syncer.calls, syncer.ids)
	}
}

func TestScreenerSessionAffinityLocalWhenCoordinationDisabled(t *testing.T) {
	screener := newTestScreener(sessionAffinityConfig())
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	screener.handle.SetCrossReplicaSyncer(&decisionSyncer{err: errMustNotCoordinate})

	first := servedRevision(screenCandidates(t, screener, sessionRequest("session", 0), candidatePool(1, 1)))
	if first == "" {
		t.Fatal("first turn screened to no endpoint")
	}
	// DisableCoordination keeps the syncer out of the session path entirely.
	if got := servedRevision(screenCandidates(t, screener, sessionRequest("session", 1), candidatePool(1, 1))); got != first {
		t.Fatalf("local session decision was not stable: first=%s got=%s", first, got)
	}
}

func TestScreenerSessionAffinityNoKeyKeepsPerRequestBehavior(t *testing.T) {
	config := sessionAffinityConfig()
	config.RevisionGating.SessionAffinity.PrefixBytes = intPtr(0)
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})

	// No session attribute, prefix hashing off: per-request weighted pick.
	request := sessionRequest("", -1)
	request.Body.RawBody = []byte(strings.Repeat("a", 64))
	got := screenCandidates(t, screener, request, candidatePool(1, 1))
	if len(got) != 1 {
		t.Fatalf("keyless request screened to %v", got)
	}
	if sticky, ok := screener.lookupStickyRevision(screener.sessionKey(request)); ok && sticky != "" {
		t.Fatalf("keyless request stored a session decision: %q", sticky)
	}
}

func TestScreenerSessionAffinityDisabledByDefault(t *testing.T) {
	screener := newTestScreener(validConfig())
	if screener.sessionDecisions != nil {
		t.Fatal("session affinity must be opt-in")
	}
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	request := sessionRequest("session", 0)
	if screener.sessionKey(request) != "" {
		t.Fatalf("disabled affinity must derive no session key: %q", screener.sessionKey(request))
	}
}

// errMustNotCoordinate fails the test when the syncer is reached in local mode.
var errMustNotCoordinate = errors.New("syncer must not be called with disableCoordination")

// flipRevisionNotReady flips every tracked pod of a revision to NotReady.
func flipRevisionNotReady(t *testing.T, screener *Screener, revision string) {
	t.Helper()
	handler := &podNotificationHandler{screener: screener}
	screener.mu.RLock()
	type tracked struct {
		name string
		role string
	}
	var pods []tracked
	for key, pod := range screener.pods {
		if pod.revision == revision {
			pods = append(pods, tracked{name: key.Name, role: pod.role})
		}
	}
	screener.mu.RUnlock()
	for _, pod := range pods {
		if err := handler.Extract(context.Background(), podEvent(t, notReadyPod(pod.name, revision, pod.role), fwkdl.EventAddOrUpdate)); err != nil {
			t.Fatalf("flip %s to NotReady: %v", pod.name, err)
		}
	}
}

func TestScreenerSessionAffinityCoordinationOutranksStickiness(t *testing.T) {
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	syncer := &decisionSyncer{}
	syncer.seed(screener.sessionStateKey, "session", "v1")
	syncer.seed(screener.revisionDecisionStateKey, "decision-id", "v2")
	screener.handle.SetCrossReplicaSyncer(syncer)

	// A parallel encoder's shards share the decision ID; its coordinated
	// revision v2 must win over the session's standing v1.
	request := sessionRequest("session", 0)
	request.Headers[reqcommon.RevisionDecisionIDHeaderKey] = "decision-id"
	got := servedRevision(screenCandidates(t, screener, request, candidatePool(1, 1)))
	if got != "v2" {
		t.Fatalf("coordinated decision must outrank session stickiness: got %s", got)
	}

	// The session decision follows the coordinated outcome, so the session's
	// next turn stays on v2 rather than flapping back to v1.
	if stored, ok := syncer.stored(screener.sessionStateKey, "session"); !ok || stored != "v2" {
		t.Fatalf("session decision must be replaced with the coordinated revision: %v", stored)
	}
	next := servedRevision(screenCandidates(t, screener, sessionRequest("session", 1), candidatePool(1, 1)))
	if next != "v2" {
		t.Fatalf("session must follow the coordinated outcome on its next turn: got %s", next)
	}
}

func TestScreenerSessionAffinityKeepsLocalSessionThroughSyncedTransition(t *testing.T) {
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{"v1": {"prefill": 1, "decode": 1}})
	v1Only := candidatePool(1, 1)

	// Single revision: the session decision is local.
	pinned := servedRevision(screenCandidates(t, screener, sessionRequest("session", 0), v1Only))
	if pinned != "v1" {
		t.Fatalf("single-revision fleet must pin v1: got %s", pinned)
	}

	// A second revision appears and a syncer is installed: coordination turns
	// on, and the local decision seeds the synced one.
	seedCounts(t, screener, map[string]map[string]int{"v2": {"prefill": 1, "decode": 1}})
	candidates := append(candidatePool(1, 1), endpoint("v2-0", revLabels("v2")))
	syncer := &decisionSyncer{}
	screener.handle.SetCrossReplicaSyncer(syncer)

	got := servedRevision(screenCandidates(t, screener, sessionRequest("session", 1), candidates))
	if got != "v1" {
		t.Fatalf("session must keep its revision when coordination turns on: got %s", got)
	}
	if stored, ok := syncer.stored(screener.sessionStateKey, "session"); !ok || stored != "v1" {
		t.Fatalf("local decision must seed the synced decision: %v", stored)
	}
}

func TestScreenerSessionAffinitySyncedInvalidationReplacesWithoutFlapBack(t *testing.T) {
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	syncer := &decisionSyncer{}
	syncer.seed(screener.sessionStateKey, "session", "v1")
	screener.handle.SetCrossReplicaSyncer(syncer)
	candidates := candidatePool(1, 1)

	// v1 loses coverage: the session must move and the synced decision must
	// be replaced atomically, not deleted for a concurrent GetOrSet to miss.
	flipRevisionNotReady(t, screener, "v1")
	moved := servedRevision(screenCandidates(t, screener, sessionRequest("session", 1), candidates))
	if moved == "" || moved == "v1" {
		t.Fatalf("session must leave a zero-share revision: got %s", moved)
	}
	if len(syncer.replaced) != 1 || syncer.replaced[0] != "session" {
		t.Fatalf("invalidation must Replace the session decision once: %v", syncer.replaced)
	}
	if stored, ok := syncer.stored(screener.sessionStateKey, "session"); !ok || stored != moved {
		t.Fatalf("synced decision must hold the replacement: %v", stored)
	}

	again := servedRevision(screenCandidates(t, screener, sessionRequest("session", 2), candidates))
	if again != moved {
		t.Fatalf("replacement decision must stick: moved=%s again=%s", moved, again)
	}
}

func TestScreenerSessionAffinityParallelEncodersConverge(t *testing.T) {
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	syncer := &decisionSyncer{}
	screener.handle.SetCrossReplicaSyncer(syncer)
	candidates := candidatePool(1, 1)

	// Two sessions, one parallel-encode request: every shard shares the
	// decision ID, so both sessions must land on the same revision.
	shard := func(sessionID string) *fwksched.InferenceRequest {
		request := sessionRequest(sessionID, 0)
		request.Headers[reqcommon.RevisionDecisionIDHeaderKey] = "decision-id"
		return request
	}
	first := servedRevision(screenCandidates(t, screener, shard("session-a"), candidates))
	second := servedRevision(screenCandidates(t, screener, shard("session-b"), candidates))
	if first == "" || second != first {
		t.Fatalf("parallel encoders must converge on one revision: first=%s second=%s", first, second)
	}
	for _, sessionID := range []string{"session-a", "session-b"} {
		if stored, ok := syncer.stored(screener.sessionStateKey, sessionID); !ok || stored != first {
			t.Fatalf("session %s must follow the coordinated revision %s: %v", sessionID, first, stored)
		}
	}
}

func TestScreenerSessionAffinityLocalDecisionExpiresAfterTTL(t *testing.T) {
	config := sessionAffinityConfig()
	config.RevisionGating.SessionAffinity.TTL = "50ms"
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})

	if pinned := servedRevision(screenCandidates(t, screener, sessionRequest("session", 0), candidatePool(1, 1))); pinned == "" {
		t.Fatal("first turn screened to no endpoint")
	}
	if _, ok := screener.lookupStickyRevision("session"); !ok {
		t.Fatal("session decision must be stored on first turn")
	}
	time.Sleep(100 * time.Millisecond)
	if _, ok := screener.lookupStickyRevision("session"); ok {
		t.Fatal("session decision must expire after the configured TTL")
	}
}

func TestScreenerSessionAffinityResumesOnOtherScreenerThroughSharedSyncer(t *testing.T) {
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screenerA := newTestScreener(config)
	screenerB := newTestScreener(config)
	for _, screener := range []*Screener{screenerA, screenerB} {
		seedCounts(t, screener, map[string]map[string]int{"v1": {"prefill": 1, "decode": 1}})
	}
	syncer := &decisionSyncer{}
	screenerA.handle.SetCrossReplicaSyncer(syncer)
	screenerB.handle.SetCrossReplicaSyncer(syncer)

	// Single-revision period: the session reaches EPP A. Its decision must
	// already live in the shared store, not only in A's local cache, or the
	// rollout transition below loses it.
	if got := servedRevision(screenCandidates(t, screenerA, sessionRequest("session", 0), candidatePool(1, 0))); got != "v1" {
		t.Fatalf("single-revision fleet must pin v1: got %s", got)
	}
	if stored, ok := syncer.stored(screenerA.sessionStateKey, "session"); !ok || stored != "v1" {
		t.Fatalf("session decision must be shared during the single-revision period: %v", stored)
	}

	// v2 appears; the session's next request reaches EPP B, which holds no
	// local entry, and must resume on v1 from the shared store.
	seedCounts(t, screenerB, map[string]map[string]int{"v2": {"prefill": 1, "decode": 1}})
	if got := servedRevision(screenCandidates(t, screenerB, sessionRequest("session", 1), candidatePool(1, 1))); got != "v1" {
		t.Fatalf("session must resume on its revision at another EPP: got %s", got)
	}
}

func TestScreenerSessionAffinityInvalidationKeepsFirstRepair(t *testing.T) {
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	syncer := &decisionSyncer{}
	syncer.seed(screener.sessionStateKey, "session", "r0")
	screener.handle.SetCrossReplicaSyncer(syncer)

	// The first invalidation repairs the stale r0 decision.
	winner, err := screener.replaceSessionRevision(context.Background(), "session", "r0", "v1")
	if err != nil || winner != "v1" {
		t.Fatalf("first repair must install v1: winner=%s err=%v", winner, err)
	}

	// A delayed writer still holding r0 must adopt the repair, not overwrite
	// it with its own pick.
	adopted, err := screener.replaceSessionRevision(context.Background(), "session", "r0", "v2")
	if err != nil || adopted != "v1" {
		t.Fatalf("delayed writer must adopt the winning repair: adopted=%s err=%v", adopted, err)
	}
	if stored, ok := syncer.stored(screener.sessionStateKey, "session"); !ok || stored != "v1" {
		t.Fatalf("shared decision must keep the first repair: %v", stored)
	}
	if len(syncer.replaced) != 1 {
		t.Fatalf("only the first repair may write: %v", syncer.replaced)
	}
}

func TestScreenerSessionAffinityLocalInvalidationKeepsFirstRepair(t *testing.T) {
	screener := newTestScreener(sessionAffinityConfig())

	// A first request stores the session decision.
	winner, err := screener.replaceSessionRevision(context.Background(), "session", "", "v1")
	if err != nil || winner != "v1" {
		t.Fatalf("first store must install v1: winner=%s err=%v", winner, err)
	}

	// A delayed writer on the same EPP that observed an older decision must
	// adopt the stored one instead of overwriting it.
	adopted, err := screener.replaceSessionRevision(context.Background(), "session", "r0", "v2")
	if err != nil || adopted != "v1" {
		t.Fatalf("delayed local writer must adopt the stored decision: adopted=%s err=%v", adopted, err)
	}
	if sticky, ok := screener.lookupStickyRevision("session"); !ok || sticky != "v1" {
		t.Fatalf("local decision must keep the first repair: %q", sticky)
	}
}
