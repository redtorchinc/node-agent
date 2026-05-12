# `POST /actions/unload-model`

Drop a specific model from Ollama's resident set. Bearer-token gated.

## Request

```http
POST /actions/unload-model HTTP/1.1
Authorization: Bearer <token>
Content-Type: application/json

{"model": "qwen3-vl:32b"}
```

## Response (200)

```json
{
  "status": "ok",
  "unloaded": ["qwen3-vl:32b"],
  "took_ms": 245
}
```

`unloaded` is empty when the model wasn't loaded — the endpoint is
**idempotent**, so the dispatcher can call it without first checking
`/health` for residency.

## Errors

| Status | Meaning |
|---|---|
| 401 | Missing/invalid Bearer. |
| 500 | Ollama is unreachable. |
| 503 | Token not configured. |

## Implementation

Tries `ollama stop <model>` first (Ollama 0.5+). Falls back to
`POST /api/generate {"model": "...", "keep_alive": 0}` on older versions
which drops the model from memory immediately. No vLLM-side equivalent
in v0.2 — vLLM doesn't expose a model-drop endpoint without restarting
the process (use `POST /actions/service` for that).
