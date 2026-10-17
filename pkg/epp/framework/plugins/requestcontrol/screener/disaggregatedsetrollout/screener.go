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
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/cespare/xxhash/v2"
	"github.com/jellydator/ttlcache/v3"
	"sigs.k8s.io/controller-runtime/pkg/log"

	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
	fwksched "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/scheduling"
	"github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/requestcontrol/requestheader/agentidentity"
)

const localRevisionDecisionStateKey fwkplugin.StateKey = "revision-decision"

var errInvalidRevisionDecision = errors.New("revision coordination returned an invalid rollout revision")

type revisionDecision string

func (r revisionDecision) Clone() fwkplugin.StateData {
	return r
}

// Screen applies revision gating and strict revision selection before
// scheduling profiles observe the endpoint set.
func (c *Screener) Screen(ctx context.Context, request *fwksched.InferenceRequest, endpoints []fwksched.Endpoint) []fwksched.Endpoint {
	requestedRevision := c.requestedRevision(request)
	if !c.config.RevisionGating.Active() {
		if requestedRevision == "" {
			return endpoints
		}
		result := c.filterRevisions(endpoints, nil, requestedRevision)
		if len(result) == 0 {
			recordStrictRevisionNoMatch(c.typedName.Name)
		}
		return result
	}

	if request == nil {
		return nil
	}
	seenRevisions := uniqueRevisions(endpoints, c.revisionLabelKey)
	distribution := c.distributionSnapshot()
	shares := make(map[string]float64, len(seenRevisions))
	allowedRevisions := make(map[string]struct{}, len(seenRevisions))
	for revision := range seenRevisions {
		share := distribution.shares[revision]
		// If one required role is missing, this revision cannot serve a
		// disaggregated request yet. Missing roles and revisions absent
		// from the warmed distribution both resolve to zero and fail closed.
		if share == 0 {
			continue
		}
		shares[revision] = share
		allowedRevisions[revision] = struct{}{}
	}
	chosenRevision := requestedRevision
	// A forwarded revision header is authoritative. In particular, a decode
	// request in P/D must use the revision stamped by prefill; it must not depend
	// on the two EPPs sharing a CrossReplicaSyncer.
	if chosenRevision == "" {
		sessionKey := c.sessionKey(request)
		stickyApplied := false
		if sessionKey != "" {
			chosenRevision, stickyApplied = c.resolveSessionRevision(ctx, sessionKey, shares, allowedRevisions)
		}
		if chosenRevision == "" {
			chosenRevision = pickWeightedRevision(shares, rand.Float64()) //#nosec G404 -- traffic split across revisions, not security-sensitive
		}
		// A sticky hit is already the coordinated outcome for this session;
		// the decision-ID path applies only to a fresh pick.
		if !stickyApplied {
			if decisionID := revisionDecisionID(request); distribution.needsCoordination && decisionID != "" && chosenRevision != "" {
				var err error
				chosenRevision, err = c.getOrSetRevision(ctx, decisionID, chosenRevision)
				if err != nil {
					log.FromContext(ctx).Error(err, "failed to coordinate rollout revision")
					return nil
				}
			}
			if sessionKey != "" && chosenRevision != "" {
				c.storeStickyRevision(ctx, sessionKey, chosenRevision)
			}
		}
	}
	result := c.filterRevisions(endpoints, allowedRevisions, chosenRevision)
	if requestedRevision != "" && len(result) == 0 {
		recordStrictRevisionNoMatch(c.typedName.Name)
	}
	return result
}

func (c *Screener) requestedRevision(request *fwksched.InferenceRequest) string {
	if request == nil {
		return ""
	}
	return request.Headers[c.revisionHeaderName]
}

func revisionDecisionID(request *fwksched.InferenceRequest) string {
	if request == nil {
		return ""
	}
	if decisionID := request.Headers[reqcommon.RevisionDecisionIDHeaderKey]; decisionID != "" {
		return decisionID
	}
	// A single downstream request needs no identifier beyond its request ID.
	return request.Headers[reqcommon.RequestIDHeaderKey]
}

func (c *Screener) getOrSetRevision(ctx context.Context, id, candidate string) (string, error) {
	syncer := c.crossReplicaSyncer()
	if syncer == nil {
		// Parallel encode requests can share a decision ID and reach this EPP
		// without an earlier x-llm-d-disagg-revision response to forward. Local state
		// makes them reuse the first revision selected for that decision ID.
		actual, _ := c.localRevisionDecisions.ReadOrWrite(id, localRevisionDecisionStateKey, revisionDecision(candidate))
		revision, ok := actual.(revisionDecision)
		if !ok || revision == "" {
			return "", errInvalidRevisionDecision
		}
		return string(revision), nil
	}

	actual, _, err := syncer.GetOrSet(ctx, c.revisionDecisionStateKey, id, candidate)
	if err != nil {
		return "", err
	}
	revision, ok := actual.(string)
	if !ok || revision == "" {
		return "", errInvalidRevisionDecision
	}
	return revision, nil
}

func (c *Screener) crossReplicaSyncer() fwkdl.CrossReplicaSyncer {
	if c.handle == nil {
		return nil
	}
	syncer, _ := c.handle.CrossReplicaSyncer().(fwkdl.CrossReplicaSyncer)
	return syncer
}

// sessionKey identifies the conversation a request belongs to: the
// agent-identity session attribute when one is set, otherwise the hash of the
// leading request-body bytes. Empty means no affinity applies and the request
// takes the per-request weighted pick.
func (c *Screener) sessionKey(request *fwksched.InferenceRequest) string {
	if request == nil || !c.sessionAffinity.active() {
		return ""
	}
	if id, ok := fwksched.ReadRequestAttribute[string](request, agentidentity.AgentIdentityKey); ok && id != "" {
		return id
	}
	prefixBytes := 0
	if c.sessionAffinity.PrefixBytes != nil {
		prefixBytes = *c.sessionAffinity.PrefixBytes
	}
	if prefixBytes <= 0 || request.Body == nil || len(request.Body.RawBody) == 0 {
		return ""
	}
	if prefixBytes > len(request.Body.RawBody) {
		prefixBytes = len(request.Body.RawBody)
	}
	return fmt.Sprintf("prefix:%016x", xxhash.Sum64(request.Body.RawBody[:prefixBytes]))
}

// sessionSynced reports whether session decisions are shared across EPP
// replicas for this request.
func (c *Screener) sessionSynced() bool {
	return c.distributionSnapshot().needsCoordination && c.crossReplicaSyncer() != nil
}

// resolveSessionRevision settles the revision for a session. The second
// return value reports that the session decision is final: a sticky hit, or a
// synced-mode get-or-set that converged with other replicas. A false return
// with an empty revision leaves the fresh weighted pick to the caller; a false
// return with a non-empty revision is an invalidated session whose replacement
// is already stored.
func (c *Screener) resolveSessionRevision(
	ctx context.Context,
	sessionKey string,
	shares map[string]float64,
	allowedRevisions map[string]struct{},
) (string, bool) {
	if c.sessionSynced() {
		// GetOrSet is the only request-level read the syncer offers, so the
		// candidate doubles as the first-turn decision: a miss stores it and
		// concurrent first turns of the session converge on one revision.
		candidate := pickWeightedRevision(shares, rand.Float64()) //#nosec G404 -- traffic split across revisions, not security-sensitive
		actual, existed, err := c.crossReplicaSyncer().GetOrSet(ctx, c.revisionDecisionStateKey, sessionKey, candidate)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to coordinate sticky rollout revision", "sessionKey", sessionKey)
			return "", false
		}
		revision, ok := actual.(string)
		if !ok || revision == "" {
			return "", false
		}
		if _, covered := allowedRevisions[revision]; covered {
			if existed {
				recordSessionAffinityDecision(c.typedName.Name, sessionAffinityResultSticky)
			} else {
				recordSessionAffinityDecision(c.typedName.Name, sessionAffinityResultAssigned)
			}
			return revision, true
		}
		// The stored revision lost gating coverage: rolled away, or missing a
		// required role. Replace the stored decision with a fresh pick.
		recordSessionAffinityDecision(c.typedName.Name, sessionAffinityResultInvalidated)
		if candidate != "" {
			c.replaceStickyRevision(ctx, sessionKey, candidate)
		}
		return candidate, candidate != ""
	}

	sticky, found := c.lookupStickyRevision(sessionKey)
	if !found {
		return "", false
	}
	if _, covered := allowedRevisions[sticky]; covered {
		recordSessionAffinityDecision(c.typedName.Name, sessionAffinityResultSticky)
		return sticky, true
	}
	// The sticky revision lost gating coverage. The caller's fresh pick is
	// stored over it by storeStickyRevision.
	recordSessionAffinityDecision(c.typedName.Name, sessionAffinityResultInvalidated)
	return "", false
}

func (c *Screener) lookupStickyRevision(sessionKey string) (string, bool) {
	if c.sessionDecisions == nil {
		return "", false
	}
	item := c.sessionDecisions.Get(sessionKey)
	if item == nil {
		return "", false
	}
	return item.Value(), item.Value() != ""
}

// storeStickyRevision records the revision chosen for a session. Synced mode
// is reached only after a syncer failure in resolveSessionRevision; the normal
// synced store is the GetOrSet inside resolveSessionRevision.
func (c *Screener) storeStickyRevision(ctx context.Context, sessionKey, revision string) {
	if c.sessionSynced() {
		if _, _, err := c.crossReplicaSyncer().GetOrSet(ctx, c.revisionDecisionStateKey, sessionKey, revision); err != nil {
			log.FromContext(ctx).Error(err, "failed to store sticky rollout revision", "sessionKey", sessionKey)
		}
		return
	}
	if c.sessionDecisions != nil {
		c.sessionDecisions.Set(sessionKey, revision, ttlcache.DefaultTTL)
		recordSessionAffinityDecision(c.typedName.Name, sessionAffinityResultAssigned)
	}
}

// replaceStickyRevision overwrites a stored session decision whose revision
// lost gating coverage. The syncer's GetOrSet cannot replace, so the stale
// entry is deleted first.
func (c *Screener) replaceStickyRevision(ctx context.Context, sessionKey, revision string) {
	if c.sessionSynced() {
		syncer := c.crossReplicaSyncer()
		if err := syncer.Delete(ctx, c.revisionDecisionStateKey, sessionKey); err != nil {
			log.FromContext(ctx).Error(err, "failed to delete stale sticky rollout revision", "sessionKey", sessionKey)
		}
		if _, _, err := syncer.GetOrSet(ctx, c.revisionDecisionStateKey, sessionKey, revision); err != nil {
			log.FromContext(ctx).Error(err, "failed to store sticky rollout revision", "sessionKey", sessionKey)
		}
		return
	}
	if c.sessionDecisions != nil {
		c.sessionDecisions.Set(sessionKey, revision, ttlcache.DefaultTTL)
	}
}

func (c *Screener) filterRevisions(
	endpoints []fwksched.Endpoint,
	allowedRevisions map[string]struct{},
	revisionDecision string,
) []fwksched.Endpoint {
	result := make([]fwksched.Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint == nil || endpoint.GetMetadata() == nil {
			continue
		}
		revision := endpoint.GetMetadata().Labels[c.revisionLabelKey]
		if allowedRevisions != nil {
			if _, covered := allowedRevisions[revision]; !covered {
				continue
			}
		}
		if revisionDecision != "" && revision != revisionDecision {
			continue
		}
		result = append(result, endpoint)
	}
	return result
}

func uniqueRevisions(endpoints []fwksched.Endpoint, revisionLabelKey string) map[string]struct{} {
	seen := make(map[string]struct{})
	for _, endpoint := range endpoints {
		if endpoint == nil || endpoint.GetMetadata() == nil {
			continue
		}
		if revision := endpoint.GetMetadata().Labels[revisionLabelKey]; revision != "" {
			seen[revision] = struct{}{}
		}
	}
	return seen
}

func revisionSumWeight(perRole map[string]int, required []string) (int, bool) {
	weight := 0
	for _, role := range required {
		count := perRole[role]
		if count == 0 {
			return 0, false
		}
		weight += count
	}
	return weight, true
}

func pickWeightedRevision(shares map[string]float64, draw float64) string {
	revisions := make([]string, 0, len(shares))
	total := 0.0
	for revision, share := range shares {
		revisions = append(revisions, revision)
		total += share
	}
	if total == 0 {
		return ""
	}
	sort.Strings(revisions)
	if len(revisions) == 1 {
		return revisions[0]
	}
	x := draw * total
	cumulative := 0.0
	for _, revision := range revisions {
		cumulative += shares[revision]
		if x < cumulative {
			return revision
		}
	}
	return revisions[len(revisions)-1]
}
