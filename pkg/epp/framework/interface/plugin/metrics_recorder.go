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

package plugin

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
)

// MetricsRecorder provides plugins access to metric registration and recording
// without depending on the concrete EPP metrics package.
type MetricsRecorder interface {
	prometheus.Registerer

	RecordRequestTTFT(ctx context.Context, modelName, targetModelName string, ttft float64) bool
	RecordRequestPredictedTTFT(ctx context.Context, modelName, targetModelName string, predictedTTFT float64) bool
	RecordRequestTTFTWithSLO(ctx context.Context, modelName, targetModelName string, ttft float64, sloThreshold float64) bool
	RecordRequestTTFTPredictionDuration(ctx context.Context, modelName, targetModelName string, duration float64) bool
	RecordRequestTPOT(ctx context.Context, modelName, targetModelName string, tpot float64) bool
	RecordRequestPredictedTPOT(ctx context.Context, modelName, targetModelName string, predictedTPOT float64) bool
	RecordRequestTPOTWithSLO(ctx context.Context, modelName, targetModelName string, tpot float64, sloThreshold float64) bool
	RecordRequestTPOTPredictionDuration(ctx context.Context, modelName, targetModelName string, duration float64) bool
	RecordPrefixCacheSize(size int64)
	RecordPrefixCacheMatch(matchedLength, totalLength int)
}

// MetricsRecorderFromHandle returns the MetricsRecorder configured on h, or a
// NoopMetricsRecorder if h is nil or has no recorder configured. Plugin factories
// should use this to defend against nil or misconfigured handles.
func MetricsRecorderFromHandle(h Handle) MetricsRecorder {
	if h != nil {
		if r := h.Metrics(); r != nil {
			return r
		}
	}
	return NewNoopMetricsRecorder()
}

// NoopMetricsRecorder is a no-op implementation for tests and fallback handles.
type NoopMetricsRecorder struct{}

var _ MetricsRecorder = &NoopMetricsRecorder{}

func NewNoopMetricsRecorder() *NoopMetricsRecorder {
	return &NoopMetricsRecorder{}
}

func (n *NoopMetricsRecorder) Register(prometheus.Collector) error {
	return nil
}

func (n *NoopMetricsRecorder) MustRegister(...prometheus.Collector) {}

func (n *NoopMetricsRecorder) Unregister(prometheus.Collector) bool {
	return false
}

func (n *NoopMetricsRecorder) RecordRequestTTFT(context.Context, string, string, float64) bool {
	return true
}

func (n *NoopMetricsRecorder) RecordRequestPredictedTTFT(context.Context, string, string, float64) bool {
	return true
}

func (n *NoopMetricsRecorder) RecordRequestTTFTWithSLO(context.Context, string, string, float64, float64) bool {
	return true
}

func (n *NoopMetricsRecorder) RecordRequestTTFTPredictionDuration(context.Context, string, string, float64) bool {
	return true
}

func (n *NoopMetricsRecorder) RecordRequestTPOT(context.Context, string, string, float64) bool {
	return true
}

func (n *NoopMetricsRecorder) RecordRequestPredictedTPOT(context.Context, string, string, float64) bool {
	return true
}

func (n *NoopMetricsRecorder) RecordRequestTPOTWithSLO(context.Context, string, string, float64, float64) bool {
	return true
}

func (n *NoopMetricsRecorder) RecordRequestTPOTPredictionDuration(context.Context, string, string, float64) bool {
	return true
}

func (n *NoopMetricsRecorder) RecordPrefixCacheSize(int64) {}

func (n *NoopMetricsRecorder) RecordPrefixCacheMatch(int, int) {}
