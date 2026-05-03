/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package weightedrandom

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwksched "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/picker"
)

func makeScored(name string, score float64) *fwksched.ScoredEndpoint {
	ep := fwksched.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: k8stypes.NamespacedName{Name: name}}, nil, nil,
	)
	return &fwksched.ScoredEndpoint{Endpoint: ep, Score: score}
}

func TestParameters_validate(t *testing.T) {
	tests := []struct {
		name    string
		in      Parameters
		wantErr bool
	}{
		{name: "no top-K (legacy default)", in: Parameters{}, wantErr: false},
		{name: "topK only", in: Parameters{TopK: 5}, wantErr: false},
		{name: "topKPercent only", in: Parameters{TopKPercent: 25}, wantErr: false},
		{name: "both set", in: Parameters{TopK: 5, TopKPercent: 25}, wantErr: false},
		{name: "negative topK", in: Parameters{TopK: -1}, wantErr: true},
		{name: "topKPercent below 0", in: Parameters{TopKPercent: -1}, wantErr: true},
		{name: "topKPercent above 100", in: Parameters{TopKPercent: 101}, wantErr: true},
		{name: "negative minTopK", in: Parameters{TopK: 5, MinTopK: -1}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.validate()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParameters_effectiveK(t *testing.T) {
	tests := []struct {
		name string
		p    Parameters
		n    int
		want int
	}{
		{name: "no top-K returns n", p: Parameters{}, n: 40, want: 40},
		{name: "no candidates", p: Parameters{TopKPercent: 25}, n: 0, want: 0},
		{name: "topKPercent 25% of 40", p: Parameters{TopKPercent: 25, MinTopK: 2}, n: 40, want: 10},
		{name: "topKPercent ceil rounds up", p: Parameters{TopKPercent: 25, MinTopK: 1}, n: 7, want: 2}, // ceil(7*25/100)=2
		{name: "topKPercent 1% with default floor", p: Parameters{TopKPercent: 1}, n: 100, want: 2},
		{name: "topKPercent 100% returns N", p: Parameters{TopKPercent: 100}, n: 40, want: 40},
		{name: "topK=5 with N=40", p: Parameters{TopK: 5}, n: 40, want: 5},
		{name: "topK exceeds N caps at N", p: Parameters{TopK: 100}, n: 10, want: 10},
		{name: "topK below floor lifts to floor", p: Parameters{TopK: 1, MinTopK: 3}, n: 40, want: 3},
		{name: "both: topK is tighter", p: Parameters{TopK: 3, TopKPercent: 25, MinTopK: 1}, n: 40, want: 3},
		{name: "both: percent is tighter", p: Parameters{TopK: 50, TopKPercent: 10, MinTopK: 1}, n: 40, want: 4},
		{name: "MinTopK=1 allows argmax", p: Parameters{TopKPercent: 1, MinTopK: 1}, n: 100, want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.p.effectiveK(tc.n))
		})
	}
}

// TestPick_NoTopK_PreservesLegacyBehavior verifies that with neither TopK nor TopKPercent set,
// the picker considers every candidate (any pod with positive score can win).
func TestPick_NoTopK_PreservesLegacyBehavior(t *testing.T) {
	const trials = 4000
	candidates := []*fwksched.ScoredEndpoint{
		makeScored("a", 1), makeScored("b", 2), makeScored("c", 3),
		makeScored("d", 4), makeScored("e", 5),
	}
	pkr := New(Parameters{
		PickerParameters: picker.PickerParameters{MaxNumOfEndpoints: 1},
	})

	hits := map[string]int{}
	for i := 0; i < trials; i++ {
		res := pkr.Pick(context.Background(), nil, candidates)
		hits[res.TargetEndpoints[0].GetMetadata().NamespacedName.Name]++
	}
	// Every positive-score pod must be sampled at least once when no top-K is set.
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		assert.Greater(t, hits[n], 0, "pod %q never sampled — legacy full-pool behavior broken", n)
	}
}

// TestPick_TopKPercent_RestrictsPool verifies that with TopKPercent=30, only the top 3 of 10
// candidates can ever be selected over many trials.
func TestPick_TopKPercent_RestrictsPool(t *testing.T) {
	const trials = 4000
	cands := make([]*fwksched.ScoredEndpoint, 10)
	for i := 0; i < 10; i++ {
		cands[i] = makeScored(fmt.Sprintf("p-%d", i), float64(i+1))
	}
	pkr := New(Parameters{
		PickerParameters: picker.PickerParameters{MaxNumOfEndpoints: 1},
		TopKPercent:      30, // top 3 (ceil(10*30/100)=3)
	})

	allowed := map[string]bool{"p-7": true, "p-8": true, "p-9": true}
	for i := 0; i < trials; i++ {
		// Shuffle each call to verify Pick doesn't rely on input order.
		shuffled := append([]*fwksched.ScoredEndpoint(nil), cands...)
		shuffled[0], shuffled[5] = shuffled[5], shuffled[0]
		shuffled[2], shuffled[8] = shuffled[8], shuffled[2]

		res := pkr.Pick(context.Background(), nil, shuffled)
		require.Len(t, res.TargetEndpoints, 1)
		name := res.TargetEndpoints[0].GetMetadata().NamespacedName.Name
		assert.True(t, allowed[name], "selected pod %q is outside the top-3 by score", name)
	}
}

// TestPick_MinTopK_AvoidsArgmax verifies the floor: TopKPercent=1 on 5 candidates would otherwise
// give K=1, but MinTopK=2 keeps two so the picker actually samples between them.
func TestPick_MinTopK_AvoidsArgmax(t *testing.T) {
	const trials = 4000
	cands := []*fwksched.ScoredEndpoint{
		makeScored("low", 1), makeScored("mid", 5),
		makeScored("high1", 9), makeScored("high2", 10),
		makeScored("verylow", 0.5),
	}
	pkr := New(Parameters{
		PickerParameters: picker.PickerParameters{MaxNumOfEndpoints: 1},
		TopKPercent:      1,
		MinTopK:          2,
	})

	hits := map[string]int{}
	for i := 0; i < trials; i++ {
		res := pkr.Pick(context.Background(), nil, cands)
		hits[res.TargetEndpoints[0].GetMetadata().NamespacedName.Name]++
	}
	assert.Greater(t, hits["high1"], 0)
	assert.Greater(t, hits["high2"], 0)
	for _, n := range []string{"low", "mid", "verylow"} {
		assert.Equal(t, 0, hits[n], "pod %q outside top-2 must never be picked", n)
	}
	// A-Res still weights inside the top-K.
	assert.Greater(t, hits["high2"], hits["high1"], "score 10 should beat score 9 inside top-K")
}

// TestPick_AllZero_WithTopK_DelegatesToRandom verifies the existing all-zero shortcut still
// fires before top-K filtering — the random-picker fallback runs over the full input, matching
// pre-change behavior on cold clusters.
func TestPick_AllZero_WithTopK_DelegatesToRandom(t *testing.T) {
	const trials = 4000
	cands := []*fwksched.ScoredEndpoint{
		makeScored("a", 0), makeScored("b", 0), makeScored("c", 0), makeScored("d", 0),
	}
	pkr := New(Parameters{
		PickerParameters: picker.PickerParameters{MaxNumOfEndpoints: 1},
		TopK:             2,
	})

	hits := map[string]int{}
	for i := 0; i < trials; i++ {
		res := pkr.Pick(context.Background(), nil, cands)
		hits[res.TargetEndpoints[0].GetMetadata().NamespacedName.Name]++
	}
	require.GreaterOrEqual(t, len(hits), 3, "all-zero must uniformly sample, not collapse to top-K=2")
}

// TestPick_TopK_NeverPicksZeroOutsidePool verifies that when some candidates are positive and
// others zero, only positive-score candidates within the top-K participate — A-Res key=0
// excludes zero-score pods even within the restricted pool.
func TestPick_TopK_NeverPicksZeroOutsidePool(t *testing.T) {
	const trials = 4000
	cands := []*fwksched.ScoredEndpoint{
		makeScored("a", 7), makeScored("b", 3),
		makeScored("c", 0), makeScored("d", 0), makeScored("e", 0),
	}
	pkr := New(Parameters{
		PickerParameters: picker.PickerParameters{MaxNumOfEndpoints: 1},
		TopK:             2, MinTopK: 1,
	})

	for i := 0; i < trials; i++ {
		res := pkr.Pick(context.Background(), nil, cands)
		require.Contains(t, []string{"a", "b"},
			res.TargetEndpoints[0].GetMetadata().NamespacedName.Name)
	}
}

// TestPick_MaxNumOfEndpointsBounded verifies maxNumOfEndpoints respects the top-K pool.
func TestPick_MaxNumOfEndpointsBounded(t *testing.T) {
	cands := make([]*fwksched.ScoredEndpoint, 20)
	for i := 0; i < 20; i++ {
		cands[i] = makeScored(fmt.Sprintf("p-%d", i), float64(i+1))
	}
	pkr := New(Parameters{
		PickerParameters: picker.PickerParameters{MaxNumOfEndpoints: 3},
		TopKPercent:      25, // top 5
		MinTopK:          2,
	})

	res := pkr.Pick(context.Background(), nil, cands)
	require.Len(t, res.TargetEndpoints, 3)
	allowed := map[string]bool{"p-15": true, "p-16": true, "p-17": true, "p-18": true, "p-19": true}
	for _, ep := range res.TargetEndpoints {
		assert.True(t, allowed[ep.GetMetadata().NamespacedName.Name],
			"output endpoint %q outside top-5 by score", ep.GetMetadata().NamespacedName.Name)
	}
}

// TestPick_TopK_TiesAtCutoffSampleAll is a regression guard against deterministic
// input-order bias at the K-th cutoff. Five candidates share the same score; with
// TopK=3 the picker must still be able to sample every one of them across many
// trials (pre-shuffle randomizes the stable sort's tie-break).
func TestPick_TopK_TiesAtCutoffSampleAll(t *testing.T) {
	const trials = 4000
	cands := []*fwksched.ScoredEndpoint{
		makeScored("a", 5), makeScored("b", 5), makeScored("c", 5),
		makeScored("d", 5), makeScored("e", 5),
	}
	pkr := New(Parameters{
		PickerParameters: picker.PickerParameters{MaxNumOfEndpoints: 1},
		TopK:             3, MinTopK: 1,
	})

	hits := map[string]int{}
	for i := 0; i < trials; i++ {
		res := pkr.Pick(context.Background(), nil, cands)
		hits[res.TargetEndpoints[0].GetMetadata().NamespacedName.Name]++
	}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		assert.Greater(t, hits[n], 0,
			"endpoint %q never sampled — input-order bias on tied scores at K-th cutoff", n)
	}
}

// TestFactory_RejectsInvalidConfig covers the JSON entry point's validation.
func TestFactory_RejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "empty config (legacy)", raw: `{}`, wantErr: false},
		{name: "topK only", raw: `{"topK": 5}`, wantErr: false},
		{name: "topKPercent only", raw: `{"topKPercent": 25}`, wantErr: false},
		{name: "topKPercent above 100", raw: `{"topKPercent": 200}`, wantErr: true},
		{name: "negative topK", raw: `{"topK": -3}`, wantErr: true},
		{name: "invalid json", raw: `{not json}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := WeightedRandomPickerFactory("wr", json.RawMessage(tc.raw), nil)
			if tc.wantErr {
				assert.Error(t, err)
				assert.Nil(t, p)
			} else {
				require.NoError(t, err)
				require.NotNil(t, p)
			}
		})
	}
}
