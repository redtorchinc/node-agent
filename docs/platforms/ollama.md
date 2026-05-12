# Ollama platform

The agent's original (v0.1.x) target. Probes `/api/ps` and converts the
result into the canonical `platforms.Model` shape under
`/health.platforms.ollama`.

## Config

```yaml
platforms:
  ollama:
    enabled: auto                          # auto | true | false
    endpoint: http://localhost:11434
```

`auto` probes on every `/health` request; `false` skips the probe entirely
and emits `{up: false}` in `/health.platforms.ollama`.

## Fields populated

| Field | Source |
|---|---|
| `name` | `/api/ps` `name` |
| `loaded` | true if returned by `/api/ps` |
| `size_mb` | `/api/ps` `size` |
| `context_window` | `/api/ps` `details.context_length` |
| `processor_split` | derived from `size` vs `size_vram` |
| `ttl_s` | `expires_at - now` |
| `quantization` | parsed from name suffix (`q4_K_M`, `fp16`, etc.) |
| `queue.running` | `/api/ps` `queued_requests` if present, else `null` |

Fields that vLLM exposes but Ollama doesn't (`kv_cache.*`, `latency_ms.*`,
`counters.*`) are simply omitted — no synthetic backfill.

## Legacy compatibility

The v0.1.x top-level `/health.ollama` field is still emitted with the
same shape, **in addition to** `/health.platforms.ollama`. New code
should read the platforms key; the legacy alias is scheduled for removal
in v0.3.0.
