#!/usr/bin/env python3
"""
Analyze fairness A/B test results.

Reads JSONL from baseline and program-aware runs, produces:
- Per-program latency table (P50, P95, P99)
- CDF plot: per-program latency, side-by-side
- Bar chart: mean/P50/P95/P99 grouped by program and config
- Fairness ratio analysis
- Throughput check

Usage:
    python3 analyze_results.py \
        --baseline results/baseline/results.jsonl \
        --program-aware results/program-aware/results.jsonl \
        --output results/comparison/
"""

import argparse
import json
import os
import sys
from collections import defaultdict

import numpy as np

try:
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
    HAS_MATPLOTLIB = True
except ImportError:
    HAS_MATPLOTLIB = False
    print("WARNING: matplotlib not installed. Plots will be skipped. Install with: pip install matplotlib")


def load_results(path: str) -> list[dict]:
    """Load JSONL results file."""
    results = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if line:
                results.append(json.loads(line))
    return results


def group_by_program(results: list[dict]) -> dict[str, list[float]]:
    """Group latencies by program_id, filtering to successful requests only."""
    groups = defaultdict(list)
    for r in results:
        if r["status"] == "ok":
            groups[r["program_id"]].append(r["total_ms"])
    return dict(groups)


def percentile(data: list[float], p: float) -> float:
    """Compute percentile."""
    if not data:
        return 0.0
    return float(np.percentile(sorted(data), p))


def compute_stats(latencies: list[float]) -> dict:
    """Compute latency statistics."""
    if not latencies:
        return {"count": 0, "mean": 0, "p50": 0, "p95": 0, "p99": 0, "min": 0, "max": 0}
    s = sorted(latencies)
    return {
        "count": len(s),
        "mean": float(np.mean(s)),
        "p50": percentile(s, 50),
        "p95": percentile(s, 95),
        "p99": percentile(s, 99),
        "min": s[0],
        "max": s[-1],
    }


def print_comparison_table(baseline_groups: dict, pa_groups: dict):
    """Print side-by-side latency comparison table."""
    all_programs = sorted(set(list(baseline_groups.keys()) + list(pa_groups.keys())))

    print("\n" + "=" * 90)
    print("LATENCY COMPARISON (milliseconds)")
    print("=" * 90)
    header = f"{'Program':<15} {'Config':<15} {'Count':>7} {'Mean':>8} {'P50':>8} {'P95':>8} {'P99':>8}"
    print(header)
    print("-" * 90)

    for prog in all_programs:
        for label, groups in [("baseline", baseline_groups), ("program-aware", pa_groups)]:
            latencies = groups.get(prog, [])
            stats = compute_stats(latencies)
            print(
                f"{prog:<15} {label:<15} {stats['count']:>7} "
                f"{stats['mean']:>8.1f} {stats['p50']:>8.1f} "
                f"{stats['p95']:>8.1f} {stats['p99']:>8.1f}"
            )
        print()


def print_fairness_analysis(baseline_groups: dict, pa_groups: dict):
    """Print fairness ratio analysis."""
    all_programs = sorted(set(list(baseline_groups.keys()) + list(pa_groups.keys())))

    print("=" * 70)
    print("FAIRNESS ANALYSIS")
    print("=" * 70)

    for label, groups in [("Baseline", baseline_groups), ("Program-Aware", pa_groups)]:
        stats_by_prog = {}
        for prog in all_programs:
            latencies = groups.get(prog, [])
            stats_by_prog[prog] = compute_stats(latencies)

        print(f"\n{label}:")

        # Find the program with highest rate (most requests = likely "heavy").
        heavy_prog = max(stats_by_prog, key=lambda p: stats_by_prog[p]["count"]) if stats_by_prog else None
        if heavy_prog and stats_by_prog[heavy_prog]["p95"] > 0:
            for prog in all_programs:
                if prog == heavy_prog:
                    continue
                ratio_p95 = stats_by_prog[prog]["p95"] / stats_by_prog[heavy_prog]["p95"]
                ratio_p99 = stats_by_prog[prog]["p99"] / stats_by_prog[heavy_prog]["p99"] if stats_by_prog[heavy_prog]["p99"] > 0 else float("inf")
                print(f"  P95 ratio {prog}/{heavy_prog}: {ratio_p95:.2f}x")
                print(f"  P99 ratio {prog}/{heavy_prog}: {ratio_p99:.2f}x")

    # Cross-config comparison for light program.
    print("\nCross-config improvement for low-rate programs:")
    for prog in all_programs:
        bl = compute_stats(baseline_groups.get(prog, []))
        pa = compute_stats(pa_groups.get(prog, []))
        if bl["p95"] > 0 and pa["p95"] > 0:
            improvement = (bl["p95"] - pa["p95"]) / bl["p95"] * 100
            print(f"  {prog} P95: {bl['p95']:.1f}ms -> {pa['p95']:.1f}ms ({improvement:+.1f}%)")
        if bl["p99"] > 0 and pa["p99"] > 0:
            improvement = (bl["p99"] - pa["p99"]) / bl["p99"] * 100
            print(f"  {prog} P99: {bl['p99']:.1f}ms -> {pa['p99']:.1f}ms ({improvement:+.1f}%)")


def print_throughput_check(baseline_results: list[dict], pa_results: list[dict]):
    """Print throughput comparison."""
    print("\n" + "=" * 70)
    print("THROUGHPUT CHECK")
    print("=" * 70)

    for label, results in [("Baseline", baseline_results), ("Program-Aware", pa_results)]:
        ok = sum(1 for r in results if r["status"] == "ok")
        err = sum(1 for r in results if r["status"] != "ok")
        total = len(results)
        if total > 0:
            timestamps = [r["sent_at"] for r in results]
            duration = max(timestamps) - min(timestamps)
            rps = ok / duration if duration > 0 else 0
            print(f"  {label}: {ok} ok / {err} errors / {total} total ({rps:.1f} req/s effective)")


def plot_cdf(baseline_groups: dict, pa_groups: dict, output_dir: str):
    """Plot per-program latency CDF, side-by-side."""
    if not HAS_MATPLOTLIB:
        return

    all_programs = sorted(set(list(baseline_groups.keys()) + list(pa_groups.keys())))
    colors = plt.cm.tab10(np.linspace(0, 1, max(len(all_programs), 1)))

    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(14, 6), sharey=True)

    for i, prog in enumerate(all_programs):
        # Baseline CDF.
        latencies = sorted(baseline_groups.get(prog, []))
        if latencies:
            cdf = np.arange(1, len(latencies) + 1) / len(latencies)
            ax1.plot(latencies, cdf, label=prog, color=colors[i], linewidth=1.5)

        # Program-aware CDF.
        latencies = sorted(pa_groups.get(prog, []))
        if latencies:
            cdf = np.arange(1, len(latencies) + 1) / len(latencies)
            ax2.plot(latencies, cdf, label=prog, color=colors[i], linewidth=1.5)

    for ax, title in [(ax1, "Baseline (No Fairness)"), (ax2, "Program-Aware Fairness")]:
        ax.set_xlabel("Latency (ms)")
        ax.set_ylabel("CDF")
        ax.set_title(title)
        ax.legend()
        ax.grid(True, alpha=0.3)
        ax.set_ylim(0, 1.05)

    plt.tight_layout()
    path = os.path.join(output_dir, "cdf_comparison.png")
    plt.savefig(path, dpi=150)
    plt.close()
    print(f"  Saved CDF plot: {path}")


def plot_cdf_overlay(baseline_groups: dict, pa_groups: dict, output_dir: str):
    """Plot overlaid CDF — all programs, both configs on one chart."""
    if not HAS_MATPLOTLIB:
        return

    all_programs = sorted(set(list(baseline_groups.keys()) + list(pa_groups.keys())))
    colors = plt.cm.tab10(np.linspace(0, 1, max(len(all_programs), 1)))

    fig, ax = plt.subplots(figsize=(10, 6))

    for i, prog in enumerate(all_programs):
        # Baseline — dashed.
        latencies = sorted(baseline_groups.get(prog, []))
        if latencies:
            cdf = np.arange(1, len(latencies) + 1) / len(latencies)
            ax.plot(latencies, cdf, label=f"{prog} (baseline)", color=colors[i], linestyle="--", linewidth=1.5)

        # Program-aware — solid.
        latencies = sorted(pa_groups.get(prog, []))
        if latencies:
            cdf = np.arange(1, len(latencies) + 1) / len(latencies)
            ax.plot(latencies, cdf, label=f"{prog} (prog-aware)", color=colors[i], linestyle="-", linewidth=1.5)

    ax.set_xlabel("Latency (ms)")
    ax.set_ylabel("CDF")
    ax.set_title("Latency CDF — Baseline vs Program-Aware Fairness")
    ax.legend(loc="lower right", fontsize=8)
    ax.grid(True, alpha=0.3)
    ax.set_ylim(0, 1.05)

    plt.tight_layout()
    path = os.path.join(output_dir, "cdf_overlay.png")
    plt.savefig(path, dpi=150)
    plt.close()
    print(f"  Saved overlay CDF plot: {path}")


def plot_bar_chart(baseline_groups: dict, pa_groups: dict, output_dir: str):
    """Plot bar chart of P50/P95/P99 grouped by program and config."""
    if not HAS_MATPLOTLIB:
        return

    all_programs = sorted(set(list(baseline_groups.keys()) + list(pa_groups.keys())))
    metrics_names = ["P50", "P95", "P99"]

    baseline_vals = {m: [] for m in metrics_names}
    pa_vals = {m: [] for m in metrics_names}

    for prog in all_programs:
        bl = compute_stats(baseline_groups.get(prog, []))
        pa = compute_stats(pa_groups.get(prog, []))
        for m, key in [("P50", "p50"), ("P95", "p95"), ("P99", "p99")]:
            baseline_vals[m].append(bl[key])
            pa_vals[m].append(pa[key])

    fig, axes = plt.subplots(1, 3, figsize=(15, 5))
    x = np.arange(len(all_programs))
    width = 0.35

    for ax, metric in zip(axes, metrics_names):
        bars1 = ax.bar(x - width / 2, baseline_vals[metric], width, label="Baseline", color="#4c72b0", alpha=0.8)
        bars2 = ax.bar(x + width / 2, pa_vals[metric], width, label="Program-Aware", color="#dd8452", alpha=0.8)
        ax.set_xlabel("Program")
        ax.set_ylabel("Latency (ms)")
        ax.set_title(f"{metric} Latency")
        ax.set_xticks(x)
        ax.set_xticklabels(all_programs, rotation=15, ha="right")
        ax.legend()
        ax.grid(True, alpha=0.3, axis="y")

    plt.tight_layout()
    path = os.path.join(output_dir, "bar_comparison.png")
    plt.savefig(path, dpi=150)
    plt.close()
    print(f"  Saved bar chart: {path}")


def plot_timeseries(baseline_results: list[dict], pa_results: list[dict], output_dir: str):
    """Plot latency over time for each program."""
    if not HAS_MATPLOTLIB:
        return

    fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(14, 8), sharex=True)

    for ax, results, title in [
        (ax1, baseline_results, "Baseline"),
        (ax2, pa_results, "Program-Aware"),
    ]:
        by_prog = defaultdict(lambda: ([], []))
        t0 = min(r["sent_at"] for r in results) if results else 0
        for r in results:
            if r["status"] == "ok":
                by_prog[r["program_id"]][0].append(r["sent_at"] - t0)
                by_prog[r["program_id"]][1].append(r["total_ms"])

        for prog in sorted(by_prog.keys()):
            times, latencies = by_prog[prog]
            ax.scatter(times, latencies, label=prog, alpha=0.3, s=8)

        ax.set_ylabel("Latency (ms)")
        ax.set_title(f"{title} — Latency Over Time")
        ax.legend()
        ax.grid(True, alpha=0.3)

    ax2.set_xlabel("Time (s)")
    plt.tight_layout()
    path = os.path.join(output_dir, "timeseries.png")
    plt.savefig(path, dpi=150)
    plt.close()
    print(f"  Saved timeseries plot: {path}")


def save_summary_json(baseline_groups: dict, pa_groups: dict, output_dir: str):
    """Save machine-readable summary as JSON."""
    all_programs = sorted(set(list(baseline_groups.keys()) + list(pa_groups.keys())))
    summary = {"baseline": {}, "program_aware": {}}

    for label, groups, key in [
        ("baseline", baseline_groups, "baseline"),
        ("program_aware", pa_groups, "program_aware"),
    ]:
        for prog in all_programs:
            summary[key][prog] = compute_stats(groups.get(prog, []))

    path = os.path.join(output_dir, "summary.json")
    with open(path, "w") as f:
        json.dump(summary, f, indent=2)
    print(f"  Saved summary JSON: {path}")


def main():
    parser = argparse.ArgumentParser(description="Analyze fairness A/B test results")
    parser.add_argument("--baseline", required=True, help="Path to baseline JSONL results")
    parser.add_argument("--program-aware", required=True, help="Path to program-aware JSONL results")
    parser.add_argument("--output", required=True, help="Output directory for plots and summary")
    args = parser.parse_args()

    os.makedirs(args.output, exist_ok=True)

    print("Loading results...")
    baseline_results = load_results(args.baseline)
    pa_results = load_results(args.program_aware)
    print(f"  Baseline: {len(baseline_results)} records")
    print(f"  Program-Aware: {len(pa_results)} records")

    baseline_groups = group_by_program(baseline_results)
    pa_groups = group_by_program(pa_results)

    print_comparison_table(baseline_groups, pa_groups)
    print_fairness_analysis(baseline_groups, pa_groups)
    print_throughput_check(baseline_results, pa_results)

    print("\nGenerating plots...")
    plot_cdf(baseline_groups, pa_groups, args.output)
    plot_cdf_overlay(baseline_groups, pa_groups, args.output)
    plot_bar_chart(baseline_groups, pa_groups, args.output)
    plot_timeseries(baseline_results, pa_results, args.output)
    save_summary_json(baseline_groups, pa_groups, args.output)

    print("\nDone!")


if __name__ == "__main__":
    main()
