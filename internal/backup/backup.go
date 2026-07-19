package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ckbackup "github.com/openclaw/crawlkit/backup"
	"github.com/openclaw/crawlkit/mirror"
	"github.com/openclaw/telecrawl/internal/store"
)

const formatVersion = ckbackup.FormatVersion

type Manifest struct {
	Format     int          `json:"format"`
	Encrypted  bool         `json:"encrypted"`
	Exported   time.Time    `json:"exported"`
	Recipients []string     `json:"recipients,omitempty"`
	Counts     Counts       `json:"counts"`
	Shards     []ShardEntry `json:"shards"`
}

type Counts struct {
	Metadata     int `json:"archive_metadata,omitempty"`
	Contacts     int `json:"contacts"`
	Chats        int `json:"chats"`
	Folders      int `json:"folders"`
	FolderChats  int `json:"folder_chats"`
	Groups       int `json:"groups"`
	Participants int `json:"participants"`
	Topics       int `json:"topics"`
	Messages     int `json:"messages"`
	Revisions    int `json:"message_revisions"`
}

type archiveMetadata struct {
	SourceIdentity string `json:"source_identity"`
}

type ShardEntry = ckbackup.ShardEntry

type Result struct {
	Repo      string `json:"repo"`
	Changed   bool   `json:"changed"`
	Encrypted bool   `json:"encrypted"`
	Shards    int    `json:"shards"`
	Messages  int    `json:"messages"`
	Ref       string `json:"ref,omitempty"`
	Tag       string `json:"tag,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

func Init(ctx context.Context, opts Options) (Config, string, error) {
	cfg, err := ResolveOptions(opts)
	if err != nil {
		return Config{}, "", err
	}
	recipient, err := EnsureIdentity(cfg.Identity)
	if err != nil {
		return Config{}, "", err
	}
	if len(cfg.Recipients) == 0 {
		cfg.Recipients = []string{recipient}
	}
	if err := SaveConfig(opts.ConfigPath, cfg); err != nil {
		return Config{}, "", err
	}
	if err := ensureRepo(ctx, cfg); err != nil {
		return Config{}, "", err
	}
	if err := writeBackupReadme(cfg.Repo); err != nil {
		return Config{}, "", err
	}
	_, err = commitAndPush(ctx, cfg, "docs: describe encrypted telecrawl backup", opts.Push)
	return cfg, recipient, err
}

func Push(ctx context.Context, st *store.Store, opts Options) (Result, error) {
	cfg, err := ResolveOptions(opts)
	if err != nil {
		return Result{}, err
	}
	if len(cfg.Recipients) == 0 {
		recipient, err := RecipientFromIdentity(cfg.Identity)
		if err != nil {
			return Result{}, err
		}
		cfg.Recipients = []string{recipient}
	}
	if err := ensureRepo(ctx, cfg); err != nil {
		return Result{}, err
	}
	if err := validateSnapshotTag(ctx, cfg.Repo, opts.Tag); err != nil {
		return Result{}, err
	}
	if err := writeBackupReadme(cfg.Repo); err != nil {
		return Result{}, err
	}
	oldManifest, _ := readManifest(cfg.Repo)
	data, err := st.ExportAll(ctx)
	if err != nil {
		return Result{}, err
	}
	manifest, err := writeSnapshot(ctx, cfg, data, oldManifest)
	if err != nil {
		return Result{}, err
	}
	pushWithTag := opts.Push && strings.TrimSpace(opts.Tag) != ""
	changed, err := commitAndPush(ctx, cfg, "sync: update encrypted telecrawl backup", opts.Push && !pushWithTag)
	if err != nil {
		return Result{}, err
	}
	tag, err := tagSnapshot(ctx, cfg, opts.Tag)
	if err != nil {
		return Result{}, err
	}
	if pushWithTag {
		if err := mirror.PushCurrentSnapshot(ctx, mirrorOptions(cfg), tag); err != nil {
			return Result{}, err
		}
	}
	return Result{Repo: cfg.Repo, Changed: changed, Encrypted: true, Shards: len(manifest.Shards), Messages: manifest.Counts.Messages, Tag: tag}, nil
}

func Pull(ctx context.Context, st *store.Store, opts Options) (Result, error) {
	if strings.TrimSpace(opts.Ref) != "" && !opts.Restore {
		return Result{}, fmt.Errorf("backup pull --ref requires --restore because historical snapshots replace local rows")
	}
	cfg, err := ResolveOptions(opts)
	if err != nil {
		return Result{}, err
	}
	ensure := ensureRepo
	if strings.TrimSpace(opts.Ref) != "" {
		ensure = ensureRepoForRead
	}
	if err := ensure(ctx, cfg); err != nil {
		return Result{}, err
	}
	manifest, ref, err := readManifestAtRef(ctx, cfg.Repo, opts.Ref)
	if err != nil {
		return Result{}, err
	}
	var data store.SnapshotData
	if ref == "" {
		data, err = readSnapshot(cfg, manifest)
	} else {
		data, err = readSnapshotAtRef(ctx, cfg, manifest, ref)
	}
	if err != nil {
		return Result{}, err
	}
	data.NormalizeLegacyEventIDs()
	if err := data.Validate(); err != nil {
		return Result{}, err
	}
	sourcePath := "backup:" + cfg.Repo
	if ref != "" {
		sourcePath += "@" + ref
	}
	mode := "merge"
	if opts.Restore {
		mode = "restore"
		err = st.RestoreSnapshot(ctx, data, sourcePath, manifest.Exported)
	} else {
		err = st.ImportSnapshot(ctx, data, sourcePath, manifest.Exported)
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Repo: cfg.Repo, Changed: true, Encrypted: manifest.Encrypted, Shards: len(manifest.Shards), Messages: len(data.Messages), Ref: ref, Mode: mode}, nil
}

func Status(ctx context.Context, opts Options) (Manifest, string, error) {
	cfg, err := ResolveOptions(opts)
	if err != nil {
		return Manifest{}, "", err
	}
	if err := ensureRepo(ctx, cfg); err != nil {
		return Manifest{}, "", err
	}
	manifest, err := readManifest(cfg.Repo)
	if err != nil {
		return Manifest{}, "", err
	}
	return manifest, cfg.Repo, nil
}

func writeSnapshot(ctx context.Context, cfg Config, data store.SnapshotData, old Manifest) (Manifest, error) {
	shards := make([]ckbackup.Shard, 0, 9)
	if strings.TrimSpace(data.SourceIdentity) != "" {
		shards = append(shards, ckbackup.Shard{Table: "archive_metadata", Path: "data/archive_metadata.jsonl.gz.age", Rows: []archiveMetadata{{SourceIdentity: data.SourceIdentity}}})
	}
	shards = append(shards, []ckbackup.Shard{
		{Table: "contacts", Path: "data/contacts.jsonl.gz.age", Rows: data.Contacts},
		{Table: "chats", Path: "data/chats.jsonl.gz.age", Rows: data.Chats},
		{Table: "folders", Path: "data/folders.jsonl.gz.age", Rows: data.Folders},
		{Table: "folder_chats", Path: "data/folder_chats.jsonl.gz.age", Rows: data.FolderChats},
		{Table: "groups", Path: "data/groups.jsonl.gz.age", Rows: data.Groups},
		{Table: "group_participants", CountKey: "participants", Path: "data/group_participants.jsonl.gz.age", Rows: data.Participants},
		{Table: "topics", Path: "data/topics.jsonl.gz.age", Rows: data.Topics},
	}...)
	for _, shard := range messageRevisionShards(data.Revisions) {
		shards = append(shards, ckbackup.Shard{Table: "message_revisions", Path: shard.path, Rows: shard.revisions})
	}
	for _, shard := range messageShards(data.Messages) {
		shards = append(shards, ckbackup.Shard{Table: "messages", Path: shard.path, Rows: shard.messages})
	}
	sharedOld := toCrawlkitManifest(old)
	if strings.TrimSpace(data.SourceIdentity) == "" {
		delete(sharedOld.Counts, "archive_metadata")
	}
	if len(data.Messages) == 0 {
		delete(sharedOld.Counts, "messages")
	}
	if len(data.Revisions) == 0 {
		delete(sharedOld.Counts, "message_revisions")
	}
	manifest, err := ckbackup.WriteSnapshot(ctx, crawlkitConfig(cfg), shards, sharedOld)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Counts["contacts"] = len(data.Contacts)
	if strings.TrimSpace(data.SourceIdentity) != "" {
		manifest.Counts["archive_metadata"] = 1
	} else {
		delete(manifest.Counts, "archive_metadata")
	}
	manifest.Counts["chats"] = len(data.Chats)
	manifest.Counts["folders"] = len(data.Folders)
	manifest.Counts["folder_chats"] = len(data.FolderChats)
	manifest.Counts["groups"] = len(data.Groups)
	manifest.Counts["participants"] = len(data.Participants)
	manifest.Counts["topics"] = len(data.Topics)
	manifest.Counts["messages"] = len(data.Messages)
	manifest.Counts["message_revisions"] = len(data.Revisions)
	if ckbackup.EquivalentManifest(toCrawlkitManifest(old), manifest) {
		return old, nil
	}
	if err := ckbackup.WriteManifest(cfg.Repo, manifest); err != nil {
		return Manifest{}, err
	}
	return fromCrawlkitManifest(manifest), nil
}

func readSnapshot(cfg Config, manifest Manifest) (store.SnapshotData, error) {
	shards, err := ckbackup.ReadSnapshot(crawlkitConfig(cfg), toCrawlkitManifest(manifest))
	if err != nil {
		return store.SnapshotData{}, err
	}
	return decodeSnapshot(shards)
}

func decodeSnapshot(shards []ckbackup.DecodedShard) (store.SnapshotData, error) {
	var data store.SnapshotData
	for _, shard := range shards {
		switch shard.Entry.Table {
		case "archive_metadata":
			var metadata []archiveMetadata
			if err := ckbackup.DecodeJSONL(shard.Plaintext, &metadata); err != nil {
				return store.SnapshotData{}, err
			}
			if len(metadata) != 1 || strings.TrimSpace(metadata[0].SourceIdentity) == "" {
				return store.SnapshotData{}, fmt.Errorf("archive metadata must contain exactly one source identity")
			}
			data.SourceIdentity = metadata[0].SourceIdentity
		case "contacts":
			if err := ckbackup.DecodeJSONL(shard.Plaintext, &data.Contacts); err != nil {
				return store.SnapshotData{}, err
			}
		case "chats":
			if err := ckbackup.DecodeJSONL(shard.Plaintext, &data.Chats); err != nil {
				return store.SnapshotData{}, err
			}
		case "folders":
			if err := ckbackup.DecodeJSONL(shard.Plaintext, &data.Folders); err != nil {
				return store.SnapshotData{}, err
			}
		case "folder_chats":
			if err := ckbackup.DecodeJSONL(shard.Plaintext, &data.FolderChats); err != nil {
				return store.SnapshotData{}, err
			}
		case "groups":
			if err := ckbackup.DecodeJSONL(shard.Plaintext, &data.Groups); err != nil {
				return store.SnapshotData{}, err
			}
		case "group_participants":
			if err := ckbackup.DecodeJSONL(shard.Plaintext, &data.Participants); err != nil {
				return store.SnapshotData{}, err
			}
		case "topics":
			if err := ckbackup.DecodeJSONL(shard.Plaintext, &data.Topics); err != nil {
				return store.SnapshotData{}, err
			}
		case "messages":
			var messages []store.Message
			if err := ckbackup.DecodeJSONL(shard.Plaintext, &messages); err != nil {
				return store.SnapshotData{}, err
			}
			data.Messages = append(data.Messages, messages...)
		case "message_revisions":
			var revisions []store.MessageRevision
			if err := ckbackup.DecodeJSONL(shard.Plaintext, &revisions); err != nil {
				return store.SnapshotData{}, err
			}
			data.Revisions = append(data.Revisions, revisions...)
		default:
			return store.SnapshotData{}, fmt.Errorf("unknown backup table %q", shard.Entry.Table)
		}
	}
	sort.Slice(data.Messages, func(i, j int) bool {
		if data.Messages[i].Timestamp.Equal(data.Messages[j].Timestamp) {
			return data.Messages[i].SourcePK < data.Messages[j].SourcePK
		}
		return data.Messages[i].Timestamp.Before(data.Messages[j].Timestamp)
	})
	sort.Slice(data.Revisions, func(i, j int) bool {
		if data.Revisions[i].EventAt.Equal(data.Revisions[j].EventAt) {
			return data.Revisions[i].EventID < data.Revisions[j].EventID
		}
		return data.Revisions[i].EventAt.Before(data.Revisions[j].EventAt)
	})
	return data, nil
}

type messageShard struct {
	path     string
	messages []store.Message
}

type messageRevisionShard struct {
	path      string
	revisions []store.MessageRevision
}

func messageRevisionShards(revisions []store.MessageRevision) []messageRevisionShard {
	buckets := map[string][]store.MessageRevision{}
	for _, revision := range revisions {
		t := revision.EventAt.UTC()
		year, month := "unknown", "00"
		if !t.IsZero() {
			year = fmt.Sprintf("%04d", t.Year())
			month = fmt.Sprintf("%02d", int(t.Month()))
		}
		rel := fmt.Sprintf("data/message_revisions/%s/%s.jsonl.gz.age", year, month)
		buckets[rel] = append(buckets[rel], revision)
	}
	paths := make([]string, 0, len(buckets))
	for path := range buckets {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]messageRevisionShard, 0, len(paths))
	for _, path := range paths {
		values := buckets[path]
		sort.Slice(values, func(i, j int) bool {
			if values[i].EventAt.Equal(values[j].EventAt) {
				return values[i].EventID < values[j].EventID
			}
			return values[i].EventAt.Before(values[j].EventAt)
		})
		out = append(out, messageRevisionShard{path: path, revisions: values})
	}
	return out
}

func messageShards(messages []store.Message) []messageShard {
	buckets := map[string][]store.Message{}
	for _, message := range messages {
		t := message.Timestamp.UTC()
		year, month := "unknown", "00"
		if !t.IsZero() {
			year = fmt.Sprintf("%04d", t.Year())
			month = fmt.Sprintf("%02d", int(t.Month()))
		}
		rel := fmt.Sprintf("data/messages/%s/%s.jsonl.gz.age", year, month)
		buckets[rel] = append(buckets[rel], message)
	}
	paths := make([]string, 0, len(buckets))
	for path := range buckets {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]messageShard, 0, len(paths))
	for _, path := range paths {
		values := buckets[path]
		sort.Slice(values, func(i, j int) bool {
			if values[i].Timestamp.Equal(values[j].Timestamp) {
				return values[i].SourcePK < values[j].SourcePK
			}
			return values[i].Timestamp.Before(values[j].Timestamp)
		})
		out = append(out, messageShard{path: path, messages: values})
	}
	return out
}

func readManifest(repo string) (Manifest, error) {
	manifest, err := ckbackup.ReadManifest(repo)
	if err != nil {
		return Manifest{}, err
	}
	return fromCrawlkitManifest(manifest), nil
}

func crawlkitConfig(cfg Config) ckbackup.Config {
	return ckbackup.Config{Repo: cfg.Repo, Identity: cfg.Identity, Recipients: cfg.Recipients}
}

func toCrawlkitManifest(manifest Manifest) ckbackup.Manifest {
	counts := map[string]int{
		"contacts":          manifest.Counts.Contacts,
		"chats":             manifest.Counts.Chats,
		"folders":           manifest.Counts.Folders,
		"folder_chats":      manifest.Counts.FolderChats,
		"groups":            manifest.Counts.Groups,
		"participants":      manifest.Counts.Participants,
		"topics":            manifest.Counts.Topics,
		"messages":          manifest.Counts.Messages,
		"message_revisions": manifest.Counts.Revisions,
	}
	if manifest.Counts.Metadata > 0 {
		counts["archive_metadata"] = manifest.Counts.Metadata
	}
	return ckbackup.Manifest{
		Format:     manifest.Format,
		Encrypted:  manifest.Encrypted,
		Exported:   manifest.Exported,
		Recipients: manifest.Recipients,
		Counts:     counts,
		Shards:     manifest.Shards,
	}
}

func fromCrawlkitManifest(manifest ckbackup.Manifest) Manifest {
	participants := manifest.Counts["participants"]
	if participants == 0 {
		participants = manifest.Counts["group_participants"]
	}
	return Manifest{
		Format:     manifest.Format,
		Encrypted:  manifest.Encrypted,
		Exported:   manifest.Exported,
		Recipients: manifest.Recipients,
		Counts: Counts{
			Metadata:     manifest.Counts["archive_metadata"],
			Contacts:     manifest.Counts["contacts"],
			Chats:        manifest.Counts["chats"],
			Folders:      manifest.Counts["folders"],
			FolderChats:  manifest.Counts["folder_chats"],
			Groups:       manifest.Counts["groups"],
			Participants: participants,
			Topics:       manifest.Counts["topics"],
			Messages:     manifest.Counts["messages"],
			Revisions:    manifest.Counts["message_revisions"],
		},
		Shards: manifest.Shards,
	}
}

func writeBackupReadme(repo string) error {
	path := filepath.Join(repo, "README.md")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	const body = `# backup-telecrawl

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
	return os.WriteFile(path, []byte(body), 0o600)
}
