# Security

## Threat model

rt-node-agent runs on every node in the RedTorch fleet and exposes an HTTP surface on port 11435. Its threat model is explicit and narrow:

- Assumed trusted: the local host, the local Ollama instance, and every other machine on the same LAN segment (the air-gapped OPSEC model).
- Assumed hostile: anything outside that LAN.

No PII, case data, or model weights flow through this agent. The only mutating action is `POST /actions/unload-model`, which is authenticated.

If you deploy this agent outside the air-gapped LAN model, terminate TLS in front of it and restrict network access at the perimeter. v2 will add native mTLS.

## Supply chain

- Releases are built by `.github/workflows/release.yml` from tagged commits.
- Each binary is signed with [minisign](https://jedisct1.github.io/minisign/). The public key is pinned in `scripts/install.sh`; rotating it requires a new install script.
- The release workflow emits a [SLSA build provenance attestation](https://slsa.dev/attestation-model) via `actions/attest-build-provenance`.
- `SHA256SUMS` is published alongside every release and signed.
- The minisign private key lives offline (never in GitHub Actions secrets at rest — loaded only at sign time, then shredded).

## Reporting a vulnerability

**Do not open a public GitHub issue for security reports.**

Email `security@redtorch.inc` (or whatever the current contact is — see the org page). Encrypted mail preferred. Expect a response within 3 business days.

Please include:
- Affected version (`rt-node-agent version` output)
- Steps to reproduce
- Impact assessment

We treat any remote code execution, authentication bypass, or path traversal on the HTTP surface as critical.

## Secret handling

- The Bearer token for `/actions/*` lives in `/etc/rt-node-agent/token` (`%ProgramData%\rt-node-agent\token` on Windows), mode 0600, owned by `root:rt-agent` on Linux.
- The agent does **not** log the token, request headers, or the request body of `/actions/*`.
- `install.sh` and `install.ps1` do **not** generate a token. Operators install one manually. Rationale: a publicly-downloadable installer that generates tokens is a footgun — the token would only be as secret as the install log.
