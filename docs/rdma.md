# RDMA fabric monitoring

Linux-only. Auto-detected from `/sys/class/infiniband/`. Hosts without IB
devices omit `/health.rdma` entirely — the dispatcher reads absence as
"no RDMA on this node" rather than "rdma broken".

Full spec: [spec/NODE_AGENT_TRAINING_EXTENSIONS.md](../spec/NODE_AGENT_TRAINING_EXTENSIONS.md) §2.1 and §6.

## What's surfaced

```json
"rdma": {
  "enabled": true,
  "gpu_direct_supported": true,
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
      "link_layer": "Ethernet",
      "rate_gbps": 200,
      "counters": {
        "port_xmit_data_bytes": 92847362874,
        "port_rcv_data_bytes":  92845129348,
        "symbol_error_counter": 0,
        "link_error_recovery":  0,
        "link_downed":          0,
        "port_rcv_errors":      0
      },
      "last_collected_ts": 1746489600
    }
  ]
}
```

## Sources

- Device state, port state, link layer, link rate: `/sys/class/infiniband/<dev>/ports/<port>/{state,phys_state,link_layer,rate}`.
- Counters: `/sys/class/infiniband/<dev>/ports/<port>/counters/<name>`.
- Kernel modules: `/sys/module/<name>` for each of `mlx5_ib`, `mlx5_core`,
  `nvidia_peermem`, `ib_core`, `ib_uverbs`. The critical one is
  `nvidia_peermem` for GPUDirect RDMA — but see the GB10 carve-out
  below.
- `gpu_direct_supported`: filled by the health composer after both GPU
  and RDMA probes complete. `true` for discrete NVIDIA GPUs, `false` for
  unified-memory NVIDIA (GB10 / DGX Spark, where GPUDirect RDMA is
  architecturally unsupported — pinned device memory cannot be
  coherently accessed by I/O peripherals), `nil` for non-NVIDIA hosts.

No shell-outs. sysfs reads are synchronous and reliable.

## GB10 / DGX Spark and GPUDirect RDMA

Per NVIDIA's official guidance, DGX Spark's unified-memory iGPU does
not support GPUDirect RDMA: pinned memory returned by
`cudaMalloc` cannot be coherently accessed by the CPU complex or PCIe
peripherals. `nvidia_peermem`, `nv-p2p`, `dma-buf`, and `GDRCopy` are
all unsupported. The recommended path for ib_verbs applications is
`cudaHostAlloc` + `ib_reg_mr`; applications should introspect
`CU_DEVICE_ATTRIBUTE_GPU_DIRECT_RDMA_SUPPORTED` and fall back
accordingly.

The agent reflects this by reporting `rdma.gpu_direct_supported:
false` on hosts with `gpus[].vram_unified: true`, and by suppressing
the `rdma_peermem_missing` degraded reason on those hosts.

## degraded_reasons fired

Hard:

- `rdma_port_down` — any device's `state != "ACTIVE"` or `physical_state != "LINK_UP"`.
- `rdma_peermem_missing` — `nvidia_peermem` kernel module absent **AND** `gpu_direct_supported` isn't explicitly false (i.e. suppressed on GB10 / DGX Spark).
- `rdma_collector_stale` — any device's `last_collected_ts` is > 30s old.

Soft:

- `rdma_link_degraded` — active port with `rate_gbps < 200`.
- `rdma_errors_growing` (reserved; counter-delta tracking is a follow-up).
- `rdma_pfc_storm` (reserved; PFC pause-frame rate tracking is a follow-up).

## Config

```yaml
rdma:
  enabled: auto                       # auto | true | false
  collect_interval_s: 5
  pfc_storm_threshold_rx_rate: 1000
  pfc_storm_window_s: 30
  errors_growing_window_s: 60
```

The thresholds in v0.2.0 are reserved for the follow-up counter-delta
collector — they're surfaced now so `config.yaml` doesn't need another
migration when that lands.
