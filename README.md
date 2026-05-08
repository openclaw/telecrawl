# telecrawl

Telegram Desktop archive CLI.

`telecrawl` reads your local Telegram Desktop `tdata` through `opentele2` /
Telethon, stores a searchable SQLite archive in `~/.telecrawl/telecrawl.db`,
and can back it up to GitHub as encrypted age shards.

## Setup

```bash
telecrawl deps install
```

## Import

```bash
telecrawl import
telecrawl status
telecrawl chats --limit 20
telecrawl messages --limit 20
telecrawl search "query"
```

Import limits default to the latest 200 dialogs and 500 messages per dialog.
Use `0` for no limit:

```bash
telecrawl import --dialogs-limit 0 --messages-limit 0
```

## Backup

Create `https://github.com/steipete/backup-telecrawl` first, then:

```bash
telecrawl backup init
telecrawl backup push
```

Backup payloads are encrypted before Git sees them. Cleartext Git metadata is
limited to manifest counts, shard paths, export time, public age recipients,
encrypted sizes, and hashes.

Restore:

```bash
telecrawl backup pull
telecrawl status
```
