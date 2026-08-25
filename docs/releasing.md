# Releasing

## Cutting a release

Releases are tag-driven. Pushing a `v*` tag runs
[.github/workflows/release.yml](../.github/workflows/release.yml), which
cross-compiles five targets, checksums them, optionally signs them, attests
build provenance, and publishes a GitHub release with generated notes.

```sh
# main must be green first — the release builds from the tag, not from a
# passing CI run, so a red main becomes a broken release.
git checkout main && git pull

git tag -a v0.3.4 -F -   # annotated; the message is the human changelog
git push origin v0.3.4
```

Then confirm the run went green and the assets landed:

```sh
gh run list --workflow release --limit 1
gh release view v0.3.4
```

### Version numbering

The tag carries the `v` prefix; the **binary reports bare semver**.
`release.yml` strips it with `VERSION="${GITHUB_REF_NAME#v}"` and the
`Makefile` does the same for local builds, so `/health.agent_version` is
`0.3.4`, not `v0.3.4` (issue #7). Don't strip at runtime —
`internal/buildinfo` stays a dumb holder of link-time strings.

### Published assets

| Asset | Always? |
|---|---|
| `rt-node-agent_{linux,darwin}_{amd64,arm64}`, `rt-node-agent_windows_amd64.exe` | yes |
| `SHA256SUMS` | yes |
| `install.sh`, `install.ps1` | yes |
| `*.minisig` | **only when signing is configured** — see below |
| Build-provenance attestation | yes (`actions/attest-build-provenance`) |

Verify a downloaded binary against the checksum file:

```sh
gh release download v0.3.4 -p 'rt-node-agent_linux_arm64' -p SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
```

## Release signing (currently NOT enabled)

**No release has ever been signed.** Two independent reasons, and both must
be fixed for signatures to actually appear and be checked.

### 1. The workflow condition was broken (fixed in v0.3.4)

The sign step was gated on:

```yaml
if: ${{ env.MINISIGN_KEY != '' }}   # WRONG
env:
  MINISIGN_KEY: ${{ secrets.MINISIGN_KEY }}
```

A step's own `env:` block is **not in scope for its own `if:`**, so the
expression read an undefined value and was always false. The step skipped
silently on every release. Testing `secrets.MINISIGN_KEY` directly in the
`if:` would not have worked either — `secrets` is not available in a
step-level `if:`.

It now resolves to a step output instead, which sidesteps the context rules
and **logs the decision**, so an unsigned release is visible in the run log
and as a workflow notice rather than being silent.

### 2. No signing key exists, and the pinned pubkey is a placeholder

[scripts/install.sh](../scripts/install.sh) carries:

```sh
PUBKEY="RWS_PLACEHOLDER_PUBKEY_REPLACE_AT_FIRST_SIGNED_RELEASE"
```

The installer detects that prefix and **skips** verification. That guard is
load-bearing, not cosmetic: the placeholder is never handed to `minisign`,
because a mismatch takes the `err()` path, which aborts the install. Feeding
it a placeholder would mean that the moment signatures started being
published, `curl | sh` would hard-fail on **every host with minisign
installed** — a fleet-wide install outage caused by turning security on.

Policy, deliberately asymmetric:

- fail **open** when signing isn't set up (placeholder pubkey; minisign
  absent; no `.minisig` published for that asset)
- fail **closed** when signing *is* set up and the signature doesn't verify
- hard error when `PUBKEY` is empty — that is a broken installer, not a
  signing state, and it deserves a different message than "signature
  mismatch"

### Enabling signing

Both halves must land, and the pubkey commit should go in **with** the
secrets so there is never a window where signatures are published but
unverifiable.

1. Generate a keypair on a trusted machine — **not in CI**, and never commit
   the secret key. Per [CLAUDE.md](../CLAUDE.md) public-repo hygiene, signed
   release keys must never enter this repo; `git log -p --all` is public
   forever.

   ```sh
   minisign -G -p rt-node-agent.pub -s rt-node-agent.key
   ```

2. Add repository secrets:

   | Secret | Value |
   |---|---|
   | `MINISIGN_KEY` | full contents of `rt-node-agent.key` (both lines) |
   | `MINISIGN_PASSPHRASE` | the passphrase chosen at generation |

3. Replace `PUBKEY` in `scripts/install.sh` with the **public** key line from
   `rt-node-agent.pub` (the base64 line, not the comment line). Store the
   secret key offline; losing it means rotating the pinned pubkey in a
   release that older installers can't verify.

4. Cut a release and confirm `sign (minisign)` **ran** rather than skipped:

   ```sh
   gh run view <run-id> --json jobs \
     --jq '.jobs[] | select(.name=="release") | .steps[] | "\(.conclusion)\t\(.name)"'
   ```

   `skipped   sign (minisign)` means the key isn't visible to the workflow.

5. Verify end to end from a clean host, with `minisign` installed, that
   `curl | sh` reports `signature verified`.

### Known gap: the downgrade window

Once signing is on, the installer still fail-opens when **no** `.minisig` is
published for an asset ("no signature published for this release; skipping
verify"). That keeps older, genuinely-unsigned releases installable, but it
means an attacker who can suppress the `.minisig` fetch downgrades the
install to unverified.

Closing it means refusing to install any release at or above the first
signed version without a signature — worth doing once a signed floor exists,
and it needs the floor version pinned in the installer. Not done, because
there is no signed release to pin.

## Pre-release checklist

- `main` green on `ci`, `codeql`, and `security`
- 0 open code-scanning alerts (`gh api repos/:owner/:repo/code-scanning/alerts?state=open --jq length`)
- `CLAUDE.md` status entry written for the version being tagged
- Wire-contract changes reflected in [spec/SPEC.md](../spec/SPEC.md) and the
  relevant [docs/api/](api/) page
- `examples/config.yaml` still byte-identical to `config.DefaultYAML` when a
  config key was added
- Working tree clean, tag annotated
