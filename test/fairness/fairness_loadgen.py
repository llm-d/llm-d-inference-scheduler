#!/usr/bin/env python3
"""
Async load generator for fairness A/B testing.

Spawns one async task per program, each sending requests at a configured rate.
Each request includes the x-gateway-inference-fairness-id header.
Results are logged as JSONL for post-hoc analysis.

Usage:
    python3 fairness_loadgen.py \
        --gateway-url http://localhost:8080 \
        --model "Qwen/Qwen2-0.5B-Instruct" \
        --programs "prog-heavy:10,prog-medium:3,prog-light:1" \
        --duration 180 --warmup 30 --max-tokens 100 \
        --output results.jsonl
"""

import argparse
import asyncio
import json
import os
import sys
import time
import uuid
from dataclasses import dataclass, field

import aiohttp

# Fixed prompt (~200 tokens) used for all requests.
FIXED_PROMPT = (
    "Explain in detail the process of photosynthesis in plants, "
    "including the light-dependent reactions and the Calvin cycle. "
    "Describe the role of chlorophyll, the electron transport chain, "
    "and ATP synthase in converting light energy into chemical energy. "
    "Also discuss the factors that affect the rate of photosynthesis "
    "such as light intensity, carbon dioxide concentration, and temperature. "
    "Finally, explain how photosynthesis is related to cellular respiration "
    "and the overall carbon cycle in the ecosystem."
)


@dataclass
class ProgramConfig:
    name: str
    rate: float  # requests per second


@dataclass
class RequestResult:
    program_id: str
    request_id: str
    sent_at: float
    completed_at: float
    total_ms: float
    status: str
    prompt_tokens: int = 0
    completion_tokens: int = 0
    error: str = ""


@dataclass
class Stats:
    """Live stats tracker per program."""
    sent: int = 0
    completed: int = 0
    errors: int = 0
    total_latency_ms: float = 0.0
    latencies: list = field(default_factory=list)

    @property
    def avg_latency_ms(self) -> float:
        return self.total_latency_ms / self.completed if self.completed > 0 else 0.0

    @property
    def p50_ms(self) -> float:
        if not self.latencies:
            return 0.0
        s = sorted(self.latencies)
        return s[len(s) // 2]


def parse_programs(spec: str) -> list[ProgramConfig]:
    """Parse 'name:rate,name:rate,...' into ProgramConfig list."""
    programs = []
    for part in spec.split(","):
        name, rate = part.strip().split(":")
        programs.append(ProgramConfig(name=name.strip(), rate=float(rate.strip())))
    return programs


async def send_request(
    session: aiohttp.ClientSession,
    url: str,
    model: str,
    program: ProgramConfig,
    max_tokens: int,
    timeout_sec: float,
) -> RequestResult:
    """Send a single completion request and return timing result."""
    request_id = str(uuid.uuid4())[:8]
    payload = {
        "model": model,
        "prompt": FIXED_PROMPT,
        "max_tokens": max_tokens,
        "temperature": 0,
    }
    headers = {
        "Content-Type": "application/json",
        "x-gateway-inference-fairness-id": program.name,
    }

    sent_at = time.time()
    try:
        async with session.post(
            url,
            json=payload,
            headers=headers,
            timeout=aiohttp.ClientTimeout(total=timeout_sec),
        ) as resp:
            completed_at = time.time()
            total_ms = (completed_at - sent_at) * 1000

            if resp.status == 200:
                body = await resp.json()
                usage = body.get("usage", {})
                return RequestResult(
                    program_id=program.name,
                    request_id=request_id,
                    sent_at=sent_at,
                    completed_at=completed_at,
                    total_ms=total_ms,
                    status="ok",
                    prompt_tokens=usage.get("prompt_tokens", 0),
                    completion_tokens=usage.get("completion_tokens", 0),
                )
            else:
                text = await resp.text()
                return RequestResult(
                    program_id=program.name,
                    request_id=request_id,
                    sent_at=sent_at,
                    completed_at=completed_at,
                    total_ms=total_ms,
                    status=f"http_{resp.status}",
                    error=text[:200],
                )
    except asyncio.TimeoutError:
        completed_at = time.time()
        return RequestResult(
            program_id=program.name,
            request_id=request_id,
            sent_at=sent_at,
            completed_at=completed_at,
            total_ms=(completed_at - sent_at) * 1000,
            status="timeout",
            error=f"Request timed out after {timeout_sec}s",
        )
    except Exception as e:
        completed_at = time.time()
        return RequestResult(
            program_id=program.name,
            request_id=request_id,
            sent_at=sent_at,
            completed_at=completed_at,
            total_ms=(completed_at - sent_at) * 1000,
            status="error",
            error=str(e)[:200],
        )


async def program_sender(
    session: aiohttp.ClientSession,
    url: str,
    model: str,
    program: ProgramConfig,
    max_tokens: int,
    duration: float,
    warmup: float,
    timeout_sec: float,
    results: list[RequestResult],
    stats: Stats,
    start_time: float,
    concurrency_limit: int,
):
    """Send requests for one program at the configured rate."""
    interval = 1.0 / program.rate
    sem = asyncio.Semaphore(concurrency_limit)

    async def do_request():
        async with sem:
            result = await send_request(session, url, model, program, max_tokens, timeout_sec)
            stats.sent += 1
            elapsed = result.sent_at - start_time
            if elapsed >= warmup:
                results.append(result)
                if result.status == "ok":
                    stats.completed += 1
                    stats.total_latency_ms += result.total_ms
                    stats.latencies.append(result.total_ms)
                else:
                    stats.errors += 1

    tasks = []
    next_send = start_time
    while True:
        now = time.time()
        elapsed = now - start_time
        if elapsed >= duration + warmup:
            break

        if now < next_send:
            await asyncio.sleep(next_send - now)

        tasks.append(asyncio.create_task(do_request()))
        next_send += interval

    # Wait for all in-flight requests to complete (cooldown).
    if tasks:
        await asyncio.gather(*tasks, return_exceptions=True)


def print_live_status(programs: list[ProgramConfig], stats_map: dict[str, Stats], elapsed: float, duration: float):
    """Print a single-line status update."""
    parts = [f"\r[{elapsed:.0f}/{duration:.0f}s]"]
    for p in programs:
        s = stats_map[p.name]
        avg = f"{s.avg_latency_ms:.0f}" if s.completed > 0 else "-"
        parts.append(f" {p.name}:{s.completed}ok/{s.errors}err/avg={avg}ms")
    sys.stdout.write("".join(parts))
    sys.stdout.flush()


async def status_printer(
    programs: list[ProgramConfig],
    stats_map: dict[str, Stats],
    start_time: float,
    duration: float,
    warmup: float,
):
    """Periodically print live stats."""
    while True:
        await asyncio.sleep(5)
        elapsed = time.time() - start_time
        if elapsed >= duration + warmup:
            break
        print_live_status(programs, stats_map, elapsed, duration + warmup)
    sys.stdout.write("\n")


async def run_loadgen(args):
    programs = parse_programs(args.programs)
    total_rate = sum(p.rate for p in programs)

    print(f"Load generator configuration:")
    print(f"  Gateway:    {args.gateway_url}")
    print(f"  Model:      {args.model}")
    print(f"  Duration:   {args.duration}s (+ {args.warmup}s warmup)")
    print(f"  Max tokens: {args.max_tokens}")
    print(f"  Programs:")
    for p in programs:
        print(f"    {p.name}: {p.rate} req/s")
    print(f"  Total rate: {total_rate} req/s")
    print(f"  Output:     {args.output}")
    print()

    # Verify gateway is reachable.
    url = f"{args.gateway_url.rstrip('/')}/v1/completions"
    async with aiohttp.ClientSession() as session:
        try:
            test_payload = {
                "model": args.model,
                "prompt": "hello",
                "max_tokens": 1,
                "temperature": 0,
            }
            async with session.post(
                url,
                json=test_payload,
                headers={"Content-Type": "application/json"},
                timeout=aiohttp.ClientTimeout(total=30),
            ) as resp:
                if resp.status != 200:
                    text = await resp.text()
                    print(f"WARNING: Gateway returned {resp.status}: {text[:200]}")
                else:
                    print("Gateway health check passed.")
        except Exception as e:
            print(f"ERROR: Cannot reach gateway: {e}")
            sys.exit(1)

    # Run the test.
    results_map: dict[str, list[RequestResult]] = {p.name: [] for p in programs}
    stats_map: dict[str, Stats] = {p.name: Stats() for p in programs}
    start_time = time.time()

    print(f"\nStarting load generation (warmup={args.warmup}s, measurement={args.duration}s)...")

    async with aiohttp.ClientSession() as session:
        sender_tasks = []
        for p in programs:
            task = asyncio.create_task(
                program_sender(
                    session=session,
                    url=url,
                    model=args.model,
                    program=p,
                    max_tokens=args.max_tokens,
                    duration=args.duration,
                    warmup=args.warmup,
                    timeout_sec=args.timeout,
                    results=results_map[p.name],
                    stats=stats_map[p.name],
                    start_time=start_time,
                    concurrency_limit=args.concurrency,
                )
            )
            sender_tasks.append(task)

        printer_task = asyncio.create_task(
            status_printer(programs, stats_map, start_time, args.duration, args.warmup)
        )

        await asyncio.gather(*sender_tasks, return_exceptions=True)
        printer_task.cancel()

    # Combine and write results.
    all_results = []
    for p in programs:
        all_results.extend(results_map[p.name])
    all_results.sort(key=lambda r: r.sent_at)

    os.makedirs(os.path.dirname(os.path.abspath(args.output)), exist_ok=True)
    with open(args.output, "w") as f:
        for r in all_results:
            f.write(json.dumps(r.__dict__) + "\n")

    # Print summary.
    total_elapsed = time.time() - start_time
    print(f"\nDone in {total_elapsed:.1f}s. Wrote {len(all_results)} results to {args.output}")
    print()
    print(f"{'Program':<15} {'Sent':>6} {'OK':>6} {'Err':>5} {'Avg(ms)':>10} {'P50(ms)':>10}")
    print("-" * 60)
    for p in programs:
        s = stats_map[p.name]
        latencies = sorted(s.latencies) if s.latencies else []
        avg = f"{s.avg_latency_ms:.1f}" if s.completed > 0 else "-"
        p50 = f"{latencies[len(latencies)//2]:.1f}" if latencies else "-"
        print(f"{p.name:<15} {s.sent:>6} {s.completed:>6} {s.errors:>5} {avg:>10} {p50:>10}")
    print()


def main():
    parser = argparse.ArgumentParser(description="Fairness A/B load generator")
    parser.add_argument("--gateway-url", required=True, help="Gateway URL (e.g., http://localhost:8080)")
    parser.add_argument("--model", required=True, help="Model name for completions API")
    parser.add_argument("--programs", required=True, help="Comma-separated name:rate pairs (e.g., prog-heavy:10,prog-light:1)")
    parser.add_argument("--duration", type=int, default=180, help="Measurement duration in seconds (default: 180)")
    parser.add_argument("--warmup", type=int, default=30, help="Warmup duration in seconds (default: 30)")
    parser.add_argument("--max-tokens", type=int, default=100, help="Max tokens per request (default: 100)")
    parser.add_argument("--timeout", type=float, default=60, help="Per-request timeout in seconds (default: 60)")
    parser.add_argument("--concurrency", type=int, default=50, help="Max concurrent requests per program (default: 50)")
    parser.add_argument("--output", required=True, help="Output JSONL file path")
    args = parser.parse_args()
    asyncio.run(run_loadgen(args))


if __name__ == "__main__":
    main()
