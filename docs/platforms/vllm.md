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
    additional_endpoints:       # v0.3.8 — further serves on this host
      - endpoint: http://localhost:8102
```

Setting `required: true` flips `vllm_down` from soft to hard
(`vllm_required_down`). Use this on nodes that exist to serve vLLM —
the dispatcher will then skip them when vLLM is unreachable, rather than
just deprioritizing.

## Several serves on one host (v0.3.8)

A host can run more than one vLLM serve, e.g. a LoRA shadow serve on its own
port beside the resident model. **The agent finds them itself**
(`platforms.discover`, default on): each GPU process is walked up its process
tree to the `vllm serve` process, and that process's own listening port is
probed. A serve found this way shows `source: discovered` in `endpoints[]`.
`additional_endpoints` remains as an override, for a serve discovery cannot
see (e.g. a container on a bridged network, whose port lives in another
network namespace).

Discovery only ever probes the API server's own ports. A `VLLM::Worker`
holds several listeners (NCCL / ZMQ); those are never touched.

### Multi-node serving

- **Ray leaf:** the GPU is held by Ray workers under `raylet`, with no API and
  no model on any local command line (vLLM does not keep weight files mapped
  after loading). `gpu_services[]` lists it as `kind: ray`, `role: leaf`,
  `head: <head host>`, and the models its head serves. The leaf reads them
  from the head's agent `/health` (the host from `ray.gcs_address`, same
  agent port, cached 60 s).
- **Ray head:** the GPU workers also sit under `raylet`, not under the API
  process, so serving API processes are found by a host scan as well. The
  API appears as its own service with `gpu_pids: []`, and its models label
  the head's `ray` service, which gets `role: head`.
- **`vllm serve --headless` leaf (no Ray):** `role: leaf`, with the model
  taken from its command line.

- Each serve is probed like the primary (`/v1/models` + `/metrics`, own 30 s
  cache). Its models are appended to `platforms.vllm.models[]`.
- Every vLLM model carries `endpoint`, the serve it came from, so a consumer
  can attribute models, queues and counters to the right port.
- A LoRA module carries `parent`, the base model it is served on. It has no
  queue/latency/counters of its own: vLLM labels its metrics with the base
  model's name, so LoRA traffic is counted on the `parent` entry.
- `platforms.vllm.endpoints[]` lists every serve, primary first, with
  `source` (`config` | `discovered`), `up`, `version` and `last_error`. A dead
  additional serve shows `up: false` and contributes no models. It is omitted
  when there is no further serve.
- `up`, `endpoint`, `vllm_down` and `vllm_required_down` keep describing the
  primary endpoint only. A down additional serve raises no degraded reason;
  read `endpoints[].up`.
- `runners[]` stays a host-wide scan of vLLM processes. It already listed every
  serve's processes.

## Histogram → percentile

vLLM exposes `time_to_first_token_seconds`, the per-output-token latency
histogram, and `e2e_request_latency_seconds` as histograms. The agent
computes p50 and p99 via linear interpolation between bucket edges (the
same algorithm Prometheus's `histogram_quantile` uses). Returned in
`/health` as milliseconds for convenience.

vLLM ≥0.6 renamed the per-output-token histogram
`time_per_output_token_seconds` → `request_time_per_output_token_seconds`
(and `gpu_cache_usage_perc` → `kv_cache_usage_perc`, and replaced the
`gpu_prefix_cache_hit_rate` gauge with `prefix_cache_hits_total` /
`prefix_cache_queries_total` counters). The agent reads the new names
first and falls back to the legacy names, so both old and new vLLM nodes
populate `kv_cache` and `tpot`.

If you need other quantiles, scrape `/metrics` directly and compute in
PromQL — the agent only surfaces p50/p99 to keep `/health` payload small.

## Eval telemetry (v0.2.13)

For serving-efficiency evaluation the agent additionally surfaces, per
model (all additive, nil when the underlying series is absent — i.e. on
pre-0.6 vLLM):

- `kv_cache.prefix_cache_hits_total` / `prefix_cache_queries_total` — the
  raw token counts behind `prefix_cache_hit_rate`, so consumers can compute
  *windowed* hit rates across their own scrape interval instead of the
  lifetime ratio (which washes out behavior changes).
- `latency_ms.prefill_p50/p99` and `decode_p50/p99` — per-request
  PREFILL / DECODE phase-time histograms
  (`vllm:request_prefill_time_seconds`, `vllm:request_decode_time_seconds`).
  Note these are request-phase durations, not token-level timings; TTFT
  also includes queue wait, these don't.
- `counters.prompt_tokens_cached_total` — cached prompt tokens (local +
  external), the per-model aggregate of the per-request `cached_tokens`.
- `counters.requests_failed_total` — now wired (was declared but never
  assigned): finished_reason `abort` + `error`. In the same change
  `requests_success_total` was fixed to sum across **all** finished_reason
  series minus failures — previously a first-match read reported only the
  first series (in practice `stop`), undercounting `length`/`repetition`
  completions.

vLLM does not expose a prefill-tokens counter separate from
`prompt_tokens_total` ("Number of prefill tokens processed" on ≥0.6), so
there is deliberately no `prefill_tokens_total` / `prefill_tokens_per_s` —
use `prompt_tokens_total` / `throughput.prompt_tokens_per_s`.

Per-*request* granularity (per-request cached_tokens, request logs) is out
of the agent's lane — read vLLM `/metrics` or request logs directly for
that; the agent provides node-level rollups.

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
