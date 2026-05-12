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
   `config.yaml.new` banner if v0.2.0 introduced keys missing from your
   existing config.

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

If v0.2.0 introduced new config keys (and it did: `platforms`, `services`,
`training_mode`, `rdma`, `disk`), the installer writes a
`/etc/rt-node-agent/config.yaml.new` next to your existing config with the
new keys appended as commented YAML. **Your existing config is never
modified.** Review and merge by hand:

```sh
diff /etc/rt-node-agent/config.yaml /etc/rt-node-agent/config.yaml.new
sudo mv /etc/rt-node-agent/config.yaml.new /etc/rt-node-agent/config.yaml
sudo systemctl restart rt-node-agent
```

You can also re-run the migration explicitly at any time:

```sh
sudo rt-node-agent config migrate
```

## Uninstall

```sh
sudo rt-node-agent uninstall
```

Removes the systemd / launchd / SCM registration, the macOS firewall rule,
and the Linux sudoers drop-in. **Config and token are preserved** at
`/etc/rt-node-agent/`. Delete those by hand if you want a full wipe.
