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

## Linux: pre-existing config not getting new keys after upgrade

By design — the installer never overwrites your config. It drops a
`config.yaml.new` next to it. Review the diff and `mv` when you're ready.
You can run the migration manually:

```
sudo rt-node-agent config migrate
```

## RDMA block missing on a DGX

Check `/sys/class/infiniband/` is non-empty:

```
ls /sys/class/infiniband
```

If empty, the kernel modules aren't loaded (typically `mlx5_ib`). The
agent doesn't `modprobe` — that's a host-config task.

## RDMA shows `rdma_peermem_missing`

`nvidia_peermem` isn't loaded. For GPUDirect RDMA over RoCE you need:

```
sudo modprobe nvidia_peermem
```

Persist it across reboots in `/etc/modules-load.d/nvidia_peermem.conf`.
