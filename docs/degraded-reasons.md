# `degraded_reasons` reference

The case-manager's `rank_nodes()` reads these strings directly. Renaming
or removing an entry is a breaking change — append-only.

Severity: **hard** reasons set `degraded=true` (dispatcher skips the
node). **soft** reasons stay in `reasons[]` but `degraded` may be false
(dispatcher deprioritizes but still uses).

## v0.1.x (frozen contract)

### Hard

| Reason | Trigger |
|---|---|
| `ollama_down` | Ollama HTTP not responding within 2s. |
| `swap_over_75pct` | Swap thrashing. |
| `vram_over_95pct` | No room to load anything. |
| `agent_stale` | Agent's own view of Ollama is older than 60s. |
| `vram_service_creep_critical` | A tracked `service_allocators` entry shows `reserved_mb / allocated_mb > 3.0` AND `reserved_mb > threshold_critical_mb`. Catches the 2026-04-22-style PyTorch allocator leak. |

### Soft

| Reason | Trigger |
|---|---|
| `swap_over_50pct` | (50, 75] swap. |
| `vram_over_90pct` | (90, 95] VRAM. |
| `load_avg_over_2x_cores` | CPU saturated. |
| `ollama_runner_stuck` | Runner PID exists, CPU 0%, queued_requests > 0 for ≥ 60s. |
| `vram_service_creep_warn` | `reserved/allocated > 2.0` AND `reserved > threshold_warn_mb` (only fires when `_critical` isn't already firing for the same entry). |

## v0.2.0 additions

### Hard

| Reason | Trigger |
|---|---|
| `disk_over_98pct` | Any monitored disk > 98% used. |
| `gpu_ecc_uncorrected` | Any GPU reports `ecc_volatile_uncorrected > 0`. |
| `vllm_required_down` | `platforms.vllm.required: true` AND probe fails. |
| `rdma_port_down` | Any `rdma.devices[].state != "ACTIVE"` or `physical_state != "LINK_UP"`. |
| `rdma_peermem_missing` | `rdma.kernel_modules.nvidia_peermem` is false. |
| `rdma_collector_stale` | Any device's `last_collected_ts` is > 30s old. |
| `training_in_progress` | `mode == "training_mode"`. Inference dispatch should skip; training dispatch checks for *this exact reason* to confirm a node it asked to enter did. |

### Soft

| Reason | Trigger |
|---|---|
| `disk_over_90pct` | Any monitored disk > 90% used. |
| `clock_skew_high` | `abs(time_sync.skew_ms) > 100`. Linux only (time_sync omitted elsewhere). |
| `cpu_thermal_throttling` | `cpu.throttled == true`. Skipped when the metric is `null` (e.g. macOS without root). |
| `gpu_thermal_throttling` | Any `gpu.throttle_reasons` contains `HW_THERMAL_SLOWDOWN` or `SW_THERMAL_SLOWDOWN`. |
| `gpu_power_throttling` | Any `gpu.throttle_reasons` contains `HW_POWER_BRAKE_SLOWDOWN` or `SW_POWER_CAP`. |
| `vllm_down` | Configured `platforms.vllm.enabled != false` but probe fails AND `required` is not set. |
| `rdma_errors_growing` | Reserved for future use (counter-delta tracking is a follow-up). |
| `rdma_pfc_storm` | Reserved. |
| `rdma_link_degraded` | Active port with `rate_gbps < 200`. |

## Contract

- Backends that don't recognize a reason **must** ignore it (forward-compat).
- The agent **never** fires a reason from a `null` metric. If
  `cpu.temps_c` is unavailable (e.g. macOS without root), `cpu_thermal_throttling`
  silently does not fire — silence is correct, not a false "all clear".
- The order in the array is stable: hard reasons first (in SPEC order),
  then soft. Don't depend on the absolute position; do depend on
  set-membership.
