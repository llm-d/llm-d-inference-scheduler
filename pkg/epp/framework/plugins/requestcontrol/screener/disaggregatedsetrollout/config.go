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

// Package disaggregatedsetrollout provides a request-control Screener for safe
// rollouts across roles of a disaggregated inference deployment. It filters a
// requested revision strictly, or selects one complete revision using Ready
// Pod counts when the request does not specify one.
package disaggregatedsetrollout

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/llm-d/llm-d-router/pkg/common/request"
)

// DisaggregatedSet rollout protocol and operator label defaults.
const (
	DefaultRevisionHeader = request.DisaggregatedRevisionHeaderKey
	DefaultRevisionLabel  = "disaggregatedset.x-k8s.io/revision"
	DefaultRoleLabel      = "disaggregatedset.x-k8s.io/role"
)

// SessionAffinity defaults, applied by Validate when the block is enabled.
const (
	DefaultSessionAffinityTTL         = "10m"
	DefaultSessionAffinityMaxSessions = 10000
	DefaultSessionAffinityPrefixBytes = 4096
)

// Config is the parameters block of a disaggregatedset-rollout-screener plugin.
type Config struct {
	Scope          Scope           `json:"scope"`
	RevisionGating *RevisionGating `json:"revisionGating"`
}

// Scope constrains which Pod notifications contribute to revision gating.
type Scope struct {
	LabelSelector string `json:"labelSelector"`
}

// RevisionGating governs revision selection. Two things happen per request
// when Active:
//
//  1. Coverage check: drop candidates whose revision has zero Ready pods
//     for any role listed in RequiredRoles.
//  2. Load shaping: when no revision header is present, weighted-random-pick
//     one surviving revision and keep only its pods.
//
// A request carrying RevisionHeaderName always selects that revision strictly.
// The selected endpoint's revision is stamped into the same response header.
type RevisionGating struct {
	RevisionHeaderName  string           `json:"revisionHeaderName,omitempty"`
	RevisionLabelKey    string           `json:"revisionLabelKey,omitempty"`
	RoleLabelKey        string           `json:"roleLabelKey,omitempty"`
	DisableCoordination bool             `json:"disableCoordination,omitempty"`
	Mode                GatingMode       `json:"mode"`
	RequiredRoles       []string         `json:"requiredRoles,omitempty"`
	SessionAffinity     *SessionAffinity `json:"sessionAffinity,omitempty"`
}

// SessionAffinity keeps an unpinned request on the revision a previous request
// with the same session key used. The key is the agent-identity session
// attribute when one is set, otherwise the hash of the leading request-body
// bytes. A sticky revision stops being used once its gating share drops to
// zero; a fresh weighted pick replaces it.
type SessionAffinity struct {
	Enabled     bool   `json:"enabled,omitempty"`
	TTL         string `json:"ttl,omitempty"`
	MaxSessions int    `json:"maxSessions,omitempty"`
	// PrefixBytes is the number of leading request-body bytes hashed for
	// requests without a session attribute. Nil selects the default; zero
	// disables the prefix-hash fallback, restricting stickiness to requests
	// that carry a session attribute.
	PrefixBytes *int `json:"prefixBytes,omitempty"`
}

func (a *SessionAffinity) active() bool {
	return a != nil && a.Enabled
}

// UnmarshalJSON rejects unknown fields inside the sessionAffinity block,
// mirroring RevisionGating's decoder so misspelled parameters fail at load.
func (a *SessionAffinity) UnmarshalJSON(data []byte) error {
	type raw SessionAffinity
	var parsed raw
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	*a = SessionAffinity(parsed)
	return nil
}

// UnmarshalJSON normalizes RevisionHeaderName to lowercase because request
// headers are lowercase in the scheduling request.
func (g *RevisionGating) UnmarshalJSON(data []byte) error {
	type raw RevisionGating
	var parsed raw
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	parsed.RevisionHeaderName = strings.ToLower(parsed.RevisionHeaderName)
	*g = RevisionGating(parsed)
	return nil
}

// GatingMode selects the gating algorithm. Open enum so future modes can
// land with their own sub-block without breaking the config surface.
type GatingMode string

const (
	// GatingModeSum does two things:
	//   1. Drop any revision missing Ready pods on any listed role
	//      (rollout drift safety).
	//   2. Weighted-random-pick ONE surviving revision, weighted by the
	//      sum of Ready pod counts across every listed role, and keep
	//      only that revision's candidates.
	// Traffic converges on (sum(crossRolePods(rev)) / sum over all
	// revisions), independent of the picker downstream. The "sum" name
	// refers to the per-revision sum used as weight.
	// For A={prefill:2,decode:9} and B={prefill:1,decode:1}, the weights
	// are 11 and 2, producing shares of 11/13 and 2/13.
	GatingModeSum GatingMode = "sum"
	// GatingModeMaxRole applies the same coverage check as GatingModeSum, sums
	// each required role across every covered revision, and selects the role
	// with the largest total. Every revision is then weighted by its Ready pod
	// count for that same role. RequiredRoles order resolves equal totals.
	GatingModeMaxRole GatingMode = "max-role"
	// GatingModeDisabled turns off revision coverage and weighted selection.
	// Revision header filtering and response stamping remain enabled.
	GatingModeDisabled GatingMode = "disabled"
)

// Active reports whether revision gating is enabled.
func (g *RevisionGating) Active() bool {
	if g == nil {
		return false
	}
	switch g.Mode {
	case GatingModeSum, GatingModeMaxRole:
		return len(g.RequiredRoles) > 0
	case GatingModeDisabled:
		return false
	}
	return false
}

func (g *RevisionGating) coordinationEnabled() bool {
	return g != nil && !g.DisableCoordination
}

// Validate performs static config checks and fills in label-key defaults.
func (c *Config) Validate() error {
	if c.Scope.LabelSelector == "" {
		return errors.New("scope.labelSelector is required")
	}
	if _, err := labels.Parse(c.Scope.LabelSelector); err != nil {
		return fmt.Errorf("scope.labelSelector is not a valid label selector: %w", err)
	}

	if c.RevisionGating == nil {
		return errors.New("revisionGating is required")
	}
	if c.RevisionGating.RevisionHeaderName == "" {
		c.RevisionGating.RevisionHeaderName = DefaultRevisionHeader
	}
	if c.RevisionGating.RevisionLabelKey == "" {
		c.RevisionGating.RevisionLabelKey = DefaultRevisionLabel
	}
	if c.RevisionGating.RoleLabelKey == "" {
		c.RevisionGating.RoleLabelKey = DefaultRoleLabel
	}
	switch c.RevisionGating.Mode {
	case GatingModeDisabled:
		// Nothing else to validate: disabled skips wiring.
	case GatingModeSum, GatingModeMaxRole:
		if len(c.RevisionGating.RequiredRoles) == 0 {
			return fmt.Errorf("revisionGating.requiredRoles must contain at least one role when revisionGating.mode=%s", c.RevisionGating.Mode)
		}
		seenRoles := make(map[string]struct{}, len(c.RevisionGating.RequiredRoles))
		for i, role := range c.RevisionGating.RequiredRoles {
			if role == "" {
				return fmt.Errorf("revisionGating.requiredRoles[%d] must not be empty", i)
			}
			if _, exists := seenRoles[role]; exists {
				return fmt.Errorf("revisionGating.requiredRoles contains duplicate role %q", role)
			}
			seenRoles[role] = struct{}{}
		}
	default:
		return fmt.Errorf("revisionGating.mode %q must be one of sum|max-role|disabled", c.RevisionGating.Mode)
	}

	if affinity := c.RevisionGating.SessionAffinity; affinity.active() {
		if affinity.TTL == "" {
			affinity.TTL = DefaultSessionAffinityTTL
		}
		if affinity.MaxSessions == 0 {
			affinity.MaxSessions = DefaultSessionAffinityMaxSessions
		}
		if affinity.PrefixBytes == nil {
			defaultPrefixBytes := DefaultSessionAffinityPrefixBytes
			affinity.PrefixBytes = &defaultPrefixBytes
		} else if *affinity.PrefixBytes < 0 {
			return errors.New("revisionGating.sessionAffinity.prefixBytes must not be negative")
		}
		ttl, err := time.ParseDuration(affinity.TTL)
		if err != nil {
			return fmt.Errorf("revisionGating.sessionAffinity.ttl %q is not a valid duration: %w", affinity.TTL, err)
		}
		if ttl <= 0 {
			return fmt.Errorf("revisionGating.sessionAffinity.ttl %q must be positive", affinity.TTL)
		}
		if affinity.MaxSessions < 0 {
			return fmt.Errorf("revisionGating.sessionAffinity.maxSessions must not be negative, got %d", affinity.MaxSessions)
		}
	}

	return nil
}
