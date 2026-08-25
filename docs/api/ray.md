# `/health.ray` — Ray / tensor-parallel group membership

**Since v0.3.3** (issue #30). Additive: a new optional block on `GET /health`.
No existing field changed, and **no new `degraded_reasons` value** — see
[Not a health signal](#not-a-health-signal).

## The problem it solves

The fleet serves large models as N-way tensor-parallel over Ray — e.g. one
model across 8× GB10 fronted by a Ray head. Only the head exposes an
OpenAI-compatible endpoint, so to anything reading `/health` the other seven
nodes look identical to idle machines with no inference platform. They were
being rendered as **offline**.

`/health` had no notion of Ray, so there was no way to tell that those eight
nodes are *one serving group* — which also meant the group's cost and
quality couldn't be attributed to the model it was serving.

## Shape

```json
{
  "ray": {
    "running": true,
    "role": "worker",
    "gcs_address": "192.168.50.96:6379",
    "node_ip": "10.10.10.7",
    "cluster_id": "session_2026-08-25_10-00-00_123456_789",
    "alive_nodes": 8,
    "resources": { "node:10.10.10.7": 1.0, "CPU": 20, "GPU": 1, "memory": 67108864 },
    "raylet_pid": 3312,
    "gcs_pid": 0,
    "session_dir": "/tmp/ray/session_2026-08-25_10-00-00_123456_789",
    "dashboard_url": "",
    "source": "cmdline",
    "error": "alive_nodes unavailable on a worker (no local dashboard); group the fleet by gcs_address instead",
    "last_probe": 1787654321,
    "probe_interval_s": 30
  }
}
```

**The block is omitted entirely when Ray isn't running** — the same contract
as `rdma`. Presence means "this node is in a Ray cluster".

| Field | Type | Meaning |
|---|---|---|
| `running` | bool | Always `true` when present. Explicit so the JSON is self-describing. |
| `role` | string | `head` (runs `gcs_server`) or `worker` (runs `raylet` only). |
| `gcs_address` | string | Cluster GCS endpoint. **Identical on every member — this is the grouping key.** |
| `node_ip` | string | Address this node registered with Ray. On multi-homed hosts, not necessarily the address `/health` arrived on. |
| `cluster_id` | string | Ray's *session name*. Cluster-wide, and changes on every cluster restart. |
| `alive_nodes` | int \| **null** | Cluster member count. `null` means unknown — see below. |
| `resources` | object | This node's Ray resource advertisement. Keys are Ray resource names. |
| `raylet_pid`, `gcs_pid` | int | For correlating with `ps` / journal output. `0` when absent. |
| `session_dir` | string | Where `cluster_id` was read from, so a surprising value is auditable. |
| `dashboard_url` | string | Endpoint consulted for `alive_nodes`, recorded even on failure. |
| `source` | string | `cmdline`, or `cmdline+dashboard` when `alive_nodes` came back. |
| `error` | string | Why the result is partial. Never a reason to discard the block. |
| `last_probe`, `probe_interval_s`, `stale` | — | Self-describing staleness, as on `platforms.*` (issue #8). |

## How to group a serving unit

```sql
SELECT ray_gcs_address, count(*) AS members
FROM compute_node_state
WHERE ray_gcs_address IS NOT NULL
GROUP BY ray_gcs_address
```

Then pick the `role: "head"` member as the addressable inference endpoint,
and treat the workers as part of that unit rather than as idle nodes.

`cluster_id` corroborates the grouping and additionally distinguishes
*incarnations*: if it changes while `gcs_address` stays the same, the cluster
was restarted.

### `alive_nodes` is a convenience, not the mechanism

**Counting fleet rows that share a `gcs_address` is strictly more reliable
than what any single node can see.** Prefer it.

`alive_nodes` comes from Ray's dashboard API, which is an internal surface
that has changed shape across Ray releases. It is:

- read only on the **head** (workers run no dashboard), unless
  `ray.dashboard_url` is set explicitly;
- `null` whenever the dashboard is unreachable or returns a shape the agent
  doesn't recognise, with `error` saying which;
- a count of nodes reported **ALIVE**, not a summary total — a total that
  silently included dead nodes would inflate a TP group's apparent size.

`alive_nodes` is a JSON `null` rather than an omitted key when unknown,
deliberately: a missing key invites a consumer to read it as `0`, and "empty
cluster" is a very different claim from "I couldn't reach the dashboard".

## Detection is cheap and read-only

Everything except `alive_nodes` is parsed out of the raylet's own command
line, which Ray already populates with what we need:

```
raylet --gcs-address=192.168.50.96:6379 \
       --session-name=session_2026-08-25_10-00-00_123456_789 \
       --node_ip_address=10.10.10.7 \
       --static_resource_list=node:10.10.10.7,1.0,CPU,20,GPU,1,memory,67108864 \
       --raylet_socket_name=/tmp/ray/session_.../sockets/raylet
```

There is deliberately **no `ray status` shell-out** on the `/health` path:
it's a Python CLI costing seconds, and v0.2.8 already had to strip a 5s
dead-weight out of darwin `/health`. Results are TTL-cached (30s) and
refreshed by the keep-warm ticker, so backend polls land warm.

Two Ray quirks the parser handles:

- Ray mixes `-` and `_` flag spellings between versions, so both are accepted.
- `--static_resource_list` is a flat `name,value,name,value` alternation, and
  one of those names (`node:10.10.10.7`) contains a colon — so pairs are read
  positionally. A trailing unpaired name is ignored rather than guessed at,
  because reporting `GPU: 0` would be worse than reporting nothing.

`session_dir` is derived from the raylet's socket path rather than assuming
`/tmp/ray`, so a non-default `--temp-dir` works for free.

### Why `gcs_address` is never synthesized

Grouping relies on **string equality** of `gcs_address` across members. The
raylet's `--gcs-address` is the one value every member is handed verbatim, so
it always matches.

The agent could compute a head's own address from its `gcs_server` flags
(`--node-ip-address` + `--gcs_server_port`), but that risks a
different-but-equivalent string — `10.10.10.7:6379` where the workers were
told `192.168.50.96:6379` — and the head would silently fail to group with
its own workers. Reporting nothing is better than reporting a value that
doesn't join.

Accepted consequence: a head whose raylet is momentarily dead reports
`role: "head"` with no `gcs_address` and is ungroupable until the raylet is
back. That node is transiently broken, and the block says so honestly.

## Not a health signal

This block adds **no** `degraded_reasons` value, by design. A node being a
TP worker is a topology fact, not a health problem, and
[docs/degraded-reasons.md](../degraded-reasons.md) is a cross-repo contract
that must not grow silently. A stale reading or a failed dashboard query
likewise produces no reason — the block just carries `error`/`stale` and the
consumer decides.

There is a test pinning this (`TestRayDoesNotAddDegradedReason`).

## Config

```yaml
ray:
  enabled: auto            # auto (= detect) | true | false
  dashboard_url: ""        # override for alive_nodes
  probe_interval_s: 30
```

`auto` and `true` are the same thing — detection is the only mode, since
there is nothing to force on a node with no Ray. `false` skips the probe
entirely.

## Feature detection

`GET /capabilities` gains `ray_supported`. It reflects whether the *probe
runs*, not whether Ray is present.

**A missing `ray` block only means "not in a cluster" when
`ray_supported: true`.** With `ray.enabled: false` the block is absent
because nothing looked — which is a different claim, and the flag is how a
backend tells them apart.
