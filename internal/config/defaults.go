package config

// SchemaVersion is the current config.yaml schema version. Bump when adding
// new top-level keys so `rt-node-agent config migrate` can surface them on
// upgrade. v1 = v0.1.x (key absent). v2 = v0.2.0 (platforms, services, rdma,
// training_mode, disk, network).
const SchemaVersion = 2

// DefaultYAML is the canonical example config for v0.2.0. It is the source
// the migrator compares against to detect missing keys on upgrade, and the
// template `rt-node-agent install` drops at config.yaml.example.
//
// Single source of truth: examples/config.yaml is generated from this
// constant by `make sync-example`. Don't edit them out of sync.
const DefaultYAML = `# rt-node-agent config — copy to /etc/rt-node-agent/config.yaml
# (or %ProgramData%\rt-node-agent\config.yaml on Windows) and edit.
#
# See SPEC.md and docs/config.md for the full reference.
# Every field is optional — defaults match SPEC.md §HTTP API.

# Schema version. Used by ` + "`rt-node-agent config migrate`" + ` to detect new keys
# on upgrade. Do not edit by hand.
config_version: 2

# HTTP listener.
port: 11435
bind: 0.0.0.0

# Bearer token for POST /actions/*. The installer auto-generates one at
# /etc/rt-node-agent/token (Linux/macOS) or %ProgramData%\rt-node-agent\token
# (Windows). Prefer token_file over inlining.
# token: "inline-only-for-dev"
token_file: /etc/rt-node-agent/token

# Expose /metrics in Prometheus text format. Off by default.
metrics_enabled: false

# Inference platforms. ` + "`enabled: auto`" + ` probes once at startup and keeps
# checking on every /health request if reachable. Set to ` + "`false`" + ` to skip
# a platform entirely on nodes that don't run it.
platforms:
  ollama:
    enabled: auto
    endpoint: http://localhost:11434
  vllm:
    enabled: auto
    endpoint: http://localhost:8000
    metrics_endpoint: http://localhost:8000/metrics
    # Set true on nodes that REQUIRE vLLM — then vllm_down becomes hard.
    required: false

# Legacy Ollama endpoint (kept for v0.1.x compatibility; new configs should
# set platforms.ollama.endpoint above). Removed in v0.3.0.
# ollama_endpoint: http://localhost:11434

# Disk paths to surface under /health.disk[]. Defaults: /, /var/lib/ollama,
# /var/lib/rt-node-agent. Auto-discovery also picks up any mount with
# >= 50GB total (capped at 10 entries to bound payload).
# disk:
#   paths:
#     - /
#     - /var/lib/ollama
#     - /mnt/models

# Remote service control (typically vLLM model units on DGX). Bearer-token
# gated. ALLOWLISTED unit names ONLY — agent shells systemctl <action> <unit>
# for unit in this list. No unit creation, no arbitrary args. The sudoers
# drop-in installed alongside this agent restricts the unit name pattern to
# rt-vllm-*.service — operators MUST name their units accordingly.
# services:
#   manager: systemd
#   allowed:
#     - name: rt-vllm-qwen3.service
#       actions: [start, stop, restart, status]
#       description: "vLLM serving qwen3-vl:32b"
#     - name: rt-vllm-llama-70b.service
#       actions: [start, restart, status]

# Scrape cooperating Python services that expose PyTorch allocator JSON.
# See SPEC.md §"Service allocator scraping". Both thresholds must be
# exceeded for the corresponding degraded reason to fire:
#   reserved/allocated > 3.0 AND reserved_mb > threshold_critical_mb → vram_service_creep_critical
#   reserved/allocated > 2.0 AND reserved_mb > threshold_warn_mb     → vram_service_creep_warn
service_allocators:
  - name: gliner2-service
    url: http://localhost:8077/v1/debug/gpu
    threshold_warn_mb: 4096
    threshold_critical_mb: 10240
    scrape_interval_s: 30
    # only_when_mode: training_mode    # skip scrape unless mode matches

# Training-mode state (Phase B). Persists training-mode across agent restarts
# so a crash doesn't leak inference back into a node mid-training.
# training_mode:
#   state_file: /var/lib/rt-node-agent/training_mode.json
#   grace_period_s: 3600       # auto-exit if expected_duration + grace exceeded
#   disable_ollama_probe: true # silence ollama_down during legitimate drain

# RDMA fabric monitoring (Linux only; auto-detected from /sys/class/infiniband).
# rdma:
#   enabled: auto
#   collect_interval_s: 5
#   pfc_storm_threshold_rx_rate: 1000
#   pfc_storm_window_s: 30
#   errors_growing_window_s: 60
`
