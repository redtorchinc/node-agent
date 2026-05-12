# PLAN.md — historical v0.1.0 build plan

**Status:** v0.1.0 shipped. All milestones M0–M12 below are complete. This
file is kept for historical context — the decisions made during the v0.1
build still apply (single static binary, no CGO, stdlib router, etc.). For
current v0.2.0 work, see [V0_2_0_PLAN.md](V0_2_0_PLAN.md). For the
file-by-file project map, see [ARCHITECTURE.md](ARCHITECTURE.md).

---

## 1. Guiding principles (still in force)

1. **Single static binary per OS/arch.** No CGO, no runtime deps on the host. `go build` with `CGO_ENABLED=0`.
2. **Stdlib-first.** Only three external imports — `gopsutil/v3`, `golang.org/x/sys`, `gopkg.in/yaml.v3`. Anything else needs a note in this file.
3. **Public repo.** Every file is world-readable forever — see [CLAUDE.md](CLAUDE.md) §Public-repo hygiene.
4. **Contract stability.** The `/health` JSON shape and the `degraded_reasons` vocabulary are cross-repo contracts with the case-manager backend. Additive changes are fine; renames and removals are breaking.
5. **Ship thin vertical slices.** Each milestone produces a working, testable artifact.
6. **No premature abstraction.** "Three similar lines is better than a premature abstraction." GPU probes look similar but differ enough that forcing a perfect interface early would hurt; they live as per-OS files behind build tags.

## 2. v0.1.0 milestone roadmap (complete)

| # | Milestone | Status |
|---|---|---|
| M0 | Scaffold (`go mod`, `main.go`, CI matrix) | ✅ shipped |
| M1 | Linux NVIDIA `/health` | ✅ shipped |
| M2 | Config + Bearer auth | ✅ shipped |
| M3 | Ollama `/api/ps` integration | ✅ shipped |
| M4 | `degraded_reasons` pure evaluator + tests | ✅ shipped |
| M5 | Apple Silicon `/health` path | ✅ shipped |
| M6 | Windows `/health` path | ✅ shipped |
| M7 | Service allocators scrape (gliner2-service contract) | ✅ shipped |
| M8 | `POST /actions/unload-model` | ✅ shipped |
| M9 | Native service install (systemd / launchd / Windows SCM) | ✅ shipped |
| M10 | `install.sh` + `install.ps1` bootstrap scripts | ✅ shipped |
| M11 | CI + cross-compile release pipeline (5 targets) | ✅ shipped |
| M12 | README + examples | ✅ shipped |

The original detailed plan for each milestone (file paths, exit criteria,
risks) is preserved in git history; see commits up to and including
[`eed5a96`](https://github.com/redtorchinc/node-agent/commit/eed5a96)
("ollama_runner_stuck: gate on real queue depth (fixes #1)").

## 3. v0.2.0 plan

See [V0_2_0_PLAN.md](V0_2_0_PLAN.md). Status: shipped. New surface added:

- Versioned config schema + `rt-node-agent config migrate` (comment-preserving YAML upgrade)
- `internal/platforms/` package: Ollama adapter + vLLM probe with Prometheus metrics scraping
- Allowlisted `POST /actions/service` for systemd unit control (Linux), with sudoers drop-in scoped to `rt-vllm-*.service`
- Extended `/health` surface: CPU usage/temps/freq/throttle, full GPU profile (NVLink, MIG, ECC, throttle reasons), disk, network, time sync
- `GET /capabilities` for dispatcher feature-detection
- Phase B (training plane): RDMA fabric monitoring, `mode` state machine, `POST /actions/training-mode`, allocator `only_when_mode`, opaque pass-through training metrics
- 14 new degraded reasons; 9 new Prometheus metric series

## 4. Open questions tracked from SPEC

Copied from [spec/SPEC.md](spec/SPEC.md) §"Open questions"; resolutions
recorded here.

- **Binary signing: cosign vs minisign.** → **Resolved:** minisign for v1.
- **Release hosting.** → **Resolved:** GitHub Releases only.
- **Homebrew tap for macOS.** → Deferred post-v0.2.0.
- **DGX Grace Hopper arm64 `nvidia-smi` compatibility.** → **Resolved:** golden-file CSV fixture from a GH200 lives in `internal/gpu/nvidia_smi_test.go` (`TestParseNvidiaSMI_GraceHopper`).
- **Service allocator scrape contract.** → **Resolved:** canonical three fields + opaque pass-through via `Scraped.Extra` (v0.2.0 added).
- **Linux service user.** → **Resolved:** dedicated `rt-agent` system user.
- **Unload endpoint blocking semantics.** → **Resolved:** wait for confirmation, 10s timeout, surfaces `took_ms`.

## 5. What this plan deliberately does *not* do

Carrying SPEC §"Non-goals for v1" forward into v0.2:

- Push model (agent → backend). Pull-only.
- Persistent metrics storage on the node. (Training-mode state file is the *only* on-node persistence, deliberately a single small JSON.)
- Log shipping.
- Auto-triggered model unloading (agent reports; backend decides).
- Multi-tenant auth, API key per caller.
- Web UI.
- TLS / mTLS (v3 if non-air-gapped deployment becomes a requirement).

If any of these becomes a requirement, open a new SPEC revision; do not
graft onto v1/v2.
