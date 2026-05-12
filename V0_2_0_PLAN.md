# rt-node-agent v0.2.0 — Implementation Plan

**Source:** ` changes.md` (the 6 bullets) + [spec/NODE_AGENT_TRAINING_EXTENSIONS.md](spec/NODE_AGENT_TRAINING_EXTENSIONS.md)
**Status:** Design — not yet implemented
**Backward compatibility:** All `/health` additions are additive; v0.1.x clients keep working. New endpoints return 404 on v0.1.x.

---

## Context

v0.1.0 shipped a complete inference-side agent: `/health`, `/metrics`, `/version`, `POST /actions/unload-model`, Bearer-auth on mutating endpoints, NVIDIA/Apple Silicon/Windows GPU paths, Ollama probe, service-allocator scraping for PyTorch leak detection (gliner2-service 2026-04-22 incident).

The fleet now needs four additional capabilities, captured in ` changes.md` and the standalone training-extensions doc:

1. **Operators reinstall in place** without losing `/etc/rt-node-agent/config.yaml` or having new config keys silently absent. Today the bootstrap is "won't overwrite" — fine for token, wrong for config (new keys never appear).
2. **vLLM is a first-class peer of Ollama** on DGX-class nodes. The dispatcher needs to see both.
3. **Remote start/stop/restart of vLLM model services** on DGX nodes, scoped to a config-declared allowlist, Bearer-gated. No remote shell.
4. **Training-plane visibility** — RDMA fabric health, mode coordination (`inference` ↔ `training_mode`), training-process allocator scrape. Full spec already exists in `NODE_AGENT_TRAINING_EXTENSIONS.md`; this plan sequences it as Phase B.

This is a single `v0.2.0` release tag. Phase A and Phase B ship together; Phase B is broken out only so review and PRs can land in sequence.

---

## Architectural constants (unchanged)

These come from SPEC.md and CLAUDE.md and **do not move**:

- Single static Go binary, no CGO, stdlib router, `gopsutil/v3` only for system stats.
- Listen `0.0.0.0:11435`. Read endpoints open on LAN. Mutating endpoints require `Authorization: Bearer`.
- **No remote shell, no file read/write endpoints, no arbitrary command execution. Ever.**
- Pull-based only. Agent never pushes to the backend. No persistence on the node *except* the new training-mode state file (single small JSON, recoverable on crash).
- Cross-platform: same binary serves Linux/DGX, macOS Apple Silicon, macOS Intel, Windows. Platform-specific code stays behind build tags in `internal/`.
- Public-repo hygiene: no real hostnames, no tokens, no internal URLs in commits or examples.

---

## Phase A — `changes.md` scope

### A1. Config preservation across install/upgrade

**Problem.** [scripts/install.sh](scripts/install.sh) calls `rt-node-agent install`, which bootstraps `/etc/rt-node-agent/` only if absent. Reinstalling on a node with a newer agent binary keeps the *old* `config.yaml` — new keys (vLLM, services, RDMA, training_mode) are silently missing. Operator has no way to know what to add. The current `installed-then-reinstall` path is therefore a quiet bug for any release that introduces new config keys.

**Design.**

- Introduce a **versioned config schema**. Add `config_version: 2` at the top of `config.yaml`. v0.1.x configs are treated as `config_version: 1` (key absent).
- New subcommand `rt-node-agent config migrate` (also run automatically from `rt-node-agent install` when an existing config is detected):
  - Loads existing `/etc/rt-node-agent/config.yaml`.
  - Compares to the embedded default tree (compiled-in via `embed.FS`, sourced from `examples/config.yaml`).
  - Three-way merge:
    - Existing values preserved verbatim (including comments — see implementation note below).
    - New keys appended in a clearly-delimited `# ── v0.2.0 additions (review and uncomment as needed) ──` block, fully commented out.
    - Deprecated keys retained but annotated `# DEPRECATED in v0.2.0 — safe to remove`.
  - Writes to `config.yaml.new`, prints a unified diff via `diff -u` (or Go `difflib`), and on TTY-attached install prompts `y/N` to apply.
- **Non-interactive install** (`curl|sudo sh`, the common path): always writes `config.yaml.new` and prints the diff to stdout + a banner: `New config keys available. Review with: diff /etc/rt-node-agent/config.yaml{,.new} && mv /etc/rt-node-agent/config.yaml{.new,}`. Never overwrites without operator approval. Memory `install_ux` flags manual steps as bugs — this is the one exception: silently merging operator-edited config is a worse failure mode than asking once.
- Token file (`/etc/rt-node-agent/token`) and state file (`/var/lib/rt-node-agent/training_mode.json`) are **never** touched on reinstall/upgrade.

**YAML comment preservation.** `yaml.v3` round-trips comments only if the file is parsed into `*yaml.Node`, not into a struct. Implementation:

- New package `internal/config/migrate/` does the Node-tree merge.
- Loading into the runtime `Config` struct keeps using `yaml.Unmarshal` (existing code in [internal/config/config.go](internal/config/config.go)) — no change to the hot path.

**Reuse.** [internal/config/config.go](internal/config/config.go) struct gets new fields (see A2/A3/B below); the embedded default is loaded via `//go:embed examples/config.yaml`.

**Critical files:**
- [internal/config/migrate/migrate.go](internal/config/migrate/migrate.go) (new)
- [cmd/rt-node-agent/main.go](cmd/rt-node-agent/main.go) — add `config migrate` subcommand (~30 lines)
- [internal/service/bootstrap.go](internal/service/bootstrap.go) — call into migrate when existing config detected
- [scripts/install.sh](scripts/install.sh), [scripts/install.ps1](scripts/install.ps1) — print the post-install banner if migration produced a `.new` file

---

### A2. Platform sensing — vLLM peer to Ollama

**Problem.** Today [internal/ollama/](internal/ollama/) is the only platform abstraction and it's hard-wired into [internal/health/report.go](internal/health/report.go) as a single field. DGX nodes increasingly run vLLM (often *and* Ollama) and the dispatcher needs both surfaced.

**Design.**

- New package `internal/platforms/` with a single small interface:

  ```go
  type Platform interface {
      Name() string                          // "ollama", "vllm"
      Detect(ctx context.Context) bool       // cheap probe (HTTP GET to known port)
      Report(ctx context.Context) PlatformReport
  }

  type PlatformReport struct {
      Up        bool           `json:"up"`
      Endpoint  string         `json:"endpoint"`
      Models    []PlatformModel `json:"models"`
      Runners   []Runner       `json:"runners,omitempty"`
      LastError string         `json:"last_error,omitempty"`
  }
  ```

- Move existing Ollama logic into `internal/platforms/ollama/` (rename of [internal/ollama/](internal/ollama/) — keep the file contents, change the package path). Implement `Platform` on top.
- New `internal/platforms/vllm/`:
  - Probe `GET {endpoint}/v1/models` (OpenAI-compatible). Default endpoint `http://localhost:8000`. Configurable via `config.yaml`.
  - Parse the OpenAI-shape model list. For each entry, surface `id` as `name`, plus `created` timestamp.
  - vLLM exposes `/metrics` (Prometheus). Optionally scrape `vllm:num_requests_running`, `vllm:gpu_cache_usage_perc`, `vllm:num_requests_waiting` and surface them under `runners[].queued_requests` and a new `queue_depth` field. 2s timeout, 5s cache (mirrors Ollama client).
  - Runners: enumerate processes via `gopsutil` whose cmdline starts with `vllm` or matches `python.*-m vllm.entrypoints`. Report PID, CPU%, RSS, queue depth.

- New `/health` shape:

  ```json
  "platforms": {
    "ollama": { "up": true, "endpoint": "...", "models": [...], "runners": [...] },
    "vllm":   { "up": true, "endpoint": "...", "models": [...], "runners": [...] }
  }
  ```

  The legacy top-level `"ollama"` key is **preserved verbatim** for the duration of v0.2.x (alias write — same data emitted twice). Frozen-schema contract from SPEC.md §"Field-removal policy". Deprecation banner added to README; removal scheduled for v0.3.0.

#### A2.1 Unified per-model metric surface

The dispatcher and remote operators need a **single model schema** regardless of which platform serves it. vLLM exposes far more detail than Ollama; the agent emits a superset where each field is filled by the best available source and `null` (or omitted via `omitempty`) elsewhere — never invented.

Per-model shape under `platforms.{ollama,vllm}.models[]`:

```json
{
  "name": "qwen3-vl:32b",
  "platform": "vllm",
  "loaded": true,
  "size_mb": 32000,
  "quantization": "AWQ",
  "context_window": 32768,
  "max_model_len": 32768,
  "processor_split": "100% GPU",
  "ttl_s": null,
  "vram_used_mb": 31800,
  "queue": { "running": 2, "waiting": 0, "swapped": 0 },
  "kv_cache": {
    "gpu_usage_pct": 74.3,
    "cpu_usage_pct": 0.0,
    "prefix_cache_hit_rate": 0.61
  },
  "latency_ms": {
    "ttft_p50": 142,
    "ttft_p99": 480,
    "tpot_p50": 21,
    "tpot_p99": 38,
    "e2e_p50": 1380,
    "e2e_p99": 6210
  },
  "throughput": {
    "prompt_tokens_per_s": 4180.2,
    "generation_tokens_per_s": 312.7,
    "tokens_per_s": 4492.9
  },
  "counters": {
    "requests_success_total": 18472,
    "requests_failed_total": 14,
    "prompt_tokens_total": 92481723,
    "generation_tokens_total": 8472398
  },
  "last_scrape_ts": 1746489600
}
```

**Source-of-truth table.** Field marked `n/a` = source platform genuinely cannot provide it; emitted as `null` (or omitted on `omitempty`). No heuristic backfill — silence is better than fabrication.

| Field | Ollama source | vLLM source |
|---|---|---|
| `name` | `/api/ps` `name` | `/v1/models` `id` |
| `loaded` | true if returned by `/api/ps` | true if listed by `/v1/models` |
| `size_mb` | `/api/ps` `size` | `/v1/models` `meta.size_bytes` if present, else `null` |
| `quantization` | parse from name suffix (`:q4_K_M`) | `/v1/models` `meta.quantization` |
| `context_window` | `/api/ps` `context` | `/v1/models` `meta.max_model_len` |
| `max_model_len` | same as `context_window` | `/v1/models` `meta.max_model_len` |
| `processor_split` | `/api/ps` `processor` (e.g. "100% GPU") | always `"100% GPU"` (vLLM is GPU-only) |
| `ttl_s` | `/api/ps` `expires_at - now` | `n/a` (vLLM keeps loaded) |
| `vram_used_mb` | correlate `nvidia-smi --query-compute-apps=pid,used_memory` to Ollama runner PIDs | sum of `nvidia-smi` VRAM for vLLM worker PIDs |
| `queue.running` | `/api/ps` `queued_requests` if present, else `n/a` | `vllm:num_requests_running{model_name}` |
| `queue.waiting` | `n/a` | `vllm:num_requests_waiting{model_name}` |
| `queue.swapped` | `n/a` | `vllm:num_requests_swapped{model_name}` |
| `kv_cache.gpu_usage_pct` | `n/a` | `vllm:gpu_cache_usage_perc{model_name}` × 100 |
| `kv_cache.cpu_usage_pct` | `n/a` | `vllm:cpu_cache_usage_perc{model_name}` × 100 |
| `kv_cache.prefix_cache_hit_rate` | `n/a` | `vllm:gpu_prefix_cache_hit_rate{model_name}` |
| `latency_ms.ttft_p*` | `n/a` | `vllm:time_to_first_token_seconds` histogram |
| `latency_ms.tpot_p*` | `n/a` | `vllm:time_per_output_token_seconds` histogram |
| `latency_ms.e2e_p*` | `n/a` | `vllm:e2e_request_latency_seconds` histogram |
| `throughput.prompt_tokens_per_s` | `n/a` | derived: `rate(vllm:prompt_tokens_total[1m])` |
| `throughput.generation_tokens_per_s` | `n/a` | derived: `rate(vllm:generation_tokens_total[1m])` |
| `counters.requests_success_total` | `n/a` | `vllm:request_success_total{model_name}` |
| `counters.requests_failed_total` | `n/a` | sum across `vllm:request_*_total{finish_reason!=stop,length}` |
| `counters.prompt_tokens_total` | `n/a` | `vllm:prompt_tokens_total{model_name}` |
| `counters.generation_tokens_total` | `n/a` | `vllm:generation_tokens_total{model_name}` |

**vLLM `/metrics` scrape implementation.**

- Pull `{endpoint}/metrics` every 10s (configurable), parse Prometheus text format (small handwritten parser — no Prometheus client dep for parsing, only for writing).
- Histograms (TTFT, TPOT, e2e) — compute percentiles from buckets server-side using linear interpolation across bucket edges. Same approach Grafana uses for `histogram_quantile`. Pure function on `[]bucket{le, count}`.
- Rates (`prompt_tokens_per_s`, `requests_per_s`) — derived from cumulative counters across two consecutive scrapes (60s window default). First scrape after agent start emits `null` for rates until the second snapshot lands.
- Cache last scrape; `/health` reads from cache. Never blocks `/health` on a live scrape.

**Per-model VRAM correlation (both platforms).** `nvidia-smi --query-compute-apps=pid,process_name,used_memory --format=csv,noheader` already runs in [internal/gpu/nvidia_smi.go](internal/gpu/nvidia_smi.go). Extend to:

- For each Ollama runner PID (from existing [internal/ollama/runners.go](internal/ollama/runners.go)), match against `/api/ps` model→PID (Ollama runner cmdline includes model path); attribute the VRAM to that model.
- For vLLM, the worker PID is the FastAPI process; multi-replica vLLM splits one model across multiple workers — sum the matched PIDs into the model.

**Wire response, `/health` excerpt:**

```json
"platforms": {
  "vllm": {
    "up": true,
    "endpoint": "http://localhost:8000",
    "version": "0.6.2",
    "models": [ /* per-model shape above */ ],
    "runners": [
      {"pid": 123456, "cpu_pct": 412.0, "rss_mb": 28400, "queue_depth": 2}
    ],
    "last_scrape_ts": 1746489600
  }
}
```

**Prometheus exposition.** Each new model field gets a series with `platform` and `model` labels:

```
rt_node_model_loaded{platform="vllm",model="qwen3-vl:32b"} 1
rt_node_model_vram_used_mb{platform="vllm",model="qwen3-vl:32b"} 31800
rt_node_model_queue_running{platform="vllm",model="qwen3-vl:32b"} 2
rt_node_model_queue_waiting{platform="vllm",model="qwen3-vl:32b"} 0
rt_node_model_kv_cache_gpu_usage_pct{platform="vllm",model="qwen3-vl:32b"} 74.3
rt_node_model_kv_cache_prefix_hit_rate{platform="vllm",model="qwen3-vl:32b"} 0.61
rt_node_model_ttft_seconds{platform="vllm",model="qwen3-vl:32b",quantile="0.5"} 0.142
rt_node_model_ttft_seconds{platform="vllm",model="qwen3-vl:32b",quantile="0.99"} 0.480
rt_node_model_tpot_seconds{platform="vllm",model="qwen3-vl:32b",quantile="0.5"} 0.021
rt_node_model_requests_success_total{platform="vllm",model="qwen3-vl:32b"} 18472
rt_node_model_prompt_tokens_total{platform="vllm",model="qwen3-vl:32b"} 92481723
```

Label cardinality is bounded by the live model count per node (typically ≤4).

**Critical files (additions):**
- [internal/platforms/vllm/metrics.go](internal/platforms/vllm/metrics.go) (new) — Prometheus text parser + histogram percentile + rate compute
- [internal/platforms/model.go](internal/platforms/model.go) (new) — shared `Model` struct, the canonical schema
- [internal/platforms/vram_attribution.go](internal/platforms/vram_attribution.go) (new) — PID→model attribution shared by Ollama and vLLM

- New config keys:

  ```yaml
  platforms:
    ollama:
      enabled: auto             # auto | true | false
      endpoint: http://localhost:11434
    vllm:
      enabled: auto
      endpoint: http://localhost:8000
      metrics_endpoint: http://localhost:8000/metrics
  ```

  `enabled: auto` = probe once at startup; if reachable, keep checking on every `/health` request.

**Reuse.**
- [internal/ollama/client.go](internal/ollama/client.go) — generalize cache pattern, move to `internal/platforms/ollama/`.
- [internal/ollama/runners.go](internal/ollama/runners.go) — generalize process-scan pattern; share between Ollama and vLLM detectors as `internal/platforms/runners.go`.

**Critical files:**
- [internal/platforms/platform.go](internal/platforms/platform.go) (new, interface)
- [internal/platforms/ollama/](internal/platforms/ollama/) (moved from internal/ollama/)
- [internal/platforms/vllm/](internal/platforms/vllm/) (new)
- [internal/platforms/runners.go](internal/platforms/runners.go) (new, shared)
- [internal/health/report.go](internal/health/report.go) — replace `Ollama` field with `Platforms map[string]PlatformReport`, also emit legacy `Ollama` alias
- [internal/config/config.go](internal/config/config.go) — add `Platforms` map

---

### A3. DGX service control — allowlisted systemctl actions

**Problem.** Operators currently SSH to DGX nodes to `systemctl start rt-vllm-qwen3.service` etc. Per `changes.md`, the agent should expose this remotely with Bearer auth and a config allowlist.

**Design — security-first.**

This is the single biggest blast-radius change in v0.2.0. The design follows the same instinct as the existing `/actions/unload-model` endpoint:

- **No unit creation by the agent.** Operator pre-creates units (typically via Ansible or the DGX baseline image). Agent only **acts on** existing units.
- **No arbitrary args.** The wire protocol carries only `unit` (string, must match the configured allowlist exactly) and `action` (enum: `start | stop | restart | status`). No environment, no override args.
- **Allowlist is config-only.** Cannot be modified at runtime. Mutating endpoints reject any unit not in `services.allowed[].name`.
- **Reload action excluded from v1.** `systemctl daemon-reload` requires loading new unit files, which is exactly the boundary we won't cross.

**Wire protocol.**

```http
POST /actions/service HTTP/1.1
Authorization: Bearer <token>
Content-Type: application/json

{"unit": "rt-vllm-qwen3.service", "action": "start"}
```

Response:

```json
{
  "status": "ok",
  "unit": "rt-vllm-qwen3.service",
  "action": "start",
  "active_state": "active",
  "sub_state": "running",
  "took_ms": 312
}
```

Errors: `401` bad token, `403` unit not in allowlist, `404` unit not found by systemd, `409` action not permitted for this unit (e.g. `stop` on a unit configured `actions: [start, restart]` only), `503` token not configured, `500` systemd unreachable.

**Config.**

```yaml
services:
  manager: systemd                # systemd | launchd | windows-svc. Default: detect.
  allowed:
    - name: rt-vllm-qwen3.service
      actions: [start, stop, restart, status]
      description: "vLLM serving qwen3-vl:32b"
    - name: rt-vllm-llama-70b.service
      actions: [start, restart, status]   # no stop — let restart suffice
```

`manager: systemd` is required for v0.2.0 (DGX is Linux). The struct admits launchd/windows-svc so v0.3 can extend; calling the endpoint on a non-systemd host returns 501.

**Implementation.**

- Shell out to `systemctl --no-pager --no-ask-password` with the action + unit. **Never** concatenate; pass `unit` as a discrete `exec.Cmd` argument (`/bin/systemctl`, `[]string{action, unit}`) — Go's `exec.Cmd` doesn't invoke a shell, so injection via unit name is structurally impossible. Still validate `unit` against allowlist *before* exec.
- `status` action additionally parses `systemctl show <unit> --property=ActiveState,SubState,MainPID,MemoryCurrent` and returns those fields. No process tree, no logs.
- Service info also surfaces under `/health` (read-only, no auth):

  ```json
  "services": [
    {"unit": "rt-vllm-qwen3.service", "active_state": "active", "sub_state": "running", "main_pid": 12345, "memory_mb": 8192}
  ]
  ```

  Only allowlisted units appear. `systemctl show` runs on the same 30s health cadence (cached). This is the "full visibility" piece of bullet #4 for service state.

**Sudo / privileges.** The agent runs as the `rt-agent` system user (created by [internal/service/systemd.go](internal/service/systemd.go)). On DGX, `rt-agent` cannot `systemctl start` arbitrary units. Install path adds a sudoers drop-in:

```
# /etc/sudoers.d/rt-node-agent
rt-agent ALL=(root) NOPASSWD: /bin/systemctl start rt-vllm-*.service, \
                              /bin/systemctl stop rt-vllm-*.service, \
                              /bin/systemctl restart rt-vllm-*.service, \
                              /bin/systemctl status rt-vllm-*.service, \
                              /bin/systemctl show rt-vllm-*.service
```

- **Pattern is hardcoded to `rt-vllm-*.service`** — operators must name their units accordingly. Documented in `/docs/remote-actions.md`. This is the safety rail: a misconfigured allowlist can't be used to start `sshd` or `docker`.
- Sudoers file is written by [internal/service/systemd.go](internal/service/systemd.go) `install()` only on Linux. `visudo -cf` validation before placement. Uninstall removes it.

**Critical files:**
- [internal/services/manager.go](internal/services/manager.go) (new — abstraction over systemd/launchd/winsvc)
- [internal/services/systemd_linux.go](internal/services/systemd_linux.go) (new, build tag `linux`)
- [internal/services/stub_other.go](internal/services/stub_other.go) (new, build tags `!linux`)
- [internal/server/handlers.go](internal/server/handlers.go) — new `handleServiceAction`, wrap with existing `requireToken` middleware from [internal/server/auth.go](internal/server/auth.go)
- [internal/server/server.go](internal/server/server.go) — register `POST /actions/service`
- [internal/service/systemd.go](internal/service/systemd.go) — write sudoers drop-in on install, remove on uninstall
- [internal/config/config.go](internal/config/config.go) — `Services` struct

---

### A4. Expanded `/health` and `/metrics` — system metrics across every architecture

"Full visibility so remote users can plan, monitor, execute" means the `/health` surface must give a *complete* hardware picture on every supported platform, with each field's source documented and `null` used (never invented) when the platform genuinely cannot supply it.

#### A4.1 CPU — full breakdown

Today `/health.cpu` has only `cores_*` and `load_*`. Extend to:

```json
"cpu": {
  "model": "AMD EPYC 9654 96-Core Processor",
  "vendor": "AuthenticAMD",
  "cores_physical": 96,
  "cores_logical": 192,
  "freq_mhz_current": 3712,
  "freq_mhz_min": 1500,
  "freq_mhz_max": 3712,
  "usage_pct": 41.2,
  "usage_per_core_pct": [38.4, 42.1, ...],
  "load_1m": 28.4,
  "load_5m": 22.8,
  "load_15m": 19.1,
  "temps_c": [
    {"sensor": "Tctl",  "value": 58.4},
    {"sensor": "Tdie",  "value": 56.2},
    {"sensor": "core0", "value": 54.0}
  ],
  "throttled": false,
  "throttle_reasons": []
}
```

- `usage_pct` / `usage_per_core_pct` — `gopsutil/cpu.Percent(0, true/false)`, 1s sample, cached 2s.
- `freq_mhz_*` — `gopsutil/cpu.Info()` (per-core average); on Linux also `/sys/devices/system/cpu/cpu*/cpufreq/scaling_cur_freq`.
- `temps_c` — `gopsutil/host.SensorsTemperatures()`. Linux backs onto `/sys/class/hwmon`. macOS uses `smc`/`powermetrics`. Windows uses WMI `MSAcpi_ThermalZoneTemperature`. Whichever sensors the platform exposes is what appears — no synthetic top-line value.
- `load_*` — gopsutil; **omitted on Windows** (no kernel-level load avg). Emit `null`, not a fake.
- `throttled` — Linux `/proc/cpuinfo` cpu_throttle_count delta + `/sys/devices/system/cpu/intel_pstate/no_turbo` if Intel; cross-checked with thermal sensor near max. On AMD EPYC use `msr-tools` only if root (otherwise omit). macOS: `pmset -g thermlog` or `sysctl machdep.xcpm.cpu_thermal_level > 0`. Windows: WMI `Win32_Processor.LoadPercentage` + thermal state.

#### A4.2 Memory — total / used / swap / unified

Today's struct is close to complete. Extensions:

```json
"memory": {
  "total_mb": 524288,
  "used_mb": 318420,
  "used_pct": 60.7,
  "available_mb": 205868,
  "buffers_mb": 4096,
  "cached_mb": 184320,
  "swap_total_mb": 16000,
  "swap_used_mb": 0,
  "swap_used_pct": 0.0,
  "unified": false,
  "pressure": "normal",
  "huge_pages_total": 2048,
  "huge_pages_free": 0
}
```

- `available_mb`, `buffers_mb`, `cached_mb` — already returned by `gopsutil/mem.VirtualMemory()`, just surface them.
- `pressure` — Linux `/proc/pressure/memory` (PSI, kernel 4.20+) → `normal | some | full`. macOS `memory_pressure` command or `vm.memory_pressure` sysctl. Windows `GetMemoryStatusEx().dwMemoryLoad` thresholded. Defensible cross-platform mapping documented in [docs/api/health.md](docs/api/health.md).
- `unified: true` already set on Apple Silicon by [internal/mem/unified_darwin_arm64.go](internal/mem/unified_darwin_arm64.go) — keep.
- `huge_pages_*` — Linux `/proc/meminfo`. Critical for DGX (`HugePages_Total` reservation for some PyTorch builds). `null` elsewhere.

#### A4.3 GPU — full per-device profile

Extend each `gpus[]` entry. Today: `name`, `vram_*`, `util_pct`, `temp_c`, `power_w`, `power_cap_w`, `processes`. Add:

```json
{
  "index": 0,
  "uuid": "GPU-abc12345-...",
  "name": "NVIDIA H100 80GB HBM3",
  "driver_version": "550.54.15",
  "cuda_version": "12.4",
  "compute_capability": "9.0",
  "pci_bus_id": "00000000:1A:00.0",
  "vram_total_mb": 81920,
  "vram_used_mb": 41280,
  "vram_used_pct": 50.4,
  "vram_reserved_mb": 41280,
  "util_pct": 78,
  "memory_util_pct": 62,
  "encoder_util_pct": 0,
  "decoder_util_pct": 0,
  "temp_c": 71,
  "temp_memory_c": 78,
  "power_w": 412,
  "power_cap_w": 700,
  "clock_graphics_mhz": 1980,
  "clock_memory_mhz": 2619,
  "clock_sm_mhz": 1980,
  "clock_graphics_max_mhz": 1980,
  "throttle_reasons": [],
  "ecc_volatile_uncorrected": 0,
  "ecc_aggregate_uncorrected": 0,
  "fan_pct": null,
  "persistence_mode": "Enabled",
  "compute_mode": "Default",
  "mig_mode": "Disabled",
  "mig_devices": [],
  "nvlink": {
    "supported": true,
    "links": [
      {"link": 0, "state": "Up", "speed_gb_s": 50, "peer_gpu_index": 1},
      {"link": 1, "state": "Up", "speed_gb_s": 50, "peer_gpu_index": 2}
    ]
  },
  "processes": [
    {"pid": 556534, "name": "python3", "cmdline_head": "vllm worker", "vram_used_mb": 31800, "type": "C"}
  ]
}
```

Source: extended `nvidia-smi --query-gpu=...` CSV — already the pattern in [internal/gpu/nvidia_smi.go](internal/gpu/nvidia_smi.go). Single shell-out gathers everything in one CSV; per-link NVLink details from `nvidia-smi nvlink --status` parsed once per `/health`. Throttle reasons from `nvidia-smi --query-gpu=clocks_throttle_reasons.active`.

**Apple Silicon equivalent:**

```json
{
  "index": 0,
  "name": "Apple M2 Ultra (76-core GPU)",
  "compute_capability": null,
  "vram_total_mb": null,
  "vram_used_mb": null,
  "vram_unified": true,
  "util_pct": 34,
  "temp_c": 52,
  "power_w": 38,
  "throttle_reasons": [],
  "processes": []
}
```

- GPU util: `ioreg -l -w 0 -r -c AGXAccelerator | grep "Device Utilization %"` (no-root). Cached 5s.
- Temp: `powermetrics --samplers thermal -i 1000 -n 1` (root-only) → if not root, omit. Document.
- Power: same `powermetrics --samplers gpu_power` — root only.
- VRAM: `null` because unified — system memory tells the same story (`memory.unified: true` is the cue).
- Per-process VRAM: `null` — no such concept under unified memory. The `processes` array is reserved (always emit, possibly empty).

**Windows NVIDIA** uses the same `nvidia-smi` path as Linux. Process correlation uses Windows PIDs.

**Per-architecture source table** (this is the canonical answer to "support all different architectures"):

| Field | Linux NVIDIA (amd64) | Linux NVIDIA (arm64 / Grace Hopper) | macOS Apple Silicon | macOS Intel + eGPU | Windows NVIDIA |
|---|---|---|---|---|---|
| CPU model/vendor | `gopsutil` | `gopsutil` | `gopsutil` | `gopsutil` | `gopsutil` |
| CPU usage % | `gopsutil` | `gopsutil` | `gopsutil` | `gopsutil` | `gopsutil` |
| CPU per-core % | `gopsutil` | `gopsutil` | `gopsutil` | `gopsutil` | `gopsutil` |
| CPU load avg | `gopsutil` | `gopsutil` | `gopsutil` | `gopsutil` | **`null`** (Windows has no load avg) |
| CPU freq | `/sys cpufreq` + gopsutil | same | `sysctl hw.cpufrequency` | `sysctl` | `gopsutil` (WMI) |
| CPU temps | `/sys/class/hwmon` | same (DGX hwmon present) | `powermetrics` root, else `null` | SMC via `smcFanControl`-style read or `null` | WMI `MSAcpi_ThermalZoneTemperature` |
| CPU throttle | `/proc cpuinfo` deltas + intel/amd specifics | same | `pmset -g thermlog` parse | `pmset` | WMI thermal state |
| Mem total/used/swap | `gopsutil` | `gopsutil` | `gopsutil` (`unified: true`) | `gopsutil` | `gopsutil` (pagefile) |
| Mem pressure | `/proc/pressure/memory` (PSI) | same | `memory_pressure` cmd | `memory_pressure` cmd | WMI memory load |
| Huge pages | `/proc/meminfo` | same | `null` | `null` | `null` |
| GPU presence | `nvidia-smi -L` | `nvidia-smi -L` | `ioreg AGXAccelerator` | `nvidia-smi` if present, else CPU-only | `nvidia-smi -L` |
| GPU VRAM | `nvidia-smi` | `nvidia-smi` | `null` (unified — see `memory.unified`) | `nvidia-smi` | `nvidia-smi` |
| GPU util | `nvidia-smi` | `nvidia-smi` | `ioreg` Device Utilization | `nvidia-smi` | `nvidia-smi` |
| GPU temp | `nvidia-smi` | `nvidia-smi` | `powermetrics` root only, else `null` | `nvidia-smi` | `nvidia-smi` |
| GPU power | `nvidia-smi` | `nvidia-smi` | `powermetrics` root only, else `null` | `nvidia-smi` | `nvidia-smi` |
| GPU clocks | `nvidia-smi` | `nvidia-smi` | `null` | `nvidia-smi` | `nvidia-smi` |
| GPU throttle reasons | `nvidia-smi clocks_throttle_reasons.active` | same | `null` | `nvidia-smi` | `nvidia-smi` |
| GPU ECC | `nvidia-smi --query-gpu=ecc.errors.*` | same | `null` | `nvidia-smi` | `nvidia-smi` |
| Per-process VRAM | `nvidia-smi --query-compute-apps` | same | `null` (unified) | `nvidia-smi` | `nvidia-smi` |
| NVLink | `nvidia-smi nvlink` | `nvidia-smi nvlink` | `null` | `null` | `nvidia-smi nvlink` |
| MIG | `nvidia-smi --query-gpu=mig.mode.current` | same (H100 supports MIG; Grace Hopper too) | `null` | `null` (consumer GPUs don't MIG) | `nvidia-smi` |
| Disk usage | `gopsutil/disk` | `gopsutil/disk` | `gopsutil/disk` | `gopsutil/disk` | `gopsutil/disk` |
| Network ifaces | `gopsutil/net` | `gopsutil/net` | `gopsutil/net` | `gopsutil/net` | `gopsutil/net` |
| Clock skew | `chronyc` / `timedatectl` | same | `sntp` query or `null` | `sntp` or `null` | `w32tm /stripchart` parse or `null` |
| RDMA (Phase B) | `/sys/class/infiniband` | `/sys/class/infiniband` (GH200) | `null` | `null` | `null` |

Build tags partition the implementation:
- `internal/sysmetrics/cpu_linux.go`, `cpu_darwin.go`, `cpu_windows.go`
- `internal/sysmetrics/gpu_nvidia.go` (shared across Linux/Windows; amd64 + arm64), `gpu_apple_darwin_arm64.go`, `gpu_noop.go`
- `internal/sysmetrics/temps_{linux,darwin,windows}.go`
- `internal/sysmetrics/throttle_{linux,darwin,windows}.go`

**Arm64 NVIDIA path** explicitly tested: DGX Grace Hopper is `linux/arm64`. The `nvidia-smi` CSV output is identical to amd64 — verify in CI with a Grace Hopper runner *or* with golden-file fixtures captured from a real GH200 (preferred; CI runners on ARM with GPU are scarce). Note in [PLAN.md](PLAN.md) §"build order" already flagged this.

#### A4.4 Disk

```json
"disk": [
  {"path": "/", "fstype": "ext4", "total_gb": 1800, "used_gb": 412, "used_pct": 22.9, "iops_read": 12, "iops_write": 4},
  {"path": "/var/lib/ollama", "fstype": "ext4", "total_gb": 3600, "used_gb": 2940, "used_pct": 81.7, "iops_read": 0, "iops_write": 0},
  {"path": "/mnt/models", "fstype": "nfs", "total_gb": 50000, "used_gb": 41200, "used_pct": 82.4}
]
```

- Configurable list of paths (default: `/`, `/var/lib/ollama` if present, `/var/lib/rt-node-agent`); plus auto-discovery of any mount with `>= 50 GB` total (caps at 10 entries to bound payload).
- `iops_read`/`iops_write` derived as 60s deltas from `gopsutil/disk.IOCounters()`.
- Windows: drive letters (`C:\`, `D:\`).

#### A4.5 Network

```json
"network": {
  "hostname_fqdn": "dgx-01.lan.internal",
  "interfaces": [
    {"name": "eno1", "up": true, "speed_mbps": 1000, "mtu": 1500, "ipv4": ["192.168.50.122"], "rx_mb_per_s": 0.4, "tx_mb_per_s": 0.1, "rx_errors_total": 0, "tx_errors_total": 0},
    {"name": "rocep1s0f0", "up": true, "speed_mbps": 200000, "mtu": 4200, "ipv4": ["10.10.1.1"], "rx_mb_per_s": 0.0, "tx_mb_per_s": 0.0}
  ]
}
```

- `gopsutil/net.Interfaces()` + `IOCounters()`.
- Rates over 60s window like elsewhere.
- Public-repo hygiene: the hostname/IP example uses the SPEC's already-public examples; nothing new committed.

#### A4.6 Time sync

```json
"time_sync": {
  "source": "chrony",
  "synced": true,
  "skew_ms": 0.42,
  "stratum": 3,
  "last_update_s": 12
}
```

- Linux: `chronyc tracking` parsed, fallback `timedatectl show-timesync`.
- macOS: `sntp -t 1 time.apple.com` if reachable, else `null`.
- Windows: `w32tm /query /status` parsed.
- `null` if no NTP service is running (don't punish — informational).

#### A4.7 `GET /capabilities`

Static-ish snapshot of what this build of the agent can do. Read-only, open on LAN. Lets the dispatcher feature-detect rather than parse semver:

```json
{
  "agent_version": "0.2.0",
  "config_version": 2,
  "os": "linux",
  "arch": "arm64",
  "platforms_supported": ["ollama", "vllm"],
  "platforms_detected": ["ollama", "vllm"],
  "actions_supported": ["unload-model", "service", "training-mode"],
  "services_allowlist": ["rt-vllm-qwen3.service", "rt-vllm-llama-70b.service"],
  "rdma_available": true,
  "training_mode_supported": true,
  "metrics_enabled": true,
  "gpu_vendor": "nvidia",
  "system_metrics_fields_supported": [
    "cpu.temps_c", "cpu.usage_per_core_pct", "cpu.throttled",
    "gpu.nvlink", "gpu.mig_mode", "gpu.ecc_*",
    "memory.pressure", "memory.huge_pages_*",
    "time_sync.skew_ms"
  ]
}
```

The `system_metrics_fields_supported` list is what makes the per-architecture coverage discoverable: a Mac responds with a shorter list than a DGX. Dispatcher can rank without trial-and-error.

#### A4.8 New `degraded_reasons` from system metrics

| Reason | Severity | Trigger |
|---|---|---|
| `disk_over_90pct` | soft | any monitored disk > 90% used |
| `disk_over_98pct` | hard | any monitored disk > 98% used |
| `clock_skew_high` | soft | `abs(skew_ms) > 100` (any platform where time_sync available) |
| `cpu_thermal_throttling` | soft | `cpu.throttled == true` |
| `gpu_thermal_throttling` | soft | any `gpu.throttle_reasons` contains `HW_THERMAL_SLOWDOWN` or `SW_THERMAL_SLOWDOWN` |
| `gpu_power_throttling` | soft | any `gpu.throttle_reasons` contains `HW_POWER_BRAKE_SLOWDOWN` or `SW_POWER_CAP` |
| `gpu_ecc_uncorrected` | hard | any `ecc_volatile_uncorrected > 0` |
| `vllm_down` | soft | configured `platforms.vllm.enabled: true` but probe fails |
| `vllm_required_down` | hard | `platforms.vllm.required: true` AND probe fails |

Critical: never invent a reason from a `null` metric. If `cpu.temps_c == null` on a Mac without root, do *not* emit `cpu_thermal_throttling: false` — the field is genuinely unknown.

**Critical files:**
- [internal/sysmetrics/](internal/sysmetrics/) (new package — replaces today's split `internal/{gpu,mem}/`; existing gpu/mem code moves in as `gpu_nvidia.go` / `mem_*.go`)
- [internal/sysmetrics/cpu_linux.go](internal/sysmetrics/cpu_linux.go) (new)
- [internal/sysmetrics/cpu_darwin.go](internal/sysmetrics/cpu_darwin.go) (new)
- [internal/sysmetrics/cpu_windows.go](internal/sysmetrics/cpu_windows.go) (new)
- [internal/sysmetrics/temps_*.go](internal/sysmetrics/) (new per-OS)
- [internal/sysmetrics/disk.go](internal/sysmetrics/disk.go) (new)
- [internal/sysmetrics/network.go](internal/sysmetrics/network.go) (new)
- [internal/sysmetrics/timesync_{linux,darwin,windows}.go](internal/sysmetrics/) (new)
- [internal/sysmetrics/gpu_nvidia.go](internal/sysmetrics/gpu_nvidia.go) — extends today's [internal/gpu/nvidia_smi.go](internal/gpu/nvidia_smi.go) with the new fields
- [internal/sysmetrics/gpu_apple_darwin_arm64.go](internal/sysmetrics/gpu_apple_darwin_arm64.go) — extends today's [internal/gpu/apple.go](internal/gpu/apple.go)
- [internal/health/report.go](internal/health/report.go) — wire new fields into `Report` struct
- [internal/health/degraded.go](internal/health/degraded.go) — new reason checks
- [internal/server/handlers.go](internal/server/handlers.go) — `handleCapabilities`
- [internal/server/server.go](internal/server/server.go) — register `GET /capabilities`
- [internal/server/metrics.go](internal/server/metrics.go) (new file, split from `handlers.go`) — Prometheus exposition for the full extended surface

---

### A5. README + `/docs` folder

**Structure** (new `docs/` directory at repo root):

```
docs/
├── README.md                    # index
├── install.md                   # full install matrix, upgrade flow, sudoers drop-in
├── config.md                    # config.yaml reference, all keys, migration v1→v2
├── api/
│   ├── health.md                # /health response, every field documented
│   ├── capabilities.md          # /capabilities
│   ├── metrics.md               # Prometheus exposition
│   ├── version.md
│   ├── actions-unload-model.md
│   ├── actions-service.md       # NEW
│   └── actions-training-mode.md # NEW (Phase B)
├── platforms/
│   ├── ollama.md
│   └── vllm.md
├── degraded-reasons.md          # canonical list, severity, contract note
├── remote-actions.md            # security model, sudoers, allowlist guidance
├── rdma.md                      # Phase B
├── training-mode.md             # Phase B
└── troubleshooting.md
```

[README.md](README.md) is shortened: a one-screen overview + links into `docs/`. Keep the curl|sh one-liner near the top (zero-friction install bias). Move per-OS install matrix and the full `degraded_reasons` table out to `docs/`.

[spec/SPEC.md](spec/SPEC.md) remains the contract document. `docs/` is human-facing.

[spec/NODE_AGENT_TRAINING_EXTENSIONS.md](spec/NODE_AGENT_TRAINING_EXTENSIONS.md) is referenced from `docs/training-mode.md` and `docs/rdma.md`; the spec doc itself remains the source of truth.

**Critical files:** all under `docs/` (~12 new markdown files, each ≤300 lines).

---

## Phase B — Training extensions

`NODE_AGENT_TRAINING_EXTENSIONS.md` is already a complete v0.2.0 spec. This phase implements it. Order matches `NODE_AGENT_TRAINING_EXTENSIONS.md` §"Implementation order".

### B1. RDMA collection

- New package `internal/rdma/` (Linux only, build tag `linux`; stub on other OSes).
- Sysfs reader: walks `/sys/class/infiniband/<dev>/ports/<port>/`. Pure function over a filesystem root (testable with fixture trees).
- Background counter snapshot every 5s (own goroutine, like [internal/allocators/scraper.go](internal/allocators/scraper.go)); sliding 60s window for rate computation.
- PFC pause frames: try sysfs, fall back to `ethtool -S <iface>`, omit silently if both miss.
- New `/health.rdma` field exactly as specified in NODE_AGENT_TRAINING_EXTENSIONS.md §2.1.
- Hosts without `/sys/class/infiniband/` omit the field entirely. Dispatcher already handles absence.

**Critical files:**
- [internal/rdma/sysfs.go](internal/rdma/sysfs.go) (new)
- [internal/rdma/counters.go](internal/rdma/counters.go) (new)
- [internal/rdma/rdma_linux.go](internal/rdma/rdma_linux.go) (new, glue)
- [internal/rdma/rdma_stub.go](internal/rdma/rdma_stub.go) (new, build tag `!linux`)
- [internal/health/report.go](internal/health/report.go) — add `RDMA` field
- [internal/health/degraded.go](internal/health/degraded.go) — add 7 new reasons from NODE_AGENT_TRAINING_EXTENSIONS §3 (`rdma_port_down`, `rdma_peermem_missing`, `rdma_collector_stale` hard; `rdma_errors_growing`, `rdma_pfc_storm`, `rdma_link_degraded` soft)

### B2. Mode state machine

- New package `internal/mode/` exposing `Get()` and `Enter(req)` / `Exit()`.
- State file at `/var/lib/rt-node-agent/training_mode.json` (configurable).
- States: `idle`, `inference` (derived from platforms[].models), `training_mode` (explicit). Computed at request time; only `training_mode` persists.
- Auto-recovery: on startup, if state file exists and `entered_at + expected_duration_s + grace_period_s` (default 1h) has passed, clear state and log a warning.

### B3. `POST /actions/training-mode`

- Wire shape exactly as NODE_AGENT_TRAINING_EXTENSIONS §4.
- Reuses [internal/server/auth.go](internal/server/auth.go) `requireToken` middleware.
- On `enter: true`: drains `release_ollama_models` via the existing unload-model logic in `internal/platforms/ollama/unload.go`; fails closed if any unload fails (node does not enter training mode with stale models).
- On `enter: true`: adds `training_in_progress` to `degraded_reasons` (hard); disables Ollama probing (prevents `ollama_down` noise during legitimate drain).
- Idempotent re-enter returns 200 with existing state.
- Errors: 401 bad token, 409 exit-when-not-in-training, 500 unload failure, 503 token unconfigured.

**Critical files:**
- [internal/mode/state.go](internal/mode/state.go) (new)
- [internal/mode/recover.go](internal/mode/recover.go) (new, on-start)
- [internal/server/handlers.go](internal/server/handlers.go) — `handleTrainingMode`
- [internal/server/server.go](internal/server/server.go) — register route
- [internal/health/report.go](internal/health/report.go) — add `Mode` and conditional `Training` fields

### B4. Training-process allocator scraping (`only_when_mode`)

- Tiny extension to existing [internal/allocators/scraper.go](internal/allocators/scraper.go): new config field `only_when_mode: training_mode`. Scraper checks `mode.Get()` before each scrape; skips if mismatched.
- Surfaces under existing `service_allocators[]` array. Extra training-specific fields (`run_id`, `step`, `epoch`, `loss_train`, `tokens_per_second`) are opaque pass-through — `json.RawMessage` preserved as-is.

### B5. Prometheus metrics additions

- New series for RDMA device state, link rate, error counters, pause frames (§10 of training extensions doc).
- New `rt_node_mode{mode="…"} 1` gauge.
- New `rt_node_training_run_id_info{run_id="…"} 1` (bounded cardinality: one active run per node).
- Disk/network/clock-skew metrics from A4.
- Wired in [internal/server/metrics.go](internal/server/metrics.go) (new file split from `handlers.go`).

---

## Reuse map (do not reimplement)

| Need | Existing code |
|---|---|
| HTTP routing | [internal/server/server.go:87](internal/server/server.go#L87) — add new routes to `routes()` |
| Bearer auth middleware | [internal/server/auth.go:16](internal/server/auth.go#L16) `requireToken` — wraps all new mutating endpoints |
| Background scrape loop with cache | [internal/allocators/scraper.go](internal/allocators/scraper.go) — same pattern for vLLM probe and RDMA counters |
| GPU probe cache (5s TTL) | [internal/gpu/cache.go](internal/gpu/cache.go) `CachedProbe` — reuse pattern for new probes |
| Process enumeration | [internal/ollama/runners.go](internal/ollama/runners.go) — generalize for vLLM |
| Config load + env override | [internal/config/config.go:71](internal/config/config.go#L71) — extend struct, reuse load hierarchy |
| Idempotent install / no-overwrite bootstrap | [internal/service/bootstrap.go:71](internal/service/bootstrap.go#L71) — call into new migrate package |
| Degraded reasons evaluator | [internal/health/degraded.go:42](internal/health/degraded.go#L42) `Evaluate` — pure function, just add cases |
| systemd unit installation pattern | [internal/service/systemd.go](internal/service/systemd.go) — extend to write sudoers drop-in |

---

## Public-repo hygiene checklist

Before every commit on this branch:

- No real hostnames (use `spark-A1`, `dgx-01`, `ctrlone-…` from SPEC.md only).
- No real tokens in fixtures — generate with `crypto/rand` in test setup.
- No internal Linear/Slack/PR URLs in code or markdown.
- New `examples/config.yaml` is the canonical illustrative config and is committed; real `/etc/rt-node-agent/config.yaml` is not.
- Sudoers drop-in is generated by the binary at install time, not committed.
- RDMA fixtures (test sysfs trees) use generic device names (`mlx5_0`, `rocep1s0f0`).

---

## Verification

End-to-end checks. None pass yet — they exist after implementation.

**Unit tests (`go test ./...`):**
- `internal/config/migrate`: feed v0.1.x config → assert new keys appended commented, existing values preserved, comments intact.
- `internal/platforms/vllm`: stub HTTP server returning OpenAI shape → assert correct `PlatformReport`.
- `internal/services`: mock systemctl → assert allowlist enforcement, action enum enforcement, sudoers drop-in well-formed (`visudo -cf` against the rendered file).
- `internal/rdma`: fixture sysfs trees from real Spark → assert `RDMA` shape, counter rate math correct over 60s window.
- `internal/mode`: drive state machine in tests, assert state file contents, assert auto-recovery on stale state file.
- `internal/health/degraded`: feed Reports for each new reason; assert expected degraded set.

**Integration on a Linux NVIDIA box (Ubuntu 22.04 with one RTX):**
- `make build && sudo ./rt-node-agent install` on a node with an existing v0.1.0 install → confirm config.yaml.new produced with diff, token untouched.
- `curl http://node:11435/health | jq` → confirm `platforms.ollama`, `platforms.vllm` (if running), `services[]`, `disk[]`, `capabilities` (via separate endpoint) all present.
- `curl http://node:11435/capabilities | jq` → confirm allowlist matches config.
- `curl -X POST -H "Authorization: Bearer $TOK" -d '{"unit":"rt-vllm-qwen3.service","action":"status"}' http://node:11435/actions/service` → 200 with state.
- Same with non-allowlisted unit → 403.
- Same with no Bearer → 401.

**DGX integration (Phase B):**
- On Spark with CX-7: `curl /health | jq .rdma` → confirm device list, counters, link state.
- Disconnect cable → assert `rdma_port_down` appears in `degraded_reasons` within 30s.
- `POST /actions/training-mode {enter: true, run_id: ..., release_ollama_models: [...]}` → confirm Ollama drained, `mode = training_mode`, `training_in_progress` in degraded_reasons.
- `kill -9` agent during training mode, restart → confirm state recovered from state file.
- `expected_duration_s + grace_period_s` exceeded → confirm auto-exit with warning log.

**Backward-compat smoke:**
- v0.1.0 case-manager backend pointed at v0.2.0 agent → confirm legacy `ollama` field still populated, all v0.1 `degraded_reasons` still emit, dispatcher unchanged.
- v0.2.0 agent on a node with no RDMA, no vLLM → confirm `/health` omits `rdma`, includes `platforms.vllm: {up: false}` only if `enabled: true` in config (otherwise omitted entirely).

**Release gate:**
- `go vet ./...` clean.
- `make release` cross-compiles all five OS/arch targets without CGO.
- `scripts/install.sh` end-to-end on a fresh Ubuntu container.

---

## Sequencing

Phase A is independent of Phase B and ships first as PRs in this order (each lands behind no flag — additive everywhere):

1. A1 — config migrate (foundation; everything else adds config keys).
2. A2 — vLLM platform.
3. A4 disk/network/timesync — small, pure-function additions.
4. A3 — service control endpoint (security review required before merge).
5. A4 capabilities endpoint + remaining degraded reasons.
6. A5 — docs (concurrent with all of the above; can be drafted alongside each feature).

Phase B follows the order already documented in `NODE_AGENT_TRAINING_EXTENSIONS.md` §"Implementation order".

`v0.2.0` tag is cut once Phase B integration test on a real Spark passes. Phase A can ship as `v0.1.x` patch releases if any item lands much earlier and is needed in production.

---

**End of V0_2_0_PLAN.md**
