# ARCHITECTURE.md

Start here. New contributors (human or AI) should read this top-to-bottom
before editing — it's the map that turns the file tree into a mental
model of the agent.

---

## What the agent does (60 seconds)

`rt-node-agent` is one Go binary per node in the RedTorch fleet. It runs
as a native service (systemd / launchd / Windows Service) and serves HTTP
on **port 11435**:

- **Read endpoints (no auth, LAN-only):** `/health`, `/capabilities`,
  `/version`, `/metrics` — used by the case-manager backend's
  `rank_nodes()` to decide which node should serve the next request.
- **Mutating endpoints (Bearer-token auth):** `/actions/unload-model`,
  `/actions/service`, `/actions/training-mode` — free VRAM, start/stop
  allowlisted vLLM units, coordinate inference ↔ training transitions.

Everything is **pull-based** — the agent never originates a call to the
backend. The only on-node persistence is a single small JSON state file
for training-mode (so a crash mid-training doesn't leak the node back
into inference). No TLS in v2; the model assumes the agent runs on the
same trusted LAN as the case-manager.

---

## Spec & plan docs (start here when in doubt)

| File | Role |
|---|---|
| [spec/SPEC.md](spec/SPEC.md) | **Authoritative wire contract.** `/health` JSON shape, `degraded_reasons` vocabulary, endpoint paths. Cross-repo contract with the case-manager backend; changes here are breaking. |
| [spec/NODE_AGENT_TRAINING_EXTENSIONS.md](spec/NODE_AGENT_TRAINING_EXTENSIONS.md) | v0.2.0 training-plane spec: RDMA fields, mode state machine, `POST /actions/training-mode`. |
| [PLAN.md](PLAN.md) | v0.1.0 build plan, historical. All milestones M0–M12 complete. |
| [V0_2_0_PLAN.md](V0_2_0_PLAN.md) | v0.2.0 design. Source-of-truth for the additions in the current release. |
| [CLAUDE.md](CLAUDE.md) | Architectural constants + public-repo hygiene. Read before adding files or committing. |
| [docs/](docs/) | Operator-facing how-to (install, config, per-endpoint reference, troubleshooting). |

---

## Entry points

| File | What it does |
|---|---|
| [cmd/rt-node-agent/main.go](cmd/rt-node-agent/main.go) | Thin subcommand dispatcher: `run`, `install`, `uninstall`, `status`, `start`, `stop`, `version`, `healthcheck`, `config migrate`, `update`. Wires the config loader → health reporter → HTTP server. |
| [Makefile](Makefile) | `make build` (native), `make cross` (5-target matrix), `make test`, `make vet`. |
| [scripts/install.sh](scripts/install.sh), [scripts/install.ps1](scripts/install.ps1) | Public `curl \| sh` / `iwr \| iex` install bootstraps. Download, verify, place binary, call `rt-node-agent install`. |

---

## Internal packages

Everything below `internal/` is unimportable from outside the module —
the agent has no public Go API.

### HTTP surface

| Path | Role |
|---|---|
| [internal/server/server.go](internal/server/server.go) | Constructor + route table. Wires reporter, services manager, mode manager. Stdlib `http.ServeMux`, no router dep. |
| [internal/server/handlers.go](internal/server/handlers.go) | `handleHealth`, `handleVersion`, `handleUnload`, `handleMetrics`. The Prometheus exposition lives in `handleMetrics`. |
| [internal/server/auth.go](internal/server/auth.go) | Bearer-token middleware (`requireToken`). Constant-time compare; 503 when token unset, 401 otherwise. |
| [internal/server/capabilities.go](internal/server/capabilities.go) | `GET /capabilities` — dispatcher feature-detection. |
| [internal/server/services_handler.go](internal/server/services_handler.go) | `POST /actions/service`. Maps typed errors from `internal/services` to HTTP codes. |
| [internal/server/training_handler.go](internal/server/training_handler.go) | `POST /actions/training-mode`. Drains Ollama before entering training-mode (fails closed on unload failure). |
| [internal/server/rdma_avail_{linux,other}.go](internal/server/) | Tiny per-OS shim so `/capabilities` can advertise RDMA availability without importing `internal/rdma` directly. |

### `/health` composition + degraded evaluation

| Path | Role |
|---|---|
| [internal/health/report.go](internal/health/report.go) | `Reporter.Report(ctx)` — composes the full payload by fanning out to GPU, mem, platforms, allocators, RDMA, mode, services with per-probe timeouts. |
| [internal/health/degraded.go](internal/health/degraded.go) | Pure function `Evaluate(Report, Config, time) → (bool, []string)`. **This is the single most important cross-repo contract.** Every reason string is read by the case-manager's `rank_nodes()`. |

### Config

| Path | Role |
|---|---|
| [internal/config/config.go](internal/config/config.go) | Loader: defaults → file → env. Struct definitions for all v0.2.0 keys (`Platforms`, `Services`, `TrainingMode`, `RDMA`, `Disk`). |
| [internal/config/defaults.go](internal/config/defaults.go) | `DefaultYAML` constant — the single source of truth for the example config. `SchemaVersion = 2`. |
| [internal/config/migrate/migrate.go](internal/config/migrate/migrate.go) | Backup-then-merge-in-place migration (since v0.2.7). Moves existing `config.yaml` to `config.yaml.bak`, writes the schema's defaults to the live path, then grafts every top-level value the operator had set onto the new tree. Idempotent: re-running is a no-op when the schema already matches. Replaced the older `.new` sidecar approach, which compounded duplicate commented blocks across re-installs. |
| [examples/config.yaml](examples/config.yaml) | Operator-facing example, kept in sync with `DefaultYAML`. |

### Platforms (inference backends)

| Path | Role |
|---|---|
| [internal/platforms/platform.go](internal/platforms/platform.go) | `Platform` interface + canonical `Model`/`Report`/`Queue`/`KVCache`/`Latency`/`Throughput`/`Counters` types. Pointer fields ⇒ `null` for un-suppliable data. |
| [internal/platforms/ollama/ollama.go](internal/platforms/ollama/ollama.go) | Adapter wrapping the existing `internal/ollama` client. Maps `/api/ps` shape into the canonical `platforms.Model`. |
| [internal/platforms/vllm/vllm.go](internal/platforms/vllm/vllm.go) | `/v1/models` + `/metrics` scraper. Builds per-model entries with queue, KV cache, latency percentiles, token counters. |
| [internal/platforms/vllm/promparse.go](internal/platforms/vllm/promparse.go) | Hand-rolled Prometheus text parser + histogram quantile (linear interpolation). No client dep. |

### Service control (Linux only)

| Path | Role |
|---|---|
| [internal/services/manager.go](internal/services/manager.go) | `Manager` interface + allowlist enforcement (`validate`). Typed errors (`ErrUnitNotAllowed`, `ErrActionNotAllowed`, `ErrUnsupported`, …) map cleanly to HTTP codes. |
| [internal/services/systemd_linux.go](internal/services/systemd_linux.go) | Shells `systemctl` via `exec.Cmd` (never via shell). Wraps `sudo` when not root. |
| [internal/services/stub_other.go](internal/services/stub_other.go) | Returns `ErrUnsupported` on macOS/Windows. |
| [internal/services/healthbridge.go](internal/services/healthbridge.go) | Adapter for `health.ServicesReporter` — keeps `internal/health` free of services imports. |
| [internal/service/sudoers_linux.go](internal/service/sudoers_linux.go) | Writes `/etc/sudoers.d/rt-node-agent` on install, validated with `visudo -cf`. Pattern: `rt-vllm-[a-zA-Z0-9_-]*.service` only. |

### Training mode

| Path | Role |
|---|---|
| [internal/mode/mode.go](internal/mode/mode.go) | State machine (`idle` / `inference` / `training_mode`). Persisted JSON state file at `/var/lib/rt-node-agent/training_mode.json`. Auto-recovery on `entered_at + expected + grace` expiry. |

### System metrics

| Path | Role |
|---|---|
| [internal/gpu/gpu.go](internal/gpu/gpu.go) | Shared types (`GPU`, `Process`, `NVLink`). v0.2.0 expanded to 25 fields incl. clocks, ECC, throttle reasons, MIG, NVLink. |
| [internal/gpu/nvidia_smi.go](internal/gpu/nvidia_smi.go) | `nvidia-smi --query-gpu=…` CSV parser + `nvidia-smi nvlink --status` parser. Tested with Grace Hopper fixtures (`linux/arm64`). |
| [internal/gpu/apple.go](internal/gpu/apple.go) | Apple Silicon (`darwin/arm64`) GPU probe via `system_profiler`. |
| [internal/gpu/cache.go](internal/gpu/cache.go) | 5s TTL `CachedProbe` wrapper — bounds `/health` p99 against `nvidia-smi` spikes. |
| [internal/mem/mem.go](internal/mem/mem.go) | RAM/swap via `gopsutil`. v0.2.0 adds `Pressure`, `HugePages_{Total,Free}` (Linux only), `Available/Buffers/Cached` always. |
| [internal/mem/pressure_linux.go](internal/mem/pressure_linux.go) | PSI parser at `/proc/pressure/memory`. |
| [internal/sysmetrics/disk/disk.go](internal/sysmetrics/disk/disk.go) | Disk usage via `gopsutil/disk` + auto-discovery of mounts ≥ 50 GB. |
| [internal/sysmetrics/network/network.go](internal/sysmetrics/network/network.go) | Interface state via `gopsutil/net`. Skips Docker / bridge / veth noise. |
| [internal/sysmetrics/timesync/timesync_linux.go](internal/sysmetrics/timesync/timesync_linux.go) | `chronyc tracking` parser, `timedatectl` fallback. Linux only. |
| [internal/rdma/rdma_linux.go](internal/rdma/rdma_linux.go) | Reads `/sys/class/infiniband/` for IB device state, link rate, counters, kernel-module presence. |

### Inference + utility

| Path | Role |
|---|---|
| [internal/ollama/client.go](internal/ollama/client.go) | `/api/ps` HTTP client with 5s cache + 2s timeout. The original v0.1 path; `internal/platforms/ollama/` wraps it for the new canonical surface. |
| [internal/ollama/runners.go](internal/ollama/runners.go) | Enumerates `ollama runner` PIDs via `gopsutil/process`. |
| [internal/ollama/unload.go](internal/ollama/unload.go) | `ollama stop <model>` first; `POST /api/generate {keep_alive: 0}` fallback. |
| [internal/allocators/scraper.go](internal/allocators/scraper.go) | Per-service HTTP poller. v0.2.0 added `only_when_mode` gate + opaque pass-through under `Scraped.Extra`. |
| [internal/service/service.go](internal/service/service.go) | OS service-manager interface. Implementations: `systemd.go` (Linux), `launchd.go` (macOS), `winsvc.go` (Windows). |
| [internal/service/bootstrap.go](internal/service/bootstrap.go) | `writeConfigExample` + `runConfigMigrate` invoked by every per-OS `install()`. |
| [internal/buildinfo/buildinfo.go](internal/buildinfo/buildinfo.go) | Three `var`s set at link time via `-ldflags`. Defaults `dev`/`unknown` for local builds. |

---

## What's critical to get right

If you only have time to understand 5 things, understand these:

1. **`degraded_reasons` vocabulary** is a cross-repo contract. The
   case-manager's `rank_nodes()` matches string-for-string. Renames or
   removals break dispatch. See [docs/degraded-reasons.md](docs/degraded-reasons.md)
   for the canonical list. Add new reasons by appending only.

2. **`/health` JSON shape is frozen for v0.x.** Additive changes are
   safe (`omitempty` is your friend); renames / removals are not. The
   legacy top-level `ollama` field is preserved as an alias of
   `platforms.ollama` through all of v0.2.x.

3. **Allowlist enforcement in [internal/services/manager.go](internal/services/manager.go)
   is the single point of trust** for `/actions/service`. Every entry
   path must call `validate()` before `exec.Cmd`. The sudoers drop-in
   restricts to `rt-vllm-*.service` as defense in depth — but config
   misconfiguration could still pick that name.

4. **No remote shell, no arbitrary args, ever.** Endpoints accept
   typed JSON; nothing from the client flows verbatim to a shell. The
   v0.1 `/actions/unload-model` and v0.2 `/actions/service` /
   `/actions/training-mode` all use closed enums + exact-match
   allowlists.

5. **Public repo hygiene.** Everything committed is world-readable
   forever — including `git log -p --all`. Tokens, real hostnames, real
   case data, signing keys: never. If a secret gets committed, **rotate**;
   don't try to rewrite history. See [CLAUDE.md](CLAUDE.md) §Public-repo
   hygiene for the full list.

---

## Build / test / release

```sh
# Local dev
make build          # native binary at ./rt-node-agent
make test           # go test ./...
make vet            # go vet ./...
./rt-node-agent run # foreground server

# Cross-compile (release artifacts)
make cross          # 5 binaries in dist/ + SHA256SUMS

# Local migration check
RT_AGENT_CONFIG=/tmp/old.yaml ./rt-node-agent config migrate
```

Release is automated: tag push (`v*`) triggers
[.github/workflows/release.yml](.github/workflows/release.yml) which
cross-compiles, signs (minisign), and publishes to GitHub Releases.

---

## Per-architecture coverage matrix

The wire contract is the same on every OS, but the set of fields the
agent can populate varies. `null` (or omitted via `omitempty`) means the
platform genuinely can't supply the data — **never** fabricate a zero.

| Field | Linux NVIDIA | macOS Apple Silicon | Windows NVIDIA |
|---|---|---|---|
| CPU usage % / per-core | ✅ | ✅ | ✅ |
| CPU load avg | ✅ | ✅ | ❌ (no kernel load avg) |
| CPU temps | ✅ (`/sys/class/hwmon`) | root-only via `powermetrics` | ✅ (WMI) |
| GPU VRAM | ✅ (`nvidia-smi`) | ✅ (unified — derived from `memory.total_mb`; `gpus[].vram_unified: true`, `memory.unified: true`) | ✅ |
| Swap counters (`pswpin`/`pswpout`), PSI raw gauges | ✅ (`/proc/vmstat`, `/proc/pressure/memory`) | ❌ | ❌ |
| `top_swap_processes[]` | ✅ (`/proc/<pid>/status:VmSwap`) | ❌ | ❌ |
| `databases[]` (Postgres/MySQL/Mongo/Redis/Neo4j/Chroma/…20 fingerprints) | ✅ | ✅ (process-only, no kstat) | ✅ |
| `storage[]` (ZFS / NFS / CIFS / Ceph / GlusterFS / Lustre) | ✅ (`/proc/spl/kstat/zfs`, `/proc/self/mounts`) | partial (NFS / SMB via gopsutil) | partial |
| GPU NVLink / MIG / ECC | ✅ | `null` | ✅ |
| Per-process VRAM | ✅ | `null` (no public API) | ✅ |
| Disk / network | ✅ | ✅ | ✅ |
| Time sync (`chronyc`/`sntp`) | ✅ | best-effort `sntp` | ❌ (v0.2 no-op) |
| RDMA fabric | ✅ when `/sys/class/infiniband` populated | `null` | `null` |
| `POST /actions/service` | ✅ | 501 stub | 501 stub |

Full table in [docs/api/health.md](docs/api/health.md) §"Per-architecture coverage".
