# Releasing

Official releases are local maintainer operations. CI and ordinary GoReleaser
snapshots do not receive signing or notarization credentials.

## Invariants

- The macOS code identifier is always `ai.openclaw.telecrawl`.
- The signing authority is `Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)` and the Team ID is `FWJYW4S8P8`.
- Official Darwin binaries run through the `release-mac-app` skill's `mac-release codesign-run` helper. `TELECRAWL_OFFICIAL_RELEASE=1` is set only inside that bounded command.
- `NOTARYTOOL_KEYCHAIN_PROFILE` is a runtime-only environment variable. Do not add it to `.mac-release.env`, CI, GitHub secrets, scripts, or committed documentation examples with a real profile value.
- Each Darwin binary is signed into a temporary candidate, submitted as an ephemeral ZIP with `notarytool --no-s3-acceleration --wait`, and checked for the exact identifier, authority, Team ID, hardened runtime, timestamp, exact embedded designated requirement from stderr-aware `codesign -d -r-`, external strict requirement acceptance, and `codesign --verify --strict --check-notarization -R='notarized'` before replacing the original GoReleaser output.
- The notarization response must explicitly report `Accepted` with a valid submission ID; command success alone is insufficient.
- Naturally quarantined clean-VM execution is the independent end-user Gatekeeper proof. Raw standalone CLIs are not application bundles, so application-bundle policy assessment is not a producer or archive-verifier success gate.
- A failed signing, notarization, metadata, designated-requirement, or online notarization check leaves the GoReleaser output untouched.
- GoReleaser creates a draft with exactly six binary-only platform archives plus `checksums.txt`. Publication and Homebrew are separate gates.
- The protected repository owns `.github/release-allowed-signers`. Every write-capable gate fetches the live remote annotated tag, verifies its exact signer principal and key with that file, requires the GitHub branch API to report the current default branch as protected at the exact fetched commit, and requires the tag commit to be reachable from it. Ambient Git SSH trust is ignored.
- Release proof binds both the exact annotated tag object and its peeled commit. GitHub release immutability is not assumed; checksums alone are not non-Darwin provenance.
- Official builds reject ambient Go build controls, including overlays, workspaces, experiments, alternate roots, and executable cache/authentication hooks. Both GoReleaser build IDs and the independent rebuilder pin module mode, FIPS mode, proxy/checksum policy, local toolchain selection, `GOCACHEPROG=`, `GOAUTH=off`, `GOWORK=off`, `GOENV=off`, and an empty `GOROOT` to the same explicit contract after selecting native Go 1.26.5.

## Serialized gates

Run each command only after its corresponding maintainer gate is granted:

```bash
make release-check
make release-pilot VERSION=v0.3.4
make release-draft
make verify-release VERSION=v0.3.4
make release VERSION=v0.3.4
make release-homebrew VERSION=v0.3.4
```

`make release VERSION=vX.Y.Z` is the one official publication command. It
delegates to the existing local release orchestrator, which refuses to publish
until the exact draft inventory has passed both native artifact-verifier jobs.
The surrounding targets retain the intentionally serialized pilot, draft,
verification, and Homebrew gates. Use `make snapshot` for credential-free
development artifacts; snapshots never publish.

`pilot` is the only no-tag official-build path. It uses GoReleaser snapshot mode
with an explicit proposed version, runs both Darwin binaries through
`codesign-run`, and independently verifies the resulting local archives,
including Go build information for the current clean default-branch commit. It
first fetches origin's current default branch and requires a clean checkout with
`HEAD` exactly equal to that fetched ref. Cleanliness explicitly includes all
untracked files and rejects skip-worktree, assume-unchanged, and replacement-ref
state. Legacy Git graft files are rejected before any ancestry decision because
`GIT_NO_REPLACE_OBJECTS` does not disable them. The producer and verifier then run from a fresh remote clone pinned to
that protected commit, never from mutable working-tree bytes. It does not create a GitHub release,
but notarization submits ephemeral ZIPs to Apple, so it still requires the
explicit upload gate. Pilot proof cannot authorize publication because it has
no revalidated signed release tag.

`draft` repeats the fetched-default exact-head check, then requires an exact
signed tag already present on origin, the runtime notary profile, and the
managed release keychain. Tag verification is pinned to the repository-owned
allowed-signers policy; a signer trusted only by global Git configuration is
rejected. It makes a fresh no-local/no-tags remote clone, fetches only the exact
verified tag into it, and invokes GoReleaser there with `--draft` through
`codesign-run`. The helper receives the absolute runtime `MAC_RELEASE_MANIFEST`
path from the operator checkout, so changing to the fresh source directory does
not lose the ignored managed-keychain configuration; the manifest is neither
copied nor committed. Draft does not dispatch the verifier, publish, or touch
Homebrew.

`verify-draft` dispatches `.github/workflows/release-assets.yml` from the
current default branch through the versioned `2026-03-10` REST endpoint. It
waits up to about five minutes for GitHub to materialize missing draft-asset
SHA-256 digests, but only when the otherwise exact release record is valid.
Any other metadata or asset mismatch still fails immediately. Release-note
comparison ignores trailing newlines added by GoReleaser and remains byte-exact
for all other content. It
requires the response's exact `workflow_run_id` and URLs, proves that ID did not
exist before the request, validates that exact run record including canonical
plain path `.github/workflows/release-assets.yml`, and retries full validation
of that same ID while GitHub materializes the run record. It then watches that
ID and requires the general newest-proof checker to return it. A concurrent
matching dispatch therefore cannot be substituted by title or timing. The
workflow fetches verifier code from that protected
branch without a token, anonymously fetches the annotated tag as data, and
forces its SSH signature through the same repository-owned allowed-signers
policy and exact principal/key check used locally. The protected run title is
bound to both the annotated tag object and peeled commit. The scoped download
step cross-checks both against REST, resolves the draft and its exact inventory,
and gives the token only to the download step through a bounded environment;
the token value is never an argument to a child process and is cleared from the
parent immediately afterward. The scoped
download step retrieves all six archives plus `checksums.txt`; after the token
is removed, each native job hashes all six platform archives and inspects every
binary without executing non-native candidates. Each must report the exact
`github.com/openclaw/telecrawl/cmd/telecrawl` main package, `go1.26.5`
toolchain, matching `GOOS`/`GOARCH`, `vcs.revision` equal to the signed tag
commit, and `vcs.modified=false`. Only the job's matching Darwin binary is
executed for functional `--version` proof. The four Linux and Windows binaries
are rebuilt from the exact signed-tag source with Go 1.26.5, the release
linker flags, and `-trimpath` in an independently fresh module cache; their
payloads must be byte-identical. The tag is only a version and artifact
selector; release-tag code is never executed. Static archive, build-info,
signature, notarization, reproducible-rebuild, frozen-hash, and protected-helper
cleanliness checks all finish before the matching native candidate is run as
the workflow's final isolated, token-free operation. No later trust decision
depends on state writable by that candidate. The proof checker requires one
exact marker line in each native job's final-step log; prefixes, suffixes, and
GitHub's duplicate combined job logs are not accepted as independent proof.

`publish` requires a successful two-architecture draft verifier run at the
current default-branch SHA, created strictly after the newest draft asset, and
rechecks that the draft notes exactly match the changelog read from the freshly
revalidated signed tag. The draft name is the exact tag and its target is the
peeled signed-tag commit. After accepting verifier proof, publication freezes
the numeric release ID plus every asset ID, name, nonzero size, and GitHub
SHA-256 digest. It revalidates that exact metadata, title, target, prerelease
state, and tagged notes by numeric-ID GET immediately before the PATCH, then
requires both the PATCH response and a fresh numeric-ID GET to be the identical
published record, then refetches the live signed tag and requires both its
object and peeled commit to remain the verifier-accepted identity before
reporting success. The PATCH always sends the six canonical mutable release
fields together; partial release PATCHes are forbidden because GitHub can
replace an omitted draft tag name with an `untagged-*` placeholder. Publication
also triggers a published-state verifier from
the release event. Before touching the
tap, `homebrew` explicitly dispatches a fresh published-state verifier from the
then-current protected default branch, so ordinary main advancement cannot
strand closeout behind the one-time release event. Manual verifier run titles
and every verifier job marker bind the protected workflow SHA, exact annotated
tag object, and peeled commit. The release-event title identifies its event
source; its job markers carry the fetched exact tag object, and the checker
revalidates it against the pinned repository signer before accepting the run.
Proof may equal the second-resolution publication
time, but it must be strictly newer than the newest asset mutation; same-second
asset proof is stale. A successful draft run or proof for an earlier tag object
or target cannot satisfy the gate. The checker selects the newest otherwise
relevant fresh successful run before requiring its exact current object/commit
title and both native job markers, so a newer conflicting B run invalidates an
older A proof even if the public tag later moves back to A. The verifier workflow itself performs no
writes.

GitHub's REST API supports conditional requests for safe reads but
[does not support them for unsafe methods unless an endpoint says
otherwise](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api#use-conditional-requests-if-appropriate);
[Update a release](https://docs.github.com/en/rest/releases/releases#update-a-release)
defines no conditional precondition spanning the release, tag, and asset
resources. The final numeric GET and all post-PATCH checks therefore detect but
cannot atomically prevent an authorized concurrent mutation in the narrow
GET-to-PATCH window. A mismatch fails closeout, but cannot undo bytes already
made public. Keep the publication gate serialized. Enabling repository release
immutability is a separate owner policy and is not performed by this workflow.

Immediately before the tap handoff, `homebrew` performs a second authenticated
download into a new directory, rechecks the exact seven-asset inventory and
native signature/notarization, and repeats all four non-Darwin byte rebuilds.
The downloaded names, sizes, and SHA-256 digests must still match the final
accepted numeric release snapshot before hashes from that copy are dispatched.
That numeric snapshot must also remain identical across the final published
verifier check; any replacement during the check stops before tap dispatch.
The tap's live default branch
must be protected; the dispatch is bound to its exact workflow ID, canonical
plain path `.github/workflows/update-formula.yml`, branch,
and head SHA. The live head must contain frozen contract base
`45b93a0b3de27e46b636a0cef819fb1ecef25bcd`, and its workflow and updater
bytes must still equal that base. Its run title is exactly `Update telecrawl for <tag>
(request-id=<id>; source-tag-object=<object>; source-tag-commit=<commit>)`.
The post-run default must be its single direct child, with subject `telecrawl:
update formula for <tag>` and ordered `Source-Repository`, `Source-Tag-Object`,
`Source-Tag-Commit`, and `Request-ID` trailers exactly matching the handoff.
Formula evaluation and clean install use a minimal credential-free
environment when the local Homebrew accepts a formula file outside a tap. If
that Homebrew requires formulae to be in a tap, the redundant local install and
test are reported as skipped after the protected hosted handoff and exact
formula/checksum verification succeed. When run, the installed binary must equal the verified archive member
byte-for-byte, then pass the full native architecture, Foundation
authority, Team, identifier, runtime, timestamp, canonical designated
requirement, online notarization, and version checks again before `brew test`
runs as the final isolated candidate operation. Closeout then reloads the live
protected tap default and requires it still equals the verified formula commit
before reporting success.

[Workflow dispatch accepts only a branch or tag
name](https://docs.github.com/en/rest/actions/workflows#create-a-workflow-dispatch-event),
not an exact commit SHA. Telecrawl rechecks the protected head before dispatch
and accepts only the exact expected run head and proof marker; the landed tap
workflow also requires the remote default to equal its own `GITHUB_SHA`
immediately before its one-shot push. Concurrent branch movement is therefore
detectable and cannot yield accepted Telecrawl closeout proof, but dispatch
could resolve to a newer protected head before the client detects it. Keep this
write gate serialized; the checks do not claim atomic dispatch pinning.

## Clean-VM Gatekeeper proof

After publication is authorized, download the matching Darwin archive through
a normal macOS browser on a clean VM so the system attaches quarantine
naturally; do not synthesize `com.apple.quarantine`. Confirm the quarantine
attribute exists, extract normally, and run `telecrawl --version`. Acceptance
requires the expected version with no Gatekeeper warning or override. Record
that behavior as the Gatekeeper proof. Producer and native verifier code-signing
and online notarization checks do not substitute for this quarantined execution
proof.

## Clean-VM protected-data continuity

After publication is authorized, use the same installed path for v0.3.3 and
v0.3.4 on a clean macOS VM. The real, read-only Telegram protected-data action
is:

```bash
telecrawl --json doctor --path "$HOME/Library/Group Containers/6N38VWS5BX.ru.keepcoder.Telegram"
```

`doctor` walks the real Telegram for macOS group container and opens file
headers without creating or changing the Telecrawl archive. Record a valid
`telegram-macos-postbox` result from a controlled archive; do not substitute a
fixture. Replace the executable in place, confirm both versions have the same
designated requirement, and repeat the exact command. The acceptance criterion
is no second protected-data consent prompt. Do not claim this proof without a
controlled valid Telegram archive. Per-artifact canonical DR checks do not
replace this later same-path old/new DR equality and TCC-continuity gate.
