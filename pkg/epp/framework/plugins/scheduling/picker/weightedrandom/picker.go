/*
Copyright 2025 The Kubernetes Authors.

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

// Package weightedrandom implements a scheduling picker that selects endpoints randomly, where the
// probability of an endpoint being selected is proportional to its score.
//
// For detailed behavioral intent and configuration, see the package README.
package weightedrandom

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/log"

	logutil "github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	framework "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/picker"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/picker/random"
)

const (
	// WeightedRandomPickerType is the registered name of the weighted random picker plugin.
	WeightedRandomPickerType = "weighted-random-picker"

	// DefaultMinTopK is the lower bound applied after topK / topKPercent. Without a floor,
	// small fleets or aggressive percentages can collapse to a single candidate (degenerating
	// into argmax). A default of 2 keeps the picker actually sampling. Set MinTopK = 1 to
	// allow degeneration to argmax.
	DefaultMinTopK = 2
)

// Parameters extends picker.PickerParameters with optional top-K filtering applied
// before A-Res sampling. When neither TopK nor TopKPercent is set, the picker
// considers every candidate (default behavior).
type Parameters struct {
	picker.PickerParameters

	// TopK is the absolute cap on the candidate pool size after sorting by score.
	// 0 (the zero value) means disabled. Combinable with TopKPercent — when both
	// are set, the more restrictive bound wins.
	TopK int `json:"topK,omitempty"`

	// TopKPercent is the percentage cap, computed as ceil(N * P / 100). Valid
	// values are 1-100; 0 (the zero value) means disabled. Combinable with TopK.
	TopKPercent int `json:"topKPercent,omitempty"`

	// MinTopK is the floor on the post-filter pool size. Default DefaultMinTopK.
	// Set to 1 to allow degeneration to argmax. Ignored when neither TopK nor
	// TopKPercent is set.
	MinTopK int `json:"minTopK,omitempty"`
}

func (p Parameters) validate() error {
	if p.TopK < 0 {
		return fmt.Errorf("'topK' must be non-negative, got %d", p.TopK)
	}
	if p.TopKPercent < 0 || p.TopKPercent > 100 {
		return fmt.Errorf("'topKPercent' must be in [0, 100], got %d", p.TopKPercent)
	}
	if p.MinTopK < 0 {
		return fmt.Errorf("'minTopK' must be non-negative, got %d", p.MinTopK)
	}
	return nil
}

// effectiveK returns the candidate-pool size after applying TopK / TopKPercent against
// n candidates. Returns n when no top-K filter is configured (full pool, default behavior).
func (p Parameters) effectiveK(n int) int {
	if n <= 0 || (p.TopK == 0 && p.TopKPercent == 0) {
		return n
	}

	k := n
	if p.TopK > 0 && p.TopK < k {
		k = p.TopK
	}
	if p.TopKPercent > 0 {
		// ceil(n * pct / 100) — boundary rounds up so a 1% cut keeps at least one.
		fromPct := (n*p.TopKPercent + 99) / 100
		if fromPct < k {
			k = fromPct
		}
	}

	floor := p.MinTopK
	if floor <= 0 {
		floor = DefaultMinTopK
	}
	if k < floor {
		k = floor
	}
	if k > n {
		k = n
	}
	return k
}

// weightedScoredEndpoint represents a scored endpoint with its A-Res sampling key
type weightedScoredEndpoint struct {
	*framework.ScoredEndpoint
	key float64
}

// compile-time type validation
var _ framework.Picker = &WeightedRandomPicker{}

// WeightedRandomPickerFactory defines the factory function for WeightedRandomPicker.
func WeightedRandomPickerFactory(name string, rawParameters json.RawMessage, _ fwkplugin.Handle) (fwkplugin.Plugin, error) {
	parameters := Parameters{
		PickerParameters: picker.PickerParameters{MaxNumOfEndpoints: picker.DefaultMaxNumOfEndpoints},
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' picker - %w", WeightedRandomPickerType, err)
		}
	}
	if err := parameters.validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration for '%s' picker: %w", WeightedRandomPickerType, err)
	}

	return New(parameters).WithName(name), nil
}

// NewWeightedRandomPicker initializes a WeightedRandomPicker considering every candidate
// (no top-K filtering). Provided for source-compatibility with callers that don't need the
// new top-K knobs; new code should prefer New(Parameters).
func NewWeightedRandomPicker(maxNumOfEndpoints int) *WeightedRandomPicker {
	return New(Parameters{PickerParameters: picker.PickerParameters{MaxNumOfEndpoints: maxNumOfEndpoints}})
}

// New constructs a WeightedRandomPicker from validated Parameters. MaxNumOfEndpoints <= 0
// falls back to picker.DefaultMaxNumOfEndpoints, matching the prior constructor's behavior.
func New(params Parameters) *WeightedRandomPicker {
	if params.MaxNumOfEndpoints <= 0 {
		params.MaxNumOfEndpoints = picker.DefaultMaxNumOfEndpoints
	}
	return &WeightedRandomPicker{
		typedName:    fwkplugin.TypedName{Type: WeightedRandomPickerType, Name: WeightedRandomPickerType},
		params:       params,
		randomPicker: random.NewRandomPicker(params.MaxNumOfEndpoints),
	}
}

// WeightedRandomPicker picks endpoint(s) from the list of candidates based on weighted random sampling using A-Res algorithm.
// Reference: https://utopia.duth.gr/~pefraimi/research/data/2007EncOfAlg.pdf.
//
// The picker at its core is picking endpoints randomly, where the probability of the endpoint to get picked is derived
// from its weighted score.
//
// Algorithm:
//   - Default path: Uses A-Res (Algorithm for Reservoir Sampling): keyᵢ = Uᵢ^(1/wᵢ).
//     Selects k items with largest keys for mathematically correct weighted sampling.
//     Single pass over candidates with O(n + k log k) complexity.
//   - Top-K path (when TopK or TopKPercent is set): randomly shuffles candidates,
//     sorts by score, truncates to effectiveK, then runs A-Res over the truncated
//     pool. The pre-shuffle randomizes tie-breaking at the K-th cutoff so equal-
//     score endpoints are not deterministically excluded by input order.
//     Complexity: O(n log n) for the score sort + O(k + maxNumOfEndpoints log
//     maxNumOfEndpoints) for A-Res.
type WeightedRandomPicker struct {
	typedName    fwkplugin.TypedName
	params       Parameters
	randomPicker *random.RandomPicker // fallback for zero weights
}

// WithName sets the name of the picker.
func (p *WeightedRandomPicker) WithName(name string) *WeightedRandomPicker {
	p.typedName.Name = name
	return p
}

// TypedName returns the type and name tuple of this plugin instance.
func (p *WeightedRandomPicker) TypedName() fwkplugin.TypedName {
	return p.typedName
}

// Pick selects the endpoint(s) randomly from the list of candidates, where the probability of the endpoint to get picked is derived
// from its weighted score. When TopK or TopKPercent is configured, only the top-K candidates by score participate in A-Res sampling.
func (p *WeightedRandomPicker) Pick(ctx context.Context, cycleState *framework.CycleState, scoredEndpoints []*framework.ScoredEndpoint) *framework.ProfileRunResult {
	// Check if there is at least one endpoint with Score > 0, if not let random picker run
	if slices.IndexFunc(scoredEndpoints, func(scoredEndpoint *framework.ScoredEndpoint) bool { return scoredEndpoint.Score > 0 }) == -1 {
		log.FromContext(ctx).V(logutil.DEBUG).Info("All scores are zero, delegating to RandomPicker for uniform selection")
		return p.randomPicker.Pick(ctx, cycleState, scoredEndpoints)
	}

	// Top-K restriction: when configured, sort by score and truncate so A-Res samples
	// only from the top tier. Pre-shuffle so the stable sort's tie-break is randomized —
	// otherwise endpoints with identical scores at the K-th cutoff would be excluded
	// deterministically by their input position, biasing routing on bucketed scorers.
	// Default (no top-K knobs) preserves the original full-pool behavior with no copy
	// or sort.
	candidates := scoredEndpoints
	if p.params.TopK > 0 || p.params.TopKPercent > 0 {
		candidates = make([]*framework.ScoredEndpoint, len(scoredEndpoints))
		copy(candidates, scoredEndpoints)
		picker.PickerRand.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].Score > candidates[j].Score
		})
		k := p.params.effectiveK(len(candidates))
		candidates = candidates[:k]
	}

	log.FromContext(ctx).V(logutil.DEBUG).Info("Selecting endpoints from candidates by random weighted picker",
		"max-num-of-endpoints", p.params.MaxNumOfEndpoints,
		"num-of-candidates", len(scoredEndpoints),
		"effective-pool", len(candidates),
		"scored-endpoints", scoredEndpoints,
	)

	// A-Res algorithm: keyᵢ = Uᵢ^(1/wᵢ)
	weightedEndpoints := make([]weightedScoredEndpoint, len(candidates))

	for i, scoredEndpoint := range candidates {
		// Handle zero score
		if scoredEndpoint.Score <= 0 {
			// Assign key=0 for zero-score endpoints (effectively excludes them from selection)
			weightedEndpoints[i] = weightedScoredEndpoint{ScoredEndpoint: scoredEndpoint, key: 0}
			continue
		}

		// If we're here the scoredEndpoint.Score > 0. Generate a random number U in (0,1)
		u := picker.PickerRand.Float64()
		if u == 0 {
			u = 1e-10 // Avoid 0 to ensure positive key
		}

		weightedEndpoints[i] = weightedScoredEndpoint{ScoredEndpoint: scoredEndpoint, key: math.Pow(u, 1.0/scoredEndpoint.Score)} // key = U^(1/weight)
	}

	// Sort by key in descending order (largest keys first)
	sort.Slice(weightedEndpoints, func(i, j int) bool {
		return weightedEndpoints[i].key > weightedEndpoints[j].key
	})

	// Select top k endpoints
	selectedCount := min(p.params.MaxNumOfEndpoints, len(weightedEndpoints))

	targetEndpoints := make([]framework.Endpoint, selectedCount)
	for i := range selectedCount {
		targetEndpoints[i] = weightedEndpoints[i].ScoredEndpoint
	}

	return &framework.ProfileRunResult{TargetEndpoints: targetEndpoints}
}
