package http

import (
	"context"
	"errors"
	"io"
	"net/url"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/metrics"
)

// fakeClient is an in-memory Client. It produces a fixed parsed value (or
// error) without touching the network so tests can drive Dispatch
// deterministically.
type fakeClient struct {
	value any
	err   error
}

func (c *fakeClient) Get(_ context.Context, _ *url.URL, _ Addressable,
	parser func(io.Reader) (any, error)) (any, error) {
	if c.err != nil {
		return nil, c.err
	}
	// Bypass the parser when a canned value is set; tests want to assert on
	// the extractor's view of Data, not on parsing.
	if c.value != nil {
		return c.value, nil
	}
	// No value, no error: simulate parser-returns-nil for pointer T paths.
	return parser(nil)
}

// stubExtractor implements Extractor[PollInput[int]] with configurable
// behavior: counts calls, optionally blocks, optionally returns an error.
type stubExtractor struct {
	extType string
	err     error
	block   time.Duration

	mu        sync.Mutex
	callCount int
	lastCtx   context.Context
}

func newStubExtractor(extType string) *stubExtractor {
	return &stubExtractor{extType: extType}
}

func (e *stubExtractor) withError(err error) *stubExtractor { e.err = err; return e }
func (e *stubExtractor) withBlock(d time.Duration) *stubExtractor {
	e.block = d
	return e
}

func (e *stubExtractor) TypedName() fwkplugin.TypedName {
	return fwkplugin.TypedName{Type: e.extType, Name: e.extType}
}

func (e *stubExtractor) Extract(ctx context.Context, _ fwkdl.PollInput[int]) error {
	e.mu.Lock()
	e.callCount++
	e.lastCtx = ctx
	e.mu.Unlock()
	if e.block > 0 {
		// Sleep is bounded by Extract's own per-step timeout; we want the
		// test to observe the ctx.Done() path, not the wall clock.
		select {
		case <-time.After(e.block):
		case <-ctx.Done():
		}
	}
	return e.err
}

func (e *stubExtractor) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.callCount
}

// wrongTypedExtractor satisfies plugin.Plugin but NOT Extractor[PollInput[int]].
// Used to drive AppendExtractor's type-assertion error path.
type wrongTypedExtractor struct{}

func (wrongTypedExtractor) TypedName() fwkplugin.TypedName {
	return fwkplugin.TypedName{Type: "wrong", Name: "wrong"}
}

func newDS(t *testing.T, srcType string, fc *fakeClient) *HTTPDataSource[int] {
	t.Helper()
	return &HTTPDataSource[int]{
		typedName: fwkplugin.TypedName{Type: srcType, Name: srcType},
		scheme:    "http",
		path:      "/test",
		client:    fc,
		parser:    func(_ io.Reader) (int, error) { return 0, nil },
	}
}

func newTestEndpoint() fwkdl.Endpoint {
	return fwkdl.NewEndpoint(&fwkdl.EndpointMetadata{MetricsHost: "10.0.0.1:8000"}, fwkdl.NewMetrics())
}

func extDelta(t *testing.T, srcType, extType string, before float64) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.DataLayerExtractErrorsTotal.WithLabelValues(srcType, extType)) - before
}

func extBefore(t *testing.T, srcType, extType string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.DataLayerExtractErrorsTotal.WithLabelValues(srcType, extType))
}

// --- Return contract -------------------------------------------------------

func TestDispatch_PollFailure_ReturnsError(t *testing.T) {
	wantErr := errors.New("upstream offline")
	s := newDS(t, "src-poll-fail", &fakeClient{err: wantErr})
	require.NoError(t, s.AppendExtractor(newStubExtractor("ext")))

	err := s.Dispatch(context.Background(), newTestEndpoint())
	require.ErrorIs(t, err, wantErr, "poll-level failure must surface as the returned error")
}

func TestDispatch_ExtractorFailure_ReturnsNil(t *testing.T) {
	const src, ext = "src-ext-fail", "ext-fail"
	s := newDS(t, src, &fakeClient{value: 42})
	require.NoError(t, s.AppendExtractor(newStubExtractor(ext).withError(errors.New("bad data"))))

	before := extBefore(t, src, ext)
	err := s.Dispatch(context.Background(), newTestEndpoint())
	require.NoError(t, err, "extractor failure must not surface as a returned error")
	assert.Equal(t, float64(1), extDelta(t, src, ext, before),
		"extractor failure must record one DataLayerExtractErrorsTotal increment")
}

// --- Metric label correctness ---------------------------------------------

func TestDispatch_ExtractorFailure_LabelsBothSrcAndExt(t *testing.T) {
	const src, ext = "src-label", "ext-label"
	s := newDS(t, src, &fakeClient{value: 1})
	require.NoError(t, s.AppendExtractor(newStubExtractor(ext).withError(errors.New("x"))))

	before := extBefore(t, src, ext)
	require.NoError(t, s.Dispatch(context.Background(), newTestEndpoint()))
	assert.Equal(t, float64(1), extDelta(t, src, ext, before),
		"counter must be incremented under (src=%q, ext=%q); any other label tuple is a regression",
		src, ext)
	// Catch the (src, src) / (ext, ext) regression: nothing should land at swapped labels.
	swappedSrc := testutil.ToFloat64(metrics.DataLayerExtractErrorsTotal.WithLabelValues(src, src))
	swappedExt := testutil.ToFloat64(metrics.DataLayerExtractErrorsTotal.WithLabelValues(ext, ext))
	assert.Zero(t, swappedSrc, "counter must not appear under (src, src)")
	assert.Zero(t, swappedExt, "counter must not appear under (ext, ext)")
}

func TestDispatch_MultipleExtractorFailures_BothRecorded(t *testing.T) {
	const src, extA, extB = "src-multi", "ext-a", "ext-b"
	s := newDS(t, src, &fakeClient{value: 1})
	require.NoError(t, s.AppendExtractor(newStubExtractor(extA).withError(errors.New("a"))))
	require.NoError(t, s.AppendExtractor(newStubExtractor(extB).withError(errors.New("b"))))

	beforeA := extBefore(t, src, extA)
	beforeB := extBefore(t, src, extB)
	require.NoError(t, s.Dispatch(context.Background(), newTestEndpoint()))

	assert.Equal(t, float64(1), extDelta(t, src, extA, beforeA),
		"each failing extractor must record its own increment")
	assert.Equal(t, float64(1), extDelta(t, src, extB, beforeB))
}

// --- Per-step timeout isolation -------------------------------------------

// A slow extractor must not starve siblings: the dispatcher bounds each
// Extract under its own defaultStepTimeout, so a stuck extractor times out
// and the next extractor gets the full budget.
func TestDispatch_SlowExtractor_DoesNotStarveNext(t *testing.T) {
	slow := newStubExtractor("ext-slow").
		withBlock(2 * defaultStepTimeout). // longer than the per-step bound
		withError(context.DeadlineExceeded)
	fast := newStubExtractor("ext-fast")

	s := newDS(t, "src-isolation", &fakeClient{value: 1})
	require.NoError(t, s.AppendExtractor(slow))
	require.NoError(t, s.AppendExtractor(fast))

	start := time.Now()
	require.NoError(t, s.Dispatch(context.Background(), newTestEndpoint()))
	elapsed := time.Since(start)

	assert.Equal(t, 1, fast.calls(), "fast extractor must still run after slow extractor timed out")
	assert.Less(t, elapsed, 2*defaultStepTimeout+500*time.Millisecond,
		"total Dispatch time must be ~one step-timeout (slow) + epsilon (fast); got %s", elapsed)
}

// --- Mid-Dispatch context cancellation -------------------------------------

func TestDispatch_ParentCtxCancelled_StopsRemainingExtractors(t *testing.T) {
	first := newStubExtractor("ext-first")
	second := newStubExtractor("ext-second")
	third := newStubExtractor("ext-third")

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel the parent the moment the first extractor runs.
	firstWithSideEffect := &cancelOnExtract{stub: first, cancel: cancel}

	s := newDS(t, "src-cancel", &fakeClient{value: 1})
	require.NoError(t, s.AppendExtractor(firstWithSideEffect))
	require.NoError(t, s.AppendExtractor(second))
	require.NoError(t, s.AppendExtractor(third))

	require.NoError(t, s.Dispatch(ctx, newTestEndpoint()))

	assert.Equal(t, 1, firstWithSideEffect.stub.calls(), "first extractor runs before cancel observed")
	assert.Equal(t, 0, second.calls(), "second extractor must be skipped once parent ctx is cancelled")
	assert.Equal(t, 0, third.calls(), "third extractor must be skipped once parent ctx is cancelled")
}

type cancelOnExtract struct {
	stub   *stubExtractor
	cancel context.CancelFunc
}

func (c *cancelOnExtract) TypedName() fwkplugin.TypedName { return c.stub.TypedName() }
func (c *cancelOnExtract) Extract(ctx context.Context, in fwkdl.PollInput[int]) error {
	err := c.stub.Extract(ctx, in)
	c.cancel()
	return err
}

// --- AppendExtractor contract ---------------------------------------------

func TestAppendExtractor_WrongType_ReturnsError(t *testing.T) {
	s := newDS(t, "src", &fakeClient{value: 1})
	err := s.AppendExtractor(wrongTypedExtractor{})
	require.Error(t, err, "appending a non-Extractor[PollInput[T]] plugin must return an error")
	assert.Contains(t, err.Error(), "expected Extractor[PollInput",
		"error must name the expected typed extractor signature")
}

func TestAppendExtractor_DuplicateType_SilentlyDropped(t *testing.T) {
	s := newDS(t, "src-dup", &fakeClient{value: 1})
	first := newStubExtractor("ext-dup")
	second := newStubExtractor("ext-dup") // same Type; should be deduped

	require.NoError(t, s.AppendExtractor(first))
	require.NoError(t, s.AppendExtractor(second), "duplicate is per-contract a silent no-op, not an error")

	require.NoError(t, s.Dispatch(context.Background(), newTestEndpoint()))
	assert.Equal(t, 1, first.calls(), "only the first-bound extractor should be invoked")
	assert.Equal(t, 0, second.calls(), "the deduplicated extractor must not be invoked")
}

func TestAppendExtractor_PreservesInsertionOrder(t *testing.T) {
	s := newDS(t, "src-order", &fakeClient{value: 1})
	calls := make([]string, 0, 3)
	var mu sync.Mutex
	for _, name := range []string{"a", "b", "c"} {
		ext := newStubExtractor(name)
		// Wrap to record order.
		wrapped := &orderedStub{stub: ext, name: name, log: &calls, mu: &mu}
		require.NoError(t, s.AppendExtractor(wrapped))
	}
	require.NoError(t, s.Dispatch(context.Background(), newTestEndpoint()))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"a", "b", "c"}, calls,
		"contract: extractors run in AppendExtractor-insertion order")
}

type orderedStub struct {
	stub *stubExtractor
	name string
	log  *[]string
	mu   *sync.Mutex
}

func (o *orderedStub) TypedName() fwkplugin.TypedName { return o.stub.TypedName() }
func (o *orderedStub) Extract(ctx context.Context, in fwkdl.PollInput[int]) error {
	o.mu.Lock()
	*o.log = append(*o.log, o.name)
	o.mu.Unlock()
	return o.stub.Extract(ctx, in)
}

// AppendExtractor under contention: many goroutines racing the same
// TypedName().Type must yield exactly one bound extractor (the dedup is
// guarded by s.mu, so the race is on the dedup check, not the slice).
func TestAppendExtractor_ConcurrentDedup_ExactlyOneBound(t *testing.T) {
	s := newDS(t, "src-race", &fakeClient{value: 1})

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	var started atomic.Int32
	gate := make(chan struct{})
	for range goroutines {
		go func() {
			defer wg.Done()
			started.Add(1)
			<-gate
			_ = s.AppendExtractor(newStubExtractor("same-type"))
		}()
	}
	// Wait until every goroutine is parked, then release.
	for started.Load() < goroutines {
		runtime.Gosched()
	}
	close(gate)
	wg.Wait()

	s.mu.RLock()
	defer s.mu.RUnlock()
	assert.Len(t, s.exts, 1, "concurrent dedup must yield exactly one bound extractor")
}
