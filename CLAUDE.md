# CLAUDE.md

Guidance for Claude Code working in this repository. New agents — orient
yourself via [ARCHITECTURE.md](ARCHITECTURE.md) before editing anything.

## Status

**v0.2.0 shipped.** The agent is feature-complete for the inference plane
+ allowlisted service control + training-mode coordination + RDMA fabric
visibility. [spec/SPEC.md](spec/SPEC.md) is the authoritative wire contract
(any change there is a cross-repo break). [V0_2_0_PLAN.md](V0_2_0_PLAN.md)
records the v0.2.0 design; [PLAN.md](PLAN.md) captures the original v0.1.0
build plan (now complete).

Before editing — re-read whichever spec doc covers the area you're touching.
Decisions there (port numbers, degraded-reasons vocabulary, endpoint shapes)
are part of the contract with the case-manager backend and must not drift.

## What this repo is

A public, self-contained Go binary (`rt-node-agent`) that runs on every
GPU/CPU node in the RedTorch fleet and exposes an HTTP surface on **port
11435** (deliberately adjacent to Ollama's 11434). The private case-manager
backend calls:

- `GET /health` to rank nodes for dispatch
- `GET /capabilities` to feature-detect (v0.2.0+)
- `POST /actions/unload-model` to free Ollama VRAM
- `POST /actions/service` to start/stop allowlisted vLLM units (v0.2.0+)
- `POST /actions/training-mode` to coordinate inference ↔ training (v0.2.0+)

**Public repo by design** so nodes can `curl | sh` install and self-update
without needing credentials for the private case-manager repo. Do not add
any dependency, reference, or secret that assumes access to the private
repo.

### Public-repo hygiene (critical)

Published at `https://github.com/redtorchinc/node-agent`. Everything
committed is world-readable forever — GitHub mirrors, archive.org,
training datasets, `git log -p --all`. Treat `.gitignore` as a safety rail,
not a tidiness tool.

- **Before adding a new file**, ask: would I paste this into a public Slack? If no, add a pattern to `.gitignore` *first*, then create the file.
- **Never commit**: bearer tokens (`RT_AGENT_TOKEN`, `/etc/rt-node-agent/token` contents), `.env` files, private keys, internal hostnames/IPs from the case-manager fleet, real node identifiers, real case data, signed release keys.
- **Reference the private case-manager repo only by role** ("the backend") — don't commit its URL, paths, or internal module names.
- **Spec examples are already public** ([spec/SPEC.md](spec/SPEC.md) mentions `ctrlone-Intel-R-Core-TM-i5-14400F`, `gliner2-service`, the 2026-04-22 incident). If a future example would reveal more than those, sanitize it.
- `git log -p --all` and `git reflog` are public too — a committed secret is compromised even if reverted. Rotate, don't rewrite.

## Architectural constants (do not change without updating the backend contract)

- **Language:** Go 1.22+, single static cross-compiled binary per OS. No runtime deps on the host.
- **Dependencies kept minimal:** `gopsutil/v3` for CPU/mem/process, `golang.org/x/sys` for Windows SCM, `gopkg.in/yaml.v3` for config parsing. Stdlib `net/http` for the server. Shell out to `nvidia-smi` for GPU. **No CGO, no NVML bindings.**
- **No framework.** Stdlib `http.ServeMux` + `encoding/json`.
- **Auth model:** read endpoints (`/health`, `/metrics`, `/version`, `/capabilities`) are open on LAN; mutating endpoints (`/actions/*`) require `Authorization: Bearer` against `RT_AGENT_TOKEN` env or `/etc/rt-node-agent/token`. Matches the air-gapped OPSEC model — do not add TLS, mTLS, or per-user auth in v1/v2.
- **Pull-based only.** The agent never pushes to the backend. The only on-node persistence is `/var/lib/rt-node-agent/training_mode.json` (single small JSON, recoverable on crash). No remote shell, no file read/write endpoints, ever.

## The `degraded_reasons` contract

This is the single most important cross-repo contract. `rank_nodes()` in
the case-manager reads these strings directly — adding, renaming, or
removing one is a breaking change. See [docs/degraded-reasons.md](docs/degraded-reasons.md)
for the canonical list and severity tiers (hard = skip node, soft =
deprioritize). The `vram_service_creep_*` reasons exist because of an
observed 2026-04-22 PyTorch allocator leak on the gliner2-service box
where `nvidia-smi` showed 16 GB used while real usage was 2 GB — keep
that motivation in mind when touching the service-allocator scrape code.

## Platform matrix

Each platform path has its own GPU-detection and service-manager story —
don't assume Linux behavior generalizes:

| OS | GPU path | Service manager | Remote service control |
|---|---|---|---|
| Linux / DGX | `nvidia-smi` | systemd | yes (allowlisted) |
| macOS Apple Silicon | `ioreg` + unified memory | launchd | no (stub returns 501) |
| macOS Intel + eGPU | `nvidia-smi` if present | launchd | no |
| Windows | `nvidia-smi` | native Windows Service | no |

On Apple Silicon, `memory.unified: true` in `/health` — RAM pressure is
GPU pressure there, and the ranker depends on that flag.

## Current layout

See [ARCHITECTURE.md](ARCHITECTURE.md) for the file-by-file map. Key
packages:

```
cmd/rt-node-agent/main.go     # subcommand dispatch (run, install, config migrate, …)
internal/server/              # HTTP handlers, routing, auth
internal/health/              # /health composer + degraded_reasons evaluator
internal/config/              # Loader; subpackage migrate/ does v1→v2 upgrade
internal/platforms/{ollama,vllm}/  # Per-platform model probes
internal/services/            # Allowlisted systemctl wrapper (Linux only)
internal/mode/                # Training-mode state machine + state file
internal/rdma/                # /sys/class/infiniband reader (Linux only)
internal/sysmetrics/{disk,network,timesync}/  # Cross-platform helpers
internal/{gpu,mem,ollama,allocators,service,buildinfo}/  # v0.1 modules
```

## Build / run

```
go build ./...
go test ./...
make build              # native binary with -ldflags
make cross              # 5-target cross-compile matrix
./rt-node-agent run     # foreground
./rt-node-agent install # register as native service (root)
./rt-node-agent config migrate   # surface new keys on upgrade
./rt-node-agent healthcheck      # /health once to stdout, exit 0 healthy / 1 degraded
```

Keep the cross-compile matrix honest — DGX Grace Hopper is arm64 Linux,
so `nvidia-smi` CSV parsing must be tested on ARM, not just amd64. Test
fixtures already cover the GH200 CSV shape.
