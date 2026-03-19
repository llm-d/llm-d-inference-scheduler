package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer/plugins/approximateprefix"
	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

// ── Shared test helpers ──────────────────────────────────────────────────────

const (
	testPodPort        = "8000"
	DefaultTestPodPort = testPodPort // alias used by dp_profile_handler_test.go

	// Profile name constants used throughout tests.
	testPrefillProfile  = "prefill"
	testEncodeProfile   = "encode"
	testPrefixCacheType = "prefix-cache-scorer"
)

// newMockSchedulerProfile and newMockProfileRunResult are aliases used by dp_profile_handler_test.go.
func newMockSchedulerProfile() scheduling.SchedulerProfile { return &mockProfile{} }
func newMockProfileRunResult(port string, names ...string) *scheduling.ProfileRunResult {
	return makeProfileRunResult(port, names...)
}

func makeEndpoint(nsn k8stypes.NamespacedName, ip, port string, labels map[string]string) scheduling.Endpoint {
	return scheduling.NewEndpoint(
		&fwkdl.EndpointMetadata{NamespacedName: nsn, Address: ip, Port: port, Labels: labels},
		nil,
		fwkdl.NewAttributes(),
	)
}

func makeProfileRunResult(port string, names ...string) *scheduling.ProfileRunResult {
	eps := make([]scheduling.Endpoint, 0, len(names))
	for i, name := range names {
		eps = append(eps, makeEndpoint(
			k8stypes.NamespacedName{Namespace: "default", Name: name},
			fmt.Sprintf("10.0.0.%d", i+1), port, nil,
		))
	}
	return &scheduling.ProfileRunResult{TargetEndpoints: eps}
}

type mockProfile struct{}

func (p *mockProfile) Run(_ context.Context, _ *scheduling.LLMRequest, _ *scheduling.CycleState, _ []scheduling.Endpoint) (*scheduling.ProfileRunResult, error) {
	return &scheduling.ProfileRunResult{}, nil
}

func profileNames(m map[string]scheduling.SchedulerProfile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// completionsRequest builds a text-only LLMRequest.
func completionsRequest(prompt string) *scheduling.LLMRequest {
	return &scheduling.LLMRequest{
		Body: &scheduling.LLMRequestBody{
			Completions: &scheduling.CompletionsRequest{Prompt: prompt},
		},
	}
}

// chatRequest builds a chat-completions LLMRequest with optional multimodal blocks.
func chatRequest(hasImage, hasVideo, hasAudio bool) *scheduling.LLMRequest {
	blocks := []scheduling.ContentBlock{{Type: "text", Text: "describe this"}}
	if hasImage {
		blocks = append(blocks, scheduling.ContentBlock{Type: "image_url", ImageURL: scheduling.ImageBlock{Url: "https://example.com/img.jpg"}})
	}
	if hasVideo {
		blocks = append(blocks, scheduling.ContentBlock{Type: "video_url"})
	}
	if hasAudio {
		blocks = append(blocks, scheduling.ContentBlock{Type: "input_audio"})
	}
	return &scheduling.LLMRequest{
		Body: &scheduling.LLMRequestBody{
			ChatCompletions: &scheduling.ChatCompletionsRequest{
				Messages: []scheduling.Message{{Role: "user", Content: scheduling.Content{Structured: blocks}}},
			},
		},
	}
}

// withPrompt adds a completions body to a chat request so the PD decider can estimate tokens.
func withPrompt(req *scheduling.LLMRequest, prompt string) *scheduling.LLMRequest {
	req.Body.Completions = &scheduling.CompletionsRequest{Prompt: prompt}
	return req
}

// injectPrefixCache sets prefix-cache match info on the decode endpoint for decider evaluation.
func injectPrefixCache(profileResults map[string]*scheduling.ProfileRunResult, cachedTokens, inputTokens int) {
	res, ok := profileResults[defaultDecodeProfile]
	if !ok || res == nil {
		return
	}
	for _, ep := range res.TargetEndpoints {
		ep.Put(approximateprefix.PrefixCacheMatchInfoKey,
			approximateprefix.NewPrefixCacheMatchInfo(cachedTokens, inputTokens, 1))
	}
}

// handleWithDeciders creates a plugin handle pre-loaded with all decider types.
func handleWithDeciders(ctx context.Context) plugin.Handle {
	h := plugin.NewEppHandle(ctx, nil)
	p1, _ := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{NonCachedTokens: 4})
	h.AddPlugin(PrefixBasedPDDeciderPluginType, p1)
	h.AddPlugin(AlwaysDisaggPDDeciderPluginType, newAlwaysDisaggPDDecider())
	h.AddPlugin(AlwaysDisaggEncodePluginType, newAlwaysDisaggEncodeDecider())
	return h
}

type mockEncodeDecider struct {
	allow bool
}

func (m *mockEncodeDecider) TypedName() plugin.TypedName { return plugin.TypedName{} }

func (m *mockEncodeDecider) disaggregate(_ context.Context, _ *scheduling.LLMRequest, _ scheduling.Endpoint) bool {
	return m.allow
}

// ── Helper function tests ────────────────────────────────────────────────────

func TestHasMultimodalContent(t *testing.T) {
	tests := []struct {
		name     string
		req      *scheduling.LLMRequest
		expected bool
	}{
		{"nil request", nil, false},
		{"nil body", &scheduling.LLMRequest{Body: nil}, false},
		{"nil chat completions", &scheduling.LLMRequest{Body: &scheduling.LLMRequestBody{}}, false},
		{"text only", chatRequest(false, false, false), false},
		{"image", chatRequest(true, false, false), true},
		{"video", chatRequest(false, true, false), true},
		{"audio", chatRequest(false, false, true), true},
		{"all types", chatRequest(true, true, true), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, hasMultimodalContent(tt.req))
		})
	}
}

// ── TypedName / WithName ─────────────────────────────────────────────────────

func TestDisaggProfileHandler_TypedName(t *testing.T) {
	h := NewDisaggProfileHandler(defaultDecodeProfile, "", testEncodeProfile, nil, nil)
	assert.Equal(t, DisaggProfileHandlerType, h.TypedName().Type)
	assert.Empty(t, h.TypedName().Name)

	h.WithName("my-handler")
	assert.Equal(t, "my-handler", h.TypedName().Name)
	assert.Equal(t, DisaggProfileHandlerType, h.TypedName().Type)
}

// ── Factory tests ─────────────────────────────────────────────────────────────

func TestDisaggProfileHandlerFactory(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handle := handleWithDeciders(ctx)

	tests := []struct {
		name      string
		params    map[string]any
		expectErr bool
	}{
		// decode-only (no prefill, no encode)
		{"decode only defaults", map[string]any{}, false},

		// P/D style (prefill + decode)
		{"PD style", map[string]any{
			"prefillProfile":           "prefill",
			"prefillDeciderPluginName": AlwaysDisaggPDDeciderPluginType,
		}, false},
		{"PD custom profiles", map[string]any{
			"decodeProfile": "my-decode", "prefillProfile": "my-prefill",
			"prefillDeciderPluginName": PrefixBasedPDDeciderPluginType,
		}, false},

		// E/PD style (encode + decode)
		{"EPD style", map[string]any{
			"encodeProfile": "encode",
		}, false},
		{"EPD with encode decider", map[string]any{
			"encodeProfile":           "encode",
			"encodeDeciderPluginName": AlwaysDisaggEncodePluginType,
		}, false},

		// E/P/D style (all three)
		{"full EPD", map[string]any{
			"prefillProfile":           "prefill",
			"encodeProfile":            "encode",
			"prefillDeciderPluginName": PrefixBasedPDDeciderPluginType,
			"encodeDeciderPluginName":  AlwaysDisaggEncodePluginType,
		}, false},

		// decider errors
		{"prefill without pdDecider is ok (stage inactive)", map[string]any{"prefillProfile": "prefill"}, false},
		{"unknown pdDecider", map[string]any{
			"prefillProfile": "prefill", "prefillDeciderPluginName": "INVALID",
		}, true},
		{"unknown encodeDecider", map[string]any{
			"encodeDeciderPluginName": "INVALID",
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := json.Marshal(tt.params)
			p, err := DisaggProfileHandlerFactory("h", b, handle)
			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, p)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, p)
			}
		})
	}
}

func TestDisaggProfileHandlerFactory_InvalidJSON(t *testing.T) {
	ctx := utils.NewTestContext(t)
	handle := handleWithDeciders(ctx)
	for _, raw := range []string{`{"prefillDeciderPluginName": `} {
		p, err := DisaggProfileHandlerFactory("h", json.RawMessage(raw), handle)
		assert.Error(t, err)
		assert.Nil(t, p)
	}
}

// ── P/D Pick tests ───────────────────────────────────────────────────────────

func TestDisaggProfileHandler_Pick_PD(t *testing.T) {
	ctx := utils.NewTestContext(t)
	req := completionsRequest("hello world hello world hello world") // ~8 tokens

	profiles := map[string]scheduling.SchedulerProfile{
		defaultDecodeProfile: &mockProfile{},
		testPrefillProfile:   &mockProfile{},
	}

	tests := []struct {
		name            string
		nonCachedTokens int
		cachedTokens    int
		profileResults  map[string]*scheduling.ProfileRunResult
		want            []string
	}{
		{
			name:            "decode not run → run decode",
			nonCachedTokens: 4,
			profileResults:  map[string]*scheduling.ProfileRunResult{},
			want:            []string{defaultDecodeProfile},
		},
		{
			name:            "decode failed → done",
			nonCachedTokens: 4,
			profileResults:  map[string]*scheduling.ProfileRunResult{defaultDecodeProfile: nil},
			want:            []string{},
		},
		{
			name:            "all profiles done → done",
			nonCachedTokens: 4,
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				testPrefillProfile:   makeProfileRunResult(testPodPort, "pod2"),
			},
			want: []string{},
		},
		{
			name:            "enough uncached tokens → run prefill",
			nonCachedTokens: 4, cachedTokens: 2,
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			want: []string{testPrefillProfile},
		},
		{
			name:            "short uncached suffix → skip prefill",
			nonCachedTokens: 4, cachedTokens: 5,
			profileResults: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decider, err := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{NonCachedTokens: tt.nonCachedTokens})
			assert.NoError(t, err)

			h := NewDisaggProfileHandler(defaultDecodeProfile, testPrefillProfile, "",
				decider, nil)

			inputTokens := len(req.Body.Completions.Prompt) / AverageCharactersPerToken
			injectPrefixCache(tt.profileResults, tt.cachedTokens, inputTokens)

			got := h.Pick(ctx, nil, req, profiles, tt.profileResults)
			assert.ElementsMatch(t, tt.want, profileNames(got))
		})
	}
}

func TestDisaggProfileHandler_Pick_PD_InputTokenError(t *testing.T) {
	ctx := utils.NewTestContext(t)
	// Request with neither Completions nor ChatCompletions → getUserInputLenInTokens fails.
	req := &scheduling.LLMRequest{
		Body: &scheduling.LLMRequestBody{},
	}
	profiles := map[string]scheduling.SchedulerProfile{
		defaultDecodeProfile: &mockProfile{},
		testPrefillProfile:   &mockProfile{},
	}
	results := map[string]*scheduling.ProfileRunResult{
		defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
	}

	decider, err := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{NonCachedTokens: 1})
	assert.NoError(t, err)
	h := NewDisaggProfileHandler(defaultDecodeProfile, testPrefillProfile, "",
		decider, nil)

	got := h.Pick(ctx, nil, req, profiles, results)
	assert.Empty(t, got, "should return empty map on input token estimation error")
}

func TestDisaggProfileHandler_Pick_PD_Series(t *testing.T) {
	ctx := context.Background()
	short := completionsRequest("hello world, hello world!")
	long := completionsRequest("hello world, hello world! and some additional padding text here")

	profiles := map[string]scheduling.SchedulerProfile{
		defaultDecodeProfile: &mockProfile{},
		testPrefillProfile:   &mockProfile{},
	}
	tests := []struct {
		name            string
		nonCachedTokens int
		steps           []struct {
			req          *scheduling.LLMRequest
			cachedTokens int
			want         []string
		}
	}{
		{
			name:            "same request twice: first disaggregates, second hits cache",
			nonCachedTokens: 2,
			steps: []struct {
				req          *scheduling.LLMRequest
				cachedTokens int
				want         []string
			}{
				{short, 0, []string{testPrefillProfile}},
				{short, len(short.Body.Completions.Prompt) / AverageCharactersPerToken, []string{}},
			},
		},
		{
			name:            "short then long: long triggers disaggregation",
			nonCachedTokens: 2,
			steps: []struct {
				req          *scheduling.LLMRequest
				cachedTokens int
				want         []string
			}{
				{short, 0, []string{testPrefillProfile}},
				{long, len(short.Body.Completions.Prompt) / AverageCharactersPerToken, []string{testPrefillProfile}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decider, err := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{NonCachedTokens: tt.nonCachedTokens})
			assert.NoError(t, err)
			h := NewDisaggProfileHandler(defaultDecodeProfile, testPrefillProfile, "",
				decider, nil)

			for _, step := range tt.steps {
				// Fresh results per step to avoid mutation leaking between iterations.
				results := map[string]*scheduling.ProfileRunResult{
					defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				}
				inputTokens := len(step.req.Body.Completions.Prompt) / AverageCharactersPerToken
				injectPrefixCache(results, step.cachedTokens, inputTokens)
				got := h.Pick(ctx, &scheduling.CycleState{}, step.req, profiles, results)
				assert.ElementsMatch(t, step.want, profileNames(got))
			}
		})
	}
}

// ── P/D ProcessResults tests ─────────────────────────────────────────────────

func TestDisaggProfileHandler_ProcessResults_PD(t *testing.T) {
	tests := []struct {
		name      string
		results   map[string]*scheduling.ProfileRunResult
		expectErr bool
		check     func(*testing.T, *scheduling.SchedulingResult)
	}{
		{
			name:      "decode failed → error",
			results:   map[string]*scheduling.ProfileRunResult{defaultDecodeProfile: nil},
			expectErr: true,
		},
		{
			name: "decode only",
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			check: func(t *testing.T, res *scheduling.SchedulingResult) {
				assert.Equal(t, defaultDecodeProfile, res.PrimaryProfileName)
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.NotContains(t, res.ProfileResults, testPrefillProfile)
				assert.Equal(t, testPodPort, res.ProfileResults[defaultDecodeProfile].TargetEndpoints[0].GetMetadata().Port)
			},
		},
		{
			name: "decode + prefill",
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				testPrefillProfile:   makeProfileRunResult(testPodPort, "pod2"),
			},
			check: func(t *testing.T, res *scheduling.SchedulingResult) {
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.Contains(t, res.ProfileResults, testPrefillProfile)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decider, _ := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{})
			h := NewDisaggProfileHandler(defaultDecodeProfile, testPrefillProfile, "",
				decider, nil)

			req := &scheduling.LLMRequest{Headers: map[string]string{}}
			res, err := h.ProcessResults(context.Background(), nil, req, tt.results)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			tt.check(t, res)
		})
	}
}

func TestDisaggProfileHandler_ProcessResults_NilRequest(t *testing.T) {
	h := NewDisaggProfileHandler(defaultDecodeProfile, testPrefillProfile, "",
		nil, nil)
	results := map[string]*scheduling.ProfileRunResult{
		defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
	}
	_, err := h.ProcessResults(context.Background(), nil, nil, results)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "request is nil")
}

// ── E/PD Pick tests ──────────────────────────────────────────────────────────

func TestDisaggProfileHandler_Pick_EPD(t *testing.T) {
	ctx := utils.NewTestContext(t)

	profiles := map[string]scheduling.SchedulerProfile{
		defaultDecodeProfile: &mockProfile{},
		testEncodeProfile:    &mockProfile{},
	}

	tests := []struct {
		name    string
		req     *scheduling.LLMRequest
		results map[string]*scheduling.ProfileRunResult
		want    []string
	}{
		{
			name:    "decode not run → run decode",
			req:     chatRequest(true, false, false),
			results: map[string]*scheduling.ProfileRunResult{},
			want:    []string{defaultDecodeProfile},
		},
		{
			name:    "decode failed → done",
			req:     chatRequest(true, false, false),
			results: map[string]*scheduling.ProfileRunResult{defaultDecodeProfile: nil},
			want:    []string{},
		},
		{
			name: "no multimodal → skip encode",
			req:  chatRequest(false, false, false),
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			want: []string{},
		},
		{
			name: "image → run encode",
			req:  chatRequest(true, false, false),
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			want: []string{testEncodeProfile},
		},
		{
			name: "video → run encode",
			req:  chatRequest(false, true, false),
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			want: []string{testEncodeProfile},
		},
		{
			name: "audio → run encode",
			req:  chatRequest(false, false, true),
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			want: []string{testEncodeProfile},
		},
		{
			name: "encode failed → done",
			req:  chatRequest(true, false, false),
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				testEncodeProfile:    nil,
			},
			want: []string{},
		},
		{
			name: "all profiles done → done",
			req:  chatRequest(true, false, false),
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				testEncodeProfile:    makeProfileRunResult(testPodPort, "pod2"),
			},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewDisaggProfileHandler(defaultDecodeProfile, "", testEncodeProfile, nil, newAlwaysDisaggEncodeDecider())
			got := h.Pick(ctx, nil, tt.req, profiles, tt.results)
			assert.ElementsMatch(t, tt.want, profileNames(got))
		})
	}
}

func TestDisaggProfileHandler_Pick_EPD_EncodeDecider(t *testing.T) {
	ctx := utils.NewTestContext(t)

	profiles := map[string]scheduling.SchedulerProfile{
		defaultDecodeProfile: &mockProfile{},
		testEncodeProfile:    &mockProfile{},
	}
	results := map[string]*scheduling.ProfileRunResult{
		defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
	}

	tests := []struct {
		name  string
		allow bool
		want  []string
	}{
		{"decider approves → run encode", true, []string{testEncodeProfile}},
		{"decider rejects → skip encode", false, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewDisaggProfileHandler(defaultDecodeProfile, "", testEncodeProfile,
				nil, &mockEncodeDecider{allow: tt.allow})
			got := h.Pick(ctx, nil, chatRequest(true, false, false), profiles, results)
			assert.ElementsMatch(t, tt.want, profileNames(got))
		})
	}
}

// ── E/PD ProcessResults tests ────────────────────────────────────────────────

func TestDisaggProfileHandler_ProcessResults_EPD(t *testing.T) {
	tests := []struct {
		name      string
		results   map[string]*scheduling.ProfileRunResult
		expectErr bool
		check     func(*testing.T, *scheduling.SchedulingResult)
	}{
		{
			name:      "decode failed → error",
			results:   map[string]*scheduling.ProfileRunResult{defaultDecodeProfile: nil},
			expectErr: true,
		},
		{
			name: "decode only",
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			check: func(t *testing.T, res *scheduling.SchedulingResult) {
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.NotContains(t, res.ProfileResults, testEncodeProfile)
			},
		},
		{
			name: "decode + encode",
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				testEncodeProfile:    makeProfileRunResult(testPodPort, "pod2"),
			},
			check: func(t *testing.T, res *scheduling.SchedulingResult) {
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.Contains(t, res.ProfileResults, testEncodeProfile)
			},
		},
		{
			name: "encode nil (rejected) → omitted",
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				testEncodeProfile:    nil,
			},
			check: func(t *testing.T, res *scheduling.SchedulingResult) {
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.NotContains(t, res.ProfileResults, testEncodeProfile)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewDisaggProfileHandler(defaultDecodeProfile, "", testEncodeProfile, nil, newAlwaysDisaggEncodeDecider())
			res, err := h.ProcessResults(context.Background(), nil, &scheduling.LLMRequest{}, tt.results)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, defaultDecodeProfile, res.PrimaryProfileName)
			tt.check(t, res)
		})
	}
}

// ── E/P/D Pick tests ─────────────────────────────────────────────────────────

func TestDisaggProfileHandler_Pick_EPD_Full(t *testing.T) {
	ctx := utils.NewTestContext(t)

	profiles := map[string]scheduling.SchedulerProfile{
		defaultDecodeProfile: &mockProfile{},
		testPrefillProfile:   &mockProfile{},
		testEncodeProfile:    &mockProfile{},
	}

	longPrompt := "hello world hello world hello world"
	multimodalLong := withPrompt(chatRequest(true, false, false), longPrompt)

	tests := []struct {
		name            string
		req             *scheduling.LLMRequest
		nonCachedTokens int
		cachedTokens    int
		results         map[string]*scheduling.ProfileRunResult
		want            []string
	}{
		{
			name:            "decode not run → run decode",
			req:             multimodalLong,
			nonCachedTokens: 1,
			results:         map[string]*scheduling.ProfileRunResult{},
			want:            []string{defaultDecodeProfile},
		},
		{
			name:            "decode failed → done",
			req:             multimodalLong,
			nonCachedTokens: 1,
			results:         map[string]*scheduling.ProfileRunResult{defaultDecodeProfile: nil},
			want:            []string{},
		},
		{
			name:            "multimodal → run encode next",
			req:             multimodalLong,
			nonCachedTokens: 1,
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			want: []string{testEncodeProfile},
		},
		{
			name:            "text-only, high uncached tokens → skip encode, run prefill",
			req:             completionsRequest(longPrompt),
			nonCachedTokens: 1, cachedTokens: 0,
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			want: []string{testPrefillProfile},
		},
		{
			name:            "text-only, prefill not needed → done",
			req:             completionsRequest(longPrompt),
			nonCachedTokens: 100,
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			want: []string{},
		},
		{
			name:            "encode failed → fall through, run prefill",
			req:             multimodalLong,
			nonCachedTokens: 1, cachedTokens: 0,
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				testEncodeProfile:    nil,
			},
			want: []string{testPrefillProfile},
		},
		{
			name:            "encode done → run prefill",
			req:             multimodalLong,
			nonCachedTokens: 1, cachedTokens: 0,
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				testEncodeProfile:    makeProfileRunResult(testPodPort, "pod2"),
			},
			want: []string{testPrefillProfile},
		},
		{
			name:            "all three done → done",
			req:             multimodalLong,
			nonCachedTokens: 1,
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				testEncodeProfile:    makeProfileRunResult(testPodPort, "pod2"),
				testPrefillProfile:   makeProfileRunResult(testPodPort, "pod3"),
			},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decider, err := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{NonCachedTokens: tt.nonCachedTokens})
			assert.NoError(t, err)

			h := NewDisaggProfileHandler(
				defaultDecodeProfile, testPrefillProfile, testEncodeProfile,
				decider, newAlwaysDisaggEncodeDecider(),
			)

			inputTokens := 0
			if tt.req.Body.Completions != nil {
				inputTokens = len(tt.req.Body.Completions.Prompt) / AverageCharactersPerToken
			} else if tt.req.Body.ChatCompletions != nil {
				b, _ := json.Marshal(tt.req.Body.ChatCompletions.Messages)
				inputTokens = len(b) / AverageCharactersPerToken
			}
			injectPrefixCache(tt.results, tt.cachedTokens, inputTokens)

			got := h.Pick(ctx, nil, tt.req, profiles, tt.results)
			assert.ElementsMatch(t, tt.want, profileNames(got))
		})
	}
}

func TestDisaggProfileHandler_Pick_EPD_Full_EncodeDecider(t *testing.T) {
	ctx := utils.NewTestContext(t)

	longPrompt := "hello world hello world hello world"
	multimodalLong := withPrompt(chatRequest(true, false, false), longPrompt)

	profiles := map[string]scheduling.SchedulerProfile{
		defaultDecodeProfile: &mockProfile{},
		testPrefillProfile:   &mockProfile{},
		testEncodeProfile:    &mockProfile{},
	}

	tests := []struct {
		name     string
		allow    bool
		wantNext []string // expected next profile from Pick (encode not yet run)
	}{
		{"decider approves → run encode next", true, []string{testEncodeProfile}},
		{"decider rejects → skip encode, run prefill next", false, []string{testPrefillProfile}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decider, err := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{NonCachedTokens: 1})
			assert.NoError(t, err)

			h := NewDisaggProfileHandler(
				defaultDecodeProfile, testPrefillProfile, testEncodeProfile,
				decider, &mockEncodeDecider{allow: tt.allow},
			)

			results := map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			}

			inputTokens := len(longPrompt) / AverageCharactersPerToken
			injectPrefixCache(results, 0, inputTokens)

			got := h.Pick(ctx, nil, multimodalLong, profiles, results)
			assert.ElementsMatch(t, tt.wantNext, profileNames(got))
		})
	}
}

// ── E/P/D ProcessResults tests ───────────────────────────────────────────────

func TestDisaggProfileHandler_ProcessResults_EPD_Full(t *testing.T) {
	tests := []struct {
		name      string
		results   map[string]*scheduling.ProfileRunResult
		expectErr bool
		check     func(*testing.T, *scheduling.SchedulingResult)
	}{
		{
			name:      "decode failed → error",
			results:   map[string]*scheduling.ProfileRunResult{defaultDecodeProfile: nil},
			expectErr: true,
		},
		{
			name: "decode only",
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
			},
			check: func(t *testing.T, res *scheduling.SchedulingResult) {
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.NotContains(t, res.ProfileResults, testEncodeProfile)
				assert.NotContains(t, res.ProfileResults, testPrefillProfile)
			},
		},
		{
			name: "all three stages",
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				testEncodeProfile:    makeProfileRunResult(testPodPort, "pod2"),
				testPrefillProfile:   makeProfileRunResult(testPodPort, "pod3"),
			},
			check: func(t *testing.T, res *scheduling.SchedulingResult) {
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.Contains(t, res.ProfileResults, testEncodeProfile)
				assert.Contains(t, res.ProfileResults, testPrefillProfile)
			},
		},
		{
			name: "encode nil → omitted",
			results: map[string]*scheduling.ProfileRunResult{
				defaultDecodeProfile: makeProfileRunResult(testPodPort, "pod1"),
				testEncodeProfile:    nil,
				testPrefillProfile:   makeProfileRunResult(testPodPort, "pod3"),
			},
			check: func(t *testing.T, res *scheduling.SchedulingResult) {
				assert.Contains(t, res.ProfileResults, defaultDecodeProfile)
				assert.NotContains(t, res.ProfileResults, testEncodeProfile)
				assert.Contains(t, res.ProfileResults, testPrefillProfile)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decider, _ := NewPrefixBasedPDDecider(PrefixBasedPDDeciderConfig{})
			h := NewDisaggProfileHandler(
				defaultDecodeProfile, testPrefillProfile, testEncodeProfile,
				decider, newAlwaysDisaggEncodeDecider(),
			)
			res, err := h.ProcessResults(context.Background(), nil, &scheduling.LLMRequest{}, tt.results)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, defaultDecodeProfile, res.PrimaryProfileName)
			tt.check(t, res)
		})
	}
}
