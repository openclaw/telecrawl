package backup

func ownedBackupReadme(content []byte) bool {
	return string(content) == backupReadme || string(content) == legacyBackupReadme
}

// Exact static template from v0.3.7, commit ba04e38f575df4c5388bf2b6714ef752d74ba47a,
// internal/backup/backup.go blob fc5cc354efab323db05e0c0b2ee39445908823c4.
// Historical bytes are recognized, not installed or rewritten.
const legacyBackupReadme = `# backup-telecrawl

Encrypted Git backup for a local telecrawl archive.

This repository is written by ` + "`telecrawl backup push`" + `. It is safe to keep on
GitHub because the archive payload is encrypted before Git sees it.

## Layout

` + "```text" + `
README.md
manifest.json
data/chats.jsonl.gz.age
data/contacts.jsonl.gz.age
data/folders.jsonl.gz.age
data/folder_chats.jsonl.gz.age
data/groups.jsonl.gz.age
data/group_participants.jsonl.gz.age
data/topics.jsonl.gz.age
data/messages/YYYY/MM.jsonl.gz.age
data/message_revisions/YYYY/MM.jsonl.gz.age
data/archive_metadata.jsonl.gz.age  # present when a Telegram source identity is known
` + "```" + `

` + "`manifest.json`" + ` is cleartext and contains format version, export time,
public age recipients, table counts, shard paths, encrypted byte sizes, and
plaintext hashes used for restore verification. Message text, contacts, chat
names, participant IDs, and media metadata stay inside encrypted ` + "`*.jsonl.gz.age`" + ` shards.

## Security Model

Shard contents are JSONL, gzip-compressed with a fixed gzip timestamp, and
encrypted with age for every configured public recipient. The local
` + "`~/.telecrawl/age.key`" + ` identity is required to decrypt.

Git can still see manifest metadata: export time, public recipients, table
names, row counts, shard paths, encrypted byte sizes, plaintext shard hashes,
backup cadence, and which encrypted shards changed. Git cannot read message
text, contacts, chat names, participant IDs, or media metadata without an age
identity.

Anyone who can push to this repository can replace encrypted backup data with
different data encrypted to your public recipient. Keep repository write access
restricted and review unexpected backup commits. If an age identity is
compromised, remove its public recipient and push a new backup; old Git history
may still contain shards decryptable by the compromised key.

## Push

` + "```bash" + `
telecrawl backup push
telecrawl backup push --tag snapshot/before-migration
` + "```" + `

The command pulls/rebases this checkout, refreshes the local telecrawl archive
according to the normal sync policy, writes encrypted shards, updates the
manifest, commits, and pushes this repository.

Every changed backup is a Git commit. Optional tags name important checkpoints;
tag names are visible Git metadata and should not contain sensitive text.

## Restore

` + "```bash" + `
telecrawl backup pull
telecrawl backup pull --restore
telecrawl backup snapshots
telecrawl --db /tmp/telecrawl-history.db backup pull --restore --ref snapshot/before-migration
` + "```" + `

` + "`backup pull`" + ` decrypts every shard with the local age identity, verifies the
manifest hashes, validates the snapshot, and merges it into the configured
telecrawl archive database. Destination-only rows and tombstones remain until
an explicit ` + "`--restore`" + ` exactly replaces them. Historical refs require restore mode
and are read directly from Git objects without changing this checkout's current branch.

## Recovery

Install telecrawl, clone this repo to the path in ` + "`~/.telecrawl/backup.json`" + `,
restore the local age identity file, then run:

` + "```bash" + `
telecrawl backup pull
telecrawl status
` + "```" + `

Do not commit the age identity. Only public ` + "`age1...`" + ` recipients belong in
config; ` + "`AGE-SECRET-KEY-...`" + ` values must stay local or in a password manager.
`
