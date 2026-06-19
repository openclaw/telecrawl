package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	ckbackup "github.com/openclaw/crawlkit/backup"
	"github.com/openclaw/crawlkit/mirror"
	"github.com/openclaw/telecrawl/internal/store"
)

func TestEncryptedBackupPushPull(t *testing.T) {
	ctx := context.Background()
	source := openFixtureStore(t, "source.db")
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	data := store.SnapshotData{
		Contacts:     []store.Contact{{JID: "alice@s.whatsapp.net", FullName: "Alice", UpdatedAt: now}},
		Chats:        []store.Chat{{JID: "chat@g.us", Kind: "group", Name: "Launch Group", LastMessageAt: now}},
		Groups:       []store.Group{{JID: "chat@g.us", Name: "Launch Group", OwnerJID: "owner@s.whatsapp.net", CreatedAt: now}},
		Participants: []store.GroupParticipant{{GroupJID: "chat@g.us", UserJID: "alice@s.whatsapp.net", ContactName: "Alice", IsAdmin: true, IsActive: true}},
		Messages: []store.Message{
			{SourcePK: 1, ChatJID: "chat@g.us", ChatName: "Launch Group", MessageID: "a", SenderJID: "alice@s.whatsapp.net", SenderName: "Alice", Timestamp: now, Text: "secret launch text", RawType: 0, MessageType: "text"},
		},
	}
	if err := source.ImportSnapshot(ctx, data, "/fixture", now); err != nil {
		t.Fatal(err)
	}

	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	repo := filepath.Join(t.TempDir(), "backup")
	identity := filepath.Join(t.TempDir(), "age.key")
	configPath := filepath.Join(t.TempDir(), "backup.json")
	cfg, recipient, err := Init(ctx, Options{ConfigPath: configPath, Repo: repo, Remote: remote, Identity: identity, Push: false})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != repo || !strings.HasPrefix(recipient, "age1") {
		t.Fatalf("unexpected init cfg=%+v recipient=%q", cfg, recipient)
	}
	opts := Options{ConfigPath: configPath, Push: false}
	result, err := Push(ctx, source, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Messages != 1 || result.Shards == 0 {
		t.Fatalf("unexpected push result: %+v", result)
	}
	second, err := Push(ctx, source, opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed {
		t.Fatalf("second push should be unchanged: %+v", second)
	}
	status, statusRepo, err := Status(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if statusRepo != repo || status.Counts.Messages != 1 {
		t.Fatalf("unexpected backup status repo=%s status=%+v", statusRepo, status)
	}
	manifest, err := readManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Encrypted || manifest.Counts.Messages != 1 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	ciphertext, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(manifest.Shards[len(manifest.Shards)-1].Path))) // #nosec G304 -- test reads a generated shard path from its temp repo manifest.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "secret launch text") {
		t.Fatal("encrypted shard contains plaintext")
	}

	restored := openFixtureStore(t, "restored.db")
	pulled, err := Pull(ctx, restored, opts)
	if err != nil {
		t.Fatal(err)
	}
	if pulled.Messages != 1 {
		t.Fatalf("unexpected pull result: %+v", pulled)
	}
	results, err := restored.Search(ctx, store.MessageFilter{Query: "secret", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Text != "secret launch text" {
		t.Fatalf("restore search mismatch: %+v", results)
	}

	secondIdentity := filepath.Join(t.TempDir(), "second-age.key")
	secondRecipient, err := EnsureIdentity(secondIdentity)
	if err != nil {
		t.Fatal(err)
	}
	updatedCfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	updatedCfg.Recipients = append(updatedCfg.Recipients, secondRecipient)
	if err := SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatal(err)
	}
	recipientChange, err := Push(ctx, source, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !recipientChange.Changed {
		t.Fatal("adding a recipient should re-encrypt unchanged shards")
	}
	secondRestored := openFixtureStore(t, "second-restored.db")
	secondPulled, err := Pull(ctx, secondRestored, Options{ConfigPath: configPath, Identity: secondIdentity})
	if err != nil {
		t.Fatal(err)
	}
	if secondPulled.Messages != 1 {
		t.Fatalf("unexpected second-recipient pull result: %+v", secondPulled)
	}
	secondResults, err := secondRestored.Search(ctx, store.MessageFilter{Query: "secret", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondResults) != 1 || secondResults[0].Text != "secret launch text" {
		t.Fatalf("second-recipient restore mismatch: %+v", secondResults)
	}
	sameRecipients, err := Push(ctx, source, opts)
	if err != nil {
		t.Fatal(err)
	}
	if sameRecipients.Changed {
		t.Fatalf("unchanged recipients should not rewrite backup: %+v", sameRecipients)
	}

	derivedRepo := filepath.Join(t.TempDir(), "derived-recipient")
	if err := os.MkdirAll(derivedRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, derivedRepo, "init")
	derived, err := Push(ctx, source, Options{Repo: derivedRepo, Identity: identity, Push: false})
	if err != nil {
		t.Fatal(err)
	}
	if !derived.Changed || derived.Messages != 1 {
		t.Fatalf("unexpected derived-recipient push: %+v", derived)
	}

	data.Messages = append(data.Messages, store.Message{SourcePK: 2, ChatJID: "chat@g.us", ChatName: "Launch Group", MessageID: "b", SenderJID: "alice@s.whatsapp.net", SenderName: "Alice", Timestamp: now.Add(time.Second), Text: "second secret", RawType: 0, MessageType: "text"})
	if err := source.ImportSnapshot(ctx, data, "/fixture", now); err != nil {
		t.Fatal(err)
	}
	pushed, err := Push(ctx, source, Options{ConfigPath: configPath, Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if !pushed.Changed || pushed.Messages != 2 {
		t.Fatalf("unexpected pushed backup: %+v", pushed)
	}
}

func TestHistoricalSnapshotRestore(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	data := store.SnapshotData{
		Chats:    []store.Chat{{JID: "chat", Kind: "dm", Name: "Chat", LastMessageAt: now}},
		Messages: []store.Message{{SourcePK: 1, ChatJID: "chat", MessageID: "one", Timestamp: now, Text: "first snapshot", RawType: 0}},
	}
	st := openFixtureStore(t, "source.db")
	if err := st.ImportSnapshot(ctx, data, "/fixture", now); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	repo := filepath.Join(t.TempDir(), "backup")
	identity := filepath.Join(t.TempDir(), "age.key")
	configPath := filepath.Join(t.TempDir(), "backup.json")
	if _, _, err := Init(ctx, Options{ConfigPath: configPath, Repo: repo, Remote: remote, Identity: identity, Push: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, st, Options{ConfigPath: configPath, Push: false, Tag: "snapshot/initial"}); err != nil {
		t.Fatal(err)
	}
	initial, err := resolveCommit(ctx, repo, "snapshot/initial")
	if err != nil {
		t.Fatal(err)
	}
	data.Messages = append(data.Messages, store.Message{SourcePK: 2, ChatJID: "chat", MessageID: "two", Timestamp: now.Add(time.Minute), Text: "second snapshot", RawType: 0})
	if err := st.ImportSnapshot(ctx, data, "/fixture", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, st, Options{ConfigPath: configPath, Push: true, Tag: "snapshot/current"}); err != nil {
		t.Fatal(err)
	}
	current, err := resolveCommit(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	snapshots, snapshotsRepo, err := Snapshots(ctx, Options{ConfigPath: configPath, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if snapshotsRepo != repo || len(snapshots) != 2 || snapshots[0].Ref != current || snapshots[1].Ref != initial {
		t.Fatalf("unexpected snapshots repo=%s snapshots=%+v", snapshotsRepo, snapshots)
	}
	restored := openFixtureStore(t, "restored.db")
	pulled, err := Pull(ctx, restored, Options{ConfigPath: configPath, Ref: "snapshot/initial"})
	if err != nil {
		t.Fatal(err)
	}
	if pulled.Ref != initial || pulled.Messages != 1 {
		t.Fatalf("unexpected historical restore: %+v", pulled)
	}
	after, err := resolveCommit(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if after != current {
		t.Fatalf("historical restore changed checkout from %s to %s", current, after)
	}
	if _, err := Push(ctx, st, Options{ConfigPath: configPath, Push: false, Tag: "snapshot/initial"}); err == nil {
		t.Fatal("moving an immutable snapshot tag should fail")
	}
}

func TestEmptyBackupPreservesCountsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := openFixtureStore(t, "empty.db")
	repo := filepath.Join(t.TempDir(), "backup")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	identity := filepath.Join(t.TempDir(), "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Repo: repo, Identity: identity, Recipients: []string{recipient}, Push: false}
	first, err := Push(ctx, st, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed {
		t.Fatal("first empty backup should commit")
	}
	manifest, err := ckbackup.ReadManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if messages, ok := manifest.Counts["messages"]; !ok || messages != 0 {
		t.Fatalf("empty message count missing: %+v", manifest.Counts)
	}
	second, err := Push(ctx, st, opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed {
		t.Fatal("identical empty backup should not commit")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.json")
	cfg := DefaultConfig()
	cfg.Repo = "~/Projects/example"
	cfg.Recipients = []string{"age1example"}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Repo != cfg.Repo || loaded.Recipients[0] != "age1example" {
		t.Fatalf("config mismatch: %+v", loaded)
	}
	if DefaultConfigPath() == "" {
		t.Fatal("default config path should not be empty")
	}
	if expandHome("~") == "~" || !strings.Contains(expandHome("~/Projects/example"), "Projects") {
		t.Fatal("home expansion did not expand")
	}
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(t.TempDir()); err == nil {
		t.Fatal("expected directory config load error")
	}
	if err := SaveConfig(t.TempDir(), cfg); err == nil {
		t.Fatal("expected directory config save error")
	}
}

func TestCryptoHelpers(t *testing.T) {
	identity := filepath.Join(t.TempDir(), "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	again, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	if again != recipient {
		t.Fatalf("recipient changed: %q != %q", again, recipient)
	}
	fromIdentity, err := RecipientFromIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	if fromIdentity != recipient {
		t.Fatalf("recipient mismatch: %q != %q", fromIdentity, recipient)
	}
	encrypted, hash, err := ckbackup.EncryptShard([]byte("private text\n"), []string{recipient})
	if err != nil {
		t.Fatal(err)
	}
	if hash != ckbackup.SHA256Hex([]byte("private text\n")) || strings.Contains(string(encrypted), "private text") {
		t.Fatal("encrypted shard mismatch")
	}
	tmp := filepath.Join(t.TempDir(), "shard.age")
	if err := os.WriteFile(tmp, encrypted, 0o600); err != nil {
		t.Fatal(err)
	}
	plain, err := ckbackup.DecryptShard(encrypted, identity)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "private text\n" {
		t.Fatalf("decrypt mismatch: %q", plain)
	}
	if _, _, err := ckbackup.EncryptShard([]byte("x"), []string{"bad"}); err == nil {
		t.Fatal("expected bad recipient error")
	}
	if _, _, err := ckbackup.EncryptShard([]byte("x"), nil); err == nil {
		t.Fatal("expected missing recipient encrypt error")
	}
	emptyIdentity := filepath.Join(t.TempDir(), "empty.key")
	if err := os.WriteFile(emptyIdentity, []byte("# comment\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecipientFromIdentity(emptyIdentity); err == nil {
		t.Fatal("expected empty identity error")
	}
	badIdentity := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(badIdentity, []byte("bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureIdentity(badIdentity); err == nil {
		t.Fatal("expected bad existing identity error")
	}
	if _, err := RecipientFromIdentity(filepath.Join(t.TempDir(), "missing.key")); err == nil {
		t.Fatal("expected missing identity error")
	}
	if _, err := RecipientFromIdentity(badIdentity); err == nil {
		t.Fatal("expected bad identity parse error")
	}
	if _, err := ckbackup.DecryptShard([]byte("not age"), identity); err == nil {
		t.Fatal("expected bad ciphertext error")
	}
	otherIdentity := filepath.Join(t.TempDir(), "other.key")
	if _, err := EnsureIdentity(otherIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err := ckbackup.DecryptShard(encrypted, otherIdentity); err == nil {
		t.Fatal("expected wrong identity decrypt error")
	}
	recipientValue, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		t.Fatal(err)
	}
	var rawAge bytes.Buffer
	w, err := age.Encrypt(&rawAge, recipientValue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("not gzip")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ckbackup.DecryptShard(rawAge.Bytes(), identity); err == nil {
		t.Fatal("expected non-gzip decrypt error")
	}
	if _, err := EnsureIdentity(filepath.Join(t.TempDir(), "missing", "dir")); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotErrorAndUtilityPaths(t *testing.T) {
	if _, _, err := ckbackup.EncodeJSONL(1); err == nil {
		t.Fatal("expected unsupported JSONL row type")
	}
	var contacts []store.Contact
	if err := ckbackup.DecodeJSONL([]byte("{bad json}\n"), &contacts); err == nil {
		t.Fatal("expected invalid JSONL error")
	}
	if err := ckbackup.RemoveStaleShards(t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if ckbackup.EquivalentManifest(toCrawlkitManifest(Manifest{Format: 1}), toCrawlkitManifest(Manifest{Format: 2})) {
		t.Fatal("different manifests should not be equivalent")
	}
	if _, err := readSnapshot(Config{}, Manifest{Format: 99}); err == nil {
		t.Fatal("expected unsupported format error")
	}
	if _, err := readSnapshot(Config{}, Manifest{Format: formatVersion, Shards: []ShardEntry{{Table: "nope"}}}); err == nil {
		t.Fatal("expected shard read error")
	}
	identity := filepath.Join(t.TempDir(), "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if _, err := ckbackup.ResolveShardPath(repo, "../outside.age"); err == nil {
		t.Fatal("expected escaping shard path error")
	}
	if _, err := ckbackup.ResolveShardPath(repo, "manifest.json"); err == nil {
		t.Fatal("expected invalid shard path error")
	}
	encrypted, hash, err := ckbackup.EncryptShard([]byte("{}\n"), []string{recipient})
	if err != nil {
		t.Fatal(err)
	}
	shardPath := filepath.Join("data", "unknown.jsonl.gz.age")
	fullShardPath := filepath.Join(repo, shardPath)
	if err := os.MkdirAll(filepath.Dir(fullShardPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullShardPath, encrypted, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repo: repo, Identity: identity}
	unknownManifest := Manifest{Format: formatVersion, Shards: []ShardEntry{{Table: "unknown", Path: filepath.ToSlash(shardPath), SHA256: hash}}}
	if _, err := readSnapshot(cfg, unknownManifest); err == nil {
		t.Fatal("expected unknown table error")
	}
	badHashManifest := Manifest{Format: formatVersion, Shards: []ShardEntry{{Table: "contacts", Path: filepath.ToSlash(shardPath), SHA256: "bad"}}}
	if _, err := readSnapshot(cfg, badHashManifest); err == nil {
		t.Fatal("expected hash mismatch")
	}
	duplicatePlain, duplicateHash, err := ckbackup.EncryptShard([]byte(`{"source_pk":1,"chat_jid":"chat","message_id":"a","timestamp":"2026-04-27T12:00:00Z","raw_type":0}`+"\n"+`{"source_pk":1,"chat_jid":"chat","message_id":"b","timestamp":"2026-04-27T12:00:01Z","raw_type":0}`+"\n"), []string{recipient})
	if err != nil {
		t.Fatal(err)
	}
	duplicatePath := filepath.Join("data", "duplicate.jsonl.gz.age")
	fullDuplicatePath := filepath.Join(repo, duplicatePath)
	if err := os.WriteFile(fullDuplicatePath, duplicatePlain, 0o600); err != nil {
		t.Fatal(err)
	}
	duplicateManifest := Manifest{Format: formatVersion, Shards: []ShardEntry{{Table: "messages", Path: filepath.ToSlash(duplicatePath), SHA256: duplicateHash}}}
	duplicateData, err := readSnapshot(cfg, duplicateManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := duplicateData.Validate(); err == nil {
		t.Fatal("expected duplicate restored data validation error")
	}
	if err := ckbackup.WriteManifest(repo, toCrawlkitManifest(Manifest{Format: formatVersion})); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(filepath.Join(repo, "missing")); err == nil {
		t.Fatal("expected missing manifest error")
	}
	unknown := store.Message{SourcePK: 1, ChatJID: "chat", MessageID: "a"}
	shards := messageShards([]store.Message{unknown})
	if len(shards) != 1 || !strings.Contains(shards[0].path, "unknown") {
		t.Fatalf("unexpected unknown-time shard: %+v", shards)
	}
	stalePath := filepath.Join(repo, "data", "stale.age")
	if err := os.WriteFile(stalePath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ckbackup.RemoveStaleShards(repo, []ShardEntry{{Path: filepath.ToSlash(shardPath)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatal("expected stale shard removal")
	}
}

func TestGitHelpersWithoutRemote(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	cfg := Config{Repo: repo}
	if err := ensureRepo(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	changed, err := commitAndPush(ctx, cfg, "test: no changes", false)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("empty repo without changes should not commit")
	}
}

func TestEnsureRepoFallsBackToLocalInitWhenCloneFails(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "backup")
	remote := filepath.Join(t.TempDir(), "missing.git")
	if err := ensureRepo(ctx, Config{Repo: repo, Remote: remote}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
	out, err := gitOutput(ctx, repo, "remote", "get-url", "origin")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != remote {
		t.Fatalf("origin = %q, want %q", strings.TrimSpace(string(out)), remote)
	}
}

func TestBackupReadmeUsesSupportedStatusCommand(t *testing.T) {
	repo := t.TempDir()
	if err := writeBackupReadme(repo); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Contains(body, "--sync never") {
		t.Fatal("backup README contains unsupported --sync flag")
	}
	if !strings.Contains(body, "telecrawl status") {
		t.Fatal("backup README omits the supported status command")
	}
}

func TestTopLevelErrorPaths(t *testing.T) {
	ctx := context.Background()
	source := openFixtureStore(t, "source.db")
	badConfig := t.TempDir()
	if _, _, err := Init(ctx, Options{ConfigPath: badConfig}); err == nil {
		t.Fatal("expected init config load error")
	}
	if _, err := Push(ctx, source, Options{ConfigPath: badConfig}); err == nil {
		t.Fatal("expected push config load error")
	}
	if _, err := Pull(ctx, source, Options{ConfigPath: badConfig}); err == nil {
		t.Fatal("expected pull config load error")
	}
	if _, _, err := Status(ctx, Options{ConfigPath: badConfig}); err == nil {
		t.Fatal("expected status config load error")
	}

	repo := t.TempDir()
	runGit(t, repo, "init")
	if _, err := Pull(ctx, source, Options{Repo: repo, Identity: filepath.Join(t.TempDir(), "age.key")}); err == nil {
		t.Fatal("expected missing manifest pull error")
	}
	if _, _, err := Status(ctx, Options{Repo: repo}); err == nil {
		t.Fatal("expected missing manifest status error")
	}

	now := time.Now().UTC()
	if err := source.ImportSnapshot(ctx, store.SnapshotData{
		Chats:    []store.Chat{{JID: "chat", Kind: "dm", Name: "Chat", LastMessageAt: now}},
		Messages: []store.Message{{SourcePK: 1, ChatJID: "chat", MessageID: "a", Timestamp: now, RawType: 0, Text: "hello"}},
	}, "/fixture", now); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, source, Options{Repo: repo, Recipients: []string{"bad"}, Push: false}); err == nil {
		t.Fatal("expected bad recipient push error")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(t.TempDir(), "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, source, Options{Repo: repo, Identity: identity, Recipients: []string{recipient}, Push: false}); err == nil {
		t.Fatal("expected closed store push error")
	}
	if err := ensureRepo(ctx, Config{}); err == nil {
		t.Fatal("expected empty repo path error")
	}
	if _, err := commitAndPush(ctx, Config{Repo: filepath.Join(t.TempDir(), "missing")}, "test", false); err == nil {
		t.Fatal("expected commit in missing repo error")
	}
}

func openFixtureStore(t *testing.T, name string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := gitOutput(context.Background(), dir, args...); err != nil {
		t.Fatal(err)
	}
}

func resolveCommit(ctx context.Context, repo, ref string) (string, error) {
	return mirror.ResolveCommit(ctx, mirror.Options{RepoPath: repo, Branch: "main"}, ref)
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- tests pass only fixed Git commands and temporary paths.
	cmd.Dir = dir
	cmd.Env = append(
		os.Environ(),
		"GIT_AUTHOR_NAME=telecrawl-test",
		"GIT_AUTHOR_EMAIL=telecrawl-test@example.invalid",
		"GIT_COMMITTER_NAME=telecrawl-test",
		"GIT_COMMITTER_EMAIL=telecrawl-test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
