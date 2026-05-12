# rt-node-agent docs

Operator-facing reference for the v0.2.0 agent. [spec/SPEC.md](../spec/SPEC.md)
is the contract document; everything here is how-to. New agents should
read [ARCHITECTURE.md](../ARCHITECTURE.md) first.

## Getting started

- [install.md](install.md) — install / upgrade / uninstall flow per OS.
- [config.md](config.md) — every `config.yaml` key, v1→v2 migration.

## API reference

- [api/health.md](api/health.md) — `GET /health`, every field.
- [api/capabilities.md](api/capabilities.md) — `GET /capabilities`, used by the dispatcher for feature detection.
- [api/version.md](api/version.md) — `GET /version`.
- [api/metrics.md](api/metrics.md) — `GET /metrics`, Prometheus exposition.
- [api/actions-unload-model.md](api/actions-unload-model.md) — `POST /actions/unload-model`.
- [api/actions-service.md](api/actions-service.md) — `POST /actions/service` (start/stop vLLM units).
- [api/actions-training-mode.md](api/actions-training-mode.md) — `POST /actions/training-mode`.

## Topics

- [degraded-reasons.md](degraded-reasons.md) — canonical reason list.
- [remote-actions.md](remote-actions.md) — security model for mutating endpoints.
- [platforms/ollama.md](platforms/ollama.md) — Ollama probe.
- [platforms/vllm.md](platforms/vllm.md) — vLLM probe + Prometheus scrape.
- [rdma.md](rdma.md) — RDMA fabric monitoring (Linux DGX).
- [training-mode.md](training-mode.md) — coordinating inference ↔ training.
- [troubleshooting.md](troubleshooting.md) — common install / runtime issues.
