# Security

## Reporting a vulnerability

**Do not open a public GitHub issue for security reports.**

Email `security@redtorch.inc` (or whatever the current contact is — see
the org page). Encrypted mail preferred. Expect a response within 3
business days.

Please include:
- Affected version (`rt-node-agent version` output)
- Steps to reproduce
- Impact assessment

We treat any remote code execution, authentication bypass, or path
traversal on the HTTP surface as critical.

## Supported versions

Only the latest minor receives security fixes. Older minors will not be
patched — the upgrade path is the same `curl … | sudo sh` one-liner and
the v0.2.7+ migrator preserves operator config in place. See
[docs/install.md](docs/install.md) §Upgrade.

| Version | Supported |
|---|---|
| 0.2.x (latest) | ✅ |
| 0.2.x (older patch) | upgrade to latest |
| 0.1.x | ❌ — please upgrade |

## Threat model

rt-node-agent runs on every node in the RedTorch fleet and exposes an
HTTP surface on port 11435. Its threat model is explicit and narrow:

- **Assumed trusted:** the local host, the local Ollama / vLLM
  instances, and every other machine on the same LAN segment (the
  air-gapped OPSEC model).
- **Assumed hostile:** anything outside that LAN.

No PII, case data, or model weights flow through this agent. The only
mutating actions are:

- `POST /actions/unload-model` — frees an Ollama model
- `POST /actions/service` — starts/stops/restarts allowlisted vLLM units
- `POST /actions/training-mode` — coordinates inference ↔ training mode

…all of which require a Bearer token.

If you deploy this agent outside the air-gapped LAN model, terminate
TLS in front of it and restrict network access at the perimeter. v2 will
add native mTLS.

## Public-repo hygiene

This repo is published; everything committed is world-readable forever
(GitHub mirrors, archive.org, training corpora, `git log -p --all`).

The rules, enforced by `.gitignore` + manual review + CI scanning
(below):

- **Never commit**: bearer tokens, `/etc/rt-node-agent/token` contents,
  `.env` files, private keys, real internal hostnames/IPs from the
  case-manager fleet, real node identifiers, real case data, signed
  release keys.
- **Example IPs / hostnames** must come from RFC 5737 documentation
  ranges (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`) and RFC
  2606 reserved domains (`example.com`, `example.net`, `example.org`).
- **Reference the private case-manager repo only by role** ("the
  backend") — don't commit its URL, paths, or internal module names.
- Spec examples that ARE already public (`ctrlone-Intel-R-Core-TM-i5-14400F`,
  `gliner2-service`, the 2026-04-22 incident, `dgx-01`, `spark-A1`,
  `ctrlone-…`) are grandfathered. Anything new must sanitize.
- A committed secret is compromised even after revert — **rotate**,
  don't rewrite history.

## Supply chain

- Releases are built by `.github/workflows/release.yml` from tagged
  commits on `main`.
- Each binary is signed with
  [minisign](https://jedisct1.github.io/minisign/). The public key is
  pinned in `scripts/install.sh`; rotating it requires a new install
  script.
- The release workflow emits a
  [SLSA build provenance attestation](https://slsa.dev/attestation-model)
  via `actions/attest-build-provenance`.
- `SHA256SUMS` is published alongside every release and signed.
- The minisign private key lives offline (never in GitHub Actions
  secrets at rest — loaded only at sign time, then shredded).

## Automated audit (GitHub Actions)

The repo runs four scanners on every push to `main`, every pull request,
and on a weekly schedule. Findings post to the
[Security tab](https://github.com/redtorchinc/node-agent/security):

| Workflow | Tool | What it checks |
|---|---|---|
| `.github/workflows/codeql.yml` | CodeQL (Go) | Static analysis — SQL injection, path traversal, command injection, hardcoded credentials, etc. Maintained by GitHub. |
| `.github/workflows/security.yml` | `govulncheck` | Known CVEs in the Go module graph and stdlib. Pulls from `vuln.go.dev`. |
| `.github/workflows/security.yml` | `gosec` | Go-specific security linter — weak crypto, file-permission flags, hardcoded secrets, unsafe HTTP defaults. |
| `.github/workflows/security.yml` | `gitleaks` | Secret detection on the working tree AND full git history. Flags accidentally-committed tokens / private keys. |
| `.github/workflows/security.yml` | `dependency-review-action` | Blocks PRs that introduce a dependency with a known vulnerability. PR-only (depends on a base ref). |
| `.github/dependabot.yml` | Dependabot | Weekly automated PRs to update Go modules and GitHub Actions to patched versions. |

The CodeQL and gitleaks workflows can be re-run manually from the Actions
tab if you want a fresh scan without pushing. CodeQL alerts auto-close
when the fix lands; gitleaks treats new findings as failures so the PR
can't merge until resolved.

In addition, GitHub-native (always-on, no config required):

- **Secret scanning** — flags committed tokens/keys (GitHub maintains
  the rule set; auto-revokes for partner providers like AWS).
- **Dependabot alerts** — surfaced in the Security tab for any module
  in `go.sum` with a known CVE.
- **Push protection** — blocks pushes containing detected secrets at
  the receive hook.

If a security alert fires:

1. The fix lands on `main` via a normal PR.
2. Tag a new patch release (`v0.2.x+1`) so operators upgrade via the
   one-liner.
3. If the alert involved a leaked secret, **rotate the secret** — the
   commit history is public forever.

## Secret handling at runtime

- The Bearer token for `/actions/*` lives in `/etc/rt-node-agent/token`
  on Linux (mode `640`, owned `root:rt-agent`), in the same path on
  macOS (mode `600`, owned `root`), and in
  `%ProgramData%\rt-node-agent\token` on Windows (default ProgramData
  ACL — Administrators + LocalSystem read).
- The agent does **not** log the token, request headers, or the request
  body of `/actions/*`.
- The installer (`rt-node-agent install`) generates a 32-byte random
  token via `crypto/rand` when the token file doesn't already exist,
  writes it with the right perms, and prints it once to stdout for the
  operator to capture. Reinstalls never rotate an existing token. To
  deploy with a fleet-wide shared token, write it to the token path
  **before** running `install.sh` — the installer sees the file and
  skips generation.
- The token is only as secret as the terminal where install runs. For
  `curl … | sudo sh`, that's the operator's local terminal — fine for
  trusted-LAN deployment. Don't pipe install output into a shared
  logger.
- Bearer tokens are compared with `crypto/subtle.ConstantTimeCompare`
  in [internal/server/auth.go](internal/server/auth.go) to avoid timing
  side-channels.
