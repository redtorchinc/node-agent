# vLLM platform

Added in v0.2.0. Probes two endpoints:

- `GET /v1/models` — OpenAI-compatible model list. Drives `up` and the
  model identity (name, max_model_len, quantization).
- `GET /metrics` — Prometheus text exposition. Drives the rich per-model
  surface (queue depth, KV cache, latency histograms, token counters).

## Config

```yaml
platforms:
  vllm:
    enabled: auto
    endpoint: http://localhost:8000
    metrics_endpoint: http://localhost:8000/metrics
    required: false             # true ⇒ vllm_down becomes hard
```

Setting `required: true` flips `vllm_down` from soft to hard
(`vllm_required_down`). Use this on nodes that exist to serve vLLM —
the dispatcher will then skip them when vLLM is unreachable, rather than
just deprioritizing.

## Histogram → percentile

vLLM exposes `time_to_first_token_seconds`, `time_per_output_token_seconds`,
and `e2e_request_latency_seconds` as histograms. The agent computes p50
and p99 via linear interpolation between bucket edges (the same algorithm
Prometheus's `histogram_quantile` uses). Returned in `/health` as
milliseconds for convenience.

If you need other quantiles, scrape `/metrics` directly and compute in
PromQL — the agent only surfaces p50/p99 to keep `/health` payload small.

## Throughput rates

`throughput.prompt_tokens_per_s` and `throughput.generation_tokens_per_s`
are derived from two consecutive `/metrics` scrapes (`vllm:prompt_tokens_total`
and `vllm:generation_tokens_total` are cumulative counters). The first
`/health` after agent start emits `null` for the rates until a second
scrape lands (5-15 seconds depending on cache + scrape cadence).

## Runners

vLLM worker processes are identified by cmdline containing
`vllm.entrypoints` or starting with `vllm`. Each is reported under
`platforms.vllm.runners[]` with `pid`, `cpu_pct`, `rss_mb`.

## When the probe fails

- `/v1/models` failing ⇒ `up: false`, `last_error` populated, `models: []`.
  - `vllm_down` (soft) fires in `degraded_reasons` if `enabled != false`.
  - `vllm_required_down` (hard) fires instead if `required: true`.
- `/metrics` failing but `/v1/models` ok ⇒ `up: true`, models surfaced
  without queue / KV cache / latency fields. Operators see the loss in
  the absence of metric-derived fields rather than a hard failure.
