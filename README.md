# rt-node-agent

A small HTTP agent that runs on every GPU/CPU node in the RedTorch fleet
and gives the case-manager backend one place to look for load visibility,
health gating, on-demand VRAM freeing, and remote control of vLLM service
units.

**Authoritative docs:** [spec/SPEC.md](./spec/SPEC.md) (wire contract) ·
[ARCHITECTURE.md](./ARCHITECTURE.md) (project map) ·
[V0_2_0_PLAN.md](./V0_2_0_PLAN.md) (v0.2.0 design) ·
[docs/](./docs/) (operator reference).

## Install

One command per OS. Each installs the binary, registers a native system
service, generates a Bearer token, and starts the listener on
**port 11435** (adjacent to Ollama's 11434).

```sh
# Linux / macOS
curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/install.sh | sudo sh

# Windows (elevated PowerShell)
iwr -useb https://github.com/redtorchinc/node-agent/releases/latest/download/install.ps1 | iex
```

Verify:

```sh
curl -s localhost:11435/version
curl -s localhost:11435/health | jq .
```

Full install / upgrade / uninstall flow: [docs/install.md](docs/install.md).

## What it does

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /health` | none (LAN) | Real CPU/mem/swap, GPU per-device, disk, network, time sync, per-platform model state (Ollama + vLLM), allowlisted service state, RDMA fabric (Linux DGX), `degraded_reasons`. The case-manager's `rank_nodes()` consumes the reasons list directly. |
| `GET /capabilities` | none (LAN) | What this build can do on this OS — used by the dispatcher for feature detection. |
| `GET /version` | none (LAN) | Version / git SHA / build time. |
| `GET /metrics` | none (LAN) | Prometheus text format (behind `metrics_enabled: true`). |
| `POST /actions/unload-model` | Bearer | Free an Ollama model. |
| `POST /actions/service` | Bearer | Start/stop/restart allowlisted systemd units (typically `rt-vllm-*.service`). |
| `POST /actions/training-mode` | Bearer | Coordinate inference ↔ training transitions. |

Field-by-field docs: [docs/api/](docs/api/). Security model:
[docs/remote-actions.md](docs/remote-actions.md).

## Configuration

Lives at `/etc/rt-node-agent/config.yaml` (Linux/macOS) or
`%ProgramData%\rt-node-agent\config.yaml` (Windows). The example file
under [examples/config.yaml](examples/config.yaml) is the canonical
reference; every key is also documented in [docs/config.md](docs/config.md).

**Upgrading from v0.1.x:** the installer auto-runs `config migrate` and
drops `config.yaml.new` next to your existing config with new keys
appended as commented YAML. The original file is never modified. Review
and merge by hand:

```sh
diff /etc/rt-node-agent/config.yaml /etc/rt-node-agent/config.yaml.new
sudo mv /etc/rt-node-agent/config.yaml.new /etc/rt-node-agent/config.yaml
sudo systemctl restart rt-node-agent
```

## Public-repo hygiene

This repo is published. Everything committed is world-readable forever
(GitHub mirrors, archive.org, training corpora, `git log -p --all`).
Before committing, check that nothing in your change includes:

- Bearer tokens, `.env` files, private keys.
- Real hostnames or IPs from the case-manager fleet.
- The private case-manager repo's URL or internal paths.
- Real model names that reveal customer / case data.

If a secret gets committed, **rotate** — don't try to rewrite history.

## License

[BSD-3-Clause](LICENSE).
