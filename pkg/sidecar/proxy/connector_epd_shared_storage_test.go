/*
Copyright 2025 The llm-d Authors.

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

package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractMMItems(t *testing.T) {
	tests := []struct {
		name     string
		request  map[string]any
		expected int
	}{
		{
			name: "no multimodal items",
			request: map[string]any{
				"messages": []any{
					map[string]any{
						"role":    "user",
						"content": "Hello, world!",
					},
				},
			},
			expected: 0,
		},
		{
			name: "single image item",
			request: map[string]any{
				"messages": []any{
					map[string]any{
						"role": "user",
						"content": []any{
							map[string]any{
								"type": "text",
								"text": "What's in this image?",
							},
							map[string]any{
								"type": "image_url",
								"image_url": map[string]any{
									"url": "https://example.com/image.jpg",
								},
							},
						},
					},
				},
			},
			expected: 1,
		},
		{
			name: "multiple multimodal items",
			request: map[string]any{
				"messages": []any{
					map[string]any{
						"role": "user",
						"content": []any{
							map[string]any{
								"type": "image_url",
								"image_url": map[string]any{
									"url": "https://example.com/image1.jpg",
								},
							},
							map[string]any{
								"type": "audio_url",
								"audio_url": map[string]any{
									"url": "https://example.com/audio.mp3",
								},
							},
							map[string]any{
								"type": "text",
								"text": "Describe these",
							},
						},
					},
				},
			},
			expected: 2,
		},
		{
			name: "input_audio type",
			request: map[string]any{
				"messages": []any{
					map[string]any{
						"role": "user",
						"content": []any{
							map[string]any{
								"type": "input_audio",
								"input_audio": map[string]any{
									"data":   "base64data",
									"format": "wav",
								},
							},
						},
					},
				},
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := extractMMItems(tt.request)
			assert.Equal(t, tt.expected, len(items), "unexpected number of MM items")
		})
	}
}

func TestBuildEncoderRequest(t *testing.T) {
	originalRequest := map[string]any{
		"model": "test-model",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "What's in this image?",
					},
					map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": "https://example.com/image.jpg",
						},
					},
				},
			},
		},
		"max_tokens": 100,
		"stream":     true,
	}

	mmItem := map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "https://example.com/image.jpg",
		},
	}

	encoderRequest := buildEncoderRequest(originalRequest, mmItem)

	// Verify encoder request modifications
	assert.Equal(t, 1, encoderRequest["max_tokens"])
	assert.Equal(t, false, encoderRequest["stream"])
	_, hasStreamOptions := encoderRequest["stream_options"]
	assert.False(t, hasStreamOptions)

	// Verify messages contain only the MM item
	messages, ok := encoderRequest["messages"].([]map[string]any)
	assert.True(t, ok)
	assert.Equal(t, 1, len(messages))

	content, ok := messages[0]["content"].([]map[string]any)
	assert.True(t, ok)
	assert.Equal(t, 1, len(content))
	assert.Equal(t, "image_url", content[0]["type"])
}
