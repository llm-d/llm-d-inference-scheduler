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

package scorer

import (
	"os"
	"strconv"
)

// Sampling configuration for TPOT predictions
var (
	// DefaultSamplingMean is the mean interval for Poisson-distributed TPOT predictions
	DefaultSamplingMean = func() float64 {
		if value, exists := os.LookupEnv("PD_SAMPLING_MEAN"); exists {
			if parsedValue, err := strconv.ParseFloat(value, 64); err == nil && parsedValue > 0 {
				return parsedValue
			}
		}
		return 100.0 // default: predict every ~100 tokens on average
	}()

	// MaxSampledTokens is the maximum number of TPOT predictions per request
	MaxSampledTokens = func() int {
		if value, exists := os.LookupEnv("PD_MAX_SAMPLED_TOKENS"); exists {
			if parsedValue, err := strconv.Atoi(value); err == nil && parsedValue > 0 {
				return parsedValue
			}
		}
		return 20 // default: max 20 predictions per request
	}()
)
