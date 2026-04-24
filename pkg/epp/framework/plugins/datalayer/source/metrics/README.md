# Metrics Data Source

The Metrics Data Source is a data layer plugin that polls a Prometheus-compatible metrics endpoint of a model server and parses the response into a structured format for extraction.

It is registered as type `metrics-data-source` and runs as a data layer source.

## What it does

1.  Periodically (or when triggered) performs an HTTP GET request to a configured metrics endpoint (e.g., `http://<endpoint-ip>:8080/metrics`).
2.  Parses the Prometheus text format response into a `PrometheusMetricMap`.
3.  Provides the parsed metrics to any registered extractors (like the `core-metrics-extractor`).

## Outputs produced

-   `PrometheusMetricType`: A map where keys are metric names and values are Prometheus `MetricFamily` objects.

## Configuration

The plugin config supports:

-   `scheme` (default "http"): The protocol scheme to use for metrics retrieval.
-   `path` (default "/metrics"): The URL path to use for metrics retrieval.
-   `insecureSkipVerify` (default true): Whether to skip TLS certificate verification when using the "https" scheme.

### Example Configuration

```yaml
type: metrics-data-source
parameters:
  scheme: "http"
  path: "/metrics"
  insecureSkipVerify: true
```
