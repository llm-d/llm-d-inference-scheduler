# Metrics

The `llm-d-inference-scheduler` exposes the following Prometheus metrics to monitor its behavior and performance, particularly concerning Prefill/Decode Disaggregation.

All metrics are in the `llm_d_inference_scheduler` subsystem.

## Scrape and see the metric

Metrics defined in llm-d-scheduler are in addition to Inference Gateway metrics. For more details of seeing metrics, see the [metrics and observability section](https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/main/site-src/guides/metrics-and-observability.md).

## Metrics Details

### `pd_decision_total`

*   **Type:** Counter
*   **Labels:**
    *   `decision_type`: string ("decode-only" or "prefill-decode")
*   **Release Stage:** ALPHA
*   **Description:** Counts the number of requests processed, broken down by the Prefill/Decode disaggregation decision.
    *   `prefill-decode`: The request was split into separate Prefill and Decode stages.
    *   `decode-only`: The request used the Decode-only path.
*   **Usage:** Provides a high-level view of how many requests are utilizing the disaggregated path versus the unified path.
*   **Actionability:**
    *   Monitor the ratio of "prefill-decode" to "decode-only" to understand the P/D engagement rate.
    *   Sudden changes in this ratio might indicate configuration issues, changes in workload patterns, or problems with the decision logic.