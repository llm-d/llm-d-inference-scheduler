#!/usr/bin/env python3

import argparse
import json
import os
import statistics
import sys
from collections import namedtuple
from datetime import datetime, timedelta

from run_nightly_perf import RUN_RECORD_SCHEMA_VERSION

THRESHOLD = 0.10
WINDOW_DAYS = 7
MIN_BASELINE_RUNS = 4
DEFAULT_REPOSITORY = "llm-d/llm-d-router"
COMPARABLE_SETUP_KEYS = ("router_config", "perf_job", "sim_replicas")

# Each metric has its own threshold so noisier measurements can be given more room.
Metric = namedtuple("Metric", ["key", "label", "path", "alerts", "threshold"], defaults=[THRESHOLD])


def container_metrics(container, alerts):
    return [
        Metric(f"{container}.idle_cpu_m", f"{container} idle CPU (m)", ("containers", container, "idle_cpu_m"), alerts),
        Metric(f"{container}.peak_cpu_m", f"{container} peak CPU (m)", ("containers", container, "peak_cpu_m"), alerts),
        Metric(f"{container}.idle_mem_mib", f"{container} idle memory (MiB)", ("containers", container, "idle_mem_mib"), alerts),
        Metric(f"{container}.peak_mem_mib", f"{container} peak memory (MiB)", ("containers", container, "peak_mem_mib"), alerts),
    ]


def scheduler_metrics():
    return [
        Metric(f"scheduler.{name}", f"scheduler {name} latency (ms)", ("scheduler_latency_ms", name), True)
        for name in ("p50", "p95", "p99", "mean")
    ]


# Lower is better for every metric. Only EPP metrics open issues; the other rows are reported.
METRICS = container_metrics("epp", True) + scheduler_metrics() + container_metrics("envoy-proxy", False) + container_metrics("TOTAL", False)


def issue_marker(test_name):
    return f"<!-- perf-trend:{test_name} -->"


def parse_time(value):
    return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ")


def load_records(results_dir, test_name):
    runs_dir = os.path.join(results_dir, "runs", test_name)
    if not os.path.isdir(runs_dir):
        return []
    records = []
    for name in os.listdir(runs_dir):
        if name.endswith(".json"):
            records.append(load_record(os.path.join(runs_dir, name)))
    return sorted(records, key=lambda record: parse_time(record["started_at"]))


def load_record(path):
    with open(path) as f:
        record = json.load(f)
    if record.get("schema_version") != RUN_RECORD_SCHEMA_VERSION:
        raise ValueError(f"{path}: unsupported run record schema_version {record.get('schema_version')!r}")
    for key in ("started_at", "status", "setup"):
        if key not in record:
            raise ValueError(f"{path}: run record is missing {key!r}")
    return record


def comparable(first, second):
    return all(first["setup"].get(key) == second["setup"].get(key) for key in COMPARABLE_SETUP_KEYS)


def select_baseline(records, tonight, window_days=WINDOW_DAYS):
    end = parse_time(tonight["started_at"])
    start = end - timedelta(days=window_days)
    return [
        record
        for record in records
        if record is not tonight
        and record["status"] == "SUCCESS"
        and comparable(record, tonight)
        and start <= parse_time(record["started_at"]) < end
    ]


def metric_value(record, metric):
    value = record
    for part in metric.path:
        if not isinstance(value, dict) or part not in value:
            return None
        value = value[part]
    # The runner reports 0 for a metric it could not measure.
    if not isinstance(value, (int, float)) or value <= 0:
        return None
    return float(value)


def compare_metric(metric, baseline_records, candidate, min_runs):
    result = {"key": metric.key, "label": metric.label, "alerts": metric.alerts, "threshold": metric.threshold, "over": False}
    tonight = metric_value(candidate, metric)
    values = [value for value in (metric_value(record, metric) for record in baseline_records) if value is not None]
    result.update(tonight=tonight, runs=len(values), baseline=None, delta=None, min=None, max=None)
    if tonight is None:
        result["verdict"] = "no data"
        return result
    if len(values) < min_runs:
        result["verdict"] = "insufficient baseline"
        return result
    baseline = statistics.median(values)
    delta = (tonight - baseline) / baseline
    # Exceeding every baseline run keeps a metric with a wide normal range from firing on ordinary noise.
    over = delta > metric.threshold and tonight > max(values)
    result.update(baseline=baseline, delta=delta, min=min(values), max=max(values), over=over)
    if not over:
        result["verdict"] = "within threshold"
    elif metric.alerts:
        result["verdict"] = "over threshold"
    else:
        result["verdict"] = "over threshold, report only"
    return result


def compare(baseline_records, candidate, min_runs=MIN_BASELINE_RUNS):
    return [compare_metric(metric, baseline_records, candidate, min_runs) for metric in METRICS]


def previous_run(records, run):
    earlier = [
        record
        for record in records
        if record["status"] == "SUCCESS"
        and comparable(record, run)
        and parse_time(record["started_at"]) < parse_time(run["started_at"])
    ]
    return earlier[-1] if earlier else None


def confirm_regressions(results, previous_over):
    # A metric regresses only when the previous comparable run was over its threshold too.
    for result in results:
        if result["verdict"] == "over threshold":
            result["verdict"] = "regressed" if result["key"] in previous_over else "over threshold, first run"


def commit_of(record):
    return record.get("provenance", {}).get("epp_commit") or None


def evaluate(records, test_name, window_days=WINDOW_DAYS, min_runs=MIN_BASELINE_RUNS):
    verdict = {"test_name": test_name, "window_days": window_days, "min_baseline_runs": min_runs, "metrics": []}
    if not records:
        verdict["status"] = "no_runs"
        return verdict
    tonight = records[-1]
    verdict["tonight"] = {"started_at": tonight["started_at"], "status": tonight["status"], "epp_commit": commit_of(tonight)}
    if tonight["status"] != "SUCCESS":
        verdict["status"] = "run_failed"
        return verdict
    previous = previous_run(records, tonight)
    # Tonight and the previous run share one baseline from before the previous run, so a lasting step counts on both.
    baseline = select_baseline(records, previous, window_days) if previous else []
    verdict["baseline_runs"] = [record["started_at"] for record in baseline]
    verdict["range_base_commit"] = commit_of(baseline[-1]) if baseline else None
    if len(baseline) < min_runs:
        verdict["status"] = "insufficient_baseline"
        return verdict
    verdict["metrics"] = compare(baseline, tonight, min_runs)
    previous_over = {result["key"] for result in compare(baseline, previous, min_runs) if result["over"]}
    confirm_regressions(verdict["metrics"], previous_over)
    regressed = any(result["verdict"] == "regressed" for result in verdict["metrics"])
    verdict["status"] = "regressed" if regressed else "ok"
    return verdict


def format_number(value):
    return "-" if value is None else f"{value:.2f}"


def format_delta(value):
    return "-" if value is None else f"{value * 100:+.1f}%"


def metrics_table(results):
    lines = [
        "| Metric | Tonight | Baseline (median) | Delta | Threshold | Baseline range | Runs | Verdict |",
        "|---|---|---|---|---|---|---|---|",
    ]
    for result in results:
        spread = "-" if result["min"] is None else f"{result['min']:.2f}-{result['max']:.2f}"
        lines.append(
            f"| {result['label']} | {format_number(result['tonight'])} | {format_number(result['baseline'])} | "
            f"{format_delta(result['delta'])} | {result['threshold'] * 100:.0f}% | {spread} | {result['runs']} | {result['verdict']} |"
        )
    return "\n".join(lines)


RULE_TEXT = (
    "A metric regresses when tonight's run and the previous comparable run are both more than its threshold over the "
    "median of the successful runs from the {window_days} days before the previous run, and above every one of them."
)

STATUS_TEXT = {
    "no_runs": "No run records found.",
    "run_failed": "Tonight's run did not succeed, so nothing was compared.",
    "insufficient_baseline": "Fewer than {min_baseline_runs} comparable successful runs in the {window_days} days before the previous run, so nothing was compared.",
    "ok": "No EPP metric regressed.",
    "regressed": "At least one EPP metric regressed.",
}


def status_line(verdict):
    return STATUS_TEXT[verdict["status"]].format(
        min_baseline_runs=verdict["min_baseline_runs"],
        window_days=verdict["window_days"],
    )


def rule_line(verdict):
    return RULE_TEXT.format(window_days=verdict["window_days"])


def render_summary(verdict):
    lines = [f"## Perf trend: {verdict['test_name']}", "", status_line(verdict), "", rule_line(verdict), ""]
    if "tonight" in verdict:
        lines.append(f"Tonight: {verdict['tonight']['started_at']} (EPP commit `{verdict['tonight']['epp_commit'] or 'unknown'}`)")
    if "baseline_runs" in verdict:
        lines.append(f"Baseline: {len(verdict['baseline_runs'])} comparable successful runs")
    if verdict["metrics"]:
        lines += ["", metrics_table(verdict["metrics"])]
    return "\n".join(lines) + "\n"


def compare_link(repository, base, head):
    if not base or not head or base == head:
        return None
    return f"https://github.com/{repository}/compare/{base}...{head}"


def render_issue_body(verdict, repository, run_url=None):
    regressed = [result for result in verdict["metrics"] if result["verdict"] == "regressed"]
    lines = [
        issue_marker(verdict["test_name"]),
        f"The nightly perf run on {verdict['tonight']['started_at']} confirmed a regression in these EPP metrics. "
        + rule_line(verdict),
        "",
        metrics_table(regressed),
        "",
    ]
    link = compare_link(repository, verdict.get("range_base_commit"), verdict["tonight"]["epp_commit"])
    if link:
        lines.append(f"Changes since the last run before the regression: {link}")
    else:
        lines.append("The commit range is unavailable: a run did not record its EPP commit, or both runs used the same commit.")
    if run_url:
        lines.append(f"Workflow run: {run_url}")
    return "\n".join(lines) + "\n"


def write_outputs(verdict, output_dir, repository, run_url):
    os.makedirs(output_dir, exist_ok=True)
    summary = render_summary(verdict)
    with open(os.path.join(output_dir, "trend-summary.md"), "w") as f:
        f.write(summary)
    with open(os.path.join(output_dir, "trend-verdict.json"), "w") as f:
        json.dump(verdict, f, indent=2, sort_keys=True)
        f.write("\n")
    if verdict["status"] == "regressed":
        with open(os.path.join(output_dir, "trend-issue.md"), "w") as f:
            f.write(render_issue_body(verdict, repository, run_url))
    step_summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if step_summary:
        with open(step_summary, "a") as f:
            f.write(summary)
    return summary


def parse_args(argv):
    parser = argparse.ArgumentParser(description="Compare the newest nightly perf run with its trailing median")
    parser.add_argument("--results-dir", required=True, help="Results folder containing runs/<test-name>/*.json")
    parser.add_argument("--test-name", required=True, help="Name of the performance test")
    parser.add_argument("--output-dir", required=True, help="Folder for trend-summary.md, trend-verdict.json and trend-issue.md")
    parser.add_argument("--repository", default=os.environ.get("GITHUB_REPOSITORY", DEFAULT_REPOSITORY), help="owner/name used for compare links")
    parser.add_argument("--run-url", default=None, help="Workflow run URL to link from the issue")
    return parser.parse_args(argv)


def main(argv=None):
    # A regression exits 0; an error in this script propagates and fails the step.
    args = parse_args(argv)
    records = load_records(args.results_dir, args.test_name)
    verdict = evaluate(records, args.test_name)
    print(write_outputs(verdict, args.output_dir, args.repository, args.run_url))
    return 0


if __name__ == "__main__":
    sys.exit(main())
