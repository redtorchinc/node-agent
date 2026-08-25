# CLAUDE.md

Guidance for Claude Code working in this repository. New agents — orient
yourself via [ARCHITECTURE.md](ARCHITECTURE.md) before editing anything.

## Status

**v0.2.x shipped.** The agent is feature-complete for the inference plane
+ allowlisted service control + training-mode coordination + RDMA fabric
visibility. v0.2.2 added unified-memory NVIDIA (GB10 / Grace-Blackwell)
detection, the `platforms.ollama.enabled: false` vLLM-only opt-out, and
self-describing `probe_interval_s` / `stale` on `platforms.*`. v0.2.3
adds raw swap counters + PSI gauges, `top_swap_processes[]`,
auto-detected `databases[]` (20 fingerprints), and auto-detected
`storage[]` (ZFS / NFS / CIFS / Ceph / GlusterFS / Lustre). v0.2.7
ships in-place config migration (config.yaml.bak), and v0.2.8 fixes
darwin /health latency (the 5s DNS dead-weight removed), populates
darwin cpu.vendor / cpu.usage_pct / memory.pressure natively, makes
vllm_down opt-in (no longer fires under `auto`), and splits the
degraded boolean into `degraded_hard` / `degraded_soft`. v0.2.11 adds
`powermetrics`-driven CPU / GPU die temps on Apple Silicon (Mac
Studio, M1/M2/M3) and cross-node time alignment: high-precision
`time_sync.now_unix_ns` for backend-side offset comparison plus an
optional agent-driven NTP probe (default `time.cloudflare.com`,
opt-out via `timesync.server: ""`) with new soft degraded reason
`clock_offset_high`. All v0.2.x additions are additive; v0.2.11 is
the first to introduce a config knob (handled gracefully by the
existing migrator's missing-top-key detection). v0.2.12 maps the
vLLM metric names that ≥0.6 renamed (`kv_cache_usage_perc`,
`prefix_cache_hits_total` / `prefix_cache_queries_total`,
`request_time_per_output_token_seconds`) with backward-compat
fallback to the legacy names, so `kv_cache` and `tpot` populate
again on current GB10 nodes. No wire-contract or config change.
v0.2.13 adds eval telemetry (additive, vLLM ≥0.6 only — nil
otherwise): raw prefix-cache hit/query counts behind the rate,
PREFILL/DECODE phase-time percentiles (`latency_ms.prefill_*` /
`decode_*`), `counters.prompt_tokens_cached_total`, and wires the
declared-but-never-set `requests_failed_total` (finished_reason
abort+error) — also fixing `requests_success_total` to sum across
all finished_reason series instead of first-match (which silently
reported only the `stop` series). v0.2.14 adds the `GET /time`
endpoint — an NTP-style four-timestamp handshake so the backend can
measure caller↔node clock offset (Offset B) to sub-ms, complementing
the node↔reference offset (Offset A) from the existing
`timesync.server` probe (now also folded into `/time`). The
`clock_offset_high` threshold becomes configurable
(`timesync.offset_degraded_ms`, default 100; `0` disables) for
measure-only fleets whose clocks intentionally free-run. New
capability flag `time_handshake_supported`; additive wire change.
v0.2.15 stops `clock_offset_high` firing off a stale reading: the
probe retains the last successful `offset_ms` across failures (by
design, for the wire), but the degraded reason now stays silent
whenever the most recent probe attempt failed (`server.error` set) —
an egress-less Mac Studio kept flagging a -488ms fossil against the
unreachable default `time.cloudflare.com` while its OS clock was
disciplined by an internal NTP server. Also darwin `probeOSSync` now
queries the **configured** `timesync.server` (falling back to
`time.apple.com` only when unset) and parses the sntp offset into
`skew_ms` instead of dropping it. **v0.3.0** ships the network flow
ownership surface (issue #21): Bearer-gated
`GET /network/{sockets,flows,resolve}` mapping gateway NetFlow tuples
to local pid/process/user/systemd-unit owners via `internal/netown`
(gopsutil socket table + `/proc/<pid>/cgroup` parse; ownership only —
no byte counters, which would need netlink inet_diag). Cmdlines are
secret-redacted before the 240-byte cap; responses carry
`training_run_id` for backend temporal joins (deliberately no
per-socket workflow attribution — the agent can't know case-manager
workflow identity). New top-level `network:` config key (surfaced by
the migrator's missing-top-key detection) and capability flag
`network_flows_supported`. Contract: docs/api/network-flows.md.
v0.3.1 makes Linux attribution actually work under the default install
(issue #23): the systemd unit gains
`AmbientCapabilities=CAP_SYS_PTRACE CAP_DAC_READ_SEARCH` whenever the
effective config has `network.flows_enabled` ≠ false (rendered at
install time — flipping the key needs `sudo rt-node-agent install`,
not just a restart), and drops `NoNewPrivileges=true`, which had been
silently breaking the sudo → systemctl path of `POST /actions/service`
since v0.2.0 (setuid blocked). `CapabilityBoundingSet` is deliberately
NOT set — it would strip the sudo'd systemctl of root caps; see the
unitTemplate comment in internal/service/unit_systemd.go. The
`partial: true` warning on `/network/*` now names the missing caps and
the fix when the agent detects the gap (`internal/netown/caps_linux.go`
parses CapEff from /proc/self/status) — warnings[] text is freeform and
NOT part of the degraded_reasons contract. Two more fixes shaken out by
on-node verification: `install()` now does `systemctl enable` +
`restart` instead of `enable --now` (which is a no-op on an active
service — upgrades had kept the OLD binary and unit running until a
manual restart), and kernel-owned sockets (`time_wait` / `syn_recv`,
which no process holds — even root sees pid 0) no longer count toward
`partial`, which connection churn had been pinning permanently true.
No wire or config-schema change.
v0.3.2 closes the macOS ownership gap for short-lived pending connects
(egress-blocked fleet: outbound connects churn through SYN_SENT and
were gone or unattributed by sentinel poll time): the darwin sampler
merges `netstat -anv -p tcp` into the lsof snapshot (`source:
"lsof+netstat"`, informational wire-value change) and RawConn gains a
sampler-provided ProcessName fallback used when the owning process
exits before gopsutil enrichment. Linux needed no code change — procfs
attributes all states including SYN_SENT (verified on arm64 GB10-class
and x86_64 Ubuntu nodes, cross-user, with closed sockets retained in
/network/flows). Also pinned `toolchain go1.25.12` in go.mod for
GO-2026-5856. The
remaining sub-poll-interval capture gap is issue #27 (event-driven
sources — EndpointSecurity / eBPF — deliberately deferred).
**Toolchain pins are now unified at go1.25.14** — go.mod `toolchain`
*and* all five workflow `go-version` pins move together. They had
drifted (go.mod on 1.25.12, workflows on 1.25.11), which is why the
daily `security` job went red 2026-08-19 → 08-25 on five stdlib
advisories fixed in go1.25.13 (GO-2026-6218 net/url, GO-2026-6090
crypto/tls, GO-2026-6089 + GO-2026-5026 net/http, GO-2026-5972
encoding/asn1). Keep them in lockstep: bumping go.mod alone does not
green the CI job, and bumping the workflows alone does not change what
the release binary is built with.
v0.3.3 fixes `POST /actions/service` being broken for *every* unit and
action since v0.3.0 (issue #28) — a sudoers argv mismatch, not a
permissions problem. The agent invoked `sudo -n /bin/systemctl
--no-pager --no-ask-password <verb> <unit>` while the drop-in granted
flagless specs (`/bin/systemctl start rt-vllm-*.service`); **sudo
matches command specs argument-by-argument**, so the two flags ahead of
the verb meant no spec matched, sudo fell through to requiring a
password, and `-n` made that a hard failure. Note this was masked
until v0.3.1 removed `NoNewPrivileges=true`, which had been failing the
same path earlier for a different reason. Fixes: the sudo branch of
`systemctlArgv` is now flagless (the flags only matter on a TTY and
output is always captured to a buffer); `status` and `show` moved off
sudo entirely since systemd serves state queries unprivileged — which
also retired two drop-in specs that never matched anyway (`show`
appends `--property=A,B,C` after the unit, and those commas are spec
separators in sudoers); the drop-in is trimmed to
`{start,stop,restart}` × `{/bin,/usr/bin}`; and the systemctl path is
resolved from a *closed* two-entry list so it can never diverge from
what the drop-in grants. `ErrSudoDenied` now makes this failure
self-describing — the 500 body names the drop-in instead of passing
through a bare `sudo: a password is required`. It is deliberately NOT
given its own status code in `mapServiceErr`: that would be a wire
change (V0_2_0_PLAN.md §A3). **The argv↔drop-in coupling is
load-bearing and now pinned from both sides** by tests in
`internal/services/systemd_linux_test.go` and
`internal/service/sudoers_linux_test.go` — never add a flag to the sudo
branch without adding it to the drop-in. Picking this up on a node
requires re-running `sudo rt-node-agent install` (rewrites the drop-in),
not just a binary swap.
**v0.3.3** adds `/health.ray` (issue #30) — this node's Ray cluster role,
so the backend can group an N-way tensor-parallel serving group. The
motivating case: a model served across 8× GB10 behind a Ray head, where
only the head has an OpenAI endpoint, so the seven workers looked like
idle nodes with no inference platform and were rendered offline.
**`ray.gcs_address` is the grouping key** — every member reports the same
one and the `role: "head"` member holds the addressable endpoint. The
block is omitted entirely when Ray isn't running (same contract as
`rdma`), so presence means "in a cluster". Detection reads the raylet's
own command line (`--gcs-address`, `--session-name`,
`--static_resource_list`, socket paths for the session dir) — there is
deliberately **no `ray status` shell-out**, which is a Python CLI costing
seconds, and v0.2.8 already had to strip a 5s dead-weight out of darwin
`/health`. Ray mixes `-` and `_` flag spellings across versions so both
are accepted. `alive_nodes` is the one network call (Ray's dashboard,
head-only, TTL-cached behind the keep-warm ticker) and is `int | null`
with an **explicit null** when unknown — a missing key would read as `0`,
and "empty cluster" is a very different claim from "dashboard
unreachable". It is a convenience only: counting fleet rows that share a
`gcs_address` is more reliable and doesn't depend on Ray's internal
dashboard API, whose shape has moved across releases. **No new
degraded_reasons value** — being a TP worker is topology, not health, and
`TestRayDoesNotAddDegradedReason` pins that. New top-level `ray:` config
key (surfaced by the migrator's missing-top-key detection) and capability
flag `ray_supported`, which reports whether the *probe ran*: a missing
block only means "not in a cluster" when that flag is true. Contract:
docs/api/ray.md.
**v0.3.4** is a release-integrity + hygiene pass, no wire change.
Release signing had **never run**: the sign step was gated on
`if: ${{ env.MINISIGN_KEY != '' }}` with the secret set in that same
step's `env:` block, and a step's own `env:` is not in scope for its own
`if:` — so it read undefined, was always false, and skipped silently on
every release (`secrets` is not available in a step-level `if:` either,
so testing the secret there would also have failed). It now resolves to
a step output and **logs the decision**, so an unsigned release is
visible instead of silent. The important half was second, though:
`scripts/install.sh` pins a **placeholder** pubkey and a verification
failure takes the `err()` path, which aborts the install — so fixing the
workflow condition alone would have meant that the moment a
`MINISIGN_KEY` was added, `curl | sh` hard-failed on every host with
minisign installed. A fleet-wide install outage caused by enabling
security. The placeholder is now detected by prefix and never handed to
minisign; policy is deliberately asymmetric — **fail open when signing
isn't set up, fail closed when it is set up and doesn't verify**, and a
hard error on an empty `PUBKEY` (a broken installer, not a signing
state). Signing is still OFF: no keypair exists, and generating one is
deliberately out of scope for an agent — it belongs on a trusted machine
outside CI, and public-repo hygiene forbids a signing key entering this
repo. New **docs/releasing.md** carries the release procedure, the exact
steps to enable signing (pubkey commit must land *with* the secrets, or
there's a window where signatures publish but can't be verified), the
known fail-open downgrade window, and a pre-release checklist. Also:
CI now gates on `gofmt` (its own Linux-only job — `gofmt -l` exits 0
even when it lists files, so the gate keys off whether the list is
empty), and the 14 files that had drifted are formatted. Checked
specifically that the realignment did not detach any `#nosec <rule> --
reason` comment from its line: gosec stays at 0 issues with 24/23/14
suppressions.
Note: the deprecated legacy `ollama_endpoint` key was NOT removed in
v0.3.0 despite older comments promising that — removal stays deferred
so v0.1.x configs keep loading. (`examples/config.yaml` used to claim it
*was* removed; corrected in v0.3.3, and the example is now byte-identical
to `config.DefaultYAML`.)
[spec/SPEC.md](spec/SPEC.md) is the authoritative wire contract (any
change there is a cross-repo break). [spec/V0_2_0_PLAN.md](spec/V0_2_0_PLAN.md)
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
internal/safecast/            # Saturating uint64↔int64 casts (host metrics → wire)
internal/{gpu,mem,ollama,allocators,service,buildinfo}/  # v0.1 modules
```

Two small helpers exist to keep a whole class of finding from coming back —
prefer them over hand-rolling at a new call site:

- `internal/safecast` — host metrics arrive as `uint64`, the wire contract is
  `int64`. `BytesToMB` / `I64` / `U64` saturate instead of wrapping, so an
  out-of-range value can't reach `/health` as a negative and mislead the
  ranker.
- `internal/server/logsafe.go` — `logSafe` / `logSafeSlice` on any
  request-derived string before it reaches `slog`.

## Static analysis

`gosec` and CodeQL both report into the Security tab; neither blocks CI
(`gosec -no-fail`). The gosec job is **matrixed over GOOS**
(linux/darwin/windows) — Go build tags mean a single-target scan is a
single-platform scan, and this job was ubuntu-only until 2026-08, which
hid every finding in `launchd.go`, `winsvc.go`, and the `*_darwin.go`
files. Each target uploads under its own SARIF category; they would
otherwise overwrite each other.

**Never rename an existing SARIF category.** GitHub only marks a
code-scanning alert fixed when a scan *under the same category* stops
reporting it. Renaming orphans every alert filed under the old name —
they stay open forever with no job able to close them. That is why the
linux target keeps the bare `gosec` category while the new targets get
`gosec-darwin` / `gosec-windows`: adding a category is safe, renaming one
is not. (Learned the hard way — the matrix change initially renamed all
three and left 41 unclosable alerts behind.)

Suppressions carry `#nosec <rule> -- <reason>`. **Treat a bare `#nosec`
with no stated reason as untriaged**, not as reviewed. Two suppressions
are load-bearing rather than cosmetic and should not be "cleaned up":

- `internal/netown/netown.go` (G401/G505) — SHA-1 in `flowID` is a
  content-addressed dedup key, not a security primitive, and
  `docs/api/network-flows.md` specifies `flow_id` as a *stable SHA-1*
  including the `sha1:` prefix in its sample. Changing the algorithm
  breaks any backend joining on that value — a cross-repo contract
  change. The prefix is what makes a future migration expressible.
- the `0o644` / `0o640` / `0o755` write and mkdir modes (G301/G302/G306) —
  each is the mode the consuming OS component *requires*. A systemd unit
  must be world-readable or systemd won't load it; `/etc/sudoers.d` must
  be `0755` or sudo ignores the drop-in; the token file is `0640
  root:rt-agent` precisely so the non-root agent can read it, and `0600`
  would lock it out.

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
