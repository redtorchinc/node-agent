# `POST /actions/service`

Start, stop, restart, or query the state of an allowlisted unit. Currently
systemd-only (Linux). Bearer-token gated.

Configured under [services](../config.md#services). The agent only acts on
units listed in `services.allowed[].name`; everything else returns 403.

## Request

```http
POST /actions/service HTTP/1.1
Host: dgx-01:11435
Authorization: Bearer <token>
Content-Type: application/json

{"unit": "rt-vllm-qwen3.service", "action": "start"}
```

### Fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `unit` | string | yes | Must match `services.allowed[].name` exactly. |
| `action` | enum | yes | One of: `start`, `stop`, `restart`, `status`. |

No other fields are accepted. Env, args, timeouts: not configurable from
the client.

## Response (200)

```json
{
  "status": "ok",
  "unit": "rt-vllm-qwen3.service",
  "action": "start",
  "active_state": "active",
  "sub_state": "running",
  "main_pid": 12345,
  "memory_mb": 8192,
  "took_ms": 312
}
```

`active_state` / `sub_state` come from `systemctl show <unit>` and are
populated after every successful action — so a `start` returns the post-
start state in one round trip.

## Errors

| Status | Meaning |
|---|---|
| 401 | Missing or invalid Bearer token. |
| 403 | Unit not in `services.allowed[]`. |
| 404 | Unit not known to systemd (typo or unit file missing). |
| 409 | Action not in the unit's `actions:` list (e.g. you asked to `stop` a unit configured `actions: [start, restart, status]`). |
| 400 | Unknown action (only `start|stop|restart|status`). |
| 500 | systemctl returned non-zero or systemd is unreachable. |
| 501 | This OS doesn't support service control (macOS / Windows in v0.2). |
| 503 | Token not configured at all (the agent hasn't been provisioned). |

## Security

See [remote-actions.md](../remote-actions.md) for the full model. Short
version:

1. The agent runs as the `rt-agent` user (created by `rt-node-agent install`).
2. `/etc/sudoers.d/rt-node-agent` (installed automatically) grants that
   user `NOPASSWD` on systemctl only for `rt-vllm-*.service` units.
3. `unit` is passed to `exec.Cmd` as a discrete argv element — no shell
   ever sees the string. Even a crafted name like `rt-vllm-x.service; rm -rf /`
   would fail the allowlist check first.
4. Operators choose which actions each unit supports (`actions:` list).
   A unit configured `[start, restart, status]` cannot be `stop`ped via
   this endpoint.

## Example

```sh
TOK=$(sudo cat /etc/rt-node-agent/token)
curl -sS -X POST \
    -H "Authorization: Bearer $TOK" \
    -H "Content-Type: application/json" \
    -d '{"unit":"rt-vllm-qwen3.service","action":"status"}' \
    http://dgx-01:11435/actions/service | jq
```

For visibility without mutating: every allowlisted unit's state also
appears in `/health.services[]` (read-only, no auth) on the same 30s
cadence.
