# Network flow ownership API

Issue: #21

This document specifies additive read-only API surfaces for correlating gateway-observed NetFlow/IPFIX records with the local process, service, and workflow that originated or accepted the traffic on a node.

The gateway sees the wire. The node-agent sees the host. The contract below lets Redtorch join those views without DPI and without requiring packet payload collection.

## Goals

- Resolve a gateway flow 5-tuple to a local PID, process, service, container, user, and command identity when the node can still observe the socket.
- Expose recent local network observations so the case-manager can build host-level traffic patterns even when the gateway did not ask in real time.
- Keep the endpoints read-only and additive. Existing `/health` consumers keep working.
- Preserve OPSEC posture: no payloads, no request bodies, no DNS interception, and no debug data from clients.

## Endpoint summary

| Method | Path | Purpose | Auth |
|---|---|---|---|
| `GET` | `/network/sockets` | Current listening and connected sockets, mapped to owner metadata. | LAN read policy |
| `GET` | `/network/flows` | Recent local flow observations derived from sockets and OS counters. | LAN read policy |
| `GET` | `/network/resolve` | Resolve one gateway-observed 5-tuple to the best local owner. | LAN read policy |

These endpoints follow the same LAN-only, read-oriented model as `GET /health`. If an installation enables bearer auth for read endpoints later, these endpoints must participate in that same policy.

## Common fields

Every response should include:

```json
{
  "ts_unix_ns": 1783290000123456789,
  "hostname": "compute-01",
  "agent_version": "0.3.0-dev",
  "source": "procfs|ss|netlink|lsof|netstat|platform",
  "stale": false,
  "partial": false,
  "warnings": []
}
```

`partial: true` means the agent returned usable data but lacked permission, kernel support, or platform support for part of the result. Do not fabricate missing fields.

## `GET /network/sockets`

Returns current sockets with owner metadata.

### Query parameters

| Name | Type | Notes |
|---|---|---|
| `state` | string | Optional socket state filter, for example `listen`, `established`, `time_wait`. |
| `proto` | string | Optional `tcp`, `udp`, or `ip`. |
| `port` | integer | Optional local or remote port filter. |
| `pid` | integer | Optional process filter. |
| `limit` | integer | Optional max results, default `1000`, max `10000`. |

### Shape

```json
{
  "ts_unix_ns": 1783290000123456789,
  "hostname": "compute-01",
  "source": "procfs",
  "partial": false,
  "items": [
    {
      "proto": "tcp",
      "state": "established",
      "local_addr": "192.168.50.185",
      "local_port": 57292,
      "remote_addr": "192.168.50.235",
      "remote_port": 22,
      "pid": 12345,
      "process_name": "ssh",
      "cmdline_head": "/usr/bin/ssh -N -L 18081:127.0.0.1:18080 ...",
      "exe": "/usr/bin/ssh",
      "user": "ctrldrive",
      "uid": 1000,
      "service": "redtorch-node-manager.service",
      "container_id": "",
      "container_name": "",
      "cgroup": "/system.slice/redtorch-node-manager.service",
      "first_seen_unix_ns": 1783289999000000000,
      "last_seen_unix_ns": 1783290000123456789,
      "confidence": 0.96
    }
  ]
}
```

## `GET /network/flows`

Returns a short rolling host-local flow view. This is not a packet capture API. It is a process ownership and counter view suitable for joining with gateway NetFlow records.

### Query parameters

| Name | Type | Notes |
|---|---|---|
| `since_unix_ns` | integer | Optional lower bound for observations. |
| `proto` | string | Optional `tcp`, `udp`, or `ip`. |
| `local_port` | integer | Optional local port filter. |
| `remote_addr` | string | Optional remote address filter. |
| `pid` | integer | Optional process filter. |
| `limit` | integer | Optional max results, default `1000`, max `10000`. |

### Shape

```json
{
  "ts_unix_ns": 1783290000123456789,
  "hostname": "compute-01",
  "source": "netlink",
  "partial": false,
  "window_s": 300,
  "items": [
    {
      "flow_id": "sha1:local-remote-proto-ports-window",
      "direction_hint": "egress|ingress|lateral|unknown",
      "proto": "tcp",
      "local_addr": "192.168.50.185",
      "local_port": 44218,
      "remote_addr": "34.233.49.149",
      "remote_port": 443,
      "state": "established",
      "pid": 88122,
      "process_name": "python3",
      "cmdline_head": "/opt/redtorch/venv/bin/python -m trainer ...",
      "service": "rt-trainer-qwen.service",
      "container_id": "",
      "workflow_id": "case-2026-07-05-train-17",
      "workflow_hint": "training",
      "bytes_sent_delta": 1834201,
      "bytes_recv_delta": 420112,
      "packets_sent_delta": 1290,
      "packets_recv_delta": 1144,
      "first_seen_unix_ns": 1783289700000000000,
      "last_seen_unix_ns": 1783290000123456789,
      "confidence": 0.88
    }
  ]
}
```

The byte and packet counters are best effort. Platforms that cannot expose per-socket deltas should omit those fields and keep the ownership fields.

## `GET /network/resolve`

Resolves one gateway flow to the best local owner. The gateway should call this on the likely inside node for flows that need attribution.

### Query parameters

| Name | Type | Required | Notes |
|---|---:|---:|---|
| `proto` | string | yes | `tcp`, `udp`, or numeric protocol string. |
| `local_addr` | string | yes | Address owned by this node from the node perspective. |
| `local_port` | integer | yes | Port owned by this node from the node perspective. |
| `remote_addr` | string | yes | Peer address from the node perspective. |
| `remote_port` | integer | yes | Peer port from the node perspective. |
| `observed_at_unix_ns` | integer | no | Gateway observation timestamp. Enables short history matching. |
| `direction_hint` | string | no | `egress`, `ingress`, `lateral`, or `unknown`. |

### Shape

```json
{
  "ts_unix_ns": 1783290000123456789,
  "hostname": "compute-01",
  "source": "procfs+cache",
  "partial": false,
  "query": {
    "proto": "tcp",
    "local_addr": "192.168.50.185",
    "local_port": 44218,
    "remote_addr": "34.233.49.149",
    "remote_port": 443,
    "observed_at_unix_ns": 1783290000000000000
  },
  "match": {
    "status": "matched|probable|not_found|ambiguous",
    "confidence": 0.91,
    "reason": "exact 5-tuple with timestamp inside cache window",
    "socket": {
      "proto": "tcp",
      "state": "established",
      "local_addr": "192.168.50.185",
      "local_port": 44218,
      "remote_addr": "34.233.49.149",
      "remote_port": 443
    },
    "owner": {
      "pid": 88122,
      "process_name": "python3",
      "cmdline_head": "/opt/redtorch/venv/bin/python -m trainer ...",
      "exe": "/opt/redtorch/venv/bin/python",
      "user": "redtorch",
      "uid": 1001,
      "service": "rt-trainer-qwen.service",
      "container_id": "",
      "container_name": "",
      "cgroup": "/system.slice/rt-trainer-qwen.service",
      "workflow_id": "case-2026-07-05-train-17",
      "workflow_hint": "training"
    }
  }
}
```

## Match semantics

Resolution confidence should be deterministic and explainable:

| Match type | Suggested confidence | Notes |
|---|---:|---|
| Exact 5-tuple and PID still alive | `0.95-1.00` | Best case. |
| Exact 5-tuple from short history cache | `0.85-0.95` | Socket closed but seen recently. |
| UDP local port owner with matching remote | `0.75-0.90` | Depends on platform support. |
| Listening socket for inbound flow | `0.70-0.90` | Accept path may be separate child PID. |
| Port-only owner | `0.40-0.65` | Useful hint, not enough for automated policy. |
| No owner | `0.0` | Return `status: not_found`, not HTTP 404. |

## Platform notes

- Linux should prefer netlink/procfs data and can enrich with cgroup/systemd metadata.
- FreeBSD/macOS can use `sockstat`, `netstat`, `procstat`, or platform-native APIs as available.
- Windows can use ETW/IP Helper API in a later pass.
- Root or elevated service mode may be required to map sockets owned by other users. If not elevated, return `partial: true` with a warning.

## Gateway correlation flow

1. Gateway receives a NetFlow record from the transparent bridge.
2. Gateway normalizes the tuple from the inside node perspective.
3. Gateway calls `GET http://node-agent:11435/network/resolve?...` on the inside node.
4. Gateway stores the owner metadata beside the flow pattern.
5. Case-manager joins the pattern to case, orchestration job, training workflow, or user action.

## Storage and privacy rules

- Do not capture packet payloads.
- Do not emit environment variables.
- Truncate `cmdline_head` to a safe configured length, default `240` bytes.
- Preserve timestamps in nanoseconds for cross-node correlation with the satellite-backed local time source.
- Mark cached owner data as stale when the process has exited or the observation is outside the cache window.

## First implementation slice

- Add data structs and JSON handlers for the three endpoints.
- Implement Linux socket ownership first because most compute nodes are Linux.
- Return `partial: true` with warnings on platforms that only support a subset.
- Add tests for query parsing, 5-tuple normalization, confidence ranking, and JSON compatibility.
- Keep every endpoint read-only and additive.
