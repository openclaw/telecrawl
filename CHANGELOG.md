# Changelog

## Unreleased

### Fixed

- Stop Telegram Desktop forum topic import from repeating the first `messages.getForumTopics` page when offsets do not advance.

## [0.3.5] - 2026-07-26

### Added

- Preserve stable Telegram message identity and append-only revision events for baseline observations, observable message edits, and explicit deletes.

### Changed

- Move official publication to the shared signed, notarized, independently verified GitHub Actions pipeline while preserving archive names, `checksums.txt`, the stable code identifier, and Homebrew delivery.
- Require the shared publication pipeline to independently rebuild every Linux and Windows binary, compare it byte-for-byte with the staged release archive, and bind both digests into the verified inventory before publication.
- Add source-attributed tombstones to every canonical archive entity, propagate parent deletions to subordinate rows, and make account-bound backup pulls merge by default with exact replacement behind explicit `--restore`.
- Standardize maintainer Make targets while preserving the serialized, fail-closed local release gates.

### Dependencies

- Update Go dependencies, including `go-faster/errors` v0.8.0, `klauspost/compress` v1.19.1, and `go-isatty` v0.0.24.

### Fixed

- Harden release closeout against delayed GitHub asset digests, GoReleaser-added trailing note newlines, draft PATCH tag resets, and Homebrew versions that reject untapped formula files.

## [0.3.4] - 2026-07-17

### Highlights

- Ship official macOS binaries signed by the OpenClaw Foundation, notarized by Apple, and independently verified on native Apple Silicon and Intel hosts before publication.
- Preserve older archive history during routine bounded imports by merging new data by default and reserving destructive replacement for explicit `--replace` restores.
- Complete Telegram Desktop imports safely when expired or service-style document media has no document payload.
- Harden release provenance from the signed source tag through verifier-approved, identity- and digest-bound assets and the final Homebrew handoff.

### Archive safety

- Preserve chats and messages outside bounded import windows, pin and verify source identity, and keep full archive replacement behind explicit `--replace`. (#21)
- Avoid nil-pointer panics while importing Telegram Desktop document media without a document payload. (#22; thanks @sw1pp3r)

### Signed release pipeline

- Notarize every official macOS binary before GoReleaser archives it, then verify exact Foundation signing metadata, the canonical designated requirement, strict code validity, and online notarization.
- Create releases as drafts, require independent native Apple Silicon and Intel verification before publication, and require a distinct published-release verifier before Homebrew updates.
- Pin signed release tags to the repository release key and exact tag object and commit, bind every platform archive to the expected Go main package, toolchain, target, and revision, and reproducibly rebuild non-Darwin payloads.
- Source release notes from the revalidated tag, preserve the tag and notes through draft publication, and wait for verifier runs before accepting their step-bound proof.
- Bind publication and Homebrew handoff bytes to the verifier-approved release record and exact asset identities, sizes, and digests, then require tap workflow and commit provenance to match the protected source.
- Reject ambient Go build controls and executable hooks, pin verifier API reads to Telecrawl, and run native candidates only after frozen static, reproducible-build, and protected-helper proof.

### Dependencies

- Update CrawlKit to v0.14.3, gotd to v0.161.0, modernc SQLite to v1.54.0, and `golang.org/x/crypto` to v0.54.0, with all direct dependencies on their latest stable releases. (#19, #23)

## [0.3.3] - 2026-07-09

### Fixed

- Fix the documented `go install ...@latest` path: v0.3.2 module builds reported 0.3.1; tagged installs now resolve their version from Go module metadata.
- Publish and verify GitHub release notes from the matching changelog section after GoReleaser uploads the release.

## [0.3.2] - 2026-07-09

### Changed

- Update CrawlKit to the signed v0.13.4 release.
- Sign official macOS release binaries with the OpenClaw Foundation Developer ID through the managed local release keychain, while keeping CI, local development, and cross-platform snapshot builds credential-free.

### Fixed

- Build with Go 1.26.5 to fix reachable `crypto/tls` vulnerability GO-2026-5856 in official binaries.

## [0.3.1] - 2026-07-02

### Fixed

- Retry Telegram API reads after bounded, visible, and cancellable flood waits without restarting paginated imports. (#16; thanks @masonc15)

## [0.3.0] - 2026-06-19

### Added

- Archive Telegram contact records from local Postbox imports. (#7; thanks @joshp123)
- Expose Telegram contacts through the crawlkit `contact-export` metadata command for Clawdex imports. (#9; thanks @joshp123)
- Add named Git backup snapshots, history listing, and non-mutating historical restores through `backup pull --ref`.

### Changed

- Replace the Python/Telethon import bridges with native Go readers for Telegram Desktop Postbox and TData archives.
- Retry concurrent encrypted backup branch-and-tag pushes after rebasing and retargeting the unpublished tag.
- Move encrypted snapshot, Git history/tag/ref, contact export, and safe FTS query mechanics to CrawlKit while preserving the archive schema, backup manifest format, and CLI JSON contracts.

### Fixed

- Migrate older local archives before creating topic indexes and tolerate nullable optional message fields from live Telegram data.
- Fix generated backup recovery instructions to use the supported `telecrawl status` command.

## [0.2.0] - 2026-05-31

### Added

- Add `import --chat ID` for targeted single-chat imports while preserving unrelated archive data. (#1; thanks @nullyn)
- Add `metadata --json` crawlkit control metadata for schedulers and local automation.
- Docker: add a local image with packaged Python bridge dependencies, `/data` persistence, read-only `tdata` mounting docs, and Docker CI smoke coverage.
- Archive locally cached Telegram macOS Postbox media by default and add opt-in `import --fetch-media` cloud media fetching through existing local session state. (#3; thanks @joshp123)
- Archive Telegram dialog folders and forum topics, with CLI reads via
  `folders`, `chats --folder`, `topics --chat`, and `messages --topic`.
- Preserve reply/thread IDs, pinned messages, edits, forwards, reactions,
  view/reply counts, and richer media titles during import, search, and
  encrypted backup restore.

### Changed

- Update `crawlkit` to v0.7.0.

## [0.1.0] - 2026-05-08

### Added

- Initial Telegram Desktop archive CLI with `doctor`, `import`, `status`,
  `chats`, `messages`, and FTS-backed `search` commands.
- Import bridge for Telegram Desktop `tdata` using `opentele2` and Telethon,
  with `telecrawl deps install` to create the local Python environment.
- Local SQLite archive at `~/.telecrawl/telecrawl.db`, including chat/message
  counts, unread counts, media metadata, and sync state.
- Encrypted Git backups with `backup init`, `backup push`, `backup pull`, and
  `backup status`, using reusable `crawlkit` age-encrypted JSONL/Gzip shard
  helpers.
- Multi-machine backup support via age recipients, manifest verification,
  shard hash checks, and restore into a fresh archive database.
- CI and release automation for linting, tests, secret scanning, GoReleaser
  artifacts, and Homebrew tap updates.
