package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSchedulerPDDecisionCount(t *testing.T) {
	model := "test-model"

	RecordPDDecision(model, DecisionTypePrefillDecode)
	RecordPDDecision(model, DecisionTypeDecodeOnly)
	RecordPDDecision(model, DecisionTypePrefillDecode)
	expected := `
		# HELP llm_d_inference_scheduler_pd_decision_total [ALPHA] Total number of P/D disaggregation decisions made
		# TYPE llm_d_inference_scheduler_pd_decision_total counter
		llm_d_inference_scheduler_pd_decision_total{decision_type="decode-only",model_name="test-model"} 1
		llm_d_inference_scheduler_pd_decision_total{decision_type="prefill-decode",model_name="test-model"} 2
	`

	if err := testutil.CollectAndCompare(SchedulerPDDecisionCount, strings.NewReader(expected),
		"llm_d_inference_scheduler_pd_decision_total"); err != nil {
		t.Errorf("RecordPDDecision() failed: %v", err)
	}
}
