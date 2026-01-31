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
	"sigs.k8s.io/controller-runtime/pkg/log" // Import config for thresholds
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	fwkschd "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/scheduling/picker"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/scheduling/scorer/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling"

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
	endpoint1 := fwkschd.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "endpoint1"},
			Address:        "1.2.3.4",
			Labels:         map[string]string{filter.RoleLabel: filter.RolePrefill},
		},
		&fwkdl.Metrics{WaitingQueueSize: 0},
		nil,
	)
	endpoint2 := fwkschd.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "endpoint2"},
			Address:        "5.6.7.8",
			Labels:         map[string]string{filter.RoleLabel: filter.RoleDecode},
		},
		&fwkdl.Metrics{WaitingQueueSize: 0},
		nil,
	)
	noRoleEndpoint1 := fwkschd.NewEndpoint(
		&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Name: "noRoleEndpoint1"},
			Address:        "1.1.1.1",
		},
		&fwkdl.Metrics{WaitingQueueSize: 2},
		nil,
	)

	prefillDecodeResult := &fwkschd.SchedulingResult{
		ProfileResults: map[string]*fwkschd.ProfileRunResult{
			decode: {TargetEndpoints: []fwkschd.Endpoint{
				&fwkschd.ScoredEndpoint{
					Endpoint: endpoint2,
				},
			},
			},
			prefill: {
				TargetEndpoints: []fwkschd.Endpoint{
					&fwkschd.ScoredEndpoint{
						Endpoint: endpoint1,
					},
				},
			},
		},

		PrimaryProfileName: decode,
	}

	decodeResult := &fwkschd.SchedulingResult{
		ProfileResults: map[string]*fwkschd.ProfileRunResult{
			decode: {
				TargetEndpoints: []fwkschd.Endpoint{
					&fwkschd.ScoredEndpoint{
						Endpoint: endpoint2,
					},
				},
			},
		},
		PrimaryProfileName: decode,
	}

	tests := []struct {
		name     string
		req      *fwkschd.LLMRequest
		input    []fwkschd.Endpoint
		wantRes  *fwkschd.SchedulingResult
		wantRes2 *fwkschd.SchedulingResult // a subsequent call to check prefix cache and how it affects PD
		err      bool
	}{
		{
			name: "no candidate endpoints",
			req: &fwkschd.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "any-model",
				Body: &fwkschd.LLMRequestBody{
					Completions: &fwkschd.CompletionsRequest{
						Prompt: "12345678901",
					},
				},
			},
			input: []fwkschd.Endpoint{},
			err:   true,
		},
		{
			name: "one decode endpoint, long prompt",
			req: &fwkschd.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Body: &fwkschd.LLMRequestBody{
					Completions: &fwkschd.CompletionsRequest{
						Prompt: "12345678901",
					},
				},
			},
			// endpoint2 will be picked because it is the only endpoint with Decode role
			input:   []fwkschd.Endpoint{endpoint2},
			wantRes: decodeResult,
		},
		{
			name: "one prefill endpoint, long prompt",
			req: &fwkschd.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Body: &fwkschd.LLMRequestBody{
					Completions: &fwkschd.CompletionsRequest{
						Prompt: "12345678901",
					},
				},
			},
			// no Decode endpoint
			input: []fwkschd.Endpoint{endpoint1},
			err:   true,
		},
		{
			name: "1P1D - long prompt",
			req: &fwkschd.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Body: &fwkschd.LLMRequestBody{
					Completions: &fwkschd.CompletionsRequest{
						Prompt: "12345678906",
					},
				},
			},
			// endpoint2 will be picked in the decode profile result, endpoint1 will be in the prefill profile result
			input:    []fwkschd.Endpoint{endpoint1, endpoint2},
			wantRes:  prefillDecodeResult,
			wantRes2: decodeResult,
		},
		{
			name: "1P1Dshort",
			req: &fwkschd.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Body: &fwkschd.LLMRequestBody{
					Completions: &fwkschd.CompletionsRequest{
						Prompt: "12345",
					},
				},
			},
			// endpoint2 will be picked because it is the decode endpoint, endpoint1 shouldn't be picked,
			// because the prompt is too short
			input:    []fwkschd.Endpoint{endpoint1, endpoint2},
			wantRes:  decodeResult,
			wantRes2: decodeResult,
		},
		{
			name: "TestRolesWithNoDecode",
			req: &fwkschd.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Body: &fwkschd.LLMRequestBody{
					Completions: &fwkschd.CompletionsRequest{
						Prompt: "12345678901",
					},
				},
			},
			input: []fwkschd.Endpoint{endpoint1, noRoleEndpoint1},
			wantRes: &fwkschd.SchedulingResult{
				ProfileResults: map[string]*fwkschd.ProfileRunResult{
					decode: {
						TargetEndpoints: []fwkschd.Endpoint{
							&fwkschd.ScoredEndpoint{
								Endpoint: noRoleEndpoint1,
							},
						},
					},
					prefill: {
						TargetEndpoints: []fwkschd.Endpoint{
							&fwkschd.ScoredEndpoint{
								Endpoint: endpoint1,
							},
						},
					},
				},
				PrimaryProfileName: decode,
			},
		},
		{
			name: "1P2D - long prompt",
			req: &fwkschd.LLMRequest{
				RequestId:   uuid.NewString(),
				TargetModel: "critical",
				Body: &fwkschd.LLMRequestBody{
					Completions: &fwkschd.CompletionsRequest{
						Prompt: "12345678906",
					},
				},
			},
			// endpoint2 will be picked in the decode profile result cause it has higher score than noRoleEndpoint1
			// endpoint1 will be in the prefill profile result
			input:    []fwkschd.Endpoint{endpoint1, endpoint2, noRoleEndpoint1},
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
			prefixScorer, _ := prefix.New(ctx, prefix.Config{BlockSizeTokens: 1, MaxPrefixBlocksToMatch: 256, LRUCapacityPerServer: 31250})

			prefillSchedulerProfile := scheduling.NewSchedulerProfile().
				WithFilters(filter.NewPrefillRole()).
				WithPicker(picker.NewMaxScorePicker(picker.DefaultMaxNumOfEndpoints))
			err := prefillSchedulerProfile.AddPlugins(scheduling.NewWeightedScorer(prefixScorer, 50))
			assert.NoError(t, err, "SchedulerProfile AddPlugins returned unexpected error")

			decodeSchedulerProfile := scheduling.NewSchedulerProfile().
				WithFilters(filter.NewDecodeRole()).
				WithScorers(scheduling.NewWeightedScorer(scorer.NewLoadAware(ctx, scorer.QueueThresholdDefault), 1)).
				WithPicker(picker.NewMaxScorePicker(picker.DefaultMaxNumOfEndpoints))
			err = decodeSchedulerProfile.AddPlugins(scheduling.NewWeightedScorer(prefixScorer, 0))
			assert.NoError(t, err, "SchedulerProfile AddPlugins returned unexpected error")

			profileHandle := profile.NewPdProfileHandler(prefill, decode, prefixScorer.TypedName().Type, prefixScorer.TypedName().Name, 10, 1, 0)

			schedulerConfig := scheduling.NewSchedulerConfig(profileHandle, map[string]fwkschd.SchedulerProfile{
				prefill: prefillSchedulerProfile,
				decode:  decodeSchedulerProfile,
			})
			scheduler := scheduling.NewSchedulerWithConfig(schedulerConfig)
			got, err := scheduler.Schedule(ctx, test.req, test.input)

			if test.err != (err != nil) {
				t.Errorf("Unexpected error, got %v, want %v", err, test.err)
			}

			if diff := cmp.Diff(test.wantRes, got, cmpopts.IgnoreUnexported(fwkdl.Attributes{}), cmpopts.IgnoreFields(fwkschd.ScoredEndpoint{}, "Score")); diff != "" {
				t.Errorf("Unexpected output (-want +got): %v", diff)
			}

			if test.wantRes2 != nil { // Checking the prefix match in the decode pod.
				// make sure prefix plugin stores the prefix hit in cache, so we can test it in the following schedule call
				prefixScorer.PreRequest(ctx, test.req, got)
				time.Sleep(time.Second)

				got, err = scheduler.Schedule(ctx, test.req, test.input)
				if test.err != (err != nil) {
					t.Errorf("Unexpected error in schedule call, got %v, want %v", err, test.err)
				}

				if diff := cmp.Diff(test.wantRes2, got, cmpopts.IgnoreUnexported(fwkdl.Attributes{}), cmpopts.IgnoreFields(fwkschd.ScoredEndpoint{}, "Score")); diff != "" {
					t.Errorf("Unexpected output in subsequent schedule call (-want +got): %v", diff)
				}
			}
		})
	}
}
