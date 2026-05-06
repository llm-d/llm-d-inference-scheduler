package datalayer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	srcmocks "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/source/mocks"
)

func TestSourceManager_Register(t *testing.T) {
	src1 := srcmocks.NewDataSource(fwkplugin.TypedName{Type: "polling", Name: "src1"})
	src2 := srcmocks.NewDataSource(fwkplugin.TypedName{Type: "polling", Name: "src2"})
	srcDup := srcmocks.NewDataSource(fwkplugin.TypedName{Type: "polling", Name: "src1"})

	tests := []struct {
		name      string
		ops       func(m *sourceManager[fwkdl.PollingDataSource, fwkdl.PollingExtractor]) error
		wantCount int
		wantErr   bool
		wantInErr string
	}{
		{
			name: "register one",
			ops: func(m *sourceManager[fwkdl.PollingDataSource, fwkdl.PollingExtractor]) error {
				return m.Register(src1, nil)
			},
			wantCount: 1,
		},
		{
			name: "register two distinct",
			ops: func(m *sourceManager[fwkdl.PollingDataSource, fwkdl.PollingExtractor]) error {
				if err := m.Register(src1, nil); err != nil {
					return err
				}
				return m.Register(src2, nil)
			},
			wantCount: 2,
		},
		{
			name: "duplicate name errors",
			ops: func(m *sourceManager[fwkdl.PollingDataSource, fwkdl.PollingExtractor]) error {
				if err := m.Register(src1, nil); err != nil {
					return err
				}
				return m.Register(srcDup, nil)
			},
			wantCount: 1,
			wantErr:   true,
			wantInErr: "duplicate polling source name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newSourceManager[fwkdl.PollingDataSource, fwkdl.PollingExtractor]("polling")
			err := tc.ops(m)
			if tc.wantErr {
				require.Error(t, err)
				if tc.wantInErr != "" {
					require.Contains(t, err.Error(), tc.wantInErr)
				}
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantCount, m.Count())
		})
	}
}

func TestSourceManager_AppendExtractorDedupesByType(t *testing.T) {
	src := srcmocks.NewDataSource(fwkplugin.TypedName{Type: "polling", Name: "src1"})
	m := newSourceManager[fwkdl.PollingDataSource, fwkdl.PollingExtractor]("polling")
	require.NoError(t, m.Register(src, nil))

	// Two mock extractors share the same type ("mock-extractor"); one is a
	// different type. Manager should keep one of the same-type pair plus the
	// distinct one.
	first := newPollingExt(t, fwkplugin.TypedName{Type: "mock-extractor", Name: "first"})
	second := newPollingExt(t, fwkplugin.TypedName{Type: "mock-extractor", Name: "second"})
	other := newPollingExt(t, fwkplugin.TypedName{Type: "different-type", Name: "other"})

	m.AppendExtractor("src1", first)
	m.AppendExtractor("src1", second) // duplicate type — should be skipped
	m.AppendExtractor("src1", other)

	got := m.ExtractorsFor("src1")
	require.Len(t, got, 2, "duplicate type should be deduped")
	require.Equal(t, "first", got[0].TypedName().Name)
	require.Equal(t, "other", got[1].TypedName().Name)
}

func TestSourceManager_AccessorsReturnCopies(t *testing.T) {
	src := srcmocks.NewDataSource(fwkplugin.TypedName{Type: "polling", Name: "src1"})
	m := newSourceManager[fwkdl.PollingDataSource, fwkdl.PollingExtractor]("polling")
	ext := newPollingExt(t, fwkplugin.TypedName{Type: "mock-extractor", Name: "ext1"})
	require.NoError(t, m.Register(src, []fwkdl.PollingExtractor{ext}))

	t.Run("Sources copy", func(t *testing.T) {
		snap := m.Sources()
		delete(snap, "src1")
		require.Equal(t, 1, m.Count(), "deleting from snapshot should not affect manager")
	})

	t.Run("ExtractorsFor copy", func(t *testing.T) {
		snap := m.ExtractorsFor("src1")
		require.Len(t, snap, 1)
		snap[0] = nil // mutate caller copy
		got := m.ExtractorsFor("src1")
		require.NotNil(t, got[0], "manager's slice must be untouched by caller mutation")
	})

	t.Run("Extractors slice values are fresh", func(t *testing.T) {
		snap := m.Extractors()
		require.Len(t, snap["src1"], 1)
		snap["src1"][0] = nil
		got := m.ExtractorsFor("src1")
		require.NotNil(t, got[0], "manager's slice must be untouched by caller mutation")
	})
}

func TestSourceManager_FindByTypeStableSorted(t *testing.T) {
	m := newSourceManager[fwkdl.PollingDataSource, fwkdl.PollingExtractor]("polling")
	// Insert in order designed to produce non-deterministic Go map iteration
	// across runs; sorted-by-name first-match must always return "aaa".
	for _, name := range []string{"zzz", "mmm", "aaa", "bbb"} {
		s := srcmocks.NewDataSource(fwkplugin.TypedName{Type: "polling", Name: name})
		require.NoError(t, m.Register(s, nil))
	}

	// Run several times to catch a non-stable implementation.
	for i := 0; i < 20; i++ {
		name, _, ok := m.FindByType("polling", nil)
		require.True(t, ok)
		require.Equal(t, "aaa", name, "first-match must be sort-by-name stable")
	}
}

func TestSourceManager_FindByTypeWithFilter(t *testing.T) {
	m := newSourceManager[fwkdl.PollingDataSource, fwkdl.PollingExtractor]("polling")
	a := srcmocks.NewDataSource(fwkplugin.TypedName{Type: "polling", Name: "a"})
	b := srcmocks.NewDataSource(fwkplugin.TypedName{Type: "polling", Name: "b"})
	require.NoError(t, m.Register(a, nil))
	require.NoError(t, m.Register(b, nil))

	tests := []struct {
		name      string
		match     func(fwkdl.PollingDataSource) bool
		wantFound bool
		wantName  string
	}{
		{
			name:      "no filter matches first sorted",
			match:     nil,
			wantFound: true,
			wantName:  "a",
		},
		{
			name:      "filter selects b",
			match:     func(s fwkdl.PollingDataSource) bool { return s.TypedName().Name == "b" },
			wantFound: true,
			wantName:  "b",
		},
		{
			name:      "filter rejects all",
			match:     func(s fwkdl.PollingDataSource) bool { return false },
			wantFound: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, _, ok := m.FindByType("polling", tc.match)
			require.Equal(t, tc.wantFound, ok)
			if tc.wantFound {
				require.Equal(t, tc.wantName, name)
			}
		})
	}
}

func TestNotificationManager_Register(t *testing.T) {
	podGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
	svcGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}

	tests := []struct {
		name      string
		ops       func(m *notificationManager) error
		wantCount int
		wantErr   bool
		wantInErr string
	}{
		{
			name: "register two distinct",
			ops: func(m *notificationManager) error {
				if err := m.Register(srcmocks.NewNotificationSource("notif", "a", podGVK), nil); err != nil {
					return err
				}
				return m.Register(srcmocks.NewNotificationSource("notif", "b", svcGVK), nil)
			},
			wantCount: 2,
		},
		{
			name: "duplicate GVK errors",
			ops: func(m *notificationManager) error {
				if err := m.Register(srcmocks.NewNotificationSource("notif", "a", podGVK), nil); err != nil {
					return err
				}
				return m.Register(srcmocks.NewNotificationSource("notif", "b", podGVK), nil)
			},
			wantCount: 1,
			wantErr:   true,
			wantInErr: "duplicate notification source GVK",
		},
		{
			name: "duplicate name errors",
			ops: func(m *notificationManager) error {
				if err := m.Register(srcmocks.NewNotificationSource("notif", "same", podGVK), nil); err != nil {
					return err
				}
				return m.Register(srcmocks.NewNotificationSource("notif", "same", svcGVK), nil)
			},
			wantCount: 1,
			wantErr:   true,
			wantInErr: "duplicate notification source name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newNotificationManager()
			err := tc.ops(m)
			if tc.wantErr {
				require.Error(t, err)
				if tc.wantInErr != "" {
					require.Contains(t, err.Error(), tc.wantInErr)
				}
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantCount, m.Count())
		})
	}
}

// newPollingExt builds a mock satisfying fwkdl.PollingExtractor with the given
// TypedName (so dedup-by-type cases can vary Type and Name independently).
func newPollingExt(t *testing.T, tn fwkplugin.TypedName) fwkdl.PollingExtractor {
	t.Helper()
	return &fakePollingExtractor{t: tn}
}

type fakePollingExtractor struct {
	t fwkplugin.TypedName
}

func (f *fakePollingExtractor) TypedName() fwkplugin.TypedName { return f.t }

func (f *fakePollingExtractor) Extract(_ context.Context, _ fwkdl.PollingInput) error {
	return nil
}
