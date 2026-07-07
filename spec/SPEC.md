# RedTorch Node Agent — SPEC

**Status:** Design — not yet implemented
**Home:** public repo (name TBD, e.g. `redtorchinc/rt-node-agent`)
**Why public:** nodes install and self-update without needing git auth or access to this private case-manager repo.

## Purpose

Give the case-manager backend a single cross-platform HTTP surface for:
1. **Load visibility** — real VRAM, RAM, swap, CPU, Ollama resident models, runner CPU usage. Replaces fragile `/api/ps` + `nvidia-smi` assumptions in the dispatcher.
2. **Health gating** — one boolean (`degraded`) + reasons array lets `rank_nodes()` drop unhealthy nodes without re-deriving heuristics from raw metrics.
3. **Targeted actions** — `POST /actions/unload-model` so the backend can free VRAM on demand when a node approaches swap.

Out of scope: long-term metrics storage, full orchestration, training-job scheduling. Those belong in Prometheus/Grafana or a dedicated control plane.

## Target platforms

| OS | Arch | GPU path | Service manager |
|---|---|---|---|
| Linux (Ubuntu, Debian, Fedora) | amd64, arm64 | `nvidia-smi` | systemd |
| macOS (Apple Silicon) | arm64 | `ioreg` + unified memory via `sysctl` | launchd |
| macOS (Intel + eGPU) | amd64 | `nvidia-smi` if present, else CPU-only report | launchd |
| Windows 10/11, Server | amd64 | `nvidia-smi` | Windows Service (native) |
| DGX OS (Ubuntu-based) | amd64, arm64 (Grace Hopper) | `nvidia-smi` | systemd |

No WSL-specific path — DGX covers it.

## Language and dependencies

**Go 1.22+** — single static binary per OS, cross-compiles cleanly, no runtime deps on the target host.

Direct dependencies (keep minimal):
- `github.com/shirou/gopsutil/v3` — CPU, memory, swap, load avg, process enumeration. Cross-platform.
- `net/http` stdlib — server + outbound `/api/ps` probe to local Ollama.
- No external NVML bindings — shell out to `nvidia-smi` with CSV output. Simpler, works identically across OSes, easy to debug.

No framework. Stdlib router + `encoding/json`.

## Deployment model

### Install
User (or a bootstrap script from the private repo) runs on each node:
```
curl -fsSL https://<public-release-host>/install.sh | sh
```
Or platform-native:
- Linux/DGX: `curl -fsSL .../rt-node-agent_linux_$(uname -m) -o /usr/local/bin/rt-node-agent && rt-node-agent install`
- macOS: `brew install redtorchinc/tap/rt-node-agent` (future) or the same curl pattern
- Windows: download `.exe` + `rt-node-agent.exe install` (requires admin PowerShell)

### Self-install subcommand
The binary installs itself. No per-OS deploy scripts in the case-manager repo.
```
rt-node-agent install       # detects OS, writes service unit, starts service
rt-node-agent uninstall
rt-node-agent version
rt-node-agent healthcheck   # runs /health logic once to stdout, exits — used by install.sh
```

### Update
```
rt-node-agent update        # pulls latest release manifest, replaces binary, restarts service
```

Releases: GitHub Releases on the public repo with signed binaries (`cosign` or `minisign`, TBD).

## HTTP API

Listen port: **11435** (configurable via env `RT_AGENT_PORT`). Deliberately adjacent to Ollama's 11434 so operators remember the mapping.

Bind: `0.0.0.0` by default (LAN); overridable via `RT_AGENT_BIND`.

Auth: shared secret via `Authorization: Bearer <token>` header on mutating endpoints, and on the read-only `/network/*` surface (v0.3.0 — a socket inventory with cmdlines and peer maps is recon material, unlike the other read endpoints). All other read-only endpoints are open on the LAN (matches air-gapped OPSEC model). Secret lives in `RT_AGENT_TOKEN` env var or `/etc/rt-node-agent/token` file.

### `GET /health`

Primary endpoint. Backend calls this on every dispatch decision (cached 30s on the backend side).

Response:
```json
{
  "ts": 1713820000,
  "hostname": "ctrlone-Intel-R-Core-TM-i5-14400F",
  "os": "linux",
  "arch": "amd64",
  "agent_version": "0.1.0",
  "uptime_s": 345123,
  "cpu": {
    "cores_physical": 10,
    "cores_logical": 16,
    "load_1m": 2.4,
    "load_5m": 1.8,
    "load_15m": 1.5
  },
  "memory": {
    "total_mb": 32000,
    "used_mb": 27840,
    "used_pct": 87.0,
    "swap_total_mb": 16000,
    "swap_used_mb": 10240,
    "swap_used_pct": 64.0,
    "unified": false
  },
  "gpus": [
    {
      "index": 0,
      "name": "NVIDIA GeForce RTX 3090",
      "vram_total_mb": 24576,
      "vram_used_mb": 17441,
      "vram_used_pct": 71.0,
      "util_pct": 4,
      "temp_c": 43,
      "power_w": 117,
      "power_cap_w": 420,
      "processes": [
        {"pid": 556534, "name": "python3", "cmdline_head": "uvicorn service.main:app", "vram_used_mb": 16188},
        {"pid": 2162806, "name": "ollama", "cmdline_head": "ollama runner --model", "vram_used_mb": 888},
        {"pid": 4519, "name": "gnome-shell", "cmdline_head": "/usr/bin/gnome-shell", "vram_used_mb": 181}
      ]
    }
  ],
  "service_allocators": [
    {
      "name": "gliner2-service",
      "url": "http://localhost:8077/v1/debug/gpu",
      "scrape_ok": true,
      "allocated_mb": 1864.8,
      "reserved_mb": 1890.0,
      "max_allocated_mb": 1874.7,
      "cache_overhead_pct": 1.4,
      "last_scrape_ts": 1713820000
    }
  ],
  "ollama": {
    "up": true,
    "endpoint": "http://localhost:11434",
    "models": [
      {
        "name": "nomic-embed-text-v2-moe:latest",
        "size_mb": 955,
        "processor": "100% GPU",
        "context": 512,
        "until_s": 3500
      }
    ],
    "runners": [
      {
        "pid": 2162806,
        "cpu_pct": 244.0,
        "rss_mb": 5632
      }
    ]
  },
  "degraded": false,
  "degraded_reasons": []
}
```

`memory.unified: true` on unified-memory hosts (Apple Silicon, NVIDIA GB10 / Grace-Blackwell) — signals to the ranker that RAM pressure = GPU pressure. Per-GPU `gpus[].vram_unified: true` is set on the same hosts; on these the agent back-fills `vram_total_mb` from `memory.total_mb` and `vram_used_mb` from per-process accounting so `vram_used_pct` is a real percentage and `vram_over_*pct` reasons fire normally.

### v0.2.3 additions (additive, forward-compat)

- `memory.swap_in_pages_total` / `memory.swap_out_pages_total` — cumulative kernel counters from `/proc/vmstat` (Linux only). Use `rate()` across two scrapes for "pages swapped per second".
- `memory.pressure_some_avg10` / `_avg60` / `pressure_full_avg10` / `_avg60` — raw PSI gauges. Backend can pick its own threshold instead of inheriting the agent's `"normal"`/`"some"`/`"full"` classification.
- `top_swap_processes[]` — up to 10 processes ranked by `/proc/<pid>/status:VmSwap`, descending. Useful for "who's the noisy neighbour?" on a thrashing host.
- `databases[]` — auto-detected DB servers (20 fingerprints incl. Postgres, MySQL, MongoDB, Redis, Neo4j, ChromaDB). Process-based detection, no config.
- `storage[]` — auto-detected NAS / pooled storage (ZFS, NFS, CIFS, Ceph, GlusterFS, Lustre). Cap from `statfs`; ZFS pool health from `/proc/spl/kstat/zfs`.

### Cross-node time alignment (v0.2.x time_sync extension)

```json
"time_sync": {
  "now_unix_ns": 1748227201123456789,
  "now_iso": "2026-05-22T14:00:01.123456789Z",
  "tz_name": "UTC",
  "tz_offset_s": 0,
  "source": "chrony",
  "synced": true,
  "skew_ms": 0.42,
  "stratum": 3,
  "last_update_s": 12,
  "server": {
    "host": "time.cloudflare.com",
    "offset_ms": 1.23,
    "rtt_ms": 14.5,
    "stratum": 3,
    "last_probe_age_s": 7,
    "probe_interval_s": 60
  }
}
```

- `now_unix_ns` is the node's wall clock at /health composition time, in nanoseconds since the Unix epoch. Always populated. **Coarse cross-node comparison primitive:** the case-manager subtracts this from its own clock to compute a per-node offset (corrected by RTT/2 if it needs sub-ms precision). It is stamped before serialization, so its accuracy is bounded by the remaining compose+write tail — for sub-ms Offset B use `GET /time` instead.
- `now_iso` is the same value rendered as RFC3339Nano UTC. Redundant with `now_unix_ns` but cheaper for log dashboards.
- `tz_name` / `tz_offset_s` describe the node's configured local time zone.
- `source` / `synced` / `skew_ms` / `stratum` / `last_update_s` come from the local OS sync daemon (chrony or systemd-timesyncd on Linux, sntp on darwin). Absent on Windows (no `w32tm` parser in v0.2.x) and on Linux hosts without either daemon.
- `server` is the agent's own NTP probe against `timesync.server` from config (default `time.cloudflare.com`). 60s background cadence. `offset_ms` is **LOCAL minus SERVER** (positive = local is ahead of the reference). `error` populates instead when the last probe failed. Omitted entirely when `timesync.server: ""` in config.
- `clock_skew_high` and `clock_offset_high` are two independent soft degraded reasons — the former reads the OS daemon's view, the latter reads the agent's own probe. Both can fire together when the local daemon and the external reference disagree.
- `clock_offset_high` fires when `|offset_ms|` exceeds `timesync.offset_degraded_ms` (config, default `100`). Setting it to `0` (or negative) **disables** the reason — intended for measure-only fleets whose node clocks intentionally free-run (the offset is consumed/compensated by the backend rather than disciplined on the node), where a fixed threshold would otherwise latch permanently.

### Two clock offsets (and which to use)

The fleet exposes **two distinct offsets**, both meaningful:

- **Offset A — node ↔ reference.** The node's clock vs. the configured `timesync.server` (an internal NTP box on air-gapped fleets). Measured by the agent's background probe; surfaced as `time_sync.server.offset_ms` (sign **node − reference**, positive = node ahead) and echoed in `GET /time`. Use for cross-node alignment: `node_i − node_j = A_i − A_j`.
- **Offset B — caller ↔ node.** The calling backend's clock vs. the node's clock, measured by the backend at call time via `GET /time` (below). Use to interpret a node's reported timestamps directly in the backend's own clock frame.

A and B are independent: A anchors every node to a shared reference; B anchors each node to *this* caller, which may run on a different reference (or none).

### `GET /time` (v0.2.14 — caller↔node offset handshake)

A minimal NTP-over-HTTP four-timestamp exchange for sub-millisecond **Offset B**, independent of `/health` composition cost. Open read endpoint (LAN trust, same posture as `/health`). The agent does no probing/allocation between the receive and transmit stamps, so the round-trip the caller measures is dominated by the network path.

Request: `GET /time?t1=<unix_ns>` — `t1` is the caller's send time in Unix nanoseconds (optional; echoed as `0` when absent).

```json
{
  "t1_unix_ns": 1749600000000000000,
  "t2_unix_ns": 1749600000001234567,
  "t3_unix_ns": 1749600000001240000,
  "server": {
    "host": "10.0.0.10",
    "offset_ms": 1.23,
    "rtt_ms": 0.4,
    "stratum": 3,
    "last_probe_age_s": 7,
    "probe_interval_s": 60
  }
}
```

- `t2_unix_ns` is the node's receive time (handler entry); `t3_unix_ns` is the node's transmit time (just before the write). The caller records `t4` on receipt and computes, per RFC 5905 §8:
  - `offset_ms = (((t2 − t1) + (t3 − t4)) / 2) / 1e6` — positive ⇒ **node ahead of caller** (add to the caller's clock to match the node).
  - `rtt_ms = ((t4 − t1) − (t3 − t2)) / 1e6`.
- `server` is the same cached probe snapshot as `/health.time_sync.server` (Offset A), folded in so a caller gets both offsets from one cheap call. Omitted when `timesync.server` is empty.
- Older agents (pre-0.2.14) lack this endpoint — feature-detect via `capabilities.time_handshake_supported` and fall back to `time_sync.now_unix_ns` + the caller's own RTT/2.

### `degraded_reasons` contract

The ranker consumes this directly. Ordered by severity; backend skips the node if any of the "hard" reasons are present.

Hard (skip node):
- `"ollama_down"` — Ollama HTTP not responding within 2s
- `"swap_over_75pct"` — swap is thrashing
- `"vram_over_95pct"` — no room to load anything
- `"agent_stale"` — agent's own view of ollama is older than 60s (runner probe failing)
- `"vram_service_creep_critical"` — a tracked `service_allocators` entry shows `reserved_mb / allocated_mb > 3.0` AND `reserved_mb > threshold_critical`. Signals a PyTorch allocator leak of the kind observed 2026-04-22 on the gliner2-service box where cache hoarded 16 GB while real usage was 2 GB. Node cannot accept ollama models until the offending service restarts.

Soft (deprioritize but usable):
- `"swap_over_50pct"`
- `"vram_over_90pct"`
- `"load_avg_over_2x_cores"` — CPU saturated
- `"ollama_runner_stuck"` — runner pid exists but CPU at 0 for >60s with queued requests
- `"vram_service_creep_warn"` — tracked `service_allocators` entry shows `reserved_mb / allocated_mb > 2.0` (less severe than the hard threshold). Early signal; backend logs and continues to use the node.

### Service allocator scraping

For Python services that can expose a `/debug/gpu` JSON endpoint (gliner2-service does as of 2026-04-22), the agent can scrape it alongside hardware metrics and surface PyTorch allocator stats in `service_allocators[]`. This catches the "nvidia-smi shows 16 GB used but the service only actually needs 2 GB" class of leak that was invisible to hardware-only metrics.

Config lives in `/etc/rt-node-agent/config.yaml`:
```yaml
service_allocators:
  - name: gliner2-service
    url: http://localhost:8077/v1/debug/gpu
    threshold_warn_mb: 4096     # reserved_mb above this with reserved/allocated > 2.0 → vram_service_creep_warn
    threshold_critical_mb: 10240 # reserved_mb above this with reserved/allocated > 3.0 → vram_service_creep_critical
    scrape_interval_s: 30
```

Expected response shape from the service:
```json
{"allocated_mb": 1864.8, "reserved_mb": 1890.0, "max_allocated_mb": 1874.7}
```

Scrape budget: 1s per service with 30s cache. If a scrape fails, the entry shows `scrape_ok: false` and is ignored for `degraded_reasons` (don't punish the node for an optional metric being down).

### `GET /metrics`

Prometheus text format. Same data as `/health`, flat. Optional — behind `RT_AGENT_METRICS=1` env.

### `GET /version`

```json
{"version": "0.1.0", "git_sha": "abc1234", "build_time": "2026-04-22T10:00:00Z"}
```

### `GET /network/{sockets,flows,resolve}` (v0.3.0 — flow ownership)

Read-only, **Bearer-gated** (same token as `/actions/*`). Maps
gateway-observed NetFlow/IPFIX tuples to the local process, user, and
service unit owning the socket, so the backend can attribute traffic to
workloads instead of bare host IPs. Full wire contract with shapes,
match-confidence tiers, redaction rules, and platform notes:
[docs/api/network-flows.md](../docs/api/network-flows.md).

- `/network/sockets` — current sockets with owner metadata (pid, process,
  cmdline_head after secret redaction, user, systemd unit / container id
  from cgroup on Linux).
- `/network/flows` — rolling window (default 300s) of live **and
  recently-closed** sockets, so late-arriving NetFlow records still
  resolve. Ownership only — no byte/packet counters in this slice (the
  field names are reserved; they'd need netlink `inet_diag`).
- `/network/resolve?proto=&local_addr=&local_port=&remote_addr=&remote_port=` —
  one 5-tuple → best owner with deterministic per-tier confidence
  (`0.97` exact-live … `0.50` port-only; `not_found` is HTTP 200).

Envelope carries `training_run_id` when training mode is active — the
backend's temporal-join key. There is deliberately **no per-socket
workflow attribution**: the agent cannot know case-manager workflow
identity (pull-based, never talks to the backend).

Feature detection: `/capabilities.network_flows_supported`. Config:
`network.flows_enabled: auto|true|false` plus `poll_interval_s`,
`window_s`, `cmdline_max_bytes` (docs/config.md). Disabled ⇒ routes not
registered (404, same as pre-v0.3.0 agents) and capability `false`.

### `POST /actions/unload-model`

Request (requires Bearer token):
```json
{"model": "qwen3-vl:32b"}
```
Response:
```json
{"status": "ok", "unloaded": ["qwen3-vl:32b"], "took_ms": 245}
```

Implementation: shells to `ollama stop <model>` (Ollama 0.5+ supports this) or POSTs to `/api/generate` with `{"keep_alive": 0}` as a fallback.

Errors:
- 401 on missing/invalid token
- 404 if model not loaded (idempotent — still returns 200 with empty `unloaded`)
- 500 if ollama itself is unreachable

### `POST /actions/restart-ollama` (future, not v1)

Deliberately not in v1. Restarting ollama mid-inference loses user work. Require analyst approval from the case-manager UI before exposing this.

## Security model

- **LAN-only** by default; bind override allowed but documented as opt-in risk.
- Read endpoints (`/health`, `/metrics`, `/version`) open on LAN — matches air-gapped OPSEC. No PII, no case data ever flows through this agent.
- Mutating endpoints (`/actions/*`) require shared-secret Bearer token. Rotated by writing a new `/etc/rt-node-agent/token` + `systemctl restart rt-node-agent`.
- The read-only `/network/*` surface (v0.3.0) also requires the Bearer token — process/socket inventories with command lines and peer maps are reconnaissance material. Command lines are secret-redacted before emission (see docs/api/network-flows.md §Privacy).
- TLS: deferred to v2. Nodes are on the trusted LAN behind the same firewall as the backend. If this ever ships to a non-air-gapped environment, switch to mTLS before exposing beyond LAN.
- No remote shell, no arbitrary command execution, no file read/write endpoints. Ever.

## Backend integration (case-manager repo side)

1. New file `backend/app/services/node_health.py`:
   - `async def get_node_health(base_url: str) -> NodeHealth | None` with a 2s timeout and 30s cache.
   - Converts agent URL from ollama URL: `http://198.51.100.122:11434` → `http://198.51.100.122:11435` (example IP from RFC 5737 TEST-NET-2).
   - Agent port configurable via `config/ollama.yaml` if someone runs a non-default.

2. `ollama_service.rank_nodes()` gains an optional async variant that consults `get_node_health` before returning. Existing sync callers keep working; the dispatcher uses the async variant.

3. `model_configs` table keeps EMA latency as a secondary signal. Agent health is primary — EMA is the tie-breaker.

4. New settings row `agent_required: bool = false`. When true, nodes without a reachable agent are skipped. Default false so adoption is gradual — nodes with no agent installed degrade gracefully to the current behavior.

## Failure modes and graceful degradation

- **Agent down on a node** → backend treats as `{degraded: false, reasons: []}` and proceeds with existing ranking logic. The only missing signal is load awareness on that node.
- **Agent hung** (2s timeout) → same as down. Backend logs once per 60s per node, doesn't spam.
- **Agent returns 5xx** → same.
- **Agent schema drift** (new field backend doesn't know) → ignored. Backend only reads known fields.
- **Old agent version** (missing a new field) → field absent = feature disabled for that node.

## Non-goals for v1

- Pushing metrics to the backend (pull-based only)
- Persistent metrics storage on the node
- Log shipping
- Auto-triggered model unloading (agent reports, backend decides)
- Multi-tenant auth
- Web UI on the agent itself
- Training job coordination

## Development and release plan

1. Scaffold public repo: `main.go`, `cmd/install.go`, `internal/gpu/`, `internal/health/`, `internal/server/`, cross-compile `Makefile`.
2. Implement `/health` for Linux NVIDIA first (the 10-node cluster here is mostly that).
3. Add Apple Silicon path (unified memory, ioreg GPU model detection).
4. Add Windows path.
5. Add `service_allocators` scrape loop — generic JSON-endpoint poller with per-service config, no hardcoded shapes.
6. GitHub Actions workflow: cross-compile, sign, publish to Releases on tag.
7. `install.sh` curl-able from the public repo or a dedicated release CDN.
8. Case-manager integration lands as a separate PR in this private repo, behind `agent_required: false` default.

## Open questions

- Binary signing: `cosign` (sigstore ecosystem) or `minisign` (simpler, no OIDC)? Pick at repo init time.
- Release hosting: GitHub Releases only, or add a simple CDN for `curl | sh` install? GitHub works fine for v1.
- Mac install via Homebrew tap: nice-to-have, not v1 blocker.
- DGX Grace Hopper is arm64 — need to test `nvidia-smi` output parsing stays identical on ARM.
- Service allocator scrape contract — should the agent define a JSON shape (`allocated_mb` / `reserved_mb` / `max_allocated_mb`) that cooperating services must expose, or should it be adapter-based per service? v1 picks the fixed shape since gliner2-service is the only target. Adapter model can come later if more service types need allocator visibility (torchserve, ray-serve, vllm).
