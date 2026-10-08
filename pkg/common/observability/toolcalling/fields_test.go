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
	"fmt"
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

func TestCompareRequestJSONNumbers(t *testing.T) {
	tests := []struct {
		name   string
		before string
		after  string
		want   FieldStatusValue
	}{
		{name: "large integer changed", before: "9007199254740992", after: "9007199254740993", want: FieldStatusChanged},
		{name: "large negative integer changed", before: "-9007199254740992", after: "-9007199254740993", want: FieldStatusChanged},
		{name: "precise decimal changed", before: "0.100000000000000005", after: "0.100000000000000006", want: FieldStatusChanged},
		{name: "integer and decimal equivalent", before: "1", after: "1.0", want: FieldStatusPreserved},
		{name: "integer and exponent equivalent", before: "1000", after: "1e3", want: FieldStatusPreserved},
		{name: "fraction and exponent equivalent", before: "0.0010", after: "1E-3", want: FieldStatusPreserved},
		{name: "negative numbers equivalent", before: "-12.50", after: "-125e-1", want: FieldStatusPreserved},
		{name: "negative zero equivalent", before: "-0.0", after: "0e1000", want: FieldStatusPreserved},
		{name: "beyond float range equivalent", before: "1e400", after: "10e399", want: FieldStatusPreserved},
		{name: "large exponent equivalent", before: "1e1000000000", after: "10e999999999", want: FieldStatusPreserved},
		{name: "large exponent changed", before: "1e1000000000", after: "1e1000000001", want: FieldStatusChanged},
		{name: "exponent beyond int64 equivalent", before: "1e9223372036854775808", after: "10e9223372036854775807", want: FieldStatusPreserved},
		{name: "number and string differ", before: "1", after: `"1"`, want: FieldStatusChanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := func(value string) []byte {
				return []byte(`{"tools":[{"type":"function","function":{"name":"example","parameters":{"enum":[` + value + `]}}}]}`)
			}
			before, err := CaptureRequestJSON(APISurfaceChatCompletions, body(tt.before))
			require.NoError(t, err)
			after, err := CaptureRequestJSON(APISurfaceChatCompletions, body(tt.after))
			require.NoError(t, err)
			statuses, err := CompareRequests(before, after)
			require.NoError(t, err)
			require.Equal(t, tt.want, resultStatus(t, statuses, FieldTools))
		})
	}
}

func TestCompareRequestNumbersAcrossPayloadTypes(t *testing.T) {
	before, err := CaptureRequest(APISurfaceMessages, map[string]any{
		"tools": []any{map[string]any{"input_schema": map[string]any{"maximum": int64(9007199254740993)}}},
	})
	require.NoError(t, err)
	after, err := CaptureRequestJSON(APISurfaceMessages, []byte(`{"tools":[{"input_schema":{"maximum":9007199254740993.0}}]}`))
	require.NoError(t, err)
	statuses, err := CompareRequests(before, after)
	require.NoError(t, err)
	require.Equal(t, FieldStatusPreserved, resultStatus(t, statuses, FieldTools))
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

func TestRejectedStatusesOnlyIncludeExplicitPresentFields(t *testing.T) {
	snapshot, err := CaptureRequest(APISurfaceChatCompletions, map[string]any{
		"tools":       []any{},
		"tool_choice": "required",
	})
	require.NoError(t, err)

	for _, tt := range []struct {
		name   string
		fields []Field
		want   []Field
	}{
		{name: "no explicit rejection"},
		{name: "only tools", fields: []Field{FieldTools}, want: []Field{FieldTools}},
		{name: "only choice", fields: []Field{FieldToolChoice}, want: []Field{FieldToolChoice}},
		{name: "duplicates count once", fields: []Field{FieldTools, FieldTools}, want: []Field{FieldTools}},
		{name: "absent field", fields: []Field{FieldParallelToolCalls}},
		{name: "unsupported field", fields: []Field{"private_unknown_field"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			results := RejectedFieldStatuses(snapshot, tt.fields...)
			require.Len(t, results, len(tt.want))
			for i, field := range tt.want {
				require.Equal(t, FieldStatus{Field: field, Status: FieldStatusRejected, Observed: true}, results[i])
			}
		})
	}

	messages, err := CaptureRequest(APISurfaceMessages, map[string]any{"parallel_tool_calls": true})
	require.NoError(t, err)
	require.Empty(t, RejectedFieldStatuses(messages, FieldParallelToolCalls))
}

func TestCaptureRequestJSON_MalformedBody(t *testing.T) {
	_, err := CaptureRequestJSON(APISurfaceChatCompletions, []byte(`{"tools":[`))
	require.Error(t, err)
}

func TestCaptureRequestJSONRejectsTrailingData(t *testing.T) {
	for _, body := range []string{`{"tools":[]} {}`, `{"tools":[]} 1`, `{"tools":[]} invalid`} {
		_, err := CaptureRequestJSON(APISurfaceChatCompletions, []byte(body))
		require.Error(t, err)
	}
}

func TestCaptureRequestJSONFieldLookup(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		present bool
	}{
		{name: "absent", body: `{"messages":[{"content":"hello"}]}`},
		{name: "nested field", body: `{"messages":[{"tools":[]}]}`},
		{name: "field name in content", body: `{"messages":[{"content":"tools tool_choice"}]}`},
		{name: "case sensitive", body: `{"TOOLS":[]}`},
		{name: "escaped field name", body: `{"to\u006fls":[]}`, present: true},
		{name: "explicit null", body: `{"tools":null}`, present: true},
		{name: "structured output only", body: `{"response_format":{"type":"json_object"}}`},
		{name: "null structured output", body: `{"response_format":null}`},
		{name: "parallel calls only", body: `{"parallel_tool_calls":false}`, present: true},
		{name: "duplicate field", body: `{"tool_choice":"required","tool_choice":"auto"}`, present: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := CaptureRequestJSON(APISurfaceChatCompletions, []byte(tt.body))
			require.NoError(t, err)
			require.Equal(t, tt.present, snapshot.Summary().ToolCallingPresent)
			if tt.name == "duplicate field" {
				require.Equal(t, ToolChoiceAuto, snapshot.Summary().ToolChoiceKind)
			}
		})
	}
	for _, body := range []string{`null`, `[]`, `42`, `{"messages":[}`, `{"model":"m"} {}`} {
		_, err := CaptureRequestJSON(APISurfaceChatCompletions, []byte(body))
		require.Error(t, err)
	}
}

func TestCaptureRequestJSONNonToolAllocations(t *testing.T) {
	small := []byte(`{"messages":[{"content":"hello"}]}`)
	large := []byte(`{"messages":[{"content":"` + strings.Repeat("x", 64*1024) + `"}]}`)
	capture := func(body []byte) float64 {
		return testing.AllocsPerRun(20, func() {
			snapshot, err := CaptureRequestJSON(APISurfaceChatCompletions, body)
			if err != nil || snapshot.Summary().ToolCallingPresent {
				t.Fatal("expected a valid non-tool snapshot", err)
			}
		})
	}
	require.LessOrEqual(t, capture(large), float64(2), "non-tool bodies should not be decoded into Go values")
	require.LessOrEqual(t, capture(small), float64(2))
}

func BenchmarkCaptureRequestJSONNonTool(b *testing.B) {
	for _, size := range []int{1024, 64 * 1024, 1024 * 1024} {
		body := []byte(`{"messages":[{"content":"` + strings.Repeat("x", size) + `"}]}`)
		b.Run(fmt.Sprintf("%d_bytes", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := CaptureRequestJSON(APISurfaceChatCompletions, body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
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

func TestRequestSummaryPresenceIncludesOnlySupportedToolFields(t *testing.T) {
	for _, tt := range []struct {
		name        string
		surface     APISurface
		body        map[string]any
		wantPresent bool
		wantChoice  bool
	}{
		{name: "absent fields", surface: APISurfaceChatCompletions},
		{name: "structured output only", surface: APISurfaceChatCompletions, body: map[string]any{"response_format": map[string]any{"type": "json_object"}}},
		{name: "null structured output", surface: APISurfaceChatCompletions, body: map[string]any{"response_format": nil}},
		{name: "empty tools", surface: APISurfaceChatCompletions, body: map[string]any{"tools": []any{}}, wantPresent: true},
		{name: "null tools", surface: APISurfaceChatCompletions, body: map[string]any{"tools": nil}, wantPresent: true},
		{name: "tool choice only", surface: APISurfaceChatCompletions, body: map[string]any{"tool_choice": "required"}, wantPresent: true, wantChoice: true},
		{name: "parallel calls only", surface: APISurfaceChatCompletions, body: map[string]any{"parallel_tool_calls": false}, wantPresent: true},
		{name: "Messages null choice", surface: APISurfaceMessages, body: map[string]any{"tool_choice": nil}, wantPresent: true, wantChoice: true},
		{name: "Messages unsupported fields", surface: APISurfaceMessages, body: map[string]any{"response_format": nil, "parallel_tool_calls": true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := CaptureRequest(tt.surface, tt.body)
			require.NoError(t, err)
			summary := snapshot.Summary()
			require.Equal(t, tt.wantPresent, summary.ToolCallingPresent)
			require.Equal(t, tt.wantChoice, summary.ToolChoicePresent)
			require.Empty(t, summary.ToolCountBucket)
		})
	}
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
		encoded.WriteString(attribute.Value.String())
		encoded.WriteByte('\n')
	}
	require.Contains(t, encoded.String(), "llm_d.tool_calling.field.tools.status=preserved")
	require.NotContains(t, encoded.String(), "sentinel_private_name")
}

func TestSpanAttributesOmittedForRequestsWithoutToolCallingFields(t *testing.T) {
	snapshot, err := CaptureRequest(APISurfaceChatCompletions, map[string]any{"model": "test-model"})
	require.NoError(t, err)

	statuses, err := CompareRequests(snapshot, snapshot)
	require.NoError(t, err)
	require.Empty(t, snapshot.SpanAttributes(statuses))
}

func TestSpanAttributesRetainObservedToolCallingMutation(t *testing.T) {
	snapshot, err := CaptureRequest(APISurfaceChatCompletions, map[string]any{"model": "test-model"})
	require.NoError(t, err)

	attrs := snapshot.SpanAttributes([]FieldStatus{{
		Field:    FieldTools,
		Status:   FieldStatusChanged,
		Observed: true,
	}})
	require.Len(t, attrs, 3)
	require.Equal(t, "llm_d.tool_calling.field.tools.status", string(attrs[2].Key))
	require.Equal(t, string(FieldStatusChanged), attrs[2].Value.AsString())
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
	require.Equal(t, "llm_d_epp_tool_calling_field_status_total", MetricToolCallingFieldStatus)
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
