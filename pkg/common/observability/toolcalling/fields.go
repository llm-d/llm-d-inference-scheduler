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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"

	"go.opentelemetry.io/otel/attribute"
)

// APISurface identifies an API with a tool-calling request contract.
type APISurface string

const (
	APISurfaceChatCompletions APISurface = "chat_completions"
	APISurfaceMessages        APISurface = "messages"
)

// Field is a request field compared at a request-mutating boundary.
type Field string

const (
	FieldTools             Field = "tools"
	FieldToolChoice        Field = "tool_choice"
	FieldParallelToolCalls Field = "parallel_tool_calls"
	FieldResponseFormat    Field = "response_format"
)

// FieldStatusValue is the bounded outcome for a compared request field.
type FieldStatusValue string

const (
	FieldStatusPreserved FieldStatusValue = "preserved"
	FieldStatusChanged   FieldStatusValue = "changed"
	FieldStatusDropped   FieldStatusValue = "dropped"
	FieldStatusRejected  FieldStatusValue = "rejected"
)

// Metric schema constants are shared by EPP and the routing sidecar.
const (
	MetricToolCallingFieldStatus = "llm_d_epp_tool_calling_field_status_total"

	MetricLabelComponent = "component"
	MetricLabelDirection = "direction"
	MetricLabelField     = "field"
	MetricLabelStatus    = "status"

	ComponentEPP            = "epp"
	ComponentRoutingSidecar = "routing_sidecar"

	DirectionRequest = "request"
)

// ToolChoiceKind is a sanitized tool_choice category.
type ToolChoiceKind string

const (
	ToolChoiceAuto     ToolChoiceKind = "auto"
	ToolChoiceNone     ToolChoiceKind = "none"
	ToolChoiceRequired ToolChoiceKind = "required"
	ToolChoiceNamed    ToolChoiceKind = "named"
	ToolChoiceUnknown  ToolChoiceKind = "unknown"
)

// ToolCountBucket is a bounded bucket for the number of tool definitions.
type ToolCountBucket string

const (
	ToolCountBucketOne        ToolCountBucket = "1"
	ToolCountBucketTwoToFive  ToolCountBucket = "2-5"
	ToolCountBucketSixToTen   ToolCountBucket = "6-10"
	ToolCountBucketElevenPlus ToolCountBucket = "11+"
)

// FieldStatus reports the comparison result for one supported field.
type FieldStatus struct {
	Field    Field
	Status   FieldStatusValue
	Observed bool
}

// RequestSummary contains only bounded, non-content-derived telemetry values.
type RequestSummary struct {
	ToolCallingPresent bool
	ToolChoicePresent  bool
	ToolChoiceKind     ToolChoiceKind
	ToolCountBucket    ToolCountBucket
}

type capturedField struct {
	present bool
	value   []byte
}

// RequestSnapshot stores serialized JSON for supported fields so later body
// mutations cannot alter the comparison baseline. Field contents remain
// private and are never included in Summary or metric labels.
type RequestSnapshot struct {
	surface APISurface
	fields  map[Field]capturedField
	summary RequestSummary
}

// CaptureRequest snapshots the fields supported by the selected API surface.
// Object-key order is normalized by encoding/json; array order is preserved.
func CaptureRequest(surface APISurface, body map[string]any) (RequestSnapshot, error) {
	fields, err := fieldsForSurface(surface)
	if err != nil {
		return RequestSnapshot{}, err
	}
	if body == nil {
		body = map[string]any{}
	}

	snapshot := RequestSnapshot{
		surface: surface,
		fields:  make(map[Field]capturedField, len(fields)),
		summary: RequestSummary{ToolChoiceKind: ToolChoiceUnknown},
	}
	for _, field := range fields {
		value, present := body[string(field)]
		captured := capturedField{present: present}
		if present {
			captured.value, err = json.Marshal(value)
			if err != nil {
				return RequestSnapshot{}, fmt.Errorf("marshal %s field: %w", field, err)
			}
		}
		snapshot.fields[field] = captured
		if present {
			snapshot.summary.ToolCallingPresent = true
		}
	}

	if tools, ok := body[string(FieldTools)].([]any); ok {
		if len(tools) > 0 {
			snapshot.summary.ToolCallingPresent = true
			snapshot.summary.ToolCountBucket = bucketToolCount(len(tools))
		}
	}
	if choice, present := body[string(FieldToolChoice)]; present {
		snapshot.summary.ToolChoicePresent = true
		snapshot.summary.ToolChoiceKind = normalizeToolChoiceValue(choice)
	}

	return snapshot, nil
}

// CaptureRequestJSON decodes only supported fields in a JSON request object.
func CaptureRequestJSON(surface APISurface, body []byte) (RequestSnapshot, error) {
	fields, err := fieldsForSurface(surface)
	if err != nil {
		return RequestSnapshot{}, err
	}
	// Escaped field names contain Unicode escapes. Possible matches still need
	// exact top-level lookup; nested keys and prompt text can match this precheck.
	possibleFields := bytes.Contains(body, []byte(`\u`))
	for _, field := range fields {
		possibleFields = possibleFields || bytes.Contains(body, []byte(field))
	}
	if !possibleFields {
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(body) {
			return RequestSnapshot{}, errors.New("decode request body: expected valid JSON object")
		}
		return RequestSnapshot{
			surface: surface,
			summary: RequestSummary{ToolChoiceKind: ToolChoiceUnknown},
		}, nil
	}

	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawFields); err != nil {
		return RequestSnapshot{}, fmt.Errorf("decode request body: %w", err)
	}
	if rawFields == nil {
		return RequestSnapshot{}, errors.New("decode request body: expected JSON object")
	}
	payload := make(map[string]any, len(fields))
	for _, field := range fields {
		if raw, present := rawFields[string(field)]; present {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			var value any
			if err := decoder.Decode(&value); err != nil {
				return RequestSnapshot{}, fmt.Errorf("decode %s field: %w", field, err)
			}
			payload[string(field)] = value
		}
	}
	return CaptureRequest(surface, payload)
}

// CompareRequests compares every supported field for the common API surface.
// An absent field and an explicit null are different values. Fields absent on
// both sides are reported as preserved. Numbers are compared exactly, allowing
// equivalent decimal and exponent notation.
func CompareRequests(before, after RequestSnapshot) ([]FieldStatus, error) {
	if before.surface != after.surface {
		return nil, fmt.Errorf("cannot compare API surfaces %q and %q", before.surface, after.surface)
	}
	fields, err := fieldsForSurface(before.surface)
	if err != nil {
		return nil, err
	}

	results := make([]FieldStatus, 0, len(fields))
	for _, field := range fields {
		left := before.fields[field]
		right := after.fields[field]
		status := FieldStatusPreserved
		switch {
		case left.present && !right.present:
			status = FieldStatusDropped
		case !left.present && right.present:
			status = FieldStatusChanged
		case left.present && right.present && !equalJSONFieldValues(left.value, right.value):
			status = FieldStatusChanged
		}
		results = append(results, FieldStatus{
			Field:    field,
			Status:   status,
			Observed: left.present || right.present,
		})
	}
	return results, nil
}

// Snapshots are serialized with sorted object keys, so token order is comparable.
// UseNumber preserves precision; normalization equates forms such as 1 and 1.0.
func equalJSONFieldValues(left, right []byte) bool {
	if bytes.Equal(left, right) {
		return true
	}
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	for {
		leftToken, leftErr := leftDecoder.Token()
		rightToken, rightErr := rightDecoder.Token()
		if leftErr != nil || rightErr != nil {
			return leftErr == io.EOF && rightErr == io.EOF
		}
		if leftToken == rightToken {
			continue
		}
		leftNumber, leftOK := leftToken.(json.Number)
		rightNumber, rightOK := rightToken.(json.Number)
		if !leftOK || !rightOK || normalizeJSONNumber(leftNumber) != normalizeJSONNumber(rightNumber) {
			return false
		}
	}
}

// Normalize numbers to a signed coefficient and decimal exponent without rounding.
// Keep the exponent separate to avoid expanding numbers such as 1e1000000000.
func normalizeJSONNumber(number json.Number) string {
	value := string(number)
	sign := ""
	if value[0] == '-' {
		sign = "-"
		value = value[1:]
	}
	var exponent big.Int
	if i := strings.IndexAny(value, "eE"); i >= 0 {
		exponent.SetString(value[i+1:], 10)
		value = value[:i]
	}
	fractionalDigits := 0
	if i := strings.IndexByte(value, '.'); i >= 0 {
		fractionalDigits = len(value) - i - 1
		value = value[:i] + value[i+1:]
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0"
	}
	coefficient := strings.TrimRight(value, "0")
	exponent.Add(&exponent, big.NewInt(int64(len(value)-len(coefficient)-fractionalDigits)))
	return sign + coefficient + "e" + exponent.String()
}

// RejectedFieldStatuses marks client-supplied fields rejected at a boundary.
func RejectedFieldStatuses(snapshot RequestSnapshot) []FieldStatus {
	fields, err := fieldsForSurface(snapshot.surface)
	if err != nil {
		return nil
	}

	results := make([]FieldStatus, 0, len(fields))
	for _, field := range fields {
		if snapshot.fields[field].present {
			results = append(results, FieldStatus{
				Field:    field,
				Status:   FieldStatusRejected,
				Observed: true,
			})
		}
	}
	return results
}

// Summary returns the snapshot's bounded telemetry values without field data.
func (snapshot RequestSnapshot) Summary() RequestSummary {
	return snapshot.summary
}

// SpanAttributes returns bounded summary values and observed field statuses.
// It returns nil when the request and comparison contain no tool-calling fields.
func (snapshot RequestSnapshot) SpanAttributes(statuses []FieldStatus) []attribute.KeyValue {
	if !snapshot.summary.ToolCallingPresent && !hasObservedFieldStatus(statuses) {
		return nil
	}

	attrs := []attribute.KeyValue{
		attribute.String("llm_d.tool_calling.api_surface", string(snapshot.surface)),
		attribute.Bool("llm_d.tool_calling.present", snapshot.summary.ToolCallingPresent),
		attribute.String("llm_d.tool_calling.tool_choice", string(snapshot.summary.ToolChoiceKind)),
	}
	if snapshot.summary.ToolCountBucket != "" {
		attrs = append(attrs, attribute.String("llm_d.tool_calling.tool_count", string(snapshot.summary.ToolCountBucket)))
	}
	for _, result := range statuses {
		if result.Observed {
			key := "llm_d.tool_calling.field." + string(result.Field) + ".status"
			attrs = append(attrs, attribute.String(key, string(result.Status)))
		}
	}
	return attrs
}

func hasObservedFieldStatus(statuses []FieldStatus) bool {
	for _, status := range statuses {
		if status.Observed {
			return true
		}
	}
	return false
}

// FieldsForSurface returns the bounded field set compared for an API surface.
func FieldsForSurface(surface APISurface) ([]Field, error) {
	fields, err := fieldsForSurface(surface)
	if err != nil {
		return nil, err
	}
	return append([]Field(nil), fields...), nil
}

// WorstFieldStatus returns the highest-priority status according to the shared
// precedence: rejected, dropped, changed, preserved.
func WorstFieldStatus(statuses ...FieldStatusValue) FieldStatusValue {
	worst := FieldStatusPreserved
	for _, status := range statuses {
		if statusRank(status) > statusRank(worst) {
			worst = status
		}
	}
	return worst
}

func fieldsForSurface(surface APISurface) ([]Field, error) {
	switch surface {
	case APISurfaceChatCompletions:
		return []Field{FieldTools, FieldToolChoice, FieldParallelToolCalls, FieldResponseFormat}, nil
	case APISurfaceMessages:
		return []Field{FieldTools, FieldToolChoice}, nil
	default:
		return nil, fmt.Errorf("unsupported tool-calling API surface %q", surface)
	}
}

func normalizeToolChoiceValue(value any) ToolChoiceKind {
	switch choice := value.(type) {
	case string:
		switch choice {
		case "auto":
			return ToolChoiceAuto
		case "none":
			return ToolChoiceNone
		case "required":
			return ToolChoiceRequired
		default:
			return ToolChoiceUnknown
		}
	case map[string]any:
		switch choiceType, _ := choice["type"].(string); choiceType {
		case "auto":
			return ToolChoiceAuto
		case "none":
			return ToolChoiceNone
		case "any":
			return ToolChoiceRequired
		case "tool":
			if name, ok := choice["name"].(string); ok && name != "" {
				return ToolChoiceNamed
			}
		case "function":
			if function, ok := choice["function"].(map[string]any); ok {
				if name, ok := function["name"].(string); ok && name != "" {
					return ToolChoiceNamed
				}
			}
		}
	}
	return ToolChoiceUnknown
}

func bucketToolCount(count int) ToolCountBucket {
	switch {
	case count <= 0:
		return ""
	case count == 1:
		return ToolCountBucketOne
	case count <= 5:
		return ToolCountBucketTwoToFive
	case count <= 10:
		return ToolCountBucketSixToTen
	default:
		return ToolCountBucketElevenPlus
	}
}

func statusRank(status FieldStatusValue) int {
	switch status {
	case FieldStatusPreserved:
		return 0
	case FieldStatusChanged:
		return 1
	case FieldStatusDropped:
		return 2
	case FieldStatusRejected:
		return 3
	default:
		return -1
	}
}
