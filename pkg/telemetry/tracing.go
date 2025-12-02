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

package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	serviceName = "llm-d-inference-scheduler"
)

// InitTracing initializes OpenTelemetry tracing with OTLP exporter.
// Configuration is done via environment variables:
// - OTEL_EXPORTER_OTLP_ENDPOINT: OTLP collector endpoint (default: http://localhost:4317)
// - OTEL_TRACES_SAMPLER: Sampling strategy (default: parentbased_traceidratio)
// - OTEL_TRACES_SAMPLER_ARG: Sampling ratio (default: 0.1 for 10%)
func InitTracing(ctx context.Context) (func(context.Context) error, error) {
	logger := log.FromContext(ctx)

	// Get OTLP endpoint from environment
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "localhost:4317"
	}

	// Strip http:// or https:// prefix if present
	// otlptracegrpc.WithEndpoint() expects host:port only
	endpoint = stripScheme(endpoint)

	logger.Info("Initializing OpenTelemetry tracing", "endpoint", endpoint, "service", serviceName)

	// Create OTLP trace exporter
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(), // Use WithTLSCredentials() in production
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}

	// Create resource with service name
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Create trace provider with parent-based sampling
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.1))), // 10% sampling
	)

	// Set global trace provider
	otel.SetTracerProvider(tp)

	// Set W3C trace context propagator
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	logger.Info("OpenTelemetry tracing initialized successfully")

	// Return shutdown function
	return tp.Shutdown, nil
}

// Tracer returns a tracer for the inference scheduler
func Tracer() trace.Tracer {
	return otel.Tracer(serviceName)
}

// stripScheme removes http:// or https:// prefix from endpoint URL
// OpenTelemetry gRPC exporter expects host:port format only
func stripScheme(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	return endpoint
}
