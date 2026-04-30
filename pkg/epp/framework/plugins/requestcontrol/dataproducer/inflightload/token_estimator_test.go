/*
Copyright 2026 The Kubernetes Authors.

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

package inflightload

import (
	"testing"

	"github.com/stretchr/testify/require"

	fwkrh "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requesthandling"
	framework "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
)

func TestSimpleTokenEstimator_Estimate(t *testing.T) {
	estimator := NewSimpleTokenEstimator()

	testCases := []struct {
		name     string
		request  *framework.InferenceRequest
		expected int64
	}{
		{
			name:     "Nil request",
			request:  nil,
			expected: 0,
		},
		{
			name:     "Empty request",
			request:  &framework.InferenceRequest{},
			expected: 0,
		},
		{
			name: "Body nil",
			request: &framework.InferenceRequest{
				Body: nil,
			},
			expected: 0,
		},
		{
			name: "Less than 4 characters",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{Raw: "123"},
					},
				},
			},
			expected: 3, // 3 chars -> 1 input token. 1 + round(1 * 1.5) = 3
		},
		{
			name: "Completions Request",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{Raw: "Hello, world!"},
					},
				},
			},
			expected: 8, // 13 / 4 = 3. output = round(3 * 1.5) = 5. total = 8
		},
		{
			name: "Completions with empty prompt",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{},
					},
				},
			},
			expected: 0, // 0 / 4 = 0
		},
		{
			name: "Completions with exactly 4 characters",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{Raw: "1234"},
					},
				},
			},
			expected: 3, // 4 / 4 = 1. output = round(1 * 1.5) = 2. total = 3
		},
		{
			name: "Chat Completions Request with Structured content",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					ChatCompletions: &fwkrh.ChatCompletionsRequest{
						Messages: []fwkrh.Message{
							{
								Role: "user",
								Content: fwkrh.Content{
									Structured: []fwkrh.ContentBlock{
										{
											Type: "text",
											Text: "This is a longer message.",
										},
									},
								},
							},
						},
					},
				},
			},
			expected: 15, // len is 27. 27 / 4 = 6. output = round(6 * 1.5) = 9. total = 15
		},
		{
			name: "Chat Completions with Raw content",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					ChatCompletions: &fwkrh.ChatCompletionsRequest{
						Messages: []fwkrh.Message{
							{
								Role: "user",
								Content: fwkrh.Content{
									Raw: "This is raw content.",
								},
							},
						},
					},
				},
			},
			expected: 13, // len is 21. 21 / 4 = 5. output = round(5 * 1.5) = 8. total = 13
		},
		{
			name: "Chat Completions with multiple messages",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					ChatCompletions: &fwkrh.ChatCompletionsRequest{
						Messages: []fwkrh.Message{
							{
								Role: "user",
								Content: fwkrh.Content{
									Structured: []fwkrh.ContentBlock{
										{Type: "text", Text: "Hi"},
									},
								},
							},
							{
								Role: "assistant",
								Content: fwkrh.Content{
									Structured: []fwkrh.ContentBlock{
										{Type: "text", Text: "Hello"},
									},
								},
							},
						},
					},
				},
			},
			expected: 5, // len is 11. 11 / 4 = 2. output = round(2 * 1.5) = 3. total = 5
		},
		{
			name: "Chat Completions with empty messages",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					ChatCompletions: &fwkrh.ChatCompletionsRequest{
						Messages: []fwkrh.Message{},
					},
				},
			},
			expected: 0, // 0 / 4 = 0
		},
		{
			name: "Responses API with string input",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Responses: &fwkrh.ResponsesRequest{
						Input: "Tell me a story about a brave knight.",
					},
				},
			},
			expected: 23, // len is 37. 37 / 4 = 9. output = round(9 * 1.5) = 14. total = 23
		},
		{
			name: "Responses API with structured input",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Responses: &fwkrh.ResponsesRequest{
						Input: []any{
							map[string]any{"role": "user", "content": "Hello"},
						},
					},
				},
			},
			expected: 20, // len is 35. 35 / 4 = 8. output = round(8 * 1.5) = 12. total = 20
		},
		{
			name: "Conversations API",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Conversations: &fwkrh.ConversationsRequest{
						Items: []fwkrh.ConversationItem{
							{Type: "message", Role: "user", Content: "Hi there"},
						},
					},
				},
			},
			expected: 33, // len is 55. 55 / 4 = 13. output = round(13 * 1.5) = 20. total = 33
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := estimator.Estimate(tc.request)
			require.Equal(t, tc.expected, actual)
		})
	}
}

func TestSimpleTokenEstimator_Estimate_CustomConfig(t *testing.T) {
	estimator := &SimpleTokenEstimator{
		outputRatio: 2.0,
	}

	testCases := []struct {
		name     string
		request  *framework.InferenceRequest
		expected int64
	}{
		{
			name: "Empty prompt with custom config",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{},
					},
				},
			},
			expected: 0, // 0 / 4 = 0
		},
		{
			name: "4 chars with custom config",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{Raw: "1234"},
					},
				},
			},
			expected: 3, // 4 / 4 = 1. output = round(1 * 2.0) = 2. total = 3
		},
		{
			name: "More than 4 chars with custom config",
			request: &framework.InferenceRequest{
				Body: &fwkrh.InferenceRequestBody{
					Completions: &fwkrh.CompletionsRequest{
						Prompt: fwkrh.Prompt{Raw: "This is a longer message."},
					},
				},
			},
			expected: 18, // len is 25. 25 / 4 = 6. output = round(6 * 2.0) = 12. total = 18
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := estimator.Estimate(tc.request)
			require.Equal(t, tc.expected, actual)
		})
	}
}
