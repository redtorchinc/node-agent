# Training-mode coordination

Full spec: [spec/NODE_AGENT_TRAINING_EXTENSIONS.md](../spec/NODE_AGENT_TRAINING_EXTENSIONS.md).
This page is the operator overview.

## State machine

```
       ┌─────────┐
       │  idle   │
       └────┬────┘
            │ ollama probe finds resident models
            ▼
      ┌──────────┐
      │ inference│
      └────┬─────┘
           │ ollama drained AND POST /actions/training-mode {enter:true}
           ▼
   ┌─────────────────┐
   │  training_mode  │◄─────┐  idempotent re-enter allowed
   └────────┬────────┘      │
            │               │
            │ POST {enter:false} OR auto-recovery
            ▼
       ┌─────────┐
       │  idle   │
       └─────────┘
```

- `idle ↔ inference` is implicit, derived from `platforms[].models`.
- `idle → training_mode` requires explicit Bearer-authenticated POST.
- `training_mode → idle` is either an explicit exit POST, or auto-recovery
  when `entered_at + expected_duration_s + grace_period_s` is exceeded.

## What "in training_mode" means

- `/health.mode == "training_mode"`.
- `/health.training` is populated with `run_id`, `entered_at`, etc.
- `degraded_reasons` includes `training_in_progress` (hard).
- Inference dispatch will skip the node automatically (the hard reason
  flips `degraded: true`).
- Allocator scrapers with `only_when_mode: training_mode` start polling
  the training-process `/v1/debug/gpu` endpoint.

## Crash recovery

State persists at `/var/lib/rt-node-agent/training_mode.json` (configurable
via `training_mode.state_file`). On agent startup:

1. If the file exists and `entered_at + expected + grace` hasn't elapsed,
   the agent resumes `training_mode` and the dispatcher sees no gap.
2. If the deadline passed during downtime, the file is removed and a
   warning is logged. Default grace is 1 hour.

This prevents stuck training_mode if the dispatcher crashed and forgot
to call exit.

## Tying training-process metrics

Add a `service_allocators` entry pointing at the training process's
`/v1/debug/gpu` endpoint with `only_when_mode: training_mode`:

```yaml
service_allocators:
  - name: training-process
    url: http://localhost:8089/v1/debug/gpu
    threshold_warn_mb: 100000
    threshold_critical_mb: 120000
    scrape_interval_s: 10
    only_when_mode: training_mode
```

The training process is expected to serve:

```json
{
  "allocated_mb": 48372.4,
  "reserved_mb":  49281.0,
  "max_allocated_mb": 49801.2,
  "run_id":  "9b1f-2c4e-...",
  "step":    1247,
  "epoch":   0.83,
  "loss_train": 0.127,
  "tokens_per_second": 18472.3
}
```

The three canonical fields drive `service_allocators[].allocated_mb` /
`.reserved_mb` / `.max_allocated_mb`. Everything else is passed through
verbatim under `service_allocators[].extra` — opaque to the agent, exact
shape preserved for the dispatcher / observability stack.

## Wire-level details

See [api/actions-training-mode.md](api/actions-training-mode.md).
