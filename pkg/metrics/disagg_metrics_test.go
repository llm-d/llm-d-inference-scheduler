package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordDisaggDecision(t *testing.T) {
	// Reset the counter before the test to avoid interference from other tests.
	SchedulerDisaggDecisionCount.Reset()

	model := "test-model"
	RecordDisaggDecision(model, DecisionTypeDecodeOnly)
	RecordDisaggDecision(model, DecisionTypePrefillDecode)
	RecordDisaggDecision(model, DecisionTypePrefillDecode)
	RecordDisaggDecision(model, DecisionTypeEncodeDecode)
	RecordDisaggDecision(model, DecisionTypeEncodePrefillDecode)
	RecordDisaggDecision(model, DecisionTypeEncodePrefillDecode)
	RecordDisaggDecision(model, DecisionTypeEncodePrefillDecode)

	expected := `
		# HELP llm_d_inference_scheduler_disagg_decision_total [ALPHA] Total number of disaggregation routing decisions made
		# TYPE llm_d_inference_scheduler_disagg_decision_total counter
		llm_d_inference_scheduler_disagg_decision_total{decision_type="decode-only",model_name="test-model"} 1
		llm_d_inference_scheduler_disagg_decision_total{decision_type="encode-decode",model_name="test-model"} 1
		llm_d_inference_scheduler_disagg_decision_total{decision_type="encode-prefill-decode",model_name="test-model"} 3
		llm_d_inference_scheduler_disagg_decision_total{decision_type="prefill-decode",model_name="test-model"} 2
	`

	if err := testutil.CollectAndCompare(SchedulerDisaggDecisionCount, strings.NewReader(expected),
		"llm_d_inference_scheduler_disagg_decision_total"); err != nil {
		t.Errorf("RecordDisaggDecision() failed: %v", err)
	}
}

func TestRecordDisaggDecisionEmptyModel(t *testing.T) {
	SchedulerDisaggDecisionCount.Reset()

	RecordDisaggDecision("", DecisionTypeDecodeOnly)

	expected := `
		# HELP llm_d_inference_scheduler_disagg_decision_total [ALPHA] Total number of disaggregation routing decisions made
		# TYPE llm_d_inference_scheduler_disagg_decision_total counter
		llm_d_inference_scheduler_disagg_decision_total{decision_type="decode-only",model_name="unknown"} 1
	`

	if err := testutil.CollectAndCompare(SchedulerDisaggDecisionCount, strings.NewReader(expected),
		"llm_d_inference_scheduler_disagg_decision_total"); err != nil {
		t.Errorf("RecordDisaggDecision() with empty model failed: %v", err)
	}
}

func TestDisaggDecisionType(t *testing.T) {
	tests := []struct {
		encodeUsed  bool
		prefillUsed bool
		want        string
	}{
		{false, false, DecisionTypeDecodeOnly},
		{false, true, DecisionTypePrefillDecode},
		{true, false, DecisionTypeEncodeDecode},
		{true, true, DecisionTypeEncodePrefillDecode},
	}
	for _, tt := range tests {
		got := DisaggDecisionType(tt.encodeUsed, tt.prefillUsed)
		if got != tt.want {
			t.Errorf("DisaggDecisionType(%v, %v) = %q, want %q", tt.encodeUsed, tt.prefillUsed, got, tt.want)
		}
	}
}

func TestGetDisaggCollectors(t *testing.T) {
	collectors := GetDisaggCollectors()
	if len(collectors) != 1 {
		t.Fatalf("GetDisaggCollectors() returned %d collectors, want 1", len(collectors))
	}
	if collectors[0] != prometheus.Collector(SchedulerDisaggDecisionCount) {
		t.Errorf("GetDisaggCollectors()[0] is not SchedulerDisaggDecisionCount")
	}
}
