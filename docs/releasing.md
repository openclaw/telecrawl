# Releasing

`.github/workflows/release-unified.yml` is the only official publication path. It calls `openclaw/release-workflows@v1` from protected `main`, requires an existing repository-allowed SSH-signed tag, preserves Telecrawl's six binary-only platform archives and `checksums.txt`, signs and notarizes both thin macOS binaries as `ai.openclaw.telecrawl`, independently verifies the shared inventory on arm64 and Intel macOS, and waits for the `openclaw/homebrew-tap` handoff.

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

Local mutation paths are retired. `make release`, `make release-artifacts`, `make release-pilot`, `make release-draft`, `make release-homebrew`, and their `scripts/release-local` modes refuse and print the official workflow command. `make snapshot` and `make release-check` remain credential-free diagnostics. `make verify-release VERSION=vX.Y.Z` retains the read-only draft verifier for historical or investigative use.

## Supplemental reproducible-build verifier

`.github/workflows/release-assets.yml` is retained only as a manual legacy verifier for pre-migration releases. It independently rebuilds Linux and Windows binaries and requires byte equality for the old exact seven-asset contract. It does not own publication or Homebrew, has no automatic release trigger, and is not compatible with the shared control-asset inventory.

The shared pipeline does not currently expose a pre-publication external-verifier hook, so Telecrawl's former byte-identical rebuild proof is no longer a publication prerequisite. Moving this proof into the shared pre-publication chain is explicitly deferred; do not describe the shared release alone as reproducible-build proof.
