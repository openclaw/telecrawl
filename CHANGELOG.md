# Changelog

All notable changes to this project are documented here.

The format follows Keep a Changelog, and this project uses Semantic Versioning.

## [Unreleased]

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
