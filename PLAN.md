# PLAN.md — rt-node-agent Implementation Plan

Derived from `SPEC.md`. SPEC defines *what*; this defines *how* and *in what order*. If SPEC and PLAN disagree, SPEC wins — update this file.

Target outcome: a single `rt-node-agent` Go binary that, after `curl | sh` (or platform equivalent), is running as a native service on Linux, macOS, or Windows with no additional manual steps, serving the HTTP surface in SPEC §"HTTP API".

---

## 1. Guiding principles (carried from SPEC + CLAUDE.md)

1. **Single static binary per OS/arch.** No CGO, no runtime deps on the host. `go build` with `CGO_ENABLED=0`.
2. **Stdlib-first.** Only three external imports are allowed in v1:
   - `github.com/shirou/gopsutil/v3` — CPU/mem/process enumeration.
   - `golang.org/x/sys` — Windows service registration, POSIX syscalls where stdlib falls short.
   - `gopkg.in/yaml.v3` — config file parsing. (Could be avoided with a JSON config; see §11.)
3. **Public repo.** Assume every file is world-readable forever. See CLAUDE.md §Public-repo hygiene.
4. **Contract stability.** The JSON shape of `/health` and the `degraded_reasons` vocabulary are cross-repo contracts with the case-manager backend. Additive changes are fine; renames and removals are breaking.
5. **Ship thin vertical slices.** Each milestone below produces a working, testable artifact — not a layer that only makes sense once the next layer lands.
6. **No premature abstraction.** Per CLAUDE.md: "three similar lines is better than a premature abstraction." The GPU probes look similar but differ enough (nvidia-smi CSV vs. ioreg plist vs. Windows WMI fallback) that forcing a perfect interface early will hurt.

---

## 2. Target repo layout

```
node-agent/
├── .github/
│   └── workflows/
│       ├── ci.yml              # test + vet on push
│       └── release.yml         # cross-compile + sign + publish on tag
├── cmd/
│   └── rt-node-agent/
│       └── main.go             # thin: flag parse → subcommand dispatch
├── internal/
│   ├── config/
│   │   ├── config.go           # Load() from /etc/rt-node-agent/config.yaml + env
│   │   └── config_test.go
│   ├── gpu/
│   │   ├── gpu.go              # shared types (GPU, Process)
│   │   ├── nvidia_smi.go       # Probe() via `nvidia-smi --query-gpu=... --format=csv,noheader,nounits`
│   │   ├── nvidia_smi_test.go  # golden-file CSV parsing (including DGX/Grace Hopper sample)
│   │   ├── apple.go            # build tag darwin/arm64; ioreg + sysctl
│   │   ├── apple_test.go       # build tag darwin
│   │   └── noop.go             # fallback when no GPU detected
│   ├── mem/
│   │   ├── mem.go              # cross-platform via gopsutil
│   │   └── unified.go          # darwin/arm64 build tag; sets memory.unified=true
│   ├── ollama/
│   │   ├── client.go           # GET /api/ps against localhost:11434, 2s timeout
│   │   ├── unload.go           # `ollama stop` shell + /api/generate fallback
│   │   └── runners.go          # map ollama runner PIDs → CPU%, RSS via gopsutil
│   ├── allocators/
│   │   ├── scraper.go          # per-service 30s-cached poller
│   │   └── scraper_test.go
│   ├── health/
│   │   ├── report.go           # composes full /health payload
│   │   ├── degraded.go         # pure function: (Report) → (bool, []string)
│   │   └── degraded_test.go    # table-driven — one row per reason in SPEC §degraded_reasons
│   ├── server/
│   │   ├── server.go           # stdlib mux, middleware chain
│   │   ├── handlers.go         # /health, /version, /metrics, /actions/unload-model
│   │   ├── auth.go             # Bearer token check for /actions/*
│   │   └── handlers_test.go    # httptest round-trips
│   ├── service/
│   │   ├── service.go          # Install/Uninstall/Status interface
│   │   ├── systemd.go          # linux build tag; writes /etc/systemd/system/rt-node-agent.service
│   │   ├── launchd.go          # darwin build tag; writes /Library/LaunchDaemons/com.redtorch.rt-node-agent.plist
│   │   └── winsvc.go           # windows build tag; uses x/sys/windows/svc/mgr
│   └── buildinfo/
│       └── buildinfo.go        # Version, GitSHA, BuildTime — set via -ldflags
├── scripts/
│   ├── install.sh              # POSIX curl|sh bootstrap (Linux + macOS)
│   ├── install.ps1             # Windows bootstrap
│   └── uninstall.sh
├── examples/
│   └── config.yaml             # sample config with all knobs documented
├── CLAUDE.md
├── SPEC.md
├── PLAN.md
├── README.md                   # produced in M10
├── LICENSE                     # Apache-2.0 or MIT — pick at M0
├── go.mod
├── go.sum
├── Makefile                    # local dev shortcuts
└── .gitignore
```

**Why `cmd/rt-node-agent/`** — standard Go layout; leaves room for a future `cmd/rt-node-agent-debug/` or similar without reshaping `main.go`.

**Why `internal/`** — everything under here is unimportable from outside the module. Agent internals are not a public Go API.

---

## 3. Milestone roadmap

Each milestone is independently shippable (installable on a test node, produces real `/health` output). The order is chosen so blocking risks surface early: cross-compile early, Linux GPU parsing before macOS, service install as its own milestone (not bolted on last).

| # | Milestone | Deliverable | Blocking risks resolved |
|---|---|---|---|
| M0 | Scaffold | `go mod`, main.go stub that serves `/version` | Layout + build on three OSes |
| M1 | Linux NVIDIA /health | Full `/health` on Linux+NVIDIA | CSV parsing shape, gopsutil on Linux |
| M2 | Config + auth | YAML loader, Bearer middleware | Config file precedence, token rotation |
| M3 | Ollama integration | `/api/ps` probe + runner CPU% | Timeout behavior, Ollama 0.5+ API drift |
| M4 | degraded_reasons | Pure evaluator + tests | Contract correctness before any node ships |
| M5 | Apple Silicon path | `/health` on M-series Mac | Unified memory model, ioreg parsing |
| M6 | Windows path | `/health` on Windows | `nvidia-smi.exe` resolution, path quoting |
| M7 | Service allocators | Scrape loop + creep detection | gliner2-service contract, timeout budget |
| M8 | /actions/unload-model | Authenticated unload endpoint | Ollama API choice (stop vs. keep_alive=0) |
| M9 | Service install (native) | `rt-node-agent install` per OS | systemd/launchd/winsvc permutations |
| M10 | Bootstrap scripts | `install.sh`, `install.ps1` | Download + verify + install in one curl |
| M11 | CI + Releases | Signed multi-arch binaries on tag | Cross-compile matrix, signing toolchain |
| M12 | README + examples | User-facing docs | — |

Rough sizing: M0–M4 is ~2 working days of focused effort; M5–M8 one day each; M9 is the longest (two days with real-machine testing); M10–M12 one day combined.

---

## 4. Milestone details

### M0 — Scaffold

**Goal:** `go run ./cmd/rt-node-agent version` prints a version string on all three OSes.

Steps:
1. `go mod init github.com/redtorchinc/node-agent`
2. Create `cmd/rt-node-agent/main.go` with a flag-based subcommand dispatcher: `run` (default), `install`, `uninstall`, `version`, `healthcheck`, `update`.
3. `internal/buildinfo/buildinfo.go` — three `var`s set by `-ldflags "-X ..."`.
4. `Makefile` with `build`, `test`, `vet`, `cross` (matrix build), `clean`.
5. `LICENSE` — Apache-2.0 (aligns with wider Go ecosystem; cosign/sigstore also Apache).
6. `.github/workflows/ci.yml` — `go vet ./...` + `go test ./...` on `ubuntu-latest`, `macos-14`, `windows-latest`.

Exit criteria: CI green on all three runners; `rt-node-agent version` works locally.

### M1 — Linux NVIDIA /health

**Goal:** `curl localhost:11435/health` on a Linux NVIDIA box returns the full SPEC-shaped payload.

Steps:
1. `internal/gpu/nvidia_smi.go` — one function, `Probe(ctx) ([]GPU, error)`. Shells out to:
   ```
   nvidia-smi --query-gpu=index,name,memory.total,memory.used,utilization.gpu,temperature.gpu,power.draw,power.limit --format=csv,noheader,nounits
   nvidia-smi --query-compute-apps=gpu_uuid,pid,process_name,used_memory --format=csv,noheader,nounits
   ```
   Two calls, joined by GPU UUID/index. 2s timeout each; `exec.CommandContext`.
2. `internal/gpu/nvidia_smi_test.go` — feed recorded CSV output (including DGX Grace Hopper sample — see Open Questions §13) into a parse function. Parsing is a pure function that takes a string; the shell-out is a separate function. Test the parser, not the shell.
3. `internal/mem/mem.go` — gopsutil `mem.VirtualMemory()` + `mem.SwapMemory()`. `unified: false` on Linux always.
4. `internal/health/report.go` — composes `Report`. CPU via `cpu.Counts()` + `load.Avg()`; hostname via `os.Hostname()`; uptime via `host.Uptime()`.
5. `internal/server/server.go` — stdlib `http.ServeMux`, one handler per endpoint. Listen on `RT_AGENT_BIND:RT_AGENT_PORT` (defaults `0.0.0.0:11435`).
6. `rt-node-agent run` becomes the default subcommand and starts the server.

Exit criteria: run on one real Linux NVIDIA box (one of the 10-node cluster); JSON matches SPEC exactly for CPU/mem/GPU fields; service allocators + ollama stay empty for now.

### M2 — Config + auth

**Goal:** `/etc/rt-node-agent/config.yaml` is respected; mutating endpoints reject unauthenticated requests.

Steps:
1. `internal/config/config.go`:
   - Load order: defaults → `/etc/rt-node-agent/config.yaml` (Linux/macOS) or `%ProgramData%\rt-node-agent\config.yaml` (Windows) → env vars.
   - Env overrides: `RT_AGENT_PORT`, `RT_AGENT_BIND`, `RT_AGENT_TOKEN`, `RT_AGENT_METRICS`.
   - Token can be inline or via `/etc/rt-node-agent/token` file path.
2. `internal/server/auth.go` — middleware that checks `Authorization: Bearer <token>` and compares with `subtle.ConstantTimeCompare`. Applied to `/actions/*` only.
3. If no token configured, `/actions/*` returns `503 token not configured` (not 401 — 401 implies auth is set up).

Exit criteria: unit tests cover env > file precedence; auth middleware table test covers missing/malformed/correct/rotated token.

### M3 — Ollama integration

**Goal:** `/health.ollama.*` populates correctly; Ollama up/down is reflected in degraded reasons (groundwork, actual evaluation in M4).

Steps:
1. `internal/ollama/client.go` — `GET http://localhost:11434/api/ps` with 2s timeout. Parse the response into the shape SPEC describes (`name`, `size_mb`, `processor`, `context`, `until_s`).
2. `internal/ollama/runners.go` — scan gopsutil processes for name containing `ollama` with cmdline `runner`; report `cpu_pct`, `rss_mb` per runner.
3. Cache `/api/ps` response for 5s within the agent to avoid hammering Ollama when `/health` is called in a tight loop.

Exit criteria: on a box with Ollama running, `/health.ollama.up == true`, `models` non-empty, `runners[].cpu_pct` within 10% of `top -p <pid>`.

### M4 — degraded_reasons evaluator

**Goal:** Given a filled `Report`, produce the exact `(degraded, reasons[])` SPEC §degraded_reasons specifies.

Steps:
1. `internal/health/degraded.go` — one pure function: `func Evaluate(r Report) (bool, []string)`.
2. Thresholds are constants in the same file; SPEC's numbers are the defaults:
   - hard: `ollama_down`, `swap_over_75pct`, `vram_over_95pct`, `agent_stale`, `vram_service_creep_critical`
   - soft: `swap_over_50pct`, `vram_over_90pct`, `load_avg_over_2x_cores`, `ollama_runner_stuck`, `vram_service_creep_warn`
3. `degraded = true` iff any *hard* reason present. Soft reasons append to the list without flipping the boolean. Order preserved per SPEC.
4. `degraded_test.go` — one table row per reason, plus a "clean node" baseline, plus a "multiple concurrent reasons" case.

Exit criteria: 100% coverage on `degraded.go`; reviewer signs off that every SPEC string is represented exactly once.

**Why this is its own milestone:** this evaluator *is* the cross-repo contract with the case-manager. Getting it right before any agent ships means we don't have to version-pin the backend to a specific agent release.

### M5 — Apple Silicon path

**Goal:** `/health` works on an M-series Mac with `memory.unified = true` and a best-effort GPU entry.

Steps:
1. `internal/gpu/apple.go` (build tag `//go:build darwin && arm64`):
   - GPU name via `ioreg -l | grep -E "\"model\" = <\"Apple M"` (or `system_profiler SPDisplaysDataType -json`).
   - `vram_total_mb` = full system RAM (unified). `vram_used_mb` = best-effort from `system_profiler` or reported as 0 with a `note` field. Be honest in the JSON — don't fake numbers.
   - No per-process VRAM list on macOS (no public API); return empty `processes: []`.
2. `internal/mem/unified.go` — on darwin+arm64 sets `memory.unified = true`.
3. Intel Mac + eGPU: falls through to `nvidia_smi.go` if `nvidia-smi` is on PATH; otherwise no GPU section.

Exit criteria: `/health` on M-series returns `memory.unified: true`, GPU list has one entry with name populated, `processes` is `[]`, and `degraded_reasons` fires `vram_over_95pct` when a test allocation pushes unified memory past the threshold.

### M6 — Windows path

**Goal:** `/health` works on Windows with NVIDIA; runs in foreground first, service in M9.

Steps:
1. `nvidia-smi.exe` resolution: try PATH, then `C:\Program Files\NVIDIA Corporation\NVSMI\nvidia-smi.exe`, then `C:\Windows\System32\nvidia-smi.exe`. Cache the resolved path.
2. gopsutil on Windows already handles CPU/mem/swap correctly — just confirm.
3. Path handling in config loader: `%ProgramData%\rt-node-agent\config.yaml`.
4. No Ollama runner probing differences expected — Ollama on Windows exposes the same HTTP API.

Exit criteria: Windows 11 test box returns correct `/health`; `rt-node-agent.exe run` in an Admin PowerShell works. No service registration yet.

### M7 — Service allocators scrape

**Goal:** `service_allocators[]` populated; `vram_service_creep_*` fires on real cache-hoarding conditions.

Steps:
1. `internal/allocators/scraper.go`:
   - Background goroutine per configured service.
   - `scrape_interval_s` from config (default 30).
   - 1s HTTP timeout per scrape.
   - On failure: `scrape_ok: false`, previous values retained, failure not propagated to `degraded_reasons` (per SPEC §"scrape budget").
2. Shared store (simple `sync.Map` or a mutex-guarded struct) that the `/health` handler reads.
3. `degraded.go` gets updated to read `service_allocators[]` and apply the warn/critical thresholds from config.
4. Contract doc in `examples/config.yaml` showing the expected service response shape (`allocated_mb` / `reserved_mb` / `max_allocated_mb`).

Exit criteria: a mock HTTP server returning allocator JSON drives the scraper; a scenario test confirms `vram_service_creep_critical` fires when `reserved_mb/allocated_mb > 3.0 && reserved_mb > threshold_critical_mb`.

**Cross-reference:** SPEC.md mentions the 2026-04-22 gliner2-service incident as the motivating scenario. Name one test case `TestGliner2Incident_2026_04_22` and encode the observed numbers (16 GB reserved vs 2 GB real) so the regression is explicit.

### M8 — /actions/unload-model

**Goal:** Authenticated POST unloads a named model; idempotent.

Steps:
1. `internal/ollama/unload.go` — try `ollama stop <model>` first (Ollama 0.5+). If the binary isn't on PATH or returns unknown-subcommand, fall back to `POST /api/generate` with `{"model":"<m>","keep_alive":0}`.
2. Handler returns `{"status":"ok","unloaded":[...],"took_ms":N}`. Empty `unloaded` is still 200 if the model wasn't loaded (idempotent).
3. 401 on missing/bad token; 500 if Ollama unreachable.
4. Log every unload at INFO with requester IP (read-only for audit; no remote caller identity in v1).

Exit criteria: integration test with a live Ollama instance (in CI: skip if `OLLAMA_TEST_URL` env unset) unloads a small model and `/health.ollama.models` no longer lists it.

### M9 — Native service install (the hard milestone)

**Goal:** `rt-node-agent install` on any supported OS produces a running service that survives reboot. `rt-node-agent uninstall` reverses it cleanly.

Common design: `internal/service/service.go` defines an interface; one file per OS with a `//go:build` tag implements it. `main.go` dispatches to the active implementation.

```go
type Manager interface {
    Install(cfg InstallConfig) error
    Uninstall() error
    Status() (State, error)
    Start() error
    Stop() error
}
```

#### Linux — systemd

File: `/etc/systemd/system/rt-node-agent.service`

```ini
[Unit]
Description=RedTorch Node Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/rt-node-agent run
Restart=on-failure
RestartSec=5
User=rt-agent
Group=rt-agent
AmbientCapabilities=
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/log/rt-node-agent
EnvironmentFile=-/etc/rt-node-agent/env

[Install]
WantedBy=multi-user.target
```

Install flow:
1. Create `rt-agent` system user if missing (`useradd --system --no-create-home --shell /usr/sbin/nologin rt-agent`).
2. Create `/etc/rt-node-agent/` (0755, root), `config.yaml` (0644), `token` (0600, root:rt-agent). Skip overwriting existing files.
3. Write the unit file (0644).
4. `systemctl daemon-reload && systemctl enable --now rt-node-agent`.

Uninstall: `systemctl disable --now`, remove unit, remove binary. Leave `/etc/rt-node-agent/` in place (operator reinstalls should preserve token) unless `--purge` passed.

**Note on the user:** `rt-agent` running `nvidia-smi` is fine; it doesn't require root. If someone insists on root, document the override.

#### macOS — launchd

File: `/Library/LaunchDaemons/com.redtorch.rt-node-agent.plist`

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>               <string>com.redtorch.rt-node-agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/rt-node-agent</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key>            <true/>
  <key>KeepAlive</key>            <true/>
  <key>StandardOutPath</key>      <string>/var/log/rt-node-agent.log</string>
  <key>StandardErrorPath</key>    <string>/var/log/rt-node-agent.err</string>
  <key>UserName</key>             <string>_rt_agent</string>
</dict>
</plist>
```

Install flow:
1. Create `_rt_agent` system user (dscl; reserved `_`-prefix convention for system daemons on macOS).
2. Write plist (root:wheel, 0644).
3. `launchctl bootstrap system /Library/LaunchDaemons/com.redtorch.rt-node-agent.plist`.
4. `launchctl enable system/com.redtorch.rt-node-agent`.

Uninstall: `launchctl bootout system/com.redtorch.rt-node-agent`, remove plist + binary.

#### Windows — native service via x/sys/windows/svc

Use `golang.org/x/sys/windows/svc/mgr` to connect to the SCM and create the service:

```go
m, _ := mgr.Connect()
defer m.Disconnect()
s, _ := m.CreateService("rt-node-agent", exePath, mgr.Config{
    DisplayName: "RedTorch Node Agent",
    Description: "Load visibility + unload-on-demand for RedTorch dispatcher",
    StartType:   mgr.StartAutomatic,
}, "run")
defer s.Close()
s.Start()
```

`main.go` must detect that it was started by the SCM (via `svc.IsWindowsService()`) and call `svc.Run(name, &handler{})`; otherwise run as a normal process.

Install flow: must be invoked from an Admin PowerShell. `rt-node-agent.exe install` checks `windows.GetCurrentProcessToken()` elevation and errors cleanly if not elevated.

Uninstall: stop + `DeleteService`.

**Decision: hand-rolled vs `kardianos/service` library.** Hand-rolled wins for v1. `kardianos/service` would save ~150 lines total but adds a dep with its own opinions on paths and logging. Revisit if the three implementations start drifting in behavior.

Exit criteria: on each OS, `install` → reboot → `curl localhost:11435/health` succeeds without touching anything else. `uninstall` returns the machine to a state where no traces remain in `ps`, service manager, or firewall (we don't touch firewall in v1 — document that port 11435 may need to be opened).

### M10 — Bootstrap scripts

**Goal:** SPEC's `curl -fsSL https://<release-host>/install.sh | sh` pattern actually works.

`scripts/install.sh` (POSIX sh, Linux + macOS):
1. Detect OS (`uname -s`) and arch (`uname -m`, map `x86_64` → `amd64`, `aarch64`/`arm64` → `arm64`).
2. Fetch latest release tag from `https://api.github.com/repos/redtorchinc/node-agent/releases/latest`.
3. Download `rt-node-agent_<os>_<arch>` and its `.sig` (minisign) or bundle (cosign).
4. Verify signature using a pinned public key embedded in the script itself (see §7).
5. `install -m 0755 rt-node-agent /usr/local/bin/rt-node-agent`.
6. `sudo rt-node-agent install`.
7. `sudo rt-node-agent healthcheck` — exits non-zero if the just-installed service isn't healthy.

`scripts/install.ps1` (Windows):
1. Detect arch via `$env:PROCESSOR_ARCHITECTURE`.
2. Download `.exe` + signature from GitHub Releases.
3. Verify signature (minisign-win or cosign; pick at M11).
4. Place in `C:\Program Files\RedTorch\rt-node-agent.exe`.
5. Run installer elevated: `Start-Process -Verb RunAs rt-node-agent.exe install`.
6. Health check.

`scripts/uninstall.sh` + `uninstall.ps1` — thin wrappers around `rt-node-agent uninstall`.

Exit criteria: a fresh Ubuntu 22.04 VM, a fresh Sonoma Mac, and a fresh Windows 11 VM all go from nothing to healthy service via one command with no additional manual steps.

### M11 — CI + Releases

**Goal:** Tag push → signed binaries appear in GitHub Releases, install scripts download the right thing.

`.github/workflows/release.yml` on tag `v*`:
1. Matrix build: `{linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64}` with `CGO_ENABLED=0`.
2. `-ldflags "-s -w -X .../buildinfo.Version=$TAG -X .../buildinfo.GitSHA=$SHA -X .../buildinfo.BuildTime=$(date -u +%FT%TZ)"`.
3. Sign each artifact.
4. Publish to GitHub Releases with a consistent name format: `rt-node-agent_<os>_<arch>[.exe]`.
5. Generate `SHA256SUMS` file (+ signature).

**Signing decision (SPEC Open Question):** pick **minisign** for v1. Single static public key, no sigstore/OIDC plumbing, `minisign -V` is tiny enough to bundle verify-logic into the install scripts. Migrate to cosign later if supply-chain attestation becomes a requirement.

Exit criteria: tag `v0.1.0` → release appears with all 5 binaries + sums + sigs; `install.sh` against the tag succeeds end-to-end on a clean VM.

### M12 — README + examples

**Goal:** Operator-facing README that matches reality.

Outline (see §6 below for content plan).

---

## 5. Install-script behavioral contract

Both install scripts conform to the same contract so integrators (and the case-manager's bootstrap) can rely on it:

- **Idempotent.** Running `install.sh` twice does not reinstall or rotate tokens.
- **Quiet success, loud failure.** Exit 0 on success with a one-line "rt-node-agent v0.1.0 installed and healthy on port 11435." Any failure prints the failing command + stderr and exits non-zero.
- **No network calls after signature verification.** Once the binary is on disk and verified, the rest is local.
- **Does not generate a token.** Token setup is a separate operator step (documented in README §Configuration). Without a token, `/actions/*` returns 503; read endpoints still work, which is enough for the backend's ranking use case.
- **Non-interactive.** Never prompt. If something requires input, fail with a clear message.

---

## 6. README.md content plan (M12)

Sections, in order:

1. **What this is** — one paragraph: the case-manager backend reads `/health` to rank nodes for inference dispatch; this is the node-side agent that produces that data.
2. **Install** — one command per OS, verbatim.
3. **Verify** — `curl localhost:11435/health | jq .degraded_reasons`.
4. **Configuration** — `/etc/rt-node-agent/config.yaml` (macOS/Linux) vs `%ProgramData%\rt-node-agent\config.yaml`. Env var reference table. Token rotation steps.
5. **API reference** — link to SPEC.md §"HTTP API" as the canonical source; README summarizes.
6. **degraded_reasons quick reference** — hard/soft table.
7. **Uninstall**.
8. **Building from source** — `make build`, `make cross`, `make test`.
9. **Security model** — brief; link to SPEC §"Security model".
10. **Troubleshooting** — the three failures we expect to see most:
    - `nvidia-smi not on PATH`: where to install NVIDIA drivers.
    - `port 11435 not reachable from backend`: firewall pointer.
    - `ollama_down` fires but Ollama is running: non-default `OLLAMA_HOST` diagnostics.
11. **Reporting issues** — GitHub Issues link. *Explicitly* request that users redact internal hostnames/IPs from bug reports (this is a public repo).

**Do not include:** internal fleet topology, real node hostnames, case-manager URLs, training-data examples, customer names. Per CLAUDE.md §Public-repo hygiene.

---

## 7. Security & supply chain

- **No token in the binary.** Tokens live only in `/etc/rt-node-agent/token` (or env), mode 0600, owner `rt-agent:rt-agent` on Linux.
- **Signature verification on download.** Install scripts embed the minisign public key as a constant. Private key lives offline (1Password or equivalent; *not* GitHub Actions secrets).
- **Release workflow provenance.** `actions/attest-build-provenance` produces a SLSA attestation alongside the signature. Cheap, buys us supply-chain credibility.
- **Capabilities.** Linux service runs with `NoNewPrivileges=true`, `ProtectSystem=strict`. It does not need root; it needs execute on `nvidia-smi` and read on `/proc`.
- **Logging.** Never log tokens or request headers. The unload endpoint logs `{model, requester_ip, duration_ms}` only.

---

## 8. Testing strategy

| Layer | Approach | Runs where |
|---|---|---|
| Parsers (nvidia-smi CSV, ollama /api/ps JSON, config YAML) | Golden-file unit tests | CI on all three OSes |
| `degraded.Evaluate` | Table-driven, one row per SPEC reason | CI |
| Server handlers | `net/http/httptest` round-trips | CI |
| Service install (systemd/launchd/winsvc) | Smoke tests in GH Actions containers/VMs; `install` → `systemctl is-active` → `uninstall` | CI (Linux only; macOS + Windows smoke via `act`-style self-hosted runners or manual QA) |
| End-to-end | `install.sh` against a tagged release in a throwaway VM | Manual pre-release checklist |
| Real-node QA | Run dev build on one of the 10-cluster nodes for 24h, compare `/health` vs. dispatcher expectations | Before v0.1.0 tag |

CI runtime budget: keep the full `go test ./...` under 60s. Heavier tests gated behind `-tags integration`.

---

## 9. Versioning & release cadence

- Pre-1.0: `0.MAJOR.MINOR`. Any change to the `/health` JSON shape or `degraded_reasons` vocabulary bumps MAJOR.
- `v0.1.0` target: M0–M11 complete, one real cluster node running for 24h clean.
- `v0.2.0` target: service allocators + unload endpoint hardened against one week of real traffic.
- Post-1.0 stability commitment: additive JSON fields only; the backend's `NodeHealth` parser must never need a code change to upgrade.

---

## 10. Backend integration (reminder — lives in the private repo)

Tracked here for context only; all code lives in the case-manager repo per SPEC §"Backend integration":

1. `backend/app/services/node_health.py` — 2s timeout, 30s cache, URL derived from ollama URL by port swap.
2. `ollama_service.rank_nodes()` gains async variant consulting agent.
3. `agent_required: bool` feature flag, default false for gradual rollout.

When implementing, confirm with case-manager owners that no extra fields are needed; adding fields is cheap, but coordinating removal is not.

---

## 11. Dependency decisions we're locking in now

| Concern | Choice | Rationale |
|---|---|---|
| Config format | YAML (`gopkg.in/yaml.v3`) | SPEC already shows YAML; operators expect it for service configs |
| GPU probe | Shell to `nvidia-smi` | SPEC explicitly avoids NVML bindings; debuggable, no CGO |
| Service registration | Hand-rolled per OS | Two small files beats a dep |
| Signing | minisign | Simpler than cosign; no OIDC; trivially verifiable in POSIX sh |
| Logging | stdlib `log/slog` (Go 1.21+) | No dep, structured, plays well with journalctl |
| HTTP mux | stdlib `http.ServeMux` (Go 1.22 patterns) | No router needed |

Any deviation requires a note in this file explaining why.

---

## 12. Risk register

| Risk | Likelihood | Mitigation |
|---|---|---|
| `nvidia-smi` CSV format changes between driver versions | Low | Pin parser to column *names* not positions; test against ≥3 driver versions including DGX |
| gopsutil returns wrong values on Windows or Apple Silicon | Medium | Smoke-test on real hardware in M5/M6; fall back to native calls if needed |
| Ollama API drift (0.5 → 0.6) on `/api/ps` or `ollama stop` | Medium | Integration test with matrix of Ollama versions in CI; pin a minimum version in README |
| Windows service install requires elevation UX users don't expect | High | Clear error message + link to install.ps1 which handles elevation via `Start-Process -Verb RunAs` |
| Token committed to repo by mistake | Medium | `.gitignore` covers `token`, `*.token`, `.env*`; reinforce in CLAUDE.md; install.sh never writes tokens |
| Signature key leak | Low, catastrophic | Offline storage, rotate process documented in SECURITY.md (added at M12) |
| Apple Silicon unified-memory VRAM numbers misleading | High | Be explicit: `vram_total_mb = system RAM`, `unified: true`; document that `vram_over_90pct` on Apple means RAM pressure |

---

## 13. Open questions tracked from SPEC

Copied from SPEC §"Open questions"; resolutions recorded here as they're made.

- **Binary signing: cosign vs minisign.** → **Resolved:** minisign for v1 (§11). Revisit if SLSA L3 becomes a requirement.
- **Release hosting: GitHub Releases only or separate CDN.** → **Resolved:** GitHub Releases only for v1. `install.sh` can switch to a CDN later via a single constant.
- **Homebrew tap for macOS.** → Deferred to post-v0.2.0. Curl install works; tap is a convenience.
- **DGX Grace Hopper arm64 `nvidia-smi` compatibility.** → **Open, blocking M11.** Need a recorded CSV sample from a Grace Hopper box before tagging v0.1.0. Add as an M1 test fixture.
- **Service allocator scrape contract: fixed shape vs adapter-based.** → **Resolved:** fixed shape (`allocated_mb` / `reserved_mb` / `max_allocated_mb`) for v1. Adapter model deferred until a second service type actually needs it (torchserve, ray-serve, vllm).

New questions identified during planning:

- **What user should the service run as on Linux — dedicated `rt-agent` or the invoking user?** → Lean toward dedicated system user; confirm with fleet operator before M9.
- **Should `/health` expose Go runtime metrics (goroutines, GC) for the agent itself?** → Defer. Not in SPEC; add only if operational debugging demands it.
- **Unload endpoint: return immediately or wait for Ollama to confirm?** → Wait, with a 10s timeout. Matches SPEC's `took_ms` field.

---

## 14. What this plan deliberately does *not* do

To avoid scope creep and stay aligned with SPEC §"Non-goals for v1":

- Push model (agent → backend). Pull-only.
- Persistent metrics storage on the node.
- Log shipping.
- Auto-triggered model unloading (agent only reports).
- Multi-tenant auth, API key per caller.
- Web UI.
- Training job coordination.
- TLS / mTLS (v2 only — see SPEC §"Security model").

If any of these becomes a requirement, open a new SPEC revision; do not graft onto v1.
