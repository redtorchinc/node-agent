# `GET /health`

The primary endpoint. The case-manager backend reads this on every dispatch
decision (cached 30s on the backend side). No auth required — LAN-only by
default, matches the air-gapped OPSEC model.

All v0.2.0 additions are **additive**. v0.1.x backends keep working with the
fields they already know about.

## Shape

```json
{
  "ts": 1746489600,
  "hostname": "dgx-01",
  "os": "linux",
  "arch": "arm64",
  "agent_version": "0.2.2",
  "uptime_s": 345123,

  "cpu": {
    "model": "AMD EPYC 9654 96-Core Processor",
    "vendor": "AuthenticAMD",
    "cores_physical": 96,
    "cores_logical": 192,
    "freq_mhz_current": 3712,
    "usage_pct": 41.2,
    "usage_per_core_pct": [38.4, 42.1, …],
    "load_1m": 28.4,
    "load_5m": 22.8,
    "load_15m": 19.1,
    "temps_c": [{"sensor": "Tctl", "value": 58.4}, …],
    "throttled": false
  },

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
    "swap_in_pages_total": 12389123,
    "swap_out_pages_total": 89432019,
    "unified": false,
    "pressure": "normal",
    "pressure_some_avg10": 0.12,
    "pressure_some_avg60": 0.07,
    "pressure_full_avg10": 0.00,
    "pressure_full_avg60": 0.00,
    "huge_pages_total": 2048,
    "huge_pages_free": 0
  },

  "top_swap_processes": [
    {"pid": 2740534, "name": "VLLM::EngineCore", "swap_mb": 1843, "cmdline_head": "/opt/vllm/bin/python -m vllm.entrypoints.openai.api_server --model …"},
    {"pid": 1234, "name": "systemd-journald", "swap_mb": 412, "cmdline_head": "/usr/lib/systemd/systemd-journald"}
  ],

  "databases": [
    {"name": "postgres", "pid": 8123, "process_name": "postgres", "version": "15", "ports": [5432], "rss_mb": 412, "cpu_pct": 0.4, "uptime_s": 5234101},
    {"name": "redis", "pid": 9012, "process_name": "redis-server", "ports": [6379], "rss_mb": 28}
  ],

  "storage": [
    {"type": "zfs", "pool_name": "tank", "pool_health": "ONLINE"},
    {"type": "zfs", "mountpoint": "/tank/models", "pool_name": "tank", "total_gb": 4096.0, "used_gb": 1834.2, "used_pct": 44.8},
    {"type": "nfs4", "mountpoint": "/mnt/shared", "server": "10.0.0.5", "export": "/srv/shared", "nfs_version": "4.2", "total_gb": 10000.0, "used_gb": 6234.1, "used_pct": 62.3}
  ],

  "gpus": [
    {
      "index": 0,
      "uuid": "GPU-...",
      "name": "NVIDIA H100 80GB HBM3",
      "driver_version": "550.54.15",
      "cuda_version": "12.4",
      "compute_capability": "9.0",
      "vram_total_mb": 81920,
      "vram_used_mb": 41280,
      "vram_used_pct": 50.4,
      "vram_unified": false,
      "util_pct": 78,
      "temp_c": 71,
      "power_w": 412,
      "clock_graphics_mhz": 1980,
      "throttle_reasons": [],
      "ecc_volatile_uncorrected": 0,
      "mig_mode": "Disabled",
      "nvlink": {
        "supported": true,
        "links": [{"link": 0, "state": "Up", "speed_gb_s": 25}]
      },
      "processes": [{"pid": 556534, "name": "python3", "vram_used_mb": 31800}]
    }
  ],

  "disk": [
    {"path": "/", "fstype": "ext4", "total_gb": 1800, "used_gb": 412, "used_pct": 22.9},
    {"path": "/var/lib/ollama", "fstype": "ext4", "total_gb": 3600, "used_gb": 2940, "used_pct": 81.7}
  ],

  "network": {
    "hostname_fqdn": "dgx-01.lan.internal",
    "interfaces": [
      {"name": "eno1", "up": true, "mtu": 1500, "ipv4": ["192.168.50.122"], "rx_bytes_total": 12345678}
    ]
  },

  "time_sync": {"source": "chrony", "synced": true, "skew_ms": 0.42, "stratum": 3},

  "service_allocators": [
    {"name": "gliner2-service", "scrape_ok": true, "allocated_mb": 1864.8, "reserved_mb": 1890.0, ...}
  ],

  "platforms": {
    "ollama": {
      "up": true,
      "endpoint": "http://localhost:11434",
      "probe_interval_s": 5,
      "stale": false,
      "models": [
        {"name": "nomic-embed-text-v2-moe:latest", "platform": "ollama", "loaded": true, "size_mb": 955}
      ],
      "runners": [{"pid": 2162806, "cpu_pct": 244.0, "rss_mb": 5632}]
    },
    "vllm": {
      "up": true,
      "endpoint": "http://localhost:8000",
      "probe_interval_s": 5,
      "stale": false,
      "models": [
        {
          "name": "qwen3-vl:32b",
          "platform": "vllm",
          "loaded": true,
          "context_window": 32768,
          "queue": {"running": 2, "waiting": 0},
          "kv_cache": {"gpu_usage_pct": 74.3, "prefix_cache_hit_rate": 0.61},
          "latency_ms": {"ttft_p50": 142, "ttft_p99": 480},
          "counters": {"requests_success_total": 18472, "prompt_tokens_total": 92481723}
        }
      ]
    }
  },

  "services": [
    {"unit": "rt-vllm-qwen3.service", "active_state": "active", "sub_state": "running", "main_pid": 12345, "memory_mb": 8192}
  ],

  "ollama": { /* legacy v0.1.x shape — alias of platforms.ollama for compat */ },

  "rdma": {
    "enabled": true,
    "gpu_direct_supported": true,
    "kernel_modules": {"mlx5_ib": true, "nvidia_peermem": true, ...},
    "devices": [
      {"name": "rocep1s0f0", "port": 1, "state": "ACTIVE", "physical_state": "LINK_UP",
       "rate_gbps": 200, "counters": {...}, "last_collected_ts": 1746489600}
    ]
  },

  "mode": "inference",
  "training": null,

  "degraded": false,
  "degraded_hard": false,
  "degraded_soft": false,
  "degraded_reasons": []
}
```

### `degraded` vs. `degraded_hard` / `degraded_soft`

The `degraded_reasons` array is the source of truth — the three booleans
are derived. `degraded_hard` is true iff any HARD reason is firing;
`degraded_soft` is true iff any SOFT reason is firing. Both can be true
simultaneously (a node with both hard + soft reasons).

`degraded` is kept for v0.1.x / v0.2.x backend compatibility — it
mirrors `degraded_hard`. New consumers should branch on `degraded_hard`
(skip the node) and `degraded_soft` (deprioritize) independently. The
legacy `degraded` field will be removed in v0.3.0.

Pre-v0.2.8 the agent emitted `degraded: false` alongside non-empty
`degraded_reasons` when only soft reasons fired, which read as
self-contradictory; the explicit `degraded_hard` / `degraded_soft`
removes that ambiguity.

## Per-architecture coverage

Not every host can supply every field. The rules:

- **Linux only:** `memory.swap_in/out_pages_total` (kernel `/proc/vmstat`
  counters), `memory.pressure_some/full_avg10/60` (PSI), and
  `top_swap_processes[]` (per-process `VmSwap`). macOS and Windows return
  absent fields or an empty array — silence beats fabrication.
- **`databases[]`** detects 20 well-known DB servers (Postgres, MySQL,
  MariaDB, MongoDB, Redis, Memcached, Cassandra, ScyllaDB, Elasticsearch,
  OpenSearch, Neo4j, InfluxDB, ClickHouse, CockroachDB, etcd, Qdrant,
  Weaviate, Milvus, ChromaDB, DragonflyDB) by process name + cmdline.
  No credentials, no config — surfaces presence, PID, listening ports,
  RSS, CPU%, uptime, and best-effort version. Ports are absent when the
  agent lacks permission to enumerate sockets for a PID it doesn't own.
- **`storage[]`** auto-detects NAS / pooled storage: ZFS pools (via
  `/proc/spl/kstat/zfs`), NFSv3/v4, CIFS/SMB, Ceph, GlusterFS, Lustre.
  Capacity from `statfs`; NFS version from mount options.
- **Unified-memory hosts (Apple Silicon, NVIDIA GB10 / Grace-Blackwell):**
  `gpus[].vram_unified: true` and `memory.unified: true`. The agent
  back-fills `vram_total_mb` from `memory.total_mb` and `vram_used_mb`
  from per-process accounting (falling back to host `memory.used_mb`
  when no per-process VRAM data exists, as on Apple Silicon). Result:
  `vram_used_pct` is a real percentage on these boxes, not a misleading
  `0.0`, and `vram_over_*pct` reasons fire normally. Apple Silicon
  `gpus[].temp_c` / `power_w` are still `0` unless the agent runs as root.
- **Windows:** `cpu.load_*` is `0` (no kernel load average). `time_sync`
  is omitted (no `w32tm` parser in v0.2).
- **macOS / Windows:** `rdma` is always omitted.
- **Any platform without a probe path** (e.g. CPU temps without
  `/sys/class/hwmon` on Linux) leaves the field absent rather than emitting
  a fake zero. Never guess from a `null` field.

See [config.md](../config.md) and [spec/SPEC.md](../../spec/SPEC.md) for field semantics.

## Field omission rules

| Condition | Omitted |
|---|---|
| No RDMA hardware | `rdma` |
| No NTP probe possible (e.g. macOS without `sntp`) | `time_sync` |
| `training_mode` not engaged | `training` |
| Platform `enabled: false` | corresponding `platforms.{name}` *kept but `up: false`* |
| Per-field absence | `omitempty` JSON tag on the field |
