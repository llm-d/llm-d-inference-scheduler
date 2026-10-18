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
		proposed := ""
		// sessionSettled reports that the session's stored decision already is
		// the proposal, so nothing needs storing for it.
		sessionSettled := false
		if sessionKey != "" {
			proposed, sessionSettled = c.sessionProposal(ctx, sessionKey, shares, allowedRevisions)
		}
		if proposed == "" {
			proposed = pickWeightedRevision(shares, rand.Float64()) //#nosec G404 -- traffic split across revisions, not security-sensitive
		}
		// Session affinity proposes; request-level coordination decides. A
		// parallel encoder's decision ID outranks stickiness so every shard of
		// one request serves the same revision.
		coordinated := proposed
		if decisionID := revisionDecisionID(request); distribution.needsCoordination && decisionID != "" && coordinated != "" {
			var err error
			coordinated, err = c.getOrSetRevision(ctx, decisionID, coordinated)
			if err != nil {
				log.FromContext(ctx).Error(err, "failed to coordinate rollout revision")
				return nil
			}
		}
		// Store the session decision when it changed: a fresh assignment, an
		// invalidated session's replacement, or a coordinated outcome that
		// overrode the proposal. When the proposal already is the stored
		// decision, storing again would only race a concurrent invalidation.
		if sessionKey != "" && coordinated != "" && (!sessionSettled || coordinated != proposed) {
			c.replaceSessionRevision(ctx, sessionKey, coordinated)
		}
		chosenRevision = coordinated
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

// sessionDecisionOptions carries the session affinity expiration policy: the
// configured session TTL, refreshed on every access so a session in continuous
// use never expires.
func (c *Screener) sessionDecisionOptions() fwkdl.DecisionOptions {
	return fwkdl.DecisionOptions{TTL: c.sessionTTL, RefreshOnAccess: true}
}

// sessionProposal returns the session's standing revision and whether that
// proposal already is the session's stored decision. An empty proposal means
// the session has no usable decision: new, expired, or invalidated by its
// revision losing gating coverage. The caller then makes a fresh weighted
// pick, runs request-level coordination over it, and stores the outcome.
func (c *Screener) sessionProposal(
	ctx context.Context,
	sessionKey string,
	shares map[string]float64,
	allowedRevisions map[string]struct{},
) (string, bool) {
	if c.sessionSynced() {
		syncer := c.crossReplicaSyncer()
		// Seed the candidate from the local decision when one exists: a
		// single-replica deployment that grows a second revision keeps its
		// sessions on the revision they already use.
		candidate := c.localSessionDecision(sessionKey)
		if candidate == "" {
			candidate = pickWeightedRevision(shares, rand.Float64()) //#nosec G404 -- traffic split across revisions, not security-sensitive
		}
		actual, existed, err := syncer.GetOrSet(ctx, c.sessionStateKey, sessionKey, candidate, c.sessionDecisionOptions())
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
		// required role. The caller's fresh pick replaces the stored decision.
		recordSessionAffinityDecision(c.typedName.Name, sessionAffinityResultInvalidated)
		return "", false
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
	// stored over it by replaceSessionRevision.
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

// localSessionDecision returns this replica's local session decision, when
// session affinity tracks one. Local decisions outlive the local cache only
// for the configured session TTL.
func (c *Screener) localSessionDecision(sessionKey string) string {
	sticky, ok := c.lookupStickyRevision(sessionKey)
	if !ok {
		return ""
	}
	return sticky
}

// replaceSessionRevision overwrites the session's stored decision: a fresh
// assignment, an invalidated session's replacement, or a coordinated outcome
// that outranked the session's proposal.
func (c *Screener) replaceSessionRevision(ctx context.Context, sessionKey, revision string) {
	if c.sessionSynced() {
		if _, _, err := c.crossReplicaSyncer().Replace(ctx, c.sessionStateKey, sessionKey, revision, c.sessionDecisionOptions()); err != nil {
			log.FromContext(ctx).Error(err, "failed to store sticky rollout revision", "sessionKey", sessionKey)
		}
		return
	}
	if c.sessionDecisions != nil {
		c.sessionDecisions.Set(sessionKey, revision, ttlcache.DefaultTTL)
		recordSessionAffinityDecision(c.typedName.Name, sessionAffinityResultAssigned)
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
