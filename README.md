# telecrawl

Telegram Desktop archive CLI.

`telecrawl` reads your local Telegram Desktop `tdata` through `opentele2` and
Telethon, stores a searchable SQLite archive in `~/.telecrawl/telecrawl.db`, and
can back it up to GitHub as encrypted age shards.

It is local-first:

- Normal archive/search commands do not upload data.
- `backup push` uploads only age-encrypted shards when you run it explicitly.
- Telegram message text, chat names, sender names, and media metadata stay inside
  encrypted backup payloads.

## Install

```bash
brew tap steipete/tap
brew install telecrawl
```

Or install with Go:

```bash
go install github.com/openclaw/telecrawl/cmd/telecrawl@latest
```

## Setup

Install the Python bridge used for Telegram Desktop `tdata` imports:

```bash
telecrawl deps install
```

This creates `~/.telecrawl/venv` and installs `opentele2` plus Telethon.

## Import

```bash
telecrawl doctor
telecrawl import
telecrawl status
```

Import defaults to:

- latest `200` dialogs
- latest `500` messages per dialog

Use `0` for no limit:

```bash
telecrawl import --dialogs-limit 0 --messages-limit 0
```

Useful reads:

```bash
telecrawl folders
telecrawl chats --limit 20
telecrawl chats --folder FOLDER_ID
telecrawl chats --unread
telecrawl topics --chat CHAT_ID
telecrawl messages --limit 20
telecrawl messages --chat CHAT_ID --after 2026-01-01
telecrawl messages --chat CHAT_ID --topic TOPIC_ID
telecrawl messages --chat CHAT_ID --pinned
telecrawl search "query"
telecrawl search "query" --chat CHAT_ID --topic TOPIC_ID
```

Telegram folders, forum topics, reply/thread IDs, pinned messages, edits,
forwards, reactions, view/reply counts, and richer media titles are archived
when Telethon exposes them for the active account. Folder rows include explicit
membership from Telegram dialog filters; dynamic folder rules are recorded as
metadata and may not expand to every matching chat.

Add `--json` before the command for machine-readable output:

```bash
telecrawl --json status
telecrawl --json search "invoice"
```

## Data Paths

Defaults:

- Telegram Desktop source: `~/Library/Application Support/Telegram Desktop/tdata`
- archive DB: `~/.telecrawl/telecrawl.db`
- Python bridge venv: `~/.telecrawl/venv`
- Telethon sessions: `~/.telecrawl/sessions/`
- backup config: `~/.telecrawl/backup.json`
- age identity: `~/.telecrawl/age.key`
- backup checkout: `~/Projects/backup-telecrawl`

Override the archive DB:

```bash
telecrawl --db /tmp/telecrawl.db status
```

Override the Telegram Desktop source:

```bash
telecrawl --source "/path/to/tdata" doctor
telecrawl --source "/path/to/tdata" import
```

## Backup

Create `https://github.com/steipete/backup-telecrawl` first, then initialize:

```bash
telecrawl backup init
telecrawl backup push
```

The default backup config points at:

```json
{
  "repo": "~/Projects/backup-telecrawl",
  "remote": "https://github.com/steipete/backup-telecrawl.git",
  "identity": "~/.telecrawl/age.key"
}
```

Use a different repository or config path:

```bash
telecrawl backup init \
  --config ~/.telecrawl/backup.json \
  --repo ~/Projects/backup-telecrawl \
  --remote https://github.com/steipete/backup-telecrawl.git
```

Inspect backup metadata:

```bash
telecrawl backup status
```

Restore into the current archive DB:

```bash
telecrawl backup pull
telecrawl status
```

Restore into a throwaway DB for validation:

```bash
telecrawl --db /tmp/telecrawl-restore-test.db backup pull
telecrawl --db /tmp/telecrawl-restore-test.db status
```

## Backup Security Model

Backup shards are JSONL, gzip-compressed with deterministic gzip metadata, and
encrypted with age before Git sees them.

Git can still see cleartext metadata:

- export time
- public age recipients
- table names
- row counts
- shard paths
- encrypted byte sizes
- plaintext shard hashes
- backup cadence and which encrypted shards changed

Git cannot read message text, chat names, sender names, or media metadata without
an age identity.

Keep `~/.telecrawl/age.key` private. If you lose it and no other recipient can
decrypt the backup, the encrypted backup cannot be restored.

## Multi-Machine Backups

On another machine:

```bash
telecrawl backup init --no-push
cat ~/.telecrawl/backup.json
```

Copy that machine's public `recipient` into the first machine's
`~/.telecrawl/backup.json`, then re-encrypt current shards:

```bash
telecrawl backup push
```

The private `AGE-SECRET-KEY-...` identity must not be committed or shared.

## Reset

Remove local state:

```bash
rm -rf ~/.telecrawl
```

Remove only the archive:

```bash
rm -f ~/.telecrawl/telecrawl.db ~/.telecrawl/telecrawl.db-*
```

Do not delete `~/.telecrawl/age.key` unless you have another working backup
recipient or you no longer need to restore existing encrypted backups.
