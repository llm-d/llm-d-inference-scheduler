package scorer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

func (s *PendingTokens) getPodTokens(endpointName string) int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.endpointTokens[endpointName]
}

func (s *PendingTokens) hasPodTokens(endpointName string) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	_, exists := s.endpointTokens[endpointName]
	return exists
}

func TestPendingTokensScorer_Score(t *testing.T) {
	endpointA := newTestEndpoint("pod-a", 0)
	endpointB := newTestEndpoint("pod-b", 0)
	endpointC := newTestEndpoint("pod-c", 0)

	tests := []struct {
		name       string
		setupCache func(*PendingTokens)
		input      []scheduling.Endpoint
		wantScores map[scheduling.Endpoint]float64
	}{
		{
			name:       "no endpoints tracked",
			setupCache: func(_ *PendingTokens) {},
			input:      []scheduling.Endpoint{endpointA, endpointB, endpointC},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 1.0,
				endpointB: 1.0,
				endpointC: 1.0,
			},
		},
		{
			name: "endpoints with different pending token counts",
			setupCache: func(s *PendingTokens) {
				s.mutex.Lock()
				s.endpointTokens["default/pod-a"] = 1000
				s.endpointTokens["default/pod-b"] = 500
				s.mutex.Unlock()
			},
			input: []scheduling.Endpoint{endpointA, endpointB, endpointC},
			wantScores: map[scheduling.Endpoint]float64{
				endpointA: 0.0,
				endpointB: 0.5,
				endpointC: 1.0,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := utils.NewTestContext(t)
			scorer := NewPendingTokens(ctx, nil)
			test.setupCache(scorer)
			got := scorer.Score(ctx, nil, nil, test.input)
			assert.Equal(t, test.wantScores, got)
		})
	}
}

func TestPendingTokensScorer_PreRequest(t *testing.T) {
	endpointA := newTestEndpoint("pod-a", 0)

	t.Run("increments by token count not by 1", func(t *testing.T) {
		ctx := utils.NewTestContext(t)
		scorer := NewPendingTokens(ctx, nil)

		request := &scheduling.LLMRequest{
			RequestId: "req-1",
			Body: &scheduling.LLMRequestBody{
				Completions: &scheduling.CompletionsRequest{
					Prompt: "hello",
				},
			},
		}
		result := newTestSchedulingResult(map[string]scheduling.Endpoint{
			"default": endpointA,
		})

		scorer.PreRequest(ctx, request, result)

		assert.Equal(t, 5, scorer.getPodTokens("default/pod-a"))
	})
}

func TestPendingTokensScorer_ResponseComplete(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := NewPendingTokens(ctx, nil)

	endpointA := newTestEndpoint("pod-a", 0)
	request := &scheduling.LLMRequest{
		RequestId: "req-1",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: "hello",
			},
		},
	}

	result := newTestSchedulingResult(map[string]scheduling.Endpoint{
		"default": endpointA,
	})
	scorer.PreRequest(ctx, request, result)

	require.Equal(t, 5, scorer.getPodTokens("default/pod-a"))

	scorer.ResponseComplete(ctx, request, &requestcontrol.Response{}, endpointA.GetMetadata())

	assert.False(t, scorer.hasPodTokens("default/pod-a"),
		"token count should be removed after ResponseComplete")
}

func TestPendingTokensScorer_TTLExpiration(t *testing.T) {
	ctx := utils.NewTestContext(t)

	params := &PendingTokensParameters{RequestTimeout: "1s"}
	scorer := NewPendingTokens(ctx, params)

	endpointA := newTestEndpoint("pod-a", 0)
	request := &scheduling.LLMRequest{
		RequestId: "req-ttl",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: "hello",
			},
		},
	}

	result := newTestSchedulingResult(map[string]scheduling.Endpoint{
		"default": endpointA,
	})
	scorer.PreRequest(ctx, request, result)

	require.Equal(t, 5, scorer.getPodTokens("default/pod-a"))

	time.Sleep(2 * time.Second)
	scorer.requestCache.DeleteExpired()

	assert.False(t, scorer.hasPodTokens("default/pod-a"),
		"token count should be cleaned up after TTL expiration")
}

func TestPendingTokensScorer_TypedName(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := NewPendingTokens(ctx, nil)
	assert.Equal(t, PendingTokensType, scorer.TypedName().Type)
}

func TestPendingTokensScorer_WithName(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := NewPendingTokens(ctx, nil)
	scorer = scorer.WithName("my-scorer")
	assert.Equal(t, "my-scorer", scorer.TypedName().Name)
}

func TestPendingTokensScorer_LoadBasedDistribution(t *testing.T) {
	ctx := utils.NewTestContext(t)
	scorer := NewPendingTokens(ctx, nil)

	podA := newTestEndpoint("pod-a", 0)
	podB := newTestEndpoint("pod-b", 0)
	podC := newTestEndpoint("pod-c", 0)
	endpoints := []scheduling.Endpoint{podA, podB, podC}

	// simulate a large request already routed to pod-a (2000 tokens)
	largeReq := &scheduling.LLMRequest{
		RequestId: "large-req",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: string(make([]byte, 2000)),
			},
		},
	}
	scorer.PreRequest(ctx, largeReq, newTestSchedulingResult(map[string]scheduling.Endpoint{
		"default": podA,
	}))

	// simulate a medium request already routed to pod-b (500 tokens)
	mediumReq := &scheduling.LLMRequest{
		RequestId: "medium-req",
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{
				Prompt: string(make([]byte, 500)),
			},
		},
	}
	scorer.PreRequest(ctx, mediumReq, newTestSchedulingResult(map[string]scheduling.Endpoint{
		"default": podB,
	}))

	// score endpoints for a new incoming request given existing load
	scores := scorer.Score(ctx, nil, nil, endpoints)

	// pod-c should score highest (no load)
	// pod-b should score middle
	// pod-a should score lowest (most loaded)
	assert.InDelta(t, 1.0, scores[podC], 0.0001, "unloaded pod should score 1.0")
	assert.True(t, scores[podB] > scores[podA], "pod-b (500 tokens) should score higher than pod-a (2000 tokens)")
	assert.InDelta(t, 0.0, scores[podA], 0.0001, "most loaded pod should score 0.0")
	assert.InDelta(t, 0.75, scores[podB], 0.0001, "pod-b: 1.0 - (500/2000) = 0.75")

	t.Logf("pod-a score: %v (2000 pending tokens)", scores[podA])
	t.Logf("pod-b score: %v (500 pending tokens)", scores[podB])
	t.Logf("pod-c score: %v (0 pending tokens)", scores[podC])
}