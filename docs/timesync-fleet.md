# Fleet time sync (chrony) + how the agent measures it

The agent **measures** clock offsets; it never disciplines the clock (it's
read-only by design). To actually keep the fleet's clocks aligned you run a
local NTP server and point every node at it. This page covers both: the
chrony setup, and how the two agent offsets light up afterward.

## Topology

```
            (WAN uplink: Starlink / internet)
                        │
                        ▼
            ┌───────────────────────┐
            │  local NTP server     │  chrony: disciplines from public NTP,
            │  e.g. 10.0.0.10       │  serves the LAN, `local stratum 10`
            └───────────┬───────────┘  fallback if the uplink drops
                        │  LAN (NTP/UDP 123)
        ┌───────────────┼───────────────┐
        ▼               ▼               ▼
     node A          node B          node C        chrony clients →
   (rt-node-agent) (rt-node-agent) (rt-node-agent)  the local server only
```

One box is the **server** (the only one that needs the WAN uplink); every
node is a **client** that needs nothing but the LAN.

## Setup

Run [`scripts/timesync/setup-timesync.sh`](../scripts/timesync/setup-timesync.sh)
as root on each box (it installs chrony, writes the config, and starts it):

```sh
# on the NTP server box — pass the LAN CIDR allowed to query it:
sudo ./scripts/timesync/setup-timesync.sh server 10.0.0.0/24

# on every node — pass the NTP server's LAN IP:
sudo ./scripts/timesync/setup-timesync.sh client 10.0.0.10
```

The server keeps serving at `stratum 10` even if the Starlink uplink drops,
so the fleet stays internally consistent through an outage (it just drifts
together from true UTC until the uplink returns).

Verify on any box: `chronyc tracking` (offset from its source) and
`chronyc -n sources` (who it's talking to).

## How the agent reports it afterward

With chrony running, the agent's existing `/health.time_sync` OS-sync fields
populate on every box — `source: "chrony"`, `synced: true`, `skew_ms`,
`stratum` — read straight from `chronyc tracking`.

To also get the two cross-node offsets, point the agent's `timesync.server`
at the **local NTP server** on each node ([config.md](config.md)):

```yaml
timesync:
  server: 10.0.0.10        # the local NTP server (NOT the internet default)
  offset_degraded_ms: 100  # clocks are disciplined now → keep the alarm on
```

Then:

- **Offset A — node ↔ reference** comes from the agent's own probe against
  that server: `/health.time_sync.server.offset_ms` (and in `GET /time`).
  With chrony disciplining the node, this stays small; `clock_offset_high`
  fires only on real drift.
- **Offset B — caller ↔ node** is measured by the backend via
  [`GET /time`](api/time.md) (the four-timestamp handshake) — independent of
  whether the node's clock is disciplined.

> Air-gapped note: if a node truly has no path to the NTP server, leave
> `timesync.server` empty (probe off) or set `offset_degraded_ms: 0` to run
> measure-only — the backend then compensates using Offset B from `GET /time`
> rather than relying on disciplined clocks.

See [api/time.md](api/time.md) for the offset formulas and sign conventions,
and [degraded-reasons.md](degraded-reasons.md) for `clock_offset_high`.
