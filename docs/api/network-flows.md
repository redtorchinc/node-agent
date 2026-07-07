# Network flow ownership API

Issue: #21 · Shipped: v0.3.0 · Capability flag: `network_flows_supported`

Three read-only endpoints that map gateway-observed NetFlow/IPFIX records
to the local process, user, and service unit that owns the socket on this
node. The gateway sees the wire; the node-agent sees the host. These
endpoints are the join between them — no DPI, no payload capture.

All examples in this document use synthetic values (RFC 5737
documentation address ranges, placeholder unit and user names).

## Endpoint summary

| Method | Path | Purpose | Auth |
|---|---|---|---|
| `GET` | `/network/sockets` | Current sockets with owner metadata. | **Bearer** |
| `GET` | `/network/flows` | Rolling window of live + recently-closed sockets. | **Bearer** |
| `GET` | `/network/resolve` | One gateway 5-tuple → best local owner. | **Bearer** |

**Auth: these are the only read endpoints that require the Bearer token**
(same token as `POST /actions/*`, from `RT_AGENT_TOKEN` or
`/etc/rt-node-agent/token`). A socket inventory with command lines,
usernames, and peer maps is reconnaissance material — it does not ride
the open-LAN read policy that `/health` does.

Feature-detect via `GET /capabilities` → `network_flows_supported: true`.
Setting `network.flows_enabled: false` in config.yaml disables the
feature entirely: the routes are not registered (404, as on pre-v0.3.0
agents) and the capability reports `false`.

## Configuration

```yaml
network:
  flows_enabled: auto     # auto (default, = true) | true | false
  poll_interval_s: 10     # background socket-table sample cadence
  window_s: 300           # retention for closed sockets
  cmdline_max_bytes: 240  # cmdline_head cap, applied after redaction
```

## Common response envelope

Every response carries:

```json
{
  "ts_unix_ns": 1783290000123456789,
  "hostname": "node-example-01",
  "agent_version": "0.3.0",
  "source": "procfs",
  "stale": false,
  "partial": false,
  "warnings": [],
  "training_run_id": "run-example-17"
}
```

- `source` — the data path on this OS: `procfs` (Linux), `lsof` (macOS),
  `iphlpapi` (Windows).
- `stale: true` — the most recent socket-table sample failed or is older
  than 3× `poll_interval_s`; entries reflect the last good sample.
- `partial: true` — usable data, but part of the result lacked
  permission or platform support (e.g. sockets owned by other users when
  the agent is not root). `warnings[]` says what was dropped. Missing
  fields are omitted, never fabricated. Kernel-owned sockets are still
  listed but never counted here: `time_wait`/`syn_recv` on any agent,
  and — on a fully-privileged agent (root or the v0.3.1 capability
  grant) — *any* pid-less socket, since with full privilege pid-less
  means kernel-owned (in-kernel NFS/iSCSI clients show as `established`
  with no process). Earlier versions counted all of these and pinned
  `partial: true` on any node with connection churn or an NFS mount.
  A privileged agent thus reports `partial: true` only when a process
  exits mid-sample; an under-privileged one reports it with a warning
  naming the missing capabilities.
- `training_run_id` — present only while the node is in training mode
  (the `run_id` from `POST /actions/training-mode`). This is the
  backend's temporal-join key: "flows observed while run X was active."
  **There is deliberately no per-socket workflow attribution** — the
  agent cannot know case-manager workflow identity (it is pull-based and
  never talks to the backend); joining flows to cases/jobs is backend
  work.

## `GET /network/sockets`

Current live sockets with owner metadata.

### Query parameters

| Name | Type | Notes |
|---|---|---|
| `state` | string | Socket state filter: `listen`, `established`, `time_wait`, … |
| `proto` | string | `tcp` or `udp`. |
| `port` | integer | Matches local **or** remote port. |
| `pid` | integer | Owner process filter. |
| `limit` | integer | Max results, default `1000`, max `10000`. |

### Shape

```json
{
  "ts_unix_ns": 1783290000123456789,
  "hostname": "node-example-01",
  "agent_version": "0.3.0",
  "source": "procfs",
  "stale": false,
  "partial": false,
  "warnings": [],
  "items": [
    {
      "proto": "tcp",
      "state": "established",
      "local_addr": "198.51.100.10",
      "local_port": 9000,
      "remote_addr": "198.51.100.20",
      "remote_port": 50123,
      "pid": 4242,
      "process_name": "python3",
      "cmdline_head": "/opt/venv/bin/python -m vllm.entrypoints.openai.api_server --api-key [redacted] --port 9000",
      "exe": "/opt/venv/bin/python3.11",
      "user": "svc",
      "uid": 990,
      "service": "rt-vllm-example.service",
      "container_id": "",
      "container_name": "",
      "cgroup": "/system.slice/rt-vllm-example.service",
      "first_seen_unix_ns": 1783289999000000000,
      "last_seen_unix_ns": 1783290000123456789
    }
  ]
}
```

Direct observations carry no per-item confidence — that concept belongs
to `/network/resolve`, where the agent is inferring a match.

## `GET /network/flows`

Rolling window of live **and recently-closed** sockets (retained
`window_s` seconds after last sight) so late-arriving NetFlow records
still resolve. Not a packet capture — an ownership view.

### Query parameters

| Name | Type | Notes |
|---|---|---|
| `since_unix_ns` | integer | Lower bound on `last_seen_unix_ns`. |
| `proto` | string | `tcp` or `udp`. |
| `local_port` | integer | Local port filter. |
| `remote_addr` | string | Remote address filter (normalized before compare). |
| `pid` | integer | Owner process filter. |
| `limit` | integer | Max results, default `1000`, max `10000`. |

### Shape

The response adds `window_s`; items are `sockets` items plus:

```json
{
  "flow_id": "sha1:5d1be29ef2c1b0a8c4e6f81a3b5c7d9e0f2a4b6c",
  "direction_hint": "egress",
  "live": false
}
```

- `flow_id` — stable SHA-1 over tuple + owner + first-sight, for dedup
  across polls.
- `direction_hint` — `egress` | `ingress` | `unknown`. A hint, not
  truth: the agent samples the socket table and cannot observe the SYN.
  A connected socket whose local port is also a listening port on this
  node is `ingress`; other connected sockets are `egress`. Omitted for
  listening/unconnected sockets.
- `live: false` — the socket has closed; the entry survives until
  `window_s` expires.

Byte/packet counters are **not emitted in v0.3.0**. The field names
`bytes_sent_delta`, `bytes_recv_delta`, `packets_sent_delta`,
`packets_recv_delta` are reserved as optional additive fields for a
later slice (they require netlink `inet_diag`, a new dependency); the
gateway already has flow volumes from NetFlow itself. Consumers must
treat them as optional and absent-by-default.

## `GET /network/resolve`

Resolves one gateway-observed flow to the best local owner. The gateway
normalizes the tuple to the node perspective (`local_*` = this node's
side) and calls the likely inside node.

### Query parameters

| Name | Type | Required | Notes |
|---|---|---:|---|
| `proto` | string | yes | `tcp` or `udp`. |
| `local_addr` | string | yes | This node's address for the flow. |
| `local_port` | integer | yes | This node's port. |
| `remote_addr` | string | yes | Peer address. |
| `remote_port` | integer | yes | Peer port. |
| `observed_at_unix_ns` | integer | no | Gateway observation timestamp; improves the `not_found` diagnosis when outside the retention window. |

Addresses are normalized before matching (IPv4-mapped IPv6 collapsed,
zone identifiers stripped, wildcard binds cover any queried local
address).

### Shape

```json
{
  "ts_unix_ns": 1783290000123456789,
  "hostname": "node-example-01",
  "agent_version": "0.3.0",
  "source": "procfs",
  "stale": false,
  "partial": false,
  "warnings": [],
  "training_run_id": "run-example-17",
  "query": {
    "proto": "tcp",
    "local_addr": "198.51.100.10",
    "local_port": 44218,
    "remote_addr": "203.0.113.50",
    "remote_port": 443,
    "observed_at_unix_ns": 1783290000000000000
  },
  "match": {
    "status": "matched",
    "confidence": 0.97,
    "reason": "exact 5-tuple, socket live",
    "socket": {
      "proto": "tcp",
      "state": "established",
      "local_addr": "198.51.100.10",
      "local_port": 44218,
      "remote_addr": "203.0.113.50",
      "remote_port": 443,
      "live": true
    },
    "owner": {
      "pid": 4242,
      "process_name": "python3",
      "cmdline_head": "/opt/venv/bin/python -m trainer.launch --run run-example-17",
      "exe": "/opt/venv/bin/python3.11",
      "user": "svc",
      "uid": 990,
      "service": "rt-train-example.service",
      "container_id": "",
      "container_name": "",
      "cgroup": "/system.slice/rt-train-example.service"
    }
  }
}
```

## Match semantics

Confidence is a fixed constant per match tier — deterministic and
explainable, so the backend can threshold without modeling agent-version
drift. Tiers are tried strongest-first:

| Tier | `status` | `confidence` | Notes |
|---|---|---:|---|
| Exact 5-tuple, socket live | `matched` | `0.97` | Best case. |
| Exact 5-tuple from retention window | `probable` | `0.90` | Socket closed but seen within `window_s`. |
| Listening socket covers inbound flow | `probable` | `0.85` | Accept path may be a separate child PID. |
| UDP local-port owner | `probable` | `0.80` | Unconnected UDP socket on the queried port. |
| Port-only owner | `probable` | `0.50` | Weak hint — do not automate policy on this. |
| No owner | `not_found` | `0.0` | HTTP 200, never 404 (404 = unknown route / feature off). |

If several distinct PIDs tie at the winning tier, `status` becomes
`ambiguous`, confidence is scaled down (×0.7), and the most recently
seen candidate is returned with a reason explaining the tie.

## Platform notes

Mirrors the repo platform matrix — Linux is the reference
implementation, everything else is best-effort with `partial`/`source`
signaling:

| OS | Socket source | `service` / `container` / `cgroup` |
|---|---|---|
| Linux / DGX | procfs | systemd unit + container id parsed from `/proc/<pid>/cgroup` |
| macOS (Apple Silicon & Intel) | lsof | empty — no cgroup analogue read today |
| Windows | IP Helper API | empty |

When attribution is incomplete the agent returns `partial: true` plus a
warning — it never guesses.

### Privileges (Linux)

The socket-inode → pid join walks other users' `/proc/<pid>/fd` and
resolves `/proc/<pid>/exe`, which the kernel guards with a ptrace
access-mode check plus 0500 directory permissions. Running as root
satisfies both; a non-root agent needs exactly two capabilities:

| Capability | Why |
|---|---|
| `CAP_SYS_PTRACE` | passes the `PTRACE_MODE_READ` check on `/proc/<pid>/{fd,exe}` |
| `CAP_DAC_READ_SEARCH` | bypasses the 0500 mode on other users' proc directories |

As of **v0.3.1** the installer grants these automatically: the systemd
unit carries

```ini
AmbientCapabilities=CAP_SYS_PTRACE CAP_DAC_READ_SEARCH
```

whenever the effective config has `network.flows_enabled` ≠ `false`
(issue #23). Nodes installed with v0.3.0 self-heal on the next one-liner
upgrade, which re-renders the unit. Flipping `flows_enabled` later
requires re-running `sudo rt-node-agent install` (idempotent; config and
token are preserved) — a restart alone does not re-render the unit.

The unit intentionally does **not** set `CapabilityBoundingSet` (it
would strip the sudo'd `systemctl` used by `POST /actions/service` of
the root capabilities it needs) — the agent process itself still only
ever holds the two ambient caps. Operators who manage their own unit
can apply the same grant as a drop-in:

```ini
# /etc/systemd/system/rt-node-agent.service.d/network-attribution.conf
[Service]
AmbientCapabilities=CAP_SYS_PTRACE CAP_DAC_READ_SEARCH
```

followed by `systemctl daemon-reload && systemctl restart
rt-node-agent`. When the agent detects the gap at runtime (non-root,
caps missing), the `partial: true` warning names the missing
capabilities and points here, and the same hint is logged to the
journal at startup.

macOS (launchd, root) and Windows (LocalSystem) need no equivalent.

## Privacy and storage rules

- No packet payloads, ever (non-goal per issue #21).
- No environment variables.
- `cmdline_head` is **redacted before truncation**: values of
  secret-shaped keys (`api-key`, `token`, `secret`, `password`,
  `credential`, `private-key`, `auth`, `Bearer …` — case-insensitive,
  covering `--flag value`, `--flag=value`, and `KEY=value` forms) are
  replaced with `[redacted]`, then the string is capped at
  `network.cmdline_max_bytes` (default 240).
- Timestamps are Unix nanoseconds throughout, consistent with
  `/health.time_sync.now_unix_ns` and `GET /time`, so the backend can
  align flows across nodes using its measured per-node clock offsets.
- Closed-socket entries expire after `window_s`; nothing is persisted to
  disk.

## Gateway correlation flow

1. Gateway receives a NetFlow record from its observation point.
2. Gateway normalizes the tuple to the inside node's perspective.
3. Gateway calls `GET http://<node>:11435/network/resolve?...` with the
   Bearer token.
4. Gateway stores the returned owner metadata beside the flow record.
5. The backend joins owner + `training_run_id` + its own case/workflow
   state to attribute the traffic.
