# Configuration

`/etc/rt-node-agent/config.yaml` (Linux/macOS) or
`%ProgramData%\rt-node-agent\config.yaml` (Windows). Override the path with
`RT_AGENT_CONFIG=/some/where.yaml`.

All keys are optional. [examples/config.yaml](../examples/config.yaml) is the
canonical example — kept in sync with the embedded default.

## Top-level keys

| Key | Type | Default | Notes |
|---|---|---|---|
| `config_version` | int | (auto) | `2` for v0.2.0. The migrator reads this to decide whether to surface new keys on upgrade. Don't edit by hand. |
| `port` | int | 11435 | HTTP listener. Adjacent to Ollama's 11434 — operators remember the pair. |
| `bind` | string | `0.0.0.0` | Bind address. Override to `127.0.0.1` to restrict to localhost (then the case-manager backend won't reach it — usually only useful for testing). |
| `token` | string | (unset) | Inline bearer token. Prefer `token_file`. |
| `token_file` | string | `/etc/rt-node-agent/token` | Path to a file containing the token. Installer generates one at install time if absent. |
| `metrics_enabled` | bool | false | Expose `/metrics` (Prometheus text format). Off by default. |
| `platforms` | map | — | Per-platform inference settings; see below. |
| `ollama_endpoint` | string | `http://localhost:11434` | Legacy v0.1.x key. Mirrors `platforms.ollama.endpoint`. Removed in v0.3.0. |
| `service_allocators` | list | (empty) | PyTorch allocator scrape targets; see below. |
| `services` | map | — | Allowlisted systemctl units for `POST /actions/service`. |
| `disk` | map | (defaults) | Disk paths to surface under `/health.disk[]`. |
| `training_mode` | map | (defaults) | State file + auto-recovery grace. |
| `rdma` | map | (defaults) | RDMA collection thresholds. |

## `platforms.{ollama,vllm}`

| Key | Type | Default | Notes |
|---|---|---|---|
| `enabled` | string | `auto` | `auto` \| `true` \| `false`. `auto` probes once at startup and on every `/health` request if reachable. Setting `platforms.ollama.enabled: false` marks the node as vLLM-only — `platforms.ollama.up: false` keeps being reported, but `ollama_down` / `agent_stale` / `ollama_runner_stuck` are suppressed in `degraded_reasons` so the ranker doesn't hard-skip the node. |
| `endpoint` | string | per-platform | Base URL. Ollama: `http://localhost:11434`. vLLM: `http://localhost:8000`. |
| `metrics_endpoint` | string | `{endpoint}/metrics` | vLLM only. The Prometheus exposition is scraped for per-model queue depth, KV cache, latency histograms, and throughput. |
| `required` | bool | false | vLLM only. When true and the probe fails, `vllm_required_down` becomes a hard `degraded_reason`. Otherwise `vllm_down` is soft. |

## `services`

```yaml
services:
  manager: systemd       # currently only systemd. macOS/Windows handlers stub out.
  allowed:
    - name: rt-vllm-qwen3.service
      actions: [start, stop, restart, status]
      description: "vLLM serving qwen3-vl:32b"
    - name: rt-vllm-llama-70b.service
      actions: [start, restart, status]   # omit `stop` to enforce restart-only
```

- `name` must match a real unit. Pattern is enforced: the sudoers drop-in
  only permits `rt-vllm-*.service` — operators MUST name their vLLM units
  accordingly.
- Empty `actions` means "all" (`start, stop, restart, status`).
- See [remote-actions.md](remote-actions.md) for the security model.

## `service_allocators`

```yaml
service_allocators:
  - name: gliner2-service
    url: http://localhost:8077/v1/debug/gpu
    threshold_warn_mb: 4096
    threshold_critical_mb: 10240
    scrape_interval_s: 30
  - name: training-process
    url: http://localhost:8089/v1/debug/gpu
    threshold_warn_mb: 100000
    threshold_critical_mb: 120000
    scrape_interval_s: 10
    only_when_mode: training_mode   # skip scrape unless mode matches
```

- The endpoint must respond with at minimum `{"allocated_mb", "reserved_mb",
  "max_allocated_mb"}` as `float64`. Any extra fields are passed through to
  `/health.service_allocators[].extra` verbatim — training jobs use this to
  emit `run_id, step, epoch, loss_train, tokens_per_second`.
- `only_when_mode` (v0.2.0) suppresses scraping unless `/health.mode` matches.
- See [spec/SPEC.md](../spec/SPEC.md) §"Service allocator scraping" for thresholds.

## `disk`

```yaml
disk:
  paths:
    - /
    - /var/lib/ollama
    - /mnt/models
```

If `disk.paths` is empty (default), the agent monitors `/`,
`/var/lib/ollama`, `/var/lib/rt-node-agent`, plus any auto-discovered mount
≥ 50 GB (capped at 10 entries).

## `training_mode`

```yaml
training_mode:
  state_file: /var/lib/rt-node-agent/training_mode.json
  grace_period_s: 3600     # auto-exit if entered_at + expected + grace exceeded
  disable_ollama_probe: true
```

See [training-mode.md](training-mode.md) for the state machine.

## `rdma`

```yaml
rdma:
  enabled: auto
  collect_interval_s: 5
  pfc_storm_threshold_rx_rate: 1000
  pfc_storm_window_s: 30
  errors_growing_window_s: 60
```

See [rdma.md](rdma.md). Linux-only; ignored on macOS/Windows.

## Environment variables (override file)

| Variable | Maps to |
|---|---|
| `RT_AGENT_PORT` | `port` |
| `RT_AGENT_BIND` | `bind` |
| `RT_AGENT_TOKEN` | `token` |
| `RT_AGENT_OLLAMA` | `platforms.ollama.endpoint` |
| `RT_AGENT_VLLM` | `platforms.vllm.endpoint` |
| `RT_AGENT_METRICS` | `metrics_enabled` (set to `1` or `true`) |
| `RT_AGENT_CONFIG` | path to the config file itself |
