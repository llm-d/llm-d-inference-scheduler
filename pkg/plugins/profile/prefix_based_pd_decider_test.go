package profile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

func TestGetUserInputLenInTokens(t *testing.T) {
	tests := []struct {
		name     string
		req      *scheduling.LLMRequest
		wantMin  int // at least this many tokens
		wantZero bool
	}{
		{
			name:    "completions prompt",
			req:     completionsRequest("hello world hello world"), // 23 chars → 5 tokens
			wantMin: 5,
		},
		{
			name:    "chat completions",
			req:     chatRequest(false, false, false),
			wantMin: 1,
		},
		{
			name:     "empty completions prompt",
			req:      completionsRequest(""),
			wantZero: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, err := getUserInputLenInTokens(tt.req)
			assert.NoError(t, err)
			if tt.wantZero {
				assert.Zero(t, tokens)
			} else {
				assert.GreaterOrEqual(t, tokens, tt.wantMin)
			}
		})
	}
}
