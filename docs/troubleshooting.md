# Troubleshooting

If the install one-liner didn't produce a working service, that's a bug
worth filing. Common runtime issues below.

## Service fails to start: "did not find expected key" / YAML parse error

Your `/etc/rt-node-agent/config.yaml` doesn't parse as YAML — usually a
missing colon, bad indentation, or a v0.1.x example that drifted from
spec. The agent refuses to start rather than silently falling back to
defaults (a surprise port/token change would be worse than a clear
failure).

Recover with one command:

```sh
sudo rt-node-agent config migrate-force
sudo systemctl restart rt-node-agent
```

That backs up the broken file to `/etc/rt-node-agent/config.yaml.broken-<unix-ts>`
and writes a fresh defaults file. The token at `/etc/rt-node-agent/token`
is untouched. Copy any customised values out of the `.broken-` file into
the new config and restart.

The installer auto-runs this same path when it detects malformed YAML —
this manual command is for the case where the service was running on a
broken config that you later edited.

## "token not configured" (503) on /actions/*

The token file is missing. On Linux, check `/etc/rt-node-agent/token` is
present, mode 0640, owned `root:rt-agent`. The installer auto-creates it
on first install; a manual chmod that broke read perms is the usual
cause. Re-run `sudo rt-node-agent install` to repair without rotating.

## POST /actions/service returns 500 "permission denied"

The sudoers drop-in didn't land or didn't apply. Check:

```
sudo ls -la /etc/sudoers.d/rt-node-agent
sudo visudo -cf /etc/sudoers.d/rt-node-agent
sudo -u rt-agent sudo -n -l
```

The third command should list the permitted `systemctl` commands. If it
prompts for a password, the drop-in isn't being read — usually because
the file's perms are wrong (must be `0440`) or there's a syntax error in
some *other* file under `/etc/sudoers.d/` that blanket-disables the
include.

Reinstalling the agent re-validates and re-writes the drop-in.

## /health is slow (>2s)

Most likely a probe is hung. Check:

- `nvidia-smi` is hanging (driver issue) — kill stuck `nvidia-smi`
  processes; the agent's 2s inner timeout will skip it on the next call.
- `ollama` is reachable but slow to respond — same 2s timeout.
- `system_profiler` on macOS is slow — already cached 5s, so first call
  after install is the worst.

Run `rt-node-agent healthcheck` from the shell to see the same payload
without HTTP indirection.

## /metrics shows no models

`metrics_enabled: false` by default. Set to `true` in config (or
`RT_AGENT_METRICS=1`) and restart. The `/health` JSON always works.

## macOS: Application Firewall blocking incoming

The installer auto-adds the binary to the Application Firewall allow-list
on Linux. If you skipped install and just dropped the binary in place:

```
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --add /usr/local/bin/rt-node-agent
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --unblockapp /usr/local/bin/rt-node-agent
```

## Pre-existing config didn't pick up new keys after upgrade

Since v0.2.7 the migration is in place: the installer moves your old
`config.yaml` to `config.yaml.bak`, writes the new schema's defaults to
`config.yaml`, and grafts every top-level value you had set onto the new
file. Edit `config.yaml` directly to enable new features.

If the migration appears to have done nothing:

- Check `/etc/rt-node-agent/config.yaml.bak` — does it match the old
  content? If yes, the migration ran but found nothing to add (already
  current). If absent, the migration short-circuited because the live
  file already matches the new schema.
- Force a re-migration: `sudo rt-node-agent config migrate`. This runs
  the same logic the installer does.
- See the previous version: `diff /etc/rt-node-agent/config.yaml.bak /etc/rt-node-agent/config.yaml`.

## RDMA block missing on a DGX

Check `/sys/class/infiniband/` is non-empty:

```
ls /sys/class/infiniband
```

If empty, the kernel modules aren't loaded (typically `mlx5_ib`). The
agent doesn't `modprobe` — that's a host-config task.

## vLLM-only node is hard-degraded with `ollama_down`

The default config probes both Ollama and vLLM. On a node that intentionally
runs only vLLM (e.g. GB10 / DGX Spark), the Ollama probe fails truthfully —
but the agent fires the hard `ollama_down` reason and the case-manager
ranker hard-skips the box.

Fix: tell the agent the node is vLLM-only in `/etc/rt-node-agent/config.yaml`:

```yaml
platforms:
  ollama:
    enabled: false
  vllm:
    enabled: auto
    endpoint: http://localhost:8000
```

After `sudo systemctl restart rt-node-agent`, `platforms.ollama.up: false`
keeps being reported truthfully, but `ollama_down` / `agent_stale` /
`ollama_runner_stuck` are suppressed in `degraded_reasons` so the ranker
can dispatch to the node again.

Distinct from `training_mode.disable_ollama_probe: true`, which stops
probing entirely for the duration of a training drain.

## GB10 / DGX Spark reports `vram_total_mb: 0`

GB10 is a unified-memory NVIDIA part — `nvidia-smi` reports `memory.total`
as `[N/A]` because there is no discrete VRAM pool. The agent detects this
at parse time, flags `gpus[0].vram_unified: true`, and back-fills
`vram_total_mb` from `memory.total_mb` plus `vram_used_mb` from
`nvidia-smi --query-compute-apps`. After v0.2.2 this is automatic — no
config required.

If you still see `vram_total_mb: 0` on GB10:

- Confirm `agent_version` >= `0.2.2` (`curl -s http://localhost:11435/version`).
- Confirm `nvidia-smi` is on the agent's PATH and reports the GB10 GPU.
- If `nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits`
  returns a non-`[N/A]` value, the detection heuristic doesn't apply on
  this driver/firmware combination — file an issue with the raw output.

## RDMA shows `rdma_peermem_missing`

`nvidia_peermem` isn't loaded. For GPUDirect RDMA over RoCE you need:

```
sudo modprobe nvidia_peermem
```

Persist it across reboots in `/etc/modules-load.d/nvidia_peermem.conf`.
