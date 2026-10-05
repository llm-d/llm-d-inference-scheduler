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

package toolcalling

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompareRequestFields(t *testing.T) {
	tests := []struct {
		name       string
		api        APISurface
		before     map[string]any
		after      map[string]any
		field      Field
		wantStatus FieldStatusValue
	}{
		{
			name: "reordered object keys are preserved",
			api:  APISurfaceChatCompletions,
			before: map[string]any{
				"tools": []any{map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":       "private_tool_name",
						"parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
					},
				}},
			},
			after: map[string]any{
				"tools": []any{map[string]any{
					"function": map[string]any{
						"parameters": map[string]any{"properties": map[string]any{"city": map[string]any{"type": "string"}}, "type": "object"},
						"name":       "private_tool_name",
					},
					"type": "function",
				}},
			},
			field:      FieldTools,
			wantStatus: FieldStatusPreserved,
		},
		{
			name:       "tool choice value changed",
			api:        APISurfaceChatCompletions,
			before:     map[string]any{"tool_choice": "required"},
			after:      map[string]any{"tool_choice": "auto"},
			field:      FieldToolChoice,
			wantStatus: FieldStatusChanged,
		},
		{
			name:       "field removed is dropped",
			api:        APISurfaceChatCompletions,
			before:     map[string]any{"parallel_tool_calls": true},
			after:      map[string]any{},
			field:      FieldParallelToolCalls,
			wantStatus: FieldStatusDropped,
		},
		{
			name:       "absent to null is changed",
			api:        APISurfaceMessages,
			before:     map[string]any{},
			after:      map[string]any{"tool_choice": nil},
			field:      FieldToolChoice,
			wantStatus: FieldStatusChanged,
		},
		{
			name:       "explicit null preserved",
			api:        APISurfaceMessages,
			before:     map[string]any{"tool_choice": nil},
			after:      map[string]any{"tool_choice": nil},
			field:      FieldToolChoice,
			wantStatus: FieldStatusPreserved,
		},
		{
			name:       "response format compared for chat completions",
			api:        APISurfaceChatCompletions,
			before:     map[string]any{"response_format": map[string]any{"type": "json_object"}},
			after:      map[string]any{"response_format": map[string]any{"type": "text"}},
			field:      FieldResponseFormat,
			wantStatus: FieldStatusChanged,
		},
		{
			name:       "array order is significant",
			api:        APISurfaceChatCompletions,
			before:     map[string]any{"tools": []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}}},
			after:      map[string]any{"tools": []any{map[string]any{"name": "b"}, map[string]any{"name": "a"}}},
			field:      FieldTools,
			wantStatus: FieldStatusChanged,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, err := CaptureRequest(tt.api, tt.before)
			require.NoError(t, err)
			after, err := CaptureRequest(tt.api, tt.after)
			require.NoError(t, err)

			results, err := CompareRequests(before, after)
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, resultStatus(t, results, tt.field))
		})
	}
}

func TestCompareRequests_ExplicitNullDiffersFromAbsent(t *testing.T) {
	absent, err := CaptureRequest(APISurfaceChatCompletions, map[string]any{})
	require.NoError(t, err)

	withNull, err := CaptureRequest(APISurfaceChatCompletions, map[string]any{"response_format": nil})
	require.NoError(t, err)

	results, err := CompareRequests(absent, withNull)
	require.NoError(t, err)
	require.Equal(t, FieldStatusChanged, resultStatus(t, results, FieldResponseFormat))
}

func TestCompareRequests_AbsentFieldsAreNotObserved(t *testing.T) {
	before, err := CaptureRequest(APISurfaceChatCompletions, map[string]any{})
	require.NoError(t, err)
	after, err := CaptureRequest(APISurfaceChatCompletions, map[string]any{})
	require.NoError(t, err)

	results, err := CompareRequests(before, after)
	require.NoError(t, err)
	require.False(t, resultFor(t, results, FieldToolChoice).Observed)
	require.Equal(t, FieldStatusPreserved, resultFor(t, results, FieldToolChoice).Status)
}

func TestRejectedStatusesOnlyIncludePresentFields(t *testing.T) {
	snapshot, err := CaptureRequest(APISurfaceChatCompletions, map[string]any{
		"tool_choice": "required",
	})
	require.NoError(t, err)

	results := RejectedFieldStatuses(snapshot)
	require.Len(t, results, 1)
	require.Equal(t, FieldToolChoice, results[0].Field)
	require.Equal(t, FieldStatusRejected, results[0].Status)
	require.True(t, results[0].Observed)
}

func TestCaptureRequestJSON_MalformedBody(t *testing.T) {
	_, err := CaptureRequestJSON(APISurfaceChatCompletions, []byte(`{"tools":[`))
	require.Error(t, err)
}

func TestCompareRequests_MessagesOnlyIncludesSupportedFields(t *testing.T) {
	before, err := CaptureRequest(APISurfaceMessages, map[string]any{
		"tools":           []any{},
		"tool_choice":     map[string]any{"type": "auto"},
		"response_format": map[string]any{"type": "json_object"},
	})
	require.NoError(t, err)
	after, err := CaptureRequest(APISurfaceMessages, map[string]any{
		"tools":           []any{},
		"tool_choice":     map[string]any{"type": "auto"},
		"response_format": map[string]any{"type": "text"},
	})
	require.NoError(t, err)

	results, err := CompareRequests(before, after)
	require.NoError(t, err)
	require.Len(t, results, 2)
	for _, result := range results {
		require.NotEqual(t, FieldResponseFormat, result.Field)
	}
}

func TestCompareRequests_RejectsDifferentSurfaces(t *testing.T) {
	chat, err := CaptureRequest(APISurfaceChatCompletions, map[string]any{})
	require.NoError(t, err)
	messages, err := CaptureRequest(APISurfaceMessages, map[string]any{})
	require.NoError(t, err)

	_, err = CompareRequests(chat, messages)
	require.Error(t, err)
}

func TestCaptureRequest_CopiesFieldValues(t *testing.T) {
	body := map[string]any{"tool_choice": "auto"}
	before, err := CaptureRequest(APISurfaceChatCompletions, body)
	require.NoError(t, err)
	body["tool_choice"] = "required"
	after, err := CaptureRequest(APISurfaceChatCompletions, body)
	require.NoError(t, err)

	results, err := CompareRequests(before, after)
	require.NoError(t, err)
	require.Equal(t, FieldStatusChanged, resultStatus(t, results, FieldToolChoice))
}

func TestWorstFieldStatus(t *testing.T) {
	require.Equal(t, FieldStatusRejected, WorstFieldStatus(FieldStatusPreserved, FieldStatusChanged, FieldStatusRejected))
	require.Equal(t, FieldStatusDropped, WorstFieldStatus(FieldStatusChanged, FieldStatusDropped))
	require.Equal(t, FieldStatusPreserved, WorstFieldStatus(FieldStatusPreserved, FieldStatusPreserved))
}

func TestRequestSummaryUsesBoundedValuesOnly(t *testing.T) {
	snapshot, err := CaptureRequest(APISurfaceChatCompletions, map[string]any{
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "sentinel_private_name", "parameters": map[string]any{"description": "sentinel_private_schema"}}},
		},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "sentinel_private_name"}},
	})
	require.NoError(t, err)

	summary := snapshot.Summary()
	require.True(t, summary.ToolCallingPresent)
	require.True(t, summary.ToolChoicePresent)
	require.Equal(t, ToolChoiceNamed, summary.ToolChoiceKind)
	require.Equal(t, ToolCountBucketOne, summary.ToolCountBucket)

	encoded, err := json.Marshal(summary)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "sentinel_private")
}

func TestRequestSummaryPresenceIncludesAnySupportedField(t *testing.T) {
	snapshot, err := CaptureRequest(APISurfaceMessages, map[string]any{
		"tool_choice": nil,
	})
	require.NoError(t, err)

	summary := snapshot.Summary()
	require.True(t, summary.ToolCallingPresent)
	require.True(t, summary.ToolChoicePresent)
	require.Equal(t, ToolChoiceUnknown, summary.ToolChoiceKind)
}

func TestSpanAttributesContainOnlyBoundedSummaryAndFieldStatuses(t *testing.T) {
	snapshot, err := CaptureRequest(APISurfaceChatCompletions, map[string]any{
		"tools":       []any{map[string]any{"function": map[string]any{"name": "sentinel_private_name"}}},
		"tool_choice": "required",
	})
	require.NoError(t, err)
	results := []FieldStatus{{Field: FieldTools, Status: FieldStatusPreserved, Observed: true}}

	attributes := snapshot.SpanAttributes(results)
	var encoded strings.Builder
	for _, attribute := range attributes {
		encoded.WriteString(string(attribute.Key) + "=")
		encoded.WriteString(attribute.Value.Emit())
		encoded.WriteByte('\n')
	}
	require.Contains(t, encoded.String(), "llm_d.tool_calling.field.tools.status=preserved")
	require.NotContains(t, encoded.String(), "sentinel_private_name")
}

func TestRequestSummaryMessagesToolChoice(t *testing.T) {
	snapshot, err := CaptureRequest(APISurfaceMessages, map[string]any{
		"tool_choice": map[string]any{"type": "tool", "name": "sentinel_private_name"},
	})
	require.NoError(t, err)

	summary := snapshot.Summary()
	require.True(t, summary.ToolChoicePresent)
	require.Equal(t, ToolChoiceNamed, summary.ToolChoiceKind)
	require.True(t, summary.ToolCallingPresent)
}

func TestMetricSchema(t *testing.T) {
	require.Equal(t, "llm_d_tool_calling_field_status_total", MetricToolCallingFieldStatus)
	require.Equal(t, []string{"component", "direction", "field", "status"}, []string{
		MetricLabelComponent,
		MetricLabelDirection,
		MetricLabelField,
		MetricLabelStatus,
	})
	require.Equal(t, []Field{FieldTools, FieldToolChoice, FieldParallelToolCalls, FieldResponseFormat}, mustFieldsForSurface(t, APISurfaceChatCompletions))
	require.Equal(t, []Field{FieldTools, FieldToolChoice}, mustFieldsForSurface(t, APISurfaceMessages))
}

func TestToolCountBucket(t *testing.T) {
	tests := []struct {
		count int
		want  ToolCountBucket
	}{
		{count: 0, want: ""},
		{count: 1, want: ToolCountBucketOne},
		{count: 2, want: ToolCountBucketTwoToFive},
		{count: 5, want: ToolCountBucketTwoToFive},
		{count: 6, want: ToolCountBucketSixToTen},
		{count: 10, want: ToolCountBucketSixToTen},
		{count: 11, want: ToolCountBucketElevenPlus},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, bucketToolCount(tt.count))
	}
}

func resultStatus(t *testing.T, results []FieldStatus, field Field) FieldStatusValue {
	t.Helper()
	for _, result := range results {
		if result.Field == field {
			return result.Status
		}
	}
	t.Fatalf("field %q missing from comparison results", field)
	return ""
}

func resultFor(t *testing.T, results []FieldStatus, field Field) FieldStatus {
	t.Helper()
	for _, result := range results {
		if result.Field == field {
			return result
		}
	}
	t.Fatalf("field %q missing from comparison results", field)
	return FieldStatus{}
}

func mustFieldsForSurface(t *testing.T, surface APISurface) []Field {
	t.Helper()
	fields, err := FieldsForSurface(surface)
	require.NoError(t, err)
	return fields
}
