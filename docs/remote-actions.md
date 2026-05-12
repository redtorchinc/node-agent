# Remote actions — security model

`/actions/*` endpoints are the only places the agent acts on the world.
They all share the same posture:

1. **Bearer token required.** Without a configured token the endpoint
   returns 503 (intentionally distinguishable from 401 "wrong token").
2. **No remote shell, no arbitrary args.** Each endpoint defines a tiny
   typed request shape; nothing the client sends ever flows verbatim to
   a shell.
3. **LAN-only by default.** Bind to `0.0.0.0` on the trusted LAN; tighten
   to `127.0.0.1` if a node must not be reachable beyond its own host.
4. **No TLS in v0.2.** The model assumes the agent runs on the same LAN
   segment as the case-manager backend (air-gapped or VPN-fenced). For
   non-air-gapped deployments, gate access at a network appliance.

## Per-endpoint specifics

### `POST /actions/unload-model`

Free a model from Ollama. Idempotent (no-op when not loaded). No
allowlist — Ollama's own model registry IS the allowlist (it only knows
about models you pulled).

### `POST /actions/service`

Start/stop/restart/status of one of N pre-existing systemd units.

**Defense in depth:**

1. **Config-level allowlist.** Only units listed in
   `services.allowed[].name` are candidates. The dispatcher cannot ask
   for a unit that isn't there.
2. **Per-unit action enum.** Each `services.allowed[]` entry can omit
   actions (e.g. `[start, restart, status]` to disallow `stop`).
3. **No shell.** `exec.Cmd(systemctl, action, unit)` — Go does not
   invoke a shell, so a unit name like `evil.service; rm -rf /` cannot
   inject (and would fail the allowlist check first anyway).
4. **Sudoers drop-in scoped to a name pattern.** On Linux, the installer
   places `/etc/sudoers.d/rt-node-agent` granting the `rt-agent` user
   `NOPASSWD` on `systemctl {start,stop,restart,status,show}` *only* for
   units matching `rt-vllm-[a-zA-Z0-9_-]*.service`. **Operators must
   name their vLLM units accordingly.** A misconfigured `services.allowed`
   entry that names `sshd.service` still cannot escalate — sudo refuses.

Naming convention: `rt-vllm-<model>.service`. Examples:

```
rt-vllm-qwen3.service
rt-vllm-llama-70b.service
rt-vllm-deepseek-coder.service
```

The pattern accepts alphanumerics + `_` and `-`. Periods are forbidden
inside the variable region to keep the suffix bound to `.service`.

### `POST /actions/training-mode`

Coordinates the inference ↔ training transition. Token-gated. Drains
Ollama via the existing `/actions/unload-model` machinery; can't escalate
beyond what that endpoint already permits.

## Audit log

Every mutating call logs structured fields via slog:

```
INFO service action ok unit=rt-vllm-qwen3.service action=start active=active sub=running took_ms=312 remote=192.168.50.10:54421
WARN service action denied or failed unit=docker.service action=stop code=403 err="unit not in allowlist" remote=...
```

Audit retention is whatever your systemd journal / launchd log /
Windows EventLog policy provides; the agent doesn't ship its own log
files.
