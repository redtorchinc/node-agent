# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Status

**Design stage — no code yet.** The only file is `SPEC.md`, which is authoritative. Any implementation work starts by scaffolding the layout described in SPEC.md §"Development and release plan". Before writing code, re-read SPEC.md — decisions here (port numbers, degraded-reasons vocabulary, endpoint shapes) are part of the contract with the case-manager backend and must not drift.

## What this repo is

A public, self-contained Go binary (`rt-node-agent`) that runs on every GPU/CPU node in the RedTorch fleet and exposes an HTTP surface on **port 11435** (deliberately adjacent to Ollama's 11434). The private case-manager backend calls `GET /health` to rank nodes for dispatch and `POST /actions/unload-model` to free VRAM.

**Public repo by design** so nodes can `curl | sh` install and self-update without needing credentials for the private case-manager repo. Do not add any dependency, reference, or secret that assumes access to the private repo.

### Public-repo hygiene (critical)

This repo is published at `https://github.com/redtorchinc/node-agent`. Everything committed is world-readable and cached forever — GitHub, archive.org, training datasets, `git clone` mirrors. Treat `.gitignore` as a safety rail, not a tidiness tool.

- **Before adding a new file**, ask: would I paste this into a public Slack? If no, add a pattern to `.gitignore` *first*, then create the file.
- **Never commit**: bearer tokens (`RT_AGENT_TOKEN`, `/etc/rt-node-agent/token` contents), `.env` files, private keys, internal hostnames/IPs from the case-manager fleet, real node identifiers, real case data, signed release keys.
- **Reference the private case-manager repo only by role** ("the backend") — don't commit its URL, paths, or internal module names.
- **Spec examples are already public** (`SPEC.md` mentions `ctrlone-Intel-R-Core-TM-i5-14400F`, `gliner2-service`, the 2026-04-22 incident). If a future example would reveal more than those, sanitize it.
- `git log -p --all` and `git reflog` are public too — a committed secret is compromised even if reverted. Rotate, don't rewrite.

## Architectural constants (do not change without updating the backend contract)

- **Language:** Go 1.22+, single static cross-compiled binary per OS. No runtime deps on the host.
- **Dependencies kept minimal:** `gopsutil/v3` for CPU/mem/process, stdlib `net/http`, shell out to `nvidia-smi --query-gpu=... --format=csv` for GPU. **No CGO, no NVML bindings** — the shell-out is deliberate for portability and debuggability.
- **No framework.** Stdlib router + `encoding/json`.
- **Auth model:** read endpoints (`/health`, `/metrics`, `/version`) are open on LAN; mutating endpoints (`/actions/*`) require `Authorization: Bearer` against `RT_AGENT_TOKEN` env or `/etc/rt-node-agent/token`. Matches the air-gapped OPSEC model — do not add TLS, mTLS, or per-user auth in v1.
- **Pull-based only.** The agent never pushes to the backend. No persistence on the node. No remote shell, no file read/write endpoints, ever.

## The `degraded_reasons` contract

This is the single most important cross-repo contract. `rank_nodes()` in the case-manager reads these strings directly — adding, renaming, or removing one is a breaking change. See SPEC.md for the current vocabulary and severity tiers (hard = skip node, soft = deprioritize). The `vram_service_creep_*` reasons exist because of an observed 2026-04-22 PyTorch allocator leak on the gliner2-service box where `nvidia-smi` showed 16 GB used while real usage was 2 GB — keep that motivation in mind when touching the service-allocator scrape code.

## Platform matrix

Each platform path has its own GPU-detection and service-manager story — don't assume Linux behavior generalizes:

| OS | GPU path | Service manager |
|---|---|---|
| Linux / DGX | `nvidia-smi` | systemd |
| macOS Apple Silicon | `ioreg` + `sysctl` unified memory | launchd |
| macOS Intel + eGPU | `nvidia-smi` if present, else CPU-only | launchd |
| Windows | `nvidia-smi` | native Windows Service |

On Apple Silicon, `memory.unified: true` in `/health` — RAM pressure is GPU pressure there, and the ranker depends on that flag.

## Planned layout (from SPEC §Development plan)

```
main.go
cmd/install.go          # self-install subcommand: detects OS, writes service unit
internal/gpu/           # nvidia-smi parser + Apple Silicon ioreg path
internal/health/        # degraded_reasons evaluator
internal/server/        # stdlib HTTP handlers
Makefile                # cross-compile matrix
```

Build order per the spec: Linux NVIDIA → Apple Silicon → Windows → service-allocator scrape loop → GH Actions release pipeline → `install.sh`.

## Build / run (once code exists)

None of these work yet — placeholder until `main.go` and `Makefile` land:

```
go build ./...
go test ./...
go test ./internal/health -run TestDegradedReasons   # single test
make release                                          # cross-compile matrix
./rt-node-agent healthcheck                           # runs /health logic once, exits
```

Keep the cross-compile matrix honest — DGX Grace Hopper is arm64 Linux, so `nvidia-smi` CSV parsing must be tested on ARM, not just amd64.
