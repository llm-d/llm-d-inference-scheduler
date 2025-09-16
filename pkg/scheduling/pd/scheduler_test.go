package pd_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics" // Import config for thresholds
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/picker"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/profile"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

const (
	prefill = "prefill"
	decode  = "decode"
)

// Tests the scheduler expected behavior.
func TestPDSchedule(t *testing.T) {
	pod1 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod1"},
			Address:        "1.2.3.4",
			Labels:         map[string]string{filter.RoleLabel: filter.RolePrefill},
		},
		MetricsState: &backendmetrics.MetricsState{WaitingQueueSize: 0},
	}
	pod2 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "pod2"},
			Address:        "5.6.7.8",
			Labels:         map[string]string{filter.RoleLabel: filter.RoleDecode},
		},
		MetricsState: &backendmetrics.MetricsState{WaitingQueueSize: 0},
	}
	noRolePod1 := &types.PodMetrics{
		Pod: &backend.Pod{
			NamespacedName: k8stypes.NamespacedName{Name: "noRolePod1"},
			Address:        "1.1.1.1",
		},
		MetricsState: &backendmetrics.MetricsState{WaitingQueueSize: 2},
	}

	prefillDecodeResult := &types.SchedulingResult{
		ProfileResults: map[string]*types.ProfileRunResult{
			decode: {TargetPods: []types.Pod{
				&types.ScoredPod{
					Pod: pod2,
				},
			},
			},
			prefill: {
				TargetPods: []types.Pod{
					&types.ScoredPod{
						Pod: pod1,
					},
				},
			},
		},

		PrimaryProfileName: decode,
	}

	decodeResult := &types.SchedulingResult{
		ProfileResults: map[string]*types.ProfileRunResult{
			decode: {
				TargetPods: []types.Pod{
					&types.ScoredPod{
						Pod: pod2,
					},
				},
			},
		},
		PrimaryProfileName: decode,
	}

	tests := []struct {
		name     string
		req      *types.LLMRequest
		input    []types.Pod
		wantRes  *types.SchedulingResult
		wantRes2 *types.SchedulingResult // a subsequent call to check prefix cache and how it affects PD
		err      bool
	}{
		{
			name: "no candidate pods",
			req: &types.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "any-model",
				Prompt:      "12345678901",
			},
			input: []types.Pod{},
			err:   true,
		},
		{
			name: "one decode pod, long prompt",
			req: &types.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Prompt:      "12345678901",
			},
			// pod2 will be picked because it is the only pod with Decode role
			input:   []types.Pod{pod2},
			wantRes: decodeResult,
		},
		{
			name: "one prefill pod, long prompt",
			req: &types.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Prompt:      "12345678901",
			},
			// no Decode pod
			input: []types.Pod{pod1},
			err:   true,
		},
		{
			name: "1P1D - long prompt",
			req: &types.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Prompt:      "12345678906",
			},
			// pod2 will be picked in the decode profile result, pod1 will be in the prefill profile result
			input:    []types.Pod{pod1, pod2},
			wantRes:  prefillDecodeResult,
			wantRes2: decodeResult,
		},
		{
			name: "1P1Dshort",
			req: &types.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Prompt:      "12345",
			},
			// pod2 will be picked because it is the decode pod, pod1 shouldn't be picked,
			// because the prompt is too short
			input:    []types.Pod{pod1, pod2},
			wantRes:  decodeResult,
			wantRes2: decodeResult,
		},
		{
			name: "TestRolesWithNoDecode",
			req: &types.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Prompt:      "12345678901",
			},
			input: []types.Pod{pod1, noRolePod1},
			wantRes: &types.SchedulingResult{
				ProfileResults: map[string]*types.ProfileRunResult{
					decode: {
						TargetPods: []types.Pod{
							&types.ScoredPod{
								Pod: noRolePod1,
							},
						},
					},
					prefill: {
						TargetPods: []types.Pod{
							&types.ScoredPod{
								Pod: pod1,
							},
						},
					},
				},
				PrimaryProfileName: decode,
			},
		},
		{
			name: "1P2D - long prompt",
			req: &types.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Prompt:      "12345678906",
			},
			// pod2 will be picked in the decode profile result cause it has higher score than noRolePod1
			// pod1 will be in the prefill profile result
			input:    []types.Pod{pod1, pod2, noRolePod1},
			wantRes:  prefillDecodeResult,
			wantRes2: decodeResult,
		},
	}

	ctx := context.Background()
	logger := testr.New(t)
	ctx = log.IntoContext(ctx, logger)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			//  initialize scheduler with config
			prefixScorer := prefix.New(ctx, prefix.Config{HashBlockSize: 5, MaxPrefixBlocksToMatch: 256, LRUCapacityPerServer: 31250})

			prefillSchedulerProfile := framework.NewSchedulerProfile().
				WithFilters(filter.NewPrefillRole()).
				WithPicker(picker.NewMaxScorePicker(picker.DefaultMaxNumOfEndpoints))
			err := prefillSchedulerProfile.AddPlugins(framework.NewWeightedScorer(prefixScorer, 50))
			assert.NoError(t, err, "SchedulerProfile AddPlugins returned unexpected error")

			decodeSchedulerProfile := framework.NewSchedulerProfile().
				WithFilters(filter.NewDecodeRole()).
				WithScorers(framework.NewWeightedScorer(scorer.NewLoadAware(ctx, scorer.QueueThresholdDefault), 1)).
				WithPicker(picker.NewMaxScorePicker(picker.DefaultMaxNumOfEndpoints))
			err = decodeSchedulerProfile.AddPlugins(framework.NewWeightedScorer(prefixScorer, 0))
			assert.NoError(t, err, "SchedulerProfile AddPlugins returned unexpected error")

			profileHandle := profile.NewPdProfileHandler(prefill, decode, prefixScorer.TypedName().Name, 10, 5)

			schedulerConfig := scheduling.NewSchedulerConfig(profileHandle, map[string]*framework.SchedulerProfile{
				prefill: prefillSchedulerProfile,
				decode:  decodeSchedulerProfile,
			})
			scheduler := scheduling.NewSchedulerWithConfig(schedulerConfig)
			got, err := scheduler.Schedule(ctx, test.req, test.input)

			if test.err != (err != nil) {
				t.Errorf("Unexpected error, got %v, want %v", err, test.err)
			}

			if diff := cmp.Diff(test.wantRes, got, cmpopts.IgnoreFields(types.ScoredPod{}, "Score")); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}

			if test.wantRes2 != nil { // Checking the prefix match in the decode pod.
				// make sure prefix plugin stores the prefix hit in cache, so we can test it in the following schedule call
				prefixScorer.PreRequest(ctx, test.req, got, 0)
				time.Sleep(time.Second)

				got, err = scheduler.Schedule(ctx, test.req, test.input)
				if test.err != (err != nil) {
					t.Errorf("Unexpected error in schedule call, got %v, want %v", err, test.err)
				}

				if diff := cmp.Diff(test.wantRes2, got, cmpopts.IgnoreFields(types.ScoredPod{}, "Score")); diff != "" {
					t.Errorf("Unexpected output in subsequent schedule call (-want +got): %v", diff)
				}
			}
		})
	}
}
