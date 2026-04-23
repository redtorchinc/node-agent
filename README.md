# rt-node-agent

A tiny HTTP agent that runs on every GPU/CPU node in the RedTorch fleet and gives the case-manager backend one place to look for load visibility, health gating, and on-demand VRAM freeing.

See **[SPEC.md](./SPEC.md)** for the authoritative API contract.

---

## What it does

- `GET /health` — real VRAM, RAM, swap, CPU, Ollama resident models, runner CPU usage, and a `degraded_reasons` list the case-manager's `rank_nodes()` consumes directly.
- `GET /version` — agent version / git SHA / build time.
- `GET /metrics` — Prometheus text format (behind `RT_AGENT_METRICS=1`).
- `POST /actions/unload-model` — authenticated; frees a named Ollama model on demand.

Listens on **port 11435** (deliberately adjacent to Ollama's 11434).

---

## Install

### Linux / macOS (one command)

```sh
curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/install.sh | sh
```

The script detects OS/arch, downloads the matching binary, places it in `/usr/local/bin/`, and registers it as a native service (systemd on Linux, launchd on macOS). Works on Ubuntu, Debian, Fedora, DGX OS, and macOS (Apple Silicon + Intel).

### Windows (elevated PowerShell)

```powershell
iwr -useb https://github.com/redtorchinc/node-agent/releases/latest/download/install.ps1 | iex
```

Registers a native Windows Service via the SCM.

### Verify

```sh
curl -s localhost:11435/health | jq '.degraded, .degraded_reasons'
```

If the node is healthy: `false` and `[]`.

---

## Configuration

Default config paths:

- Linux / macOS: `/etc/rt-node-agent/config.yaml`
- Windows: `%ProgramData%\rt-node-agent\config.yaml`

Every field is optional. See [`examples/config.yaml`](./examples/config.yaml) for the full set with comments.

| Env var              | Default              | Purpose |
|---                   |---                   |---      |
| `RT_AGENT_PORT`      | `11435`              | Listen port |
| `RT_AGENT_BIND`      | `0.0.0.0`            | Bind address |
| `RT_AGENT_TOKEN`     | *unset*              | Bearer token for `/actions/*` |
| `RT_AGENT_CONFIG`    | platform default     | Config file path override |
| `RT_AGENT_OLLAMA`    | `http://localhost:11434` | Ollama endpoint |
| `RT_AGENT_METRICS`   | *unset*              | Set `1` to enable `/metrics` |

### Enabling `/actions/*`

Read endpoints are open on the LAN by design (matches the air-gapped OPSEC model — no PII or case data flows through this agent). Mutating endpoints need a token:

```sh
# Generate and install a token on each node
openssl rand -hex 32 | sudo tee /etc/rt-node-agent/token >/dev/null
sudo chmod 600 /etc/rt-node-agent/token
sudo chown root:rt-agent /etc/rt-node-agent/token
sudo systemctl restart rt-node-agent
```

Then the backend passes it as `Authorization: Bearer <token>`.

Without a token configured, `POST /actions/*` returns **503 token not configured** — intentional signal that the endpoint is not yet provisioned (vs. 401 which implies auth is set up and rejected).

---

## API quick reference

Full JSON shape: **[SPEC.md §HTTP API](./SPEC.md)**.

### `degraded_reasons` vocabulary

| Reason | Severity | Trigger |
|---|---|---|
| `ollama_down` | **hard** | Ollama HTTP not responding |
| `swap_over_75pct` | **hard** | Thrashing |
| `vram_over_95pct` | **hard** | No room to load |
| `agent_stale` | **hard** | Ollama probe older than 60s |
| `vram_service_creep_critical` | **hard** | PyTorch allocator leak (reserved/allocated > 3.0 AND reserved > `threshold_critical_mb`) |
| `swap_over_50pct` | soft | Deprioritize |
| `vram_over_90pct` | soft | Deprioritize |
| `load_avg_over_2x_cores` | soft | CPU saturated |
| `ollama_runner_stuck` | soft | Runner PID at 0% CPU with a model loaded |
| `vram_service_creep_warn` | soft | Early leak signal (ratio > 2.0 AND reserved > `threshold_warn_mb`) |

**Hard reasons flip `degraded: true` and tell the ranker to skip the node.** Additive changes to this list are safe; renames are breaking.

---

## Uninstall

```sh
# Linux / macOS
curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/uninstall.sh | sh

# Windows (elevated PowerShell)
iwr -useb https://github.com/redtorchinc/node-agent/releases/latest/download/uninstall.ps1 | iex
```

Config and token files are preserved on uninstall so reinstalling doesn't rotate secrets. Pass `--purge` (future) or `rm -rf /etc/rt-node-agent` manually to wipe everything.

---

## Building from source

```sh
git clone https://github.com/redtorchinc/node-agent.git
cd node-agent
make build           # local binary
make cross           # all 5 targets into dist/
make test            # unit tests (under 60s)
```

Requires Go 1.22+. No CGO, no non-Go build tooling.

---

## Security model

- **LAN-only** by default. Read endpoints are open on the LAN; mutating endpoints require a Bearer token.
- **No TLS in v1.** Assumes the air-gapped OPSEC model (nodes share a firewall with the backend). If you're deploying outside that model, terminate TLS in front of the agent or wait for v2 mTLS.
- **No remote shell, no file read/write endpoints, ever.** The only mutating action is "unload this named Ollama model."
- **No PII or case data** passes through this agent. It observes the host and talks to local Ollama only.

See [SECURITY.md](./SECURITY.md) for supply-chain (signing, attestation) and disclosure policy.

---

## Troubleshooting

### `/health` shows `"ollama_down"` but Ollama is running

Check `RT_AGENT_OLLAMA` — the default is `http://localhost:11434`. If Ollama was started with `OLLAMA_HOST=0.0.0.0:11434`, the agent still reaches it locally; if with a non-default port, override:

```sh
echo 'ollama_endpoint: http://localhost:11500' | sudo tee -a /etc/rt-node-agent/config.yaml
sudo systemctl restart rt-node-agent
```

### GPU detection per platform

The agent auto-selects a GPU probe based on the host:

- **Linux / Windows / Intel-Mac-with-eGPU** — shells out to `nvidia-smi`. If not on PATH, the `gpus` list is empty (not an error). Ensure the NVIDIA driver is installed and `nvidia-smi` is reachable by the service user (`rt-agent` on Linux).
- **Apple Silicon** — uses `system_profiler SPDisplaysDataType`. No `nvidia-smi` required or expected. The `gpus` list contains one entry with the M-series chip name; per-process VRAM is not exposed (no public macOS API). `memory.unified: true` signals to the ranker that RAM pressure is the GPU-pressure signal on this class of host.
- **Any host with no supported GPU stack** — `gpus: []`. Agent still serves `/health` correctly; the ranker can treat this as CPU-only.

### Port 11435 not reachable from the backend

The agent binds `0.0.0.0:11435` by default. If the backend still can't reach it, check the host firewall (`ufw`, `firewalld`, Windows Defender Firewall). The agent does not open firewall ports itself.

### Windows service won't install

`install.ps1` must be run from an **elevated** PowerShell. The SCM refuses `CreateService` otherwise. Right-click PowerShell → Run as Administrator.

---

## Reporting issues

Open a GitHub issue at [redtorchinc/node-agent/issues](https://github.com/redtorchinc/node-agent/issues).

**Please redact internal hostnames, IPs, and any fleet-specific identifiers from bug reports** — this is a public repository and everything posted is world-readable forever.

---

## License

Apache-2.0. See [LICENSE](./LICENSE).
