/*
Copyright 2025 The llm-d Authors.

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

package predictors

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	latencypredictor "sigs.k8s.io/gateway-api-inference-extension/sidecars/latencypredictorasync"
)

// PDPredictorSet holds two separate predictor clients for PD disaggregated scheduling:
// - PrefillPredictor: Predicts prefill processing time (TTFT only)
// - DecodePredictor: Predicts decode queue wait + per-token latency (TTFT and TPOT)
type PDPredictorSet struct {
	PrefillPredictor *latencypredictor.Predictor
	DecodePredictor  *latencypredictor.Predictor
	logger           logr.Logger
}

// NewPDPredictorSet creates two separate predictor client instances,
// each configured via environment variables to point to different predictor services.
func NewPDPredictorSet(logger logr.Logger) (*PDPredictorSet, error) {
	// Prefill Predictor Configuration (TTFT only)
	prefillConfig := &latencypredictor.Config{
		TrainingURL:            getEnvOrDefault("PREFILL_TRAINING_URL", ""),
		PredictionURLs:         getPredictionURLs("PREFILL_PREDICTION_URL"),
		MaxSampleSize:          1000,
		FlushInterval:          1 * time.Second,
		UseNativeXGBoost:       false,
		HTTPTimeout:            10 * time.Second,
		MetricsRefreshInterval: 30 * time.Second,
		MaxBulkSize:            100,
	}

	if prefillConfig.TrainingURL == "" || len(prefillConfig.PredictionURLs) == 0 {
		return nil, fmt.Errorf("PREFILL_TRAINING_URL and PREFILL_PREDICTION_URL must be set")
	}

	prefillPredictor := latencypredictor.New(prefillConfig, logger.WithName("prefill-predictor"))

	// Decode Predictor Configuration (TTFT and TPOT)
	decodeConfig := &latencypredictor.Config{
		TrainingURL:            getEnvOrDefault("DECODE_TRAINING_URL", ""),
		PredictionURLs:         getPredictionURLs("DECODE_PREDICTION_URL"),
		MaxSampleSize:          1000,
		FlushInterval:          1 * time.Second,
		UseNativeXGBoost:       false,
		HTTPTimeout:            10 * time.Second,
		MetricsRefreshInterval: 30 * time.Second,
		MaxBulkSize:            100,
	}

	if decodeConfig.TrainingURL == "" || len(decodeConfig.PredictionURLs) == 0 {
		return nil, fmt.Errorf("DECODE_TRAINING_URL and DECODE_PREDICTION_URL must be set")
	}

	decodePredictor := latencypredictor.New(decodeConfig, logger.WithName("decode-predictor"))

	return &PDPredictorSet{
		PrefillPredictor: prefillPredictor,
		DecodePredictor:  decodePredictor,
		logger:           logger,
	}, nil
}

// Start initializes both predictors. Must be called before using the predictor set.
func (p *PDPredictorSet) Start(ctx context.Context) error {
	p.logger.Info("Starting PD predictor set")

	if err := p.PrefillPredictor.Start(ctx); err != nil {
		return fmt.Errorf("failed to start prefill predictor: %w", err)
	}

	if err := p.DecodePredictor.Start(ctx); err != nil {
		return fmt.Errorf("failed to start decode predictor: %w", err)
	}

	p.logger.Info("Successfully started all PD predictors")
	return nil
}

// Stop gracefully stops both predictors. Should be called during shutdown.
func (p *PDPredictorSet) Stop() {
	p.logger.Info("Stopping PD predictor set")
	p.PrefillPredictor.Stop()
	p.DecodePredictor.Stop()
	p.logger.Info("Stopped all PD predictors")
}

// Helper functions

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getPredictionURLs parses comma-separated prediction URLs from environment variable
func getPredictionURLs(envKey string) []string {
	urls := os.Getenv(envKey)
	if urls == "" {
		return []string{}
	}

	// Split by comma and trim whitespace
	urlList := strings.Split(urls, ",")
	result := make([]string, 0, len(urlList))
	for _, url := range urlList {
		trimmed := strings.TrimSpace(url)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
