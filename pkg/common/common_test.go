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

package common

import "testing"

func TestStripScheme(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "http scheme",
			input:    "http://localhost:4317",
			expected: "localhost:4317",
		},
		{
			name:     "https scheme",
			input:    "https://localhost:4317",
			expected: "localhost:4317",
		},
		{
			name:     "no scheme",
			input:    "localhost:4317",
			expected: "localhost:4317",
		},
		{
			name:     "host only",
			input:    "localhost",
			expected: "localhost",
		},
		{
			name:     "http with domain",
			input:    "http://otel-collector.monitoring.svc.cluster.local:4317",
			expected: "otel-collector.monitoring.svc.cluster.local:4317",
		},
		{
			name:     "https with domain",
			input:    "https://otel-collector.monitoring.svc.cluster.local:4317",
			expected: "otel-collector.monitoring.svc.cluster.local:4317",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "ip address with http",
			input:    "http://10.0.0.1:4317",
			expected: "10.0.0.1:4317",
		},
		{
			name:     "ip address with https",
			input:    "https://10.0.0.1:4317",
			expected: "10.0.0.1:4317",
		},
		{
			name:     "ip address without scheme",
			input:    "10.0.0.1:4317",
			expected: "10.0.0.1:4317",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripScheme(tt.input)
			if result != tt.expected {
				t.Errorf("StripScheme(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
