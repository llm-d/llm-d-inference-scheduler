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
