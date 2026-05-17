/*
Copyright 2026 The Kubernetes Authors.

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

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

func TestRecorderImplementsInterface(t *testing.T) {
	var _ fwkplugin.MetricsRecorder = &Recorder{}
}

func TestRecorderRegistersCustomCollector(t *testing.T) {
	recorder := NewRecorder()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "test_plugin_specific_metric",
		Help: "Test plugin metric.",
	})

	require.NoError(t, recorder.Register(gauge))
	defer recorder.Unregister(gauge)

	gauge.Set(7)
	families, err := ctrlmetrics.Registry.Gather()
	require.NoError(t, err)

	for _, family := range families {
		if family.GetName() == "test_plugin_specific_metric" {
			require.Len(t, family.GetMetric(), 1)
			require.Equal(t, float64(7), family.GetMetric()[0].GetGauge().GetValue())
			return
		}
	}

	t.Fatal("custom collector was not registered")
}

func TestRecorderDelegates(t *testing.T) {
	Reset()
	recorder := NewRecorder()
	ctx := t.Context()

	require.True(t, recorder.RecordRequestTTFT(ctx, "model", "target", 0.5))
	require.True(t, recorder.RecordRequestPredictedTTFT(ctx, "model", "target", 0.5))
	require.True(t, recorder.RecordRequestTTFTWithSLO(ctx, "model", "target", 0.5, 1.0))
	require.True(t, recorder.RecordRequestTTFTPredictionDuration(ctx, "model", "target", 0.1))
	require.True(t, recorder.RecordRequestTPOT(ctx, "model", "target", 0.02))
	require.True(t, recorder.RecordRequestPredictedTPOT(ctx, "model", "target", 0.02))
	require.True(t, recorder.RecordRequestTPOTWithSLO(ctx, "model", "target", 0.02, 0.05))
	require.True(t, recorder.RecordRequestTPOTPredictionDuration(ctx, "model", "target", 0.1))

	recorder.RecordPrefixCacheSize(100)
	recorder.RecordPrefixCacheMatch(10, 20)
}
