# `POST /actions/training-mode`

Coordinate the inference ↔ training transition on a node. Bearer-token
gated.

The full spec is [spec/NODE_AGENT_TRAINING_EXTENSIONS.md](../../spec/NODE_AGENT_TRAINING_EXTENSIONS.md)
§4 and §7. This page is the operator quick-reference.

## Enter training_mode

```http
POST /actions/training-mode HTTP/1.1
Authorization: Bearer <token>
Content-Type: application/json

{
  "enter": true,
  "run_id": "9b1f-2c4e-...",
  "expected_duration_s": 7200,
  "release_ollama_models": ["nomic-embed-text-v2-moe:latest", "qwen3-vl:32b"],
  "restore_on_exit": true
}
```

Behaviour on success:

1. The agent unloads each model in `release_ollama_models`. If any unload
   fails, the request **fails closed** (500) — the node does not enter
   training_mode with stale models still loaded.
2. `/health.mode` switches to `"training_mode"`.
3. `training_in_progress` is added to `degraded_reasons` (hard).
4. `/var/lib/rt-node-agent/training_mode.json` is written with the snapshot.

Idempotent re-entry with the same `run_id` returns 200 with the existing
state. A different `run_id` while already in training_mode returns 409.

### Response (200)

```json
{
  "status": "ok",
  "mode": "training_mode",
  "run_id": "9b1f-2c4e-...",
  "entered_at": 1746489600,
  "models_released": ["nomic-embed-text-v2-moe:latest", "qwen3-vl:32b"],
  "took_ms": 2340
}
```

## Exit training_mode

```json
{"enter": false}
```

Returns 409 if not in training_mode (so the dispatcher can distinguish
"never entered" from "exited successfully").

### Response (200)

```json
{
  "status": "ok",
  "mode": "idle",
  "previous_run_id": "9b1f-2c4e-...",
  "duration_s": 7180,
  "took_ms": 12
}
```

## Crash recovery

The state file at `/var/lib/rt-node-agent/training_mode.json` is the source
of truth. On agent startup:

- If the file exists and `entered_at + expected_duration_s + grace_period_s`
  hasn't passed: `/health.mode` reads `training_mode`, `degraded_reasons`
  includes `training_in_progress`. Normal resume.
- If the file exists but the deadline has passed: the file is deleted and
  a warning is logged. Default `grace_period_s` is 3600 (1 hour).

## Errors

| Status | Meaning |
|---|---|
| 400 | `run_id` missing when `enter: true`. |
| 401 | Missing/invalid Bearer. |
| 409 | `enter: false` but not in training_mode; or re-`enter: true` with a different `run_id`. |
| 500 | Unloading one of `release_ollama_models` failed. |
| 503 | Token not configured. |
