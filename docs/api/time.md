# `GET /time`

A minimal NTP-over-HTTP four-timestamp handshake. It lets the calling
backend measure its clock offset to *this node* (**Offset B**) to sub-ms,
independent of `/health` composition cost. No auth (LAN-open, same posture
as `/health`).

```
GET /time?t1=<unix_ns>
```

`t1` is the caller's send time in Unix nanoseconds (optional — echoed as
`0` when absent).

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

| field | meaning |
|---|---|
| `t1_unix_ns` | echoed caller send time (`0` if `?t1` absent/unparseable) |
| `t2_unix_ns` | node **receive** time, stamped at handler entry |
| `t3_unix_ns` | node **transmit** time, stamped just before the write |
| `server` | the cached `timesync.server` probe (**Offset A**, node ↔ reference); omitted when `timesync.server` is empty |

The agent does no probing or allocation between `t2` and `t3`, so the
round-trip the caller measures is the network path, not agent work.

## Computing Offset B (caller side)

Record `t4` on receipt, then per RFC 5905 §8:

```
offset_ms = (((t2 - t1) + (t3 - t4)) / 2) / 1e6     # + => node ahead of caller
rtt_ms    = ((t4 - t1) - (t3 - t2)) / 1e6
```

`offset_ms` is what you add to the caller's clock to match the node's. To
translate a node-reported timestamp into the caller's frame, **subtract**
`offset_ms`.

## The two offsets

- **Offset A — node ↔ reference.** `server.offset_ms` here (and
  `/health.time_sync.server.offset_ms`): the node's clock vs. the
  configured `timesync.server`. Sign **node − reference** (positive = node
  ahead). Use for cross-node alignment — `node_i − node_j = A_i − A_j`.
- **Offset B — caller ↔ node.** Computed by the caller from the handshake
  above. Use to read a node's timestamps directly in the caller's frame.

They are independent: A anchors every node to one shared reference; B
anchors each node to *this* caller, which may run on a different reference
(or none — e.g. an air-gapped fleet measuring offsets without disciplining
clocks).

## Feature detection

Older agents (pre-0.2.14) don't serve `/time`. Check
`capabilities.time_handshake_supported`; when false, fall back to
`/health.time_sync.now_unix_ns` + the caller's own RTT/2 (coarser — bounded
by the `/health` compose+write tail).

## Notes

- Method other than `GET` → `405`.
- Air-gapped fleets must point `timesync.server` at an internal NTP box, or
  the `server` block has no `offset_ms` (probe only logs timeouts). See
  [timesync-fleet.md](../timesync-fleet.md) for standing one up with chrony.
