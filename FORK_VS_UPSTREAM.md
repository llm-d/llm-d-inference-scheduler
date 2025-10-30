# Fork vs Upstream: Differences Overview

Legend: UPSTREAM = inherited as-is from upstream; OURS = changes introduced in this fork.

## Dockerfile (OURS)
- Runtime: UBI9 minimal with microdnf only (no full dnf).
- Runtime installs Python 3.12; builder has Python dev headers for CGO.
- `render_jinja_template_wrapper.py` installed to `/usr/local/lib/python3.12/site-packages/`.
- Pip installs use `--no-cache-dir` and `--target /usr/local/lib/python3.12/site-packages` (single location).
- `PYTHONPATH=/usr/local/lib/python3.12/site-packages:/usr/lib/python3.12/site-packages`.
- `torch` filtered from requirements (not required for templating).
- `HF_HOME=/tmp/.cache`; symlink `python` and `python3` to 3.12.

## pkg/plugins/scorer/precise_prefix_cache.go (OURS)
- Use `msg.Content.Raw` when flattening chat messages (fixes 500 path with structured content).
- Add `PREPROCESSING:` info logs (init, fetch template, render, sizes, previews).
- Better error logs for `FetchChatTemplate` (includes model, HF env hints).

## pkg/plugins/profile/pd_profile_handler.go (OURS)
- Remove unused `preprocessRequest` and stray Python imports.
- Keep `getUserInputBytes` helper (PD logic), no behavior change.

## go.mod / go.sum (UPSTREAM)
- `github.com/llm-d/llm-d-kv-cache-manager` v0.3.2.
- `sigs.k8s.io/gateway-api-inference-extension` v1.1.0-rc.1.
- Align `k8s.io/*` and `controller-runtime` versions.

## Build flags (UPSTREAM)
- Use `version` package for ldflags (`CommitSHA`, `BuildRef`).

## Configuration (OURS)
- EPP profile: use `precise-prefix-cache-scorer` with `indexerConfig` (`blockSize: 64`, `hashSeed: "42"`).

## Docs (OURS)
- README notes reflect runtime Python and chat preprocessing flow.

## Not changed (UPSTREAM)
- Plugin registration (`plugins.RegisterAllPlugins`).
- Sidecar and unrelated packages.
