---

```markdown
# Node Agent — Training Plane Extensions

**Status:** Proposed for v0.2.0
**Parent spec:** [SPEC.md](./SPEC.md)
**Owner:** Tommy Stiansen
**Last updated:** 2026-05-06

This document defines extensions to `rt-node-agent` to support the training plane (4× Dell Pro Max with GB10 + MikroTik CRS520 fabric). Inference-plane behavior defined in SPEC.md is unchanged and fully backward compatible.

The training dispatch service consumes these extensions to:
1. Verify cluster health before launching multi-node training jobs.
2. Drain inference workloads from a node before training starts.
3. Surface RDMA fabric health alongside existing GPU/CPU/memory signals.
4. Track the training process's PyTorch allocator state via the existing `service_allocators` mechanism.

All new fields are additive and optional. Backends that don't know about them keep working with v0.1 behavior.

---

## Table of contents

1. [Goals](#1-goals)
2. [New `/health` fields](#2-new-health-fields)
3. [New `degraded_reasons`](#3-new-degraded_reasons)
4. [New endpoint: `POST /actions/training-mode`](#4-new-endpoint-post-actionstraining-mode)
5. [Training-process allocator scraping](#5-training-process-allocator-scraping)
6. [RDMA collection details](#6-rdma-collection-details)
7. [Mode state machine](#7-mode-state-machine)
8. [Backward compatibility contract](#8-backward-compatibility-contract)
9. [Configuration additions](#9-configuration-additions)
10. [Prometheus metrics additions](#10-prometheus-metrics-additions)
11. [Test plan](#11-test-plan)
12. [Open questions](#12-open-questions)

---

## 1. Goals

The training plane needs three capabilities the agent doesn't currently provide:

- **Fabric health visibility.** The training dispatcher must know whether RoCE/RDMA is healthy on a node before launching multi-node training. Without this, every dispatch decision requires a fresh SSH round-trip to `ibv_devinfo` and `lsmod`.

- **Inference/training mode coordination.** When training starts on a Spark, that node must drop out of the inference pool. When training ends, it returns. Both transitions need to be deterministic and auditable.

- **Training-process introspection.** Same `service_allocators` pattern that watches `gliner2-service` should watch the training process during a run. PyTorch allocator stats from inside the training loop, surfaced through the same Prometheus endpoint as everything else.

Non-goals:

- Pushing training metrics to MLflow. That's the training process's responsibility.
- Coordinating multi-node rendezvous. That's torchrun's responsibility.
- Restarting training jobs on failure. That's the dispatch service's responsibility.

---

## 2. New `/health` fields

Two new top-level fields, both optional. Hosts without RDMA hardware omit `rdma` entirely. The `mode` field is always present in v0.2.0+.

### 2.1 `rdma`

```json
"rdma": {
  "enabled": true,
  "kernel_modules": {
    "mlx5_ib": true,
    "mlx5_core": true,
    "nvidia_peermem": true,
    "ib_core": true,
    "ib_uverbs": true
  },
  "devices": [
    {
      "name": "rocep1s0f0",
      "port": 1,
      "state": "ACTIVE",
      "physical_state": "LINK_UP",
      "active_mtu": 4096,
      "max_mtu": 4096,
      "link_layer": "Ethernet",
      "gid_index": 3,
      "rate_gbps": 200,
      "counters": {
        "port_xmit_data_bytes":  92847362874,
        "port_rcv_data_bytes":   92845129348,
        "port_xmit_packets":     8472398472,
        "port_rcv_packets":      8472187234,
        "symbol_error_counter":  0,
        "link_error_recovery":   0,
        "link_downed":           0,
        "port_rcv_errors":       0,
        "excessive_buffer_overrun_errors": 0
      },
      "pause_frames": {
        "rx":      124583,
        "tx":      12482,
        "rx_rate": 0,
        "tx_rate": 0
      },
      "last_collected_ts": 1746489600
    },
    {
      "name": "rocep1s0f1",
      "port": 1,
      "state": "ACTIVE",
      "...": "..."
    }
  ]
}
```

**Field semantics:**

- `enabled`: true if RDMA hardware was detected at agent start. Once set, never changes during a run; agent restart required to re-detect.
- `kernel_modules`: each is `true`/`false` based on `lsmod` output. `nvidia_peermem` is the critical one for GPUDirect RDMA.
- `devices[]`: one entry per RDMA device the agent can see. Empty array means RDMA hardware exists but no devices are reachable (driver issue).
- `state`: matches `ibv_devinfo` enumeration: `ACTIVE`, `DOWN`, `INIT`, `ARMED`, `UNKNOWN`.
- `physical_state`: matches `ibv_devinfo`: `LINK_UP`, `DISABLED`, `POLLING`, `SLEEP`, `UNKNOWN`.
- `link_layer`: `Ethernet` (RoCE) or `InfiniBand`. For Spark hardware this is always `Ethernet`.
- `rate_gbps`: negotiated link speed. Useful for catching "200G cable used where 400G expected" misconfigurations.
- `counters.*`: cumulative counters from `/sys/class/infiniband/<dev>/ports/<port>/counters/`. Wraparound at uint64 max — backends should compute deltas, not absolutes.
- `pause_frames.rx`/`pause_frames.tx`: cumulative PFC pause frame counts.
- `pause_frames.rx_rate`/`pause_frames.tx_rate`: pause frames per second computed over the last 60 seconds. Non-zero rates during sustained training mean the fabric is hitting backpressure.
- `last_collected_ts`: unix timestamp of the most recent counter snapshot. Stale data (>30s old) is a symptom that something is wrong with the agent's collection loop.

### 2.2 `mode`

```json
"mode": "idle"
```

One of:

- `"idle"` — node is doing nothing in particular. May still serve `/health`.
- `"inference"` — implicit when ollama has resident models. Agent infers from `ollama.models[]` length.
- `"training_mode"` — set by `POST /actions/training-mode {enter: true}`. Persists across requests until explicitly cleared.

The `mode` field is computed at request time, not stored. `training_mode` is the only mode that requires explicit transition.

When `mode == "training_mode"`, the response also includes:

```json
"training": {
  "run_id": "9b1f-2c4e-...",
  "entered_at": 1746489600,
  "expected_duration_s": 7200,
  "ollama_models_released": ["nomic-embed-text-v2-moe:latest"],
  "ollama_models_to_restore": ["nomic-embed-text-v2-moe:latest"]
}
```

`ollama_models_to_restore` is what the agent will reload (config-driven) on training-mode exit. Empty list means no auto-restore.

---

## 3. New `degraded_reasons`

Additive to the existing list in SPEC.md. Backends that don't recognize these names keep working — they just don't act on the new signals.

| Reason | Severity | Trigger |
|---|---|---|
| `rdma_port_down` | **hard** | Any `rdma.devices[].state != "ACTIVE"` or `physical_state != "LINK_UP"` |
| `rdma_peermem_missing` | **hard** | `rdma.kernel_modules.nvidia_peermem == false` |
| `training_in_progress` | **hard** | `mode == "training_mode"` |
| `rdma_collector_stale` | **hard** | `last_collected_ts` older than 30s on any device |
| `rdma_errors_growing` | soft | Any error counter (`symbol_error_counter`, `link_error_recovery`, `port_rcv_errors`) increased over the last 60s |
| `rdma_pfc_storm` | soft | `pause_frames.rx_rate > 1000` sustained for 30s |
| `rdma_link_degraded` | soft | `rate_gbps < 200` (cable mis-spec or auto-negotiation issue) |

**Hard reasons block training launch** in the dispatch service (training spec §22.5). They also cause the inference dispatcher's `rank_nodes()` to skip the node, which is exactly the desired behavior for `training_in_progress`.

**Soft reasons are informational** — the dispatcher can launch a training job on a node with `rdma_pfc_storm` reported, but it will log a warning and surface the signal in the run's metadata. This is correct: a node with mild PFC backpressure is still trainable, just slower.

---

## 4. New endpoint: `POST /actions/training-mode`

Authenticated (Bearer token). Idempotent — repeating a request with `enter: true` when already in training mode returns 200 with the existing state, not 409.

### Request

```http
POST /actions/training-mode HTTP/1.1
Host: spark-A1:11435
Authorization: Bearer <token>
Content-Type: application/json

{
  "enter": true,
  "run_id": "9b1f-2c4e-...",
  "expected_duration_s": 7200,
  "release_ollama_models": ["nomic-embed-text-v2-moe:latest", "qwen3-vl:32b"],
  "restore_on_exit": true
}
```

Fields:

- `enter` (bool, required): `true` to enter training mode, `false` to exit.
- `run_id` (string, required when `enter=true`): training run UUID, recorded for audit.
- `expected_duration_s` (int, optional): for surfacing in `/health` while training is running. Does not enforce — training mode persists until explicit exit.
- `release_ollama_models` (string array, optional): models to unload before reporting training-mode entered. Each calls the existing unload-model logic. If unloading fails, the request fails — the node does NOT enter training mode with stale ollama models still loaded.
- `restore_on_exit` (bool, default true): if true, the agent reloads the released models on `enter: false`. Only used to populate the `ollama_models_to_restore` field in `/health`; the actual restore is the responsibility of whatever workload follows training (the inference dispatcher will reload models on demand anyway).

### Response

Success (200):

```json
{
  "status": "ok",
  "mode": "training_mode",
  "run_id": "9b1f-2c4e-...",
  "entered_at": 1746489600,
  "models_released": ["nomic-embed-text-v2-moe:latest", "qwen3-vl:32b"],
  "took_ms": 2340
}
```

Exit (200):

```json
{
  "status": "ok",
  "mode": "idle",
  "previous_run_id": "9b1f-2c4e-...",
  "duration_s": 7180,
  "took_ms": 12
}
```

Errors:

- **401** — missing or invalid Bearer token.
- **409** — `enter: false` requested but the node is not in training mode (vs. `enter: true` when already in training mode, which is idempotent).
- **500** — failed to unload one or more models in `release_ollama_models`. The node remains in its previous mode.
- **503** — token not configured (matches existing convention).

### Side effects

On `enter: true`:

1. For each model in `release_ollama_models`, call the existing internal `unload-model` function. Stop on first failure; node does not enter training mode.
2. Add `training_in_progress` to `degraded_reasons` (hard).
3. Set `mode = "training_mode"` and persist `run_id`, `entered_at`, `expected_duration_s`, `ollama_models_to_restore` to a state file at `/var/lib/rt-node-agent/training_mode.json`. Persisting allows the agent to recover state across crashes.
4. Disable ollama health probing for the duration of training mode. Ollama is intentionally drained; probing it would generate `ollama_down` noise.

On `enter: false`:

1. Clear `mode`. Remove the state file.
2. Re-enable ollama health probing.
3. Do NOT proactively reload models — the inference dispatcher's natural behavior will reload them on demand. (See §12 Open questions for whether this should change.)

### Persistence and crash recovery

The training-mode state file is the source of truth. On agent startup, if the state file exists, the agent restores `mode = "training_mode"` and continues reporting `training_in_progress` until either:

- An explicit `enter: false` is received, or
- The state file's `entered_at + expected_duration_s + grace_period` (default 1 hour) is exceeded, at which point the agent auto-exits training mode and logs a warning. This prevents stuck training-mode state if the dispatch service crashes and forgets to call exit.

---

## 5. Training-process allocator scraping

Reuses the existing `service_allocators` mechanism. The training process exposes `/v1/debug/gpu` on a configurable port; the agent scrapes it and surfaces it under the existing `service_allocators[]` array in `/health`.

### Training process contract

The training process is responsible for serving:

```http
GET /v1/debug/gpu HTTP/1.1
Host: localhost:8089
```

```json
{
  "allocated_mb":      48372.4,
  "reserved_mb":       49281.0,
  "max_allocated_mb":  49801.2,
  "run_id":            "9b1f-2c4e-...",
  "step":              1247,
  "epoch":             0.83,
  "loss_train":        0.127,
  "tokens_per_second": 18472.3
}
```

The first three fields (`allocated_mb`, `reserved_mb`, `max_allocated_mb`) are the existing contract from gliner2-service. The remaining fields are training-specific and the agent treats them as opaque pass-through — they appear in the `service_allocators[]` entry as-is.

Reference implementation goes in `training_lib/debug_endpoint.py` in the redtorch case-manager repo:

```python
from fastapi import FastAPI
import torch
import threading
from uvicorn import Config, Server

class TrainingDebugServer:
    def __init__(self, port: int, run_id: str, get_trainer_state):
        self.app = FastAPI()
        self.run_id = run_id
        self.get_trainer_state = get_trainer_state

        @self.app.get("/v1/debug/gpu")
        def debug_gpu():
            state = self.get_trainer_state()
            return {
                "allocated_mb":      torch.cuda.memory_allocated() / 1e6,
                "reserved_mb":       torch.cuda.memory_reserved() / 1e6,
                "max_allocated_mb":  torch.cuda.max_memory_allocated() / 1e6,
                "run_id":            self.run_id,
                "step":              state.global_step,
                "epoch":             state.epoch,
                "loss_train":        state.last_loss,
                "tokens_per_second": state.last_throughput,
            }

        config = Config(self.app, host="127.0.0.1", port=port, log_level="warning")
        self.server = Server(config)

    def start(self):
        threading.Thread(target=self.server.run, daemon=True).start()
```

The training script starts this in a daemon thread alongside the trainer. Listening on 127.0.0.1 only — no exposure beyond the local node.

### Agent config

Add to `/etc/rt-node-agent/config.yaml`:

```yaml
service_allocators:
  - name: gliner2-service
    url: http://localhost:8077/v1/debug/gpu
    threshold_warn_mb: 4096
    threshold_critical_mb: 10240
    scrape_interval_s: 30
  - name: training-process
    url: http://localhost:8089/v1/debug/gpu
    threshold_warn_mb: 100000        # training legitimately uses lots
    threshold_critical_mb: 120000    # close to GB10's 128 GB physical
    scrape_interval_s: 10            # more frequent during training
    only_when_mode: training_mode    # NEW field; skip scrape otherwise
```

### New config field: `only_when_mode`

When set, the agent only scrapes that allocator when `mode` matches. Avoids spamming the connection-refused log when the training process isn't running.

When `only_when_mode` is unset (the default for existing entries), behavior is unchanged from v0.1 — always scrape.

---

## 6. RDMA collection details

### 6.1 Source paths

The agent collects RDMA state from `/sys/class/infiniband/`. No `nvidia-smi`-equivalent shell-out for RDMA — the sysfs interface is reliable, fast, and present on every Spark.

```
/sys/class/infiniband/<device>/
├── fw_ver
├── hw_rev
├── ports/
│   └── <port>/
│       ├── state                    # "4: ACTIVE" etc.
│       ├── phys_state               # "5: LinkUp" etc.
│       ├── rate                     # "200 Gb/sec (4X EDR)"
│       ├── link_layer               # "Ethernet"
│       ├── counters/
│       │   ├── port_xmit_data
│       │   ├── port_rcv_data
│       │   ├── port_xmit_packets
│       │   ├── port_rcv_packets
│       │   ├── symbol_error
│       │   ├── link_error_recovery
│       │   ├── link_downed
│       │   ├── port_rcv_errors
│       │   └── excessive_buffer_overrun_errors
│       └── gids/<index>             # for GID enumeration
└── ...
```

PFC pause frame counts come from the corresponding netdev:

```
/sys/class/net/<iface>/statistics/
├── rx_pause_frames                  # may be ethtool -S key, not sysfs
├── tx_pause_frames
```

On some kernels the pause frame counters are only available via `ethtool -S <iface> | grep pause`. The agent tries sysfs first, falls back to ethtool, omits the field if neither works. Don't fail health collection over missing pause frame counters.

### 6.2 Cadence

- Device state, kernel modules, link layer: at agent startup + every `/health` request (cheap, all sysfs reads).
- Counters: every 5s in a background loop, cached for `/health` to read.
- Computed rates (`pause_frames.rx_rate`, error-counter deltas): computed over a 60s sliding window from the cached counter snapshots.

### 6.3 Fallback

If `/sys/class/infiniband/` doesn't exist (host has no RDMA hardware, or modules aren't loaded), the agent omits the `rdma` field entirely from `/health`. Backends that handle the absence as "no RDMA on this node" — which is the correct interpretation for inference-only Macs — keep working.

If `/sys/class/infiniband/` exists but a specific counter is missing (different MOFED version, kernel quirk), the agent omits that single field from `counters{}`. Don't fail the whole RDMA block over one missing counter.

---

## 7. Mode state machine

```
       ┌─────────┐
       │  idle   │
       └────┬────┘
            │
            │ ollama probe finds resident models
            ▼
      ┌──────────┐
      │ inference│
      └────┬─────┘
           │
           │ ollama models drained AND
           │ POST /actions/training-mode {enter:true}
           ▼
   ┌─────────────────┐
   │ training_mode   │◄─────┐ idempotent re-enter allowed
   └────────┬────────┘      │
            │               │
            │ POST /actions/training-mode {enter:false}
            │ OR
            │ entered_at + expected + grace exceeded (auto-recovery)
            ▼
       ┌─────────┐
       │  idle   │
       └─────────┘
```

Transitions:

- `idle` ↔ `inference` is implicit, computed each request from ollama state.
- `idle` → `training_mode` requires explicit `POST` with valid token.
- `inference` → `training_mode` requires the caller to pass `release_ollama_models` so the agent can drain ollama as part of the transition. Alternatively, the caller can use the existing `/actions/unload-model` endpoint first, then call `training-mode` with an empty `release_ollama_models` list.
- `training_mode` → `idle` requires explicit `POST {enter: false}` OR auto-recovery after expected_duration + grace expires.

There is no `training_mode` → `inference` transition. Exiting training always goes to `idle`; the implicit `inference` mode kicks back in only when ollama actually has resident models again.

---

## 8. Backward compatibility contract

The v0.2.0 agent must serve old backends correctly, and the v0.1.x agent must serve training backends correctly (with degraded functionality).

### 8.1 v0.2.0 agent + v0.1.x backend

- New `/health` fields are additive. Old backend ignores `rdma` and `mode` — no break.
- New `degraded_reasons` are additive. Old backend's `rank_nodes()` only knows about the v0.1 reasons; new ones are silently treated as soft (not in old hard list). This is the intended behavior — adoption is gradual, training-aware backends opt in.
- New `/actions/training-mode` endpoint: old backends never call it. No effect.

### 8.2 v0.1.x agent + v0.2 (training) backend

- Training backend's `pre_launch_checks()` queries `/health`. v0.1 agent returns `/health` without `rdma` or `mode` fields. Backend treats this as "RDMA info unavailable, fall back to direct verification."
- Training backend tries `POST /actions/training-mode`. v0.1 agent returns 404. Backend treats this as "node-agent doesn't support coordinated drain, fall back to manual `/actions/unload-model` for known inference models."
- This means **training Phase 1 can ship before v0.2.0 is deployed everywhere**. Phase 1 acceptance still passes; the dispatch service does extra work, but the work is well-defined.

### 8.3 Field-removal policy

No fields are removed in v0.2. The existing v0.1 `/health` schema is a frozen contract. New behavior is opt-in via new fields and new endpoints.

---

## 9. Configuration additions

New keys in `/etc/rt-node-agent/config.yaml`. All optional; sensible defaults.

```yaml
# RDMA — defaults usually correct
rdma:
  enabled: auto                      # auto | true | false
  collect_interval_s: 5
  pfc_storm_threshold_rx_rate: 1000  # frames/sec
  pfc_storm_window_s: 30
  errors_growing_window_s: 60

# Training mode
training_mode:
  state_file: /var/lib/rt-node-agent/training_mode.json
  grace_period_s: 3600               # auto-exit if expected_duration + grace exceeded
  disable_ollama_probe: true         # while in training mode

# Service allocators — extends existing key
service_allocators:
  - name: training-process
    url: http://localhost:8089/v1/debug/gpu
    threshold_warn_mb: 100000
    threshold_critical_mb: 120000
    scrape_interval_s: 10
    only_when_mode: training_mode
```

---

## 10. Prometheus metrics additions

When `RT_AGENT_METRICS=1`, `/metrics` adds:

```
# RDMA device state (1=ACTIVE, 0=else)
rt_node_rdma_device_active{device="rocep1s0f0",port="1"} 1
rt_node_rdma_device_active{device="rocep1s0f1",port="1"} 1

# Link rate
rt_node_rdma_link_rate_gbps{device="rocep1s0f0",port="1"} 200

# Counters (cumulative; use rate() in PromQL)
rt_node_rdma_xmit_bytes_total{device="rocep1s0f0",port="1"} 92847362874
rt_node_rdma_rcv_bytes_total{device="rocep1s0f0",port="1"} 92845129348
rt_node_rdma_symbol_errors_total{device="rocep1s0f0",port="1"} 0
rt_node_rdma_link_recovery_total{device="rocep1s0f0",port="1"} 0
rt_node_rdma_link_downed_total{device="rocep1s0f0",port="1"} 0
rt_node_rdma_rcv_errors_total{device="rocep1s0f0",port="1"} 0
rt_node_rdma_pause_frames_rx_total{device="rocep1s0f0",port="1"} 124583
rt_node_rdma_pause_frames_tx_total{device="rocep1s0f0",port="1"} 12482

# Mode (1 if matching, 0 otherwise — for easy alerting)
rt_node_mode{mode="idle"} 0
rt_node_mode{mode="inference"} 0
rt_node_mode{mode="training_mode"} 1

# Training info (only when in training_mode)
rt_node_training_run_id_info{run_id="9b1f-2c4e-..."} 1
rt_node_training_seconds_remaining 4862
```

These mirror the JSON in `/health` but in Prometheus shape. Keep label cardinality bounded — `run_id` is bounded because only one run can be active per node at a time.

---

## 11. Test plan

Unit tests:

- RDMA sysfs parsing: feed fixture sysfs trees from a real Spark, assert correct `rdma` field shape.
- Counter delta math: feed two snapshots 60s apart, assert `rx_rate` and error-growing detection are correct.
- Mode state machine: drive transitions in unit tests, assert state file contents and `degraded_reasons` membership.
- `/actions/training-mode` request validation: missing fields, invalid `run_id` format, etc.
- Backward compat: load v0.1.x `/health` fixtures, confirm v0.2 agent still emits valid v0.1 shape with the new fields appended.

Integration tests:

- Spin up agent on a real Spark, run nccl-tests in another window, assert pause-frame counters in `/health` move correctly.
- Disconnect a CX-7 cable, assert `rdma_port_down` appears in `degraded_reasons` within 30s.
- Call `POST /actions/training-mode {enter: true}` with a release-list, assert ollama models are gone and `/health` shows training mode.
- Kill agent during training mode, restart, assert state recovery from state file.
- Let `expected_duration_s + grace` expire, assert auto-recovery and warning log.

Acceptance:

- Run on all 4 Sparks (Phase 1 of the training spec).
- Sustained 5-minute 4-node nccl-test with `RT_AGENT_METRICS=1` and Prometheus scraping; confirm no metric drops, no agent crashes, no measurable impact on training throughput.

---

## 12. Open questions

1. **Should training-mode exit auto-restore released ollama models?** Current proposal: no, let the inference dispatcher's natural behavior reload on demand. Argument for yes: inference latency spikes for the first few requests post-training. Worth measuring before Phase 2.

2. **Should the agent expose a `/v1/debug/training` endpoint that mirrors the training process's `/v1/debug/gpu` for backends that can't reach localhost?** Currently the dispatch service queries the training process directly via the agent's `service_allocators[]`. If the dispatch service ever needs to fetch this without the agent being a middleman, it'd need a separate way. Defer until concrete need.

3. **PFC storm threshold default of 1000 rx/s.** This is a guess. Real value depends on MikroTik PFC config and training workload. Phase 1 acceptance should record actual rx_rate during a healthy 4-node nccl-test; tune the threshold to be ~3× that baseline.

4. **`expected_duration_s` enforcement.** Current proposal: no enforcement, only used for `/health` display + auto-recovery grace. Should the agent kill the training process if it overruns by some factor? Probably not — that's the dispatch service's responsibility, and the agent has no way to know what process is actually doing the training. But worth confirming.

5. **Interaction with the existing `agent_required` settings flag.** When `agent_required: true` is set in the case-manager, nodes without an agent are skipped. Should there be an analogous `training_agent_required` for the training dispatcher? Probably yes, same gradual-adoption story.

6. **`nvidia_peermem` auto-load.** If the agent detects `nvidia_peermem` is missing, should it try to `modprobe` it? No — that requires elevated privileges the agent doesn't have, and silent driver loading is a security smell. The agent reports `rdma_peermem_missing` and lets the operator fix it.

7. **Multi-tenant runs.** If two training jobs ever run on the same node simultaneously (unlikely on Spark with one GPU, but possible if we ever support partial-node allocation), `mode = "training_mode"` and a single `run_id` aren't expressive enough. Defer until multi-tenancy is on the roadmap.

---

## Implementation order

1. **Week 1**: RDMA collection + new `/health` fields. Pure read-only. No new endpoints, no auth changes. Two days of Go work, one day of fixtures and tests.
2. **Week 1-2**: New `degraded_reasons` for RDMA states. Same scope.
3. **Week 2**: `mode` field, state file, auto-recovery logic.
4. **Week 2**: `POST /actions/training-mode` endpoint.
5. **Week 3**: `service_allocators.only_when_mode` config addition. Tiny.
6. **Week 3**: Prometheus metrics additions.
7. **Week 3-4**: Integration tests on real Spark hardware.

Total: ~3-4 weeks of work to get v0.2.0 production-ready. Phase 1 of the training spec depends on it but doesn't block — the dispatch service falls back to direct verification on v0.1.x agents, so this can land in parallel.

---

**End of NODE_AGENT_TRAINING_EXTENSIONS.md**
```

---

