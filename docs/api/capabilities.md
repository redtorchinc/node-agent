# `GET /capabilities`

Static-ish snapshot of what this agent build can do on this OS. Read-only,
no auth. The dispatcher reads it once per node to decide which features to
rely on without parsing semver.

## Response

```json
{
  "agent_version": "0.2.0",
  "config_version": 2,
  "os": "linux",
  "arch": "arm64",
  "platforms_supported": ["ollama", "vllm"],
  "actions_supported": ["unload-model", "service", "training-mode"],
  "services_allowlist": ["rt-vllm-qwen3.service", "rt-vllm-llama-70b.service"],
  "rdma_available": true,
  "training_mode_supported": true,
  "metrics_enabled": true,
  "time_handshake_supported": true,
  "network_flows_supported": true,
  "system_metrics_fields_supported": [
    "cpu.usage_pct", "cpu.usage_per_core_pct", "cpu.load_1m",
    "memory.unified", "memory.pressure", "memory.huge_pages_total",
    "gpu.vram_used_pct", "gpu.processes", "gpu.nvlink",
    "disk.used_pct", "network.interfaces", "time_sync.skew_ms"
  ]
}
```

## Semantics

- `rdma_available` is `true` only when `/sys/class/infiniband` has devices.
  Inference-only Macs / consumer GPU boxes always report `false`.
- `training_mode_supported` is `true` on every OS — the state machine is
  cross-platform. The endpoint will still 503 if the host can't actually
  drain Ollama (e.g. no Ollama running).
- `time_handshake_supported` is `true` from v0.2.14 on — the node serves
  `GET /time` (the caller↔node NTP-style offset handshake). When `false`
  (older agents), fall back to `time_sync.now_unix_ns` + the caller's own
  RTT/2. See [time.md](time.md).
- `network_flows_supported` is `true` from v0.3.0 on when the Bearer-gated
  `/network/{sockets,flows,resolve}` flow-ownership surface is enabled
  (`network.flows_enabled`, default on). `false` (or the key absent, on
  pre-v0.3.0 agents) means the routes 404 — the gateway must not attempt
  flow resolution against this node. See
  [network-flows.md](network-flows.md).
- `services_allowlist` lists exactly what `POST /actions/service` will
  accept. Anything outside this list returns 403.
- `system_metrics_fields_supported` is the canonical list of `/health`
  paths populated by this build on this OS — Mac responses are shorter,
  DGX responses longer. Use this rather than parsing the agent version.
