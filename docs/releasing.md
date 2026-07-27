# Releasing

`.github/workflows/release-unified.yml` is the only official publication path. It calls `openclaw/release-workflows@v1` from protected `main`, requires an existing repository-allowed SSH-signed tag, preserves Telecrawl's six binary-only platform archives and `checksums.txt`, signs and notarizes both thin macOS binaries as `ai.openclaw.telecrawl`, independently rebuilds every Linux and Windows binary and requires byte identity with the staged archives, independently verifies the shared inventory on arm64 and Intel macOS, and waits for the `openclaw/homebrew-tap` handoff.

The public compatibility contract remains:

- `telecrawl_VERSION_{darwin,linux}_{amd64,arm64}.tar.gz`
- `telecrawl_VERSION_windows_{amd64,arm64}.zip`
- `checksums.txt`
- one Telecrawl executable and no documentation payloads inside each archive
- OpenClaw Foundation Team ID `FWJYW4S8P8` and code identifier `ai.openclaw.telecrawl`

The shared pipeline also publishes verifier control assets (`ASSET-INVENTORY.json`, `SIGNING-MANIFEST.json`, and `RELEASE-NOTES.md`). Its checksum manifest covers those controls in addition to the six archives.

## Release

Prepare a dated changelog section and land it on protected `main`. The `user.signingkey` SSH key must be listed for your principal in `.github/release-allowed-signers`. Create and verify the annotated signed tag, push it, and dispatch the workflow:

```bash
git -c gpg.format=ssh tag -s vX.Y.Z -m "Release X.Y.Z"
git -c gpg.format=ssh -c gpg.ssh.allowedSignersFile=.github/release-allowed-signers tag -v vX.Y.Z
git push origin vX.Y.Z
gh workflow run release-unified.yml --repo openclaw/telecrawl -f version=X.Y.Z
```

The release is complete only when the shared run publishes the exact asset inventory, both native macOS verifiers pass, and the Homebrew handoff is green.

## Local diagnostics

Local mutation and release-verification paths are retired. `make release`, `make release-artifacts`, `make release-pilot`, `make release-draft`, `make verify-release`, `make release-homebrew`, and their `scripts/release-local` modes refuse and print the official workflow command. `make snapshot` and `make release-check` remain credential-free diagnostics.

## Automated reproducible-build gate

`reproducible-rebuild: non-darwin` is a mandatory pre-publication stage for every Telecrawl release. The shared pipeline rebuilds the exact frozen tag in a fresh read-only job with the primary build's pinned Go and GoReleaser versions, configuration, flags, and runner. That job cannot read the staged release payload. A separate clean job compares the complete rebuilt Linux and Windows binary set against the corresponding binaries extracted from the post-signing staged archives and fails closed on any missing, extra, or byte-different member.

Each staged and rebuilt SHA-256 is recorded in `ASSET-INVENTORY.json`, rechecked by both independent macOS verifier jobs, and bound to the draft bytes before publication. Darwin is intentionally excluded because Developer ID signing and trusted timestamps change those bytes; the pipeline rejects `reproducible-rebuild: all` rather than claiming a vacuous Darwin proof.
