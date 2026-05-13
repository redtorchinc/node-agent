# Install / upgrade

The one-liner is the supported path on every OS. If it doesn't work, that's a
bug — open an issue rather than scripting around it.

## Linux / macOS

```sh
curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/install.sh | sudo sh
```

The script:

1. Downloads the binary for the detected OS/arch (no Docker, no Python).
2. Installs it to `/usr/local/bin/rt-node-agent`.
3. Calls `rt-node-agent install`, which:
   - Creates the system user (`rt-agent` on Linux, `_rt_agent` on macOS).
   - Writes `/etc/rt-node-agent/config.yaml.example` (template; the real
     `config.yaml` is yours to write or migrate from v0.1.x — see below).
   - Generates a fresh Bearer token in `/etc/rt-node-agent/token` **only
     if no token already exists**. Reinstalls preserve the existing token.
   - **Linux only:** drops `/etc/sudoers.d/rt-node-agent` scoping the
     `rt-agent` user to `systemctl {start,stop,restart,status,show}` on
     `rt-vllm-*.service` units.
   - **macOS only:** allows incoming connections to the binary through
     the Application Firewall.
   - Registers + starts the service (systemd / launchd).
4. Healthchecks `http://127.0.0.1:11435/version`.
5. Prints the bearer token (only when generated fresh) and the
   `config updated in place` banner if the schema gained new keys
   relative to your existing config.

## Windows

From elevated PowerShell:

```powershell
iwr -useb https://github.com/redtorchinc/node-agent/releases/latest/download/install.ps1 | iex
```

The script registers a Windows Service (`rt-node-agent`) and writes
`%ProgramData%\rt-node-agent\config.yaml.example`.

## Upgrade from v0.1.x

Re-running the one-liner upgrades the binary in place. The service restarts
automatically.

If the new binary's schema gained keys relative to your existing
config (e.g. `platforms`, `services`, `training_mode`, `rdma`, `disk`
added in v0.2.0), the installer updates `config.yaml` **in place**:

1. Moves the current `config.yaml` to `config.yaml.bak` (single backup,
   overwritten on each migration).
2. Writes the new schema's defaults to `config.yaml`.
3. Grafts every top-level value you had set in the backup back into the
   live file — so your `port`, `bind`, `ollama_endpoint`, allocator
   list, etc. are preserved.

To enable new features, edit `config.yaml` directly and restart:

```sh
sudo nano /etc/rt-node-agent/config.yaml
sudo systemctl restart rt-node-agent  # Linux
sudo launchctl kickstart -k system/com.redtorch.rt-node-agent  # macOS
Restart-Service rt-node-agent  # Windows (elevated PowerShell)
```

To see what changed across the upgrade:

```sh
diff /etc/rt-node-agent/config.yaml.bak /etc/rt-node-agent/config.yaml
```

You can also re-run the migration explicitly at any time:

```sh
sudo rt-node-agent config migrate
```

### When your existing config is malformed YAML

If `/etc/rt-node-agent/config.yaml` doesn't parse (typo, missing colon,
hand-edit gone wrong, drifted v0.1 example), the installer detects this
and **auto-recovers**:

1. Original file → `/etc/rt-node-agent/config.yaml.broken-<unix-ts>`
2. Fresh defaults written to `/etc/rt-node-agent/config.yaml`
3. The token file is **not** touched.
4. install.sh prints a banner pointing at the backup.

Your customised settings are preserved in the `.broken-` file — copy them
across into the fresh config and restart:

```sh
sudo nano /etc/rt-node-agent/config.yaml          # incorporate your old values
sudo systemctl restart rt-node-agent
```

To trigger this recovery manually (e.g. service refused to start with a
"did not find expected key" error):

```sh
sudo rt-node-agent config migrate-force
```

## Uninstall

```sh
sudo rt-node-agent uninstall
```

Removes the systemd / launchd / SCM registration, the macOS firewall rule,
and the Linux sudoers drop-in. **Config and token are preserved** at
`/etc/rt-node-agent/`. Delete those by hand if you want a full wipe.
