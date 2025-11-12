package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSchedulerPDDecisionCount(t *testing.T) {
	RecordPDDecisionCounter(DecisionTypePrefillDecode)
	RecordPDDecisionCounter(DecisionTypeDecodeOnly)
	RecordPDDecisionCounter(DecisionTypePrefillDecode)
	if err := testutil.CollectAndCompare(SchedulerPDDecisionCount, strings.NewReader(`
		# HELP llm_d_inference_scheduler_pd_decision_total [ALPHA] Total number of P/D disaggregation decisions made
		# TYPE llm_d_inference_scheduler_pd_decision_total counter
		llm_d_inference_scheduler_pd_decision_total{decision_type="decode-only"} 1
		llm_d_inference_scheduler_pd_decision_total{decision_type="prefill-decode"} 2
	`), "decision_type"); err != nil {
		t.Errorf("RecordPDDecisionCounter() failed: %v", err)
	}
}
