# rt-node-agent

A small HTTP agent that runs on every GPU/CPU node in the RedTorch fleet and gives the case-manager backend one place to look for load visibility, health gating, and on-demand VRAM freeing.

See **[SPEC.md](./SPEC.md)** for the authoritative API contract and **[PLAN.md](./PLAN.md)** for the implementation plan.

---

## What it does

- `GET /health` — real VRAM, RAM, swap, CPU, Ollama resident models, runner CPU usage, and a `degraded_reasons` list the case-manager's `rank_nodes()` consumes directly.
- `GET /version` — agent version / git SHA / build time.
- `GET /metrics` — Prometheus text format (behind `RT_AGENT_METRICS=1`).
- `POST /actions/unload-model` — authenticated; frees a named Ollama model on demand.

Listens on **port 11435** (adjacent to Ollama's 11434).

---

## Install

One command per OS. Each installs the binary, registers a native system service, and starts it.

### Linux (systemd)

```sh
curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/install.sh | sudo sh
```

Supported: Ubuntu, Debian, Fedora, DGX OS — `amd64` and `arm64`.

What it does: downloads the matching binary to `/usr/local/bin/rt-node-agent`, creates the `rt-agent` system user, writes `/etc/systemd/system/rt-node-agent.service`, then `systemctl enable --now rt-node-agent`.

Verify:

```sh
curl -s localhost:11435/version
systemctl status rt-node-agent
```

### macOS (launchd)

```sh
curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/install.sh | sudo sh
```

Supported: macOS 12+ on Apple Silicon (`arm64`) and Intel (`amd64`).

What it does: downloads the matching binary to `/usr/local/bin/rt-node-agent`, writes `/Library/LaunchDaemons/com.redtorch.rt-node-agent.plist`, then `launchctl bootstrap system …` to load and start it as a system daemon.

Verify:

```sh
curl -s localhost:11435/version
sudo launchctl print system/com.redtorch.rt-node-agent | head
```

**Gatekeeper note:** binaries downloaded by `curl` are not quarantined, so the install runs without security dialogs. If you download the binary manually via a browser, run this before first launch:

```sh
sudo xattr -dr com.apple.quarantine /usr/local/bin/rt-node-agent
```

### Windows (Service Control Manager)

Open PowerShell **as Administrator** (right-click → Run as Administrator), then:

```powershell
iwr -useb https://github.com/redtorchinc/node-agent/releases/latest/download/install.ps1 | iex
```

Supported: Windows 10/11 and Windows Server — `amd64`.

What it does: downloads the `.exe` to `C:\Program Files\RedTorch\rt-node-agent.exe`, registers it with the Windows Service Control Manager (`StartType = Automatic`), and starts it.

Verify:

```powershell
Invoke-WebRequest http://127.0.0.1:11435/version -UseBasicParsing
Get-Service rt-node-agent
```

If `install.ps1` prints `must run from an elevated PowerShell`, the session isn't elevated — close it and reopen as Administrator.

---

## Verify the node is healthy

Any OS:

```sh
curl -s http://localhost:11435/health
```

Look for `"degraded": false` and `"degraded_reasons": []`. See the [degraded_reasons quick reference](#degraded_reasons-quick-reference) below for the full vocabulary.

---

## Configuration

### Config file location

| OS       | Path                                          |
|---       |---                                            |
| Linux    | `/etc/rt-node-agent/config.yaml`              |
| macOS    | `/etc/rt-node-agent/config.yaml`              |
| Windows  | `%ProgramData%\rt-node-agent\config.yaml`     |

Every field is optional. See [`examples/config.yaml`](./examples/config.yaml) for the full set with comments.

### Environment variables (all OSes)

| Variable             | Default                    | Purpose                                   |
|---                   |---                         |---                                        |
| `RT_AGENT_PORT`      | `11435`                    | Listen port                               |
| `RT_AGENT_BIND`      | `0.0.0.0`                  | Bind address                              |
| `RT_AGENT_TOKEN`     | *unset*                    | Bearer token for `/actions/*`             |
| `RT_AGENT_CONFIG`    | platform default           | Config file path override                 |
| `RT_AGENT_OLLAMA`    | `http://localhost:11434`   | Ollama endpoint                           |
| `RT_AGENT_METRICS`   | *unset*                    | Set `1` to enable `/metrics`              |

### Bearer token for `/actions/*`

Read endpoints (`/health`, `/version`, `/metrics`) are open on the LAN by design — matches the air-gapped OPSEC model, no PII flows through this agent. Mutating endpoints need a Bearer token.

**The installer generates a token automatically and prints it once at the end of install.** Capture that output — it's what the case-manager backend uses for `Authorization: Bearer`. The token file is also persisted with correct perms at:

| OS       | Path                                       | Perms                      |
|---       |---                                         |---                         |
| Linux    | `/etc/rt-node-agent/token`                 | `640` owned `root:rt-agent`|
| macOS    | `/etc/rt-node-agent/token`                 | `600` owned `root`         |
| Windows  | `%ProgramData%\rt-node-agent\token`        | default ACL                |

Without a token configured, `POST /actions/*` returns **503 token not configured** — an intentional signal that the endpoint is not yet provisioned (vs. 401 which implies auth is set up and rejected).

### Rotating the token

#### Linux

```sh
openssl rand -hex 32 | sudo tee /etc/rt-node-agent/token >/dev/null
sudo chown root:rt-agent /etc/rt-node-agent/token
sudo chmod 640 /etc/rt-node-agent/token
sudo systemctl restart rt-node-agent
```

#### macOS

```sh
openssl rand -hex 32 | sudo tee /etc/rt-node-agent/token >/dev/null
sudo chmod 600 /etc/rt-node-agent/token
sudo launchctl kickstart -k system/com.redtorch.rt-node-agent
```

#### Windows

From elevated PowerShell:

```powershell
$token = -join (1..64 | ForEach-Object { '{0:x}' -f (Get-Random -Max 16) })
Set-Content -Path "$env:ProgramData\rt-node-agent\token" -Value $token -NoNewline -Encoding ASCII
Restart-Service rt-node-agent
```

### Using a fleet-wide shared token

If you want every node to accept the same token (simpler for the backend), write it to the same path **before** running `install.sh` — the installer only generates a token when the file doesn't already exist. Or run the rotate steps above on each node after install.

---

## API reference

Full JSON shape: **[SPEC.md §HTTP API](./SPEC.md)**.

### `degraded_reasons` quick reference

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

Config files under `/etc/rt-node-agent/` (or `%ProgramData%\rt-node-agent\` on Windows) are **preserved** so a reinstall keeps the existing token. Delete them manually if you want a clean slate.

### Linux

```sh
curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/uninstall.sh | sudo sh
```

Disables and stops the systemd unit, removes the unit file, and deletes the binary.

### macOS

```sh
curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/uninstall.sh | sudo sh
```

Unloads and removes the launchd plist, and deletes the binary.

### Windows

From elevated PowerShell:

```powershell
iwr -useb https://github.com/redtorchinc/node-agent/releases/latest/download/uninstall.ps1 | iex
```

Stops and deletes the Windows Service, and removes the `.exe`.

---

## Building from source

```sh
git clone https://github.com/redtorchinc/node-agent.git
cd node-agent
make build           # local binary for this OS/arch
make cross           # all 5 targets into dist/
make test            # unit tests (under 60s)
```

Requires Go 1.22+. No CGO, no non-Go build tooling.

---

## Security model

- **LAN-only** by default. Read endpoints open; mutating endpoints require a Bearer token.
- **No TLS in v1** (assumes air-gapped OPSEC — nodes share a firewall with the backend). If you're deploying outside that model, terminate TLS in front of the agent.
- **No remote shell, no file read/write endpoints, ever.** The only mutating action is "unload this named Ollama model."
- **No PII or case data** passes through this agent.

See [SECURITY.md](./SECURITY.md) for supply-chain (signing, attestation) and disclosure policy.

---

## Troubleshooting

### Any OS: `/health` shows `"ollama_down"` but Ollama is running

Default Ollama endpoint is `http://localhost:11434`. If Ollama was started on a non-default port, override:

```yaml
# /etc/rt-node-agent/config.yaml (Linux/macOS)  |  %ProgramData%\rt-node-agent\config.yaml (Windows)
ollama_endpoint: http://localhost:11500
```

Then restart the service (see the restart commands in [Configuration → Enable `/actions/*`](#enable-actions-set-a-token)).

### Any OS: GPU detection per platform

The agent auto-selects a GPU probe based on the host — no per-OS configuration needed:

- **Linux / Windows / Intel Mac + eGPU** — shells out to `nvidia-smi`. If not on PATH, `gpus: []` (not an error). Ensure the NVIDIA driver is installed and `nvidia-smi` is reachable by the service user.
- **Apple Silicon** — uses `system_profiler SPDisplaysDataType`. **No `nvidia-smi` required or expected.** The `gpus` list contains one entry with the M-series chip name; per-process VRAM is not exposed (no public macOS API). `memory.unified: true` signals to the ranker that RAM pressure is the GPU-pressure signal on this class of host.
- **Any host with no supported GPU stack** — `gpus: []`. Agent still serves `/health`; the ranker treats as CPU-only.

### Linux

```sh
systemctl status rt-node-agent        # state + last log lines
journalctl -u rt-node-agent -f        # live tail
journalctl -u rt-node-agent --since '1 hour ago'
```

If `install.sh` fails at `useradd`, you're not root — run with `sudo sh`, not `sh`.

### macOS

**Application Firewall blocking port 11435.** If the case-manager can't reach `/health` but `curl localhost:11435/health` from the node itself works, the macOS firewall is dropping incoming connections because `rt-node-agent` isn't in its allow-list. From v0.2.0 the install step handles this automatically; for v0.1.0 installs:

```sh
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --add /usr/local/bin/rt-node-agent
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --unblockapp /usr/local/bin/rt-node-agent
sudo launchctl kickstart -k system/com.redtorch.rt-node-agent
```

Verify:

```sh
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --getappblocked /usr/local/bin/rt-node-agent
# → "incoming connections are not being blocked for rt-node-agent"
```

Log files (launchd writes to these, not to Console.app):

```sh
tail -f /var/log/rt-node-agent.log
tail -f /var/log/rt-node-agent.err
```

Service state:

```sh
sudo launchctl print system/com.redtorch.rt-node-agent
```

If the service doesn't start and logs are empty, the binary was likely quarantined (downloaded via browser, not curl). Fix:

```sh
sudo xattr -dr com.apple.quarantine /usr/local/bin/rt-node-agent
sudo launchctl kickstart -k system/com.redtorch.rt-node-agent
```

### Windows

```powershell
Get-Service rt-node-agent
Get-EventLog -LogName Application -Source rt-node-agent -Newest 20
```

If `install.ps1` errors with **`must run from an elevated PowerShell`**, the session isn't elevated. Close and reopen PowerShell via right-click → Run as Administrator. The Service Control Manager refuses `CreateService` from non-elevated processes.

If port 11435 isn't reachable from the case-manager, check Windows Defender Firewall — the agent binds `0.0.0.0:11435` but does not open the port itself.

---

## Reporting issues

Open a GitHub issue at [redtorchinc/node-agent/issues](https://github.com/redtorchinc/node-agent/issues).

**Please redact internal hostnames, IPs, and any fleet-specific identifiers from bug reports** — this is a public repository and everything posted is world-readable forever.

---

## License

Apache-2.0. See [LICENSE](./LICENSE).
