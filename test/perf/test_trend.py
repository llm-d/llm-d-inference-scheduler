import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import trend


SETUP = {
    "router_config": "test/perf/config/router-configs/optimized-baseline.yaml",
    "perf_job": "test/perf/config/shared_prefix_job1.yaml",
    "sim_replicas": 10,
    "epp_replicas": 1,
    "machine_family": "e2",
    "epp_log_verbosity": 4,
}


def run_record(day, status="SUCCESS", epp_peak_cpu=2000, p95=9.0, envoy_peak_cpu=2500, commit=None, **setup):
    return {
        "schema_version": trend.RUN_RECORD_SCHEMA_VERSION,
        "test_name": "job1",
        "started_at": f"2026-10-{day:02d}T09:00:00Z",
        "status": status,
        "setup": {**SETUP, **setup},
        "provenance": {"epp_commit": commit or f"commit{day:02d}", "images": [], "image_ids": {}},
        "containers": {
            "epp": {"idle_cpu_m": 200, "idle_mem_mib": 25, "peak_cpu_m": epp_peak_cpu, "peak_mem_mib": 70},
            "envoy-proxy": {"idle_cpu_m": 30, "idle_mem_mib": 17, "peak_cpu_m": envoy_peak_cpu, "peak_mem_mib": 50},
            "TOTAL": {"idle_cpu_m": 230, "idle_mem_mib": 42, "peak_cpu_m": epp_peak_cpu + envoy_peak_cpu, "peak_mem_mib": 120},
        },
        "scheduler_latency_ms": {"p50": 3.5, "p95": p95, "p99": 16.0, "mean": 4.0},
    }


def history(*tonight_args, **tonight_kwargs):
    baseline = [run_record(day) for day in range(10, 17)]
    return baseline + [run_record(17, *tonight_args, **tonight_kwargs)]


def result_for(verdict, key):
    return next(result for result in verdict["metrics"] if result["key"] == key)


class BaselineSelectionTests(unittest.TestCase):
    def test_window_is_the_seven_days_before_tonight(self):
        records = [run_record(day) for day in range(5, 18)]
        baseline = trend.select_baseline(records, records[-1])
        self.assertEqual([record["started_at"][:10] for record in baseline], [f"2026-10-{day}" for day in range(10, 17)])

    def test_failed_and_mismatched_runs_are_excluded(self):
        records = [
            run_record(14, status="FAILED"),
            run_record(15, sim_replicas=100),
            run_record(16, router_config="other.yaml"),
            run_record(16, perf_job="other.yaml"),
            run_record(16),
            run_record(17),
        ]
        baseline = trend.select_baseline(records, records[-1])
        self.assertEqual(baseline, [records[4]])

    def test_other_setup_fields_do_not_split_the_baseline(self):
        records = [run_record(16, machine_family="c3"), run_record(17)]
        self.assertEqual(trend.select_baseline(records, records[-1]), [records[0]])


class EvaluateTests(unittest.TestCase):
    def test_no_runs(self):
        self.assertEqual(trend.evaluate([], "job1")["status"], "no_runs")

    def test_failed_tonight_is_not_compared(self):
        verdict = trend.evaluate(history(status="FAILED"), "job1")
        self.assertEqual(verdict["status"], "run_failed")
        self.assertEqual(verdict["metrics"], [])

    def test_fewer_than_four_baseline_runs_is_insufficient(self):
        records = [run_record(day) for day in range(14, 18)]
        verdict = trend.evaluate(records, "job1")
        self.assertEqual(verdict["status"], "insufficient_baseline")
        self.assertEqual(len(verdict["baseline_runs"]), 3)
        self.assertEqual(trend.evaluate([run_record(day) for day in range(13, 18)], "job1")["status"], "ok")

    def test_more_than_ten_percent_worse_regresses(self):
        verdict = trend.evaluate(history(epp_peak_cpu=2201), "job1")
        self.assertEqual(verdict["status"], "regressed")
        result = result_for(verdict, "epp.peak_cpu_m")
        self.assertEqual(result["verdict"], "regressed")
        self.assertEqual(result["baseline"], 2000)
        self.assertAlmostEqual(result["delta"], 0.1005)
        self.assertEqual(result["runs"], 7)

    def test_exactly_ten_percent_and_improvements_do_not_regress(self):
        for value in (2200, 1500):
            with self.subTest(value=value):
                verdict = trend.evaluate(history(epp_peak_cpu=value), "job1")
                self.assertEqual(verdict["status"], "ok")
                self.assertEqual(result_for(verdict, "epp.peak_cpu_m")["verdict"], "within threshold")

    def test_scheduler_latency_regresses(self):
        verdict = trend.evaluate(history(p95=18.5), "job1")
        self.assertEqual(verdict["status"], "regressed")
        self.assertEqual(result_for(verdict, "scheduler.p95")["verdict"], "regressed")

    def test_envoy_and_total_rows_never_regress(self):
        verdict = trend.evaluate(history(envoy_peak_cpu=5000), "job1")
        self.assertEqual(verdict["status"], "ok")
        self.assertEqual(result_for(verdict, "envoy-proxy.peak_cpu_m")["verdict"], "over threshold, report only")
        self.assertEqual(result_for(verdict, "TOTAL.peak_cpu_m")["verdict"], "over threshold, report only")

    def test_unmeasured_values_are_missing_not_zero(self):
        records = history(p95=0.0)
        for record in records[:4]:
            record["scheduler_latency_ms"]["p99"] = 0.0
        verdict = trend.evaluate(records, "job1")
        self.assertEqual(result_for(verdict, "scheduler.p95")["verdict"], "no data")
        self.assertEqual(result_for(verdict, "scheduler.p99")["verdict"], "insufficient baseline")
        self.assertEqual(verdict["status"], "ok")

    def test_custom_threshold(self):
        verdict = trend.evaluate(history(epp_peak_cpu=2101), "job1", threshold=0.05)
        self.assertEqual(verdict["status"], "regressed")

    def test_commit_range_uses_newest_baseline_run(self):
        verdict = trend.evaluate(history(), "job1")
        self.assertEqual(verdict["baseline_commit"], "commit16")
        self.assertEqual(verdict["tonight"]["epp_commit"], "commit17")


class IssueBodyTests(unittest.TestCase):
    def test_body_has_marker_regressed_metrics_and_compare_link(self):
        verdict = trend.evaluate(history(epp_peak_cpu=2500), "job1")
        body = trend.render_issue_body(verdict, "llm-d/llm-d-router", "https://example.com/run/1")
        self.assertTrue(body.startswith("<!-- perf-trend:job1 -->\n"))
        self.assertIn("epp peak CPU (m)", body)
        self.assertNotIn("epp idle CPU (m)", body)
        self.assertIn("https://github.com/llm-d/llm-d-router/compare/commit16...commit17", body)
        self.assertIn("https://example.com/run/1", body)

    def test_missing_commit_has_no_compare_link(self):
        records = history(epp_peak_cpu=2500)
        records[-1]["provenance"]["epp_commit"] = None
        verdict = trend.evaluate(records, "job1")
        body = trend.render_issue_body(verdict, "llm-d/llm-d-router")
        self.assertNotIn("/compare/", body)
        self.assertIn("commit range is unavailable", body)


class FileTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.directory = Path(directory.name)
        self.results = self.directory / "results"
        self.output = self.directory / "out"
        self.runs = self.results / "runs" / "job1"
        self.runs.mkdir(parents=True)
        patcher = mock.patch.dict(os.environ, {}, clear=False)
        patcher.start()
        self.addCleanup(patcher.stop)
        os.environ.pop("GITHUB_STEP_SUMMARY", None)

    def write_records(self, records):
        for index, record in enumerate(records):
            (self.runs / f"{index:02d}.json").write_text(json.dumps(record))

    def run_main(self):
        with mock.patch("builtins.print"):
            return trend.main([
                "--results-dir", str(self.results),
                "--test-name", "job1",
                "--output-dir", str(self.output),
            ])

    def test_records_load_in_time_order(self):
        records = history()
        self.write_records(list(reversed(records)))
        loaded = trend.load_records(str(self.results), "job1")
        self.assertEqual([record["started_at"] for record in loaded], [record["started_at"] for record in records])

    def test_missing_runs_folder_is_no_runs(self):
        self.assertEqual(trend.load_records(str(self.directory / "empty"), "job1"), [])

    def test_regression_exits_zero_and_writes_issue_body(self):
        self.write_records(history(epp_peak_cpu=2500))
        summary_file = self.directory / "step-summary.md"
        os.environ["GITHUB_STEP_SUMMARY"] = str(summary_file)
        self.assertEqual(self.run_main(), 0)
        verdict = json.loads((self.output / "trend-verdict.json").read_text())
        self.assertEqual(verdict["status"], "regressed")
        self.assertTrue((self.output / "trend-issue.md").exists())
        self.assertIn("Perf trend: job1", summary_file.read_text())

    def test_no_regression_writes_no_issue_body(self):
        self.write_records(history())
        self.assertEqual(self.run_main(), 0)
        self.assertFalse((self.output / "trend-issue.md").exists())
        self.assertIn("| epp peak CPU (m) |", (self.output / "trend-summary.md").read_text())

    def test_broken_records_raise(self):
        cases = {
            "unparsable": "{not json",
            "wrong schema": json.dumps({**run_record(17), "schema_version": 99}),
            "missing status": json.dumps({key: value for key, value in run_record(17).items() if key != "status"}),
        }
        for name, content in cases.items():
            with self.subTest(case=name):
                (self.runs / "bad.json").write_text(content)
                with self.assertRaises(ValueError):
                    self.run_main()


if __name__ == "__main__":
    unittest.main()
