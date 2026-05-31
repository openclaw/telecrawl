# Changelog

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
