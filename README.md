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

### Linux

```sh
curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/install.sh | sudo sh
```

### macOS

```sh
curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/install.sh | sudo sh
```

### Windows (elevated PowerShell)

```powershell
iwr -useb https://github.com/redtorchinc/node-agent/releases/latest/download/install.ps1 | iex
```

### Verify

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

**Upgrading from an earlier version:** the installer auto-runs `config
migrate`, which (when the schema gained keys) moves your existing
`config.yaml` to `config.yaml.bak`, writes the new schema's defaults to
`config.yaml`, and grafts every top-level value you had set back onto
the live file. To enable new features, edit `config.yaml` directly and
restart. The migration is idempotent — running it twice in a row is a
no-op the second time.

```sh
diff /etc/rt-node-agent/config.yaml.bak /etc/rt-node-agent/config.yaml  # see what changed
sudo nano /etc/rt-node-agent/config.yaml                                 # enable new features
sudo systemctl restart rt-node-agent                                     # (or launchctl / Restart-Service)
```

## Operating the agent (per OS)

Day-to-day commands differ per service manager — `systemctl` on Linux,
`launchctl` on macOS, `Get-Service` on Windows. The agent surface
(`/health`, `/actions/*`, port 11435) is identical across all three.

### Linux (systemd)

| Action | Command |
|---|---|
| Status | `sudo systemctl status rt-node-agent` |
| Start / stop | `sudo systemctl start rt-node-agent` / `sudo systemctl stop rt-node-agent` |
| Restart (e.g. after config edit) | `sudo systemctl restart rt-node-agent` |
| Reload only (signal-based, where supported) | `sudo systemctl reload rt-node-agent` |
| Enable / disable at boot | `sudo systemctl enable rt-node-agent` / `sudo systemctl disable rt-node-agent` |
| Live logs | `sudo journalctl -u rt-node-agent -f` |
| Recent logs | `sudo journalctl -u rt-node-agent -n 200 --no-pager` |
| Config file | `/etc/rt-node-agent/config.yaml` |
| Bearer token | `/etc/rt-node-agent/token` (`chmod 0640`, group `rt-agent`) |
| Unit file | `/etc/systemd/system/rt-node-agent.service` |
| sudoers drop-in (service control) | `/etc/sudoers.d/rt-node-agent` |
| Uninstall | `sudo rt-node-agent uninstall` |

### macOS (launchd)

| Action | Command |
|---|---|
| Status | `sudo launchctl print system/com.redtorch.rt-node-agent` |
| Start / stop | `sudo launchctl kickstart system/com.redtorch.rt-node-agent` / `sudo launchctl kill SIGTERM system/com.redtorch.rt-node-agent` |
| Restart (e.g. after config edit) | `sudo launchctl kickstart -k system/com.redtorch.rt-node-agent` |
| Enable / disable at boot | `sudo launchctl enable system/com.redtorch.rt-node-agent` / `sudo launchctl disable system/com.redtorch.rt-node-agent` |
| Unload (full disable, survives reboot) | `sudo launchctl bootout system /Library/LaunchDaemons/com.redtorch.rt-node-agent.plist` |
| Load (after `bootout`) | `sudo launchctl bootstrap system /Library/LaunchDaemons/com.redtorch.rt-node-agent.plist` |
| Live logs (stdout) | `tail -f /var/log/rt-node-agent.log` |
| Live logs (stderr) | `tail -f /var/log/rt-node-agent.err` |
| Unified log stream | `log stream --predicate 'process == "rt-node-agent"'` |
| Config file | `/etc/rt-node-agent/config.yaml` |
| Bearer token | `/etc/rt-node-agent/token` (`chmod 0640`) |
| Plist | `/Library/LaunchDaemons/com.redtorch.rt-node-agent.plist` |
| Uninstall | `sudo rt-node-agent uninstall` |

Notes:
- Apple's Application Firewall is auto-allowed by the installer; if you
  bypass the installer and drop the binary in by hand, see
  [docs/troubleshooting.md](docs/troubleshooting.md#macos-application-firewall-blocking-incoming).
- `POST /actions/service` returns **501** on macOS — remote service
  control is Linux-only by design (no allowlisted unit mechanism exists
  for launchd in this build).

### Windows (Service Control Manager — elevated PowerShell)

| Action | Command |
|---|---|
| Status | `Get-Service rt-node-agent` |
| Start / stop | `Start-Service rt-node-agent` / `Stop-Service rt-node-agent` |
| Restart (e.g. after config edit) | `Restart-Service rt-node-agent` |
| Set startup type | `Set-Service rt-node-agent -StartupType Automatic` (or `Manual` / `Disabled`) |
| Live logs (Event Viewer CLI) | `Get-WinEvent -LogName Application -ProviderName rt-node-agent -MaxEvents 50` |
| Service detail via `sc.exe` | `sc.exe query rt-node-agent` |
| Config file | `%ProgramData%\rt-node-agent\config.yaml` |
| Bearer token | `%ProgramData%\rt-node-agent\token` |
| Uninstall | `rt-node-agent.exe uninstall` (elevated) |

Notes:
- `POST /actions/service` returns **501** on Windows (same rationale as
  macOS).
- `GET /health.time_sync` is **omitted** on Windows (no `w32tm` parser
  in v0.2).

### Cross-platform CLI (works on every OS)

```sh
rt-node-agent run            # foreground (used by the service unit)
rt-node-agent healthcheck    # /health logic once to stdout; exit 0 healthy, 1 degraded
rt-node-agent version        # version / git SHA / build time
rt-node-agent config migrate # back up config.yaml → .bak, merge in new schema, preserve operator values
rt-node-agent install        # register as native service (Administrator/root)
rt-node-agent uninstall      # deregister
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
