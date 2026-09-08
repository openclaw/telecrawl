package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	ckbackup "github.com/openclaw/crawlkit/backup"
	"github.com/openclaw/telecrawl/internal/store"
)

func TestZeroCountPublicationTransitions(t *testing.T) {
	ctx := context.Background()
	opts := zeroCountOptions(t)
	st := openFixtureStore(t, "source.db")
	if err := os.WriteFile(filepath.Join(opts.Repo, "notes.txt"), []byte("unrelated staged fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, opts.Repo, "add", "--", "notes.txt")
	staged := zeroCountGit(t, opts.Repo, "diff", "--cached", "--binary")
	var prior ckbackup.Manifest
	for step, counts := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}, {0, 0}} {
		messages, revisions := counts[0], counts[1]
		t.Run(fmt.Sprintf("step-%d-messages-%d-revisions-%d", step, messages, revisions), func(t *testing.T) {
			if err := st.RestoreSnapshot(ctx, zeroCountData(messages, revisions), "/fixture", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			// Archive restore seeds message baselines independently of backup.
			exported, err := st.ExportAll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantRevisions := len(exported.Revisions)
			result, err := Push(ctx, st, opts)
			if err != nil || !result.Changed {
				t.Fatalf("transition push: %+v, %v", result, err)
			}
			current := zeroCountManifest(t, opts, messages, wantRevisions)
			currentPaths := map[string]bool{}
			for _, shard := range current.Shards {
				currentPaths[shard.Path] = true
			}
			for _, old := range prior.Shards {
				if !currentPaths[old.Path] {
					if _, err := scopeGit(ctx, opts.Repo, "cat-file", "-e", "HEAD:"+old.Path); err == nil {
						t.Fatalf("obsolete managed path remains committed: %s", old.Path)
					}
				}
			}
			zeroCountStable(t, st, opts, messages, wantRevisions)
			if got := zeroCountGit(t, opts.Repo, "diff", "--cached", "--binary"); got != staged {
				t.Fatal("backup changed unrelated staged content")
			}
			target := openFixtureStore(t, "restored.db")
			if err := target.RestoreSnapshot(ctx, zeroCountData(1, 1), "/fixture", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			zeroCountPull(t, target, opts)
			got, err := target.ExportAll(ctx)
			if err != nil || len(got.Messages) != messages || len(got.Revisions) != wantRevisions || got.SourceIdentity != "" {
				t.Fatalf("restore: messages=%d revisions=%d identity=%q err=%v", len(got.Messages), len(got.Revisions), got.SourceIdentity, err)
			}
			prior = current
		})
	}
}

func TestZeroCountIndependentTables(t *testing.T) {
	for _, counts := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		messages, revisions := counts[0], counts[1]
		t.Run(fmt.Sprintf("messages-%d-revisions-%d", messages, revisions), func(t *testing.T) {
			ctx := context.Background()
			opts := zeroCountOptions(t)
			cfg, err := ResolveOptions(opts)
			if err != nil {
				t.Fatal(err)
			}
			data := zeroCountData(messages, revisions)
			var old Manifest
			var published map[string]string
			for attempt := 0; attempt < 3; attempt++ {
				manifest, err := writeSnapshot(ctx, cfg, data, old)
				if err != nil {
					t.Fatal(err)
				}
				changed, err := commitAndPush(ctx, cfg, "test: independent empty tables", false, toCrawlkitManifest(old), toCrawlkitManifest(manifest))
				if err != nil || changed != (attempt == 0) {
					t.Fatalf("attempt=%d changed=%v err=%v", attempt, changed, err)
				}
				zeroCountManifest(t, opts, messages, revisions)
				got, err := readSnapshot(cfg, manifest)
				if err != nil || len(got.Messages) != messages || len(got.Revisions) != revisions {
					t.Fatalf("decoded messages=%d revisions=%d err=%v", len(got.Messages), len(got.Revisions), err)
				}
				current := zeroCountPublished(t, opts)
				if attempt != 0 && !reflect.DeepEqual(published, current) {
					t.Fatal("unchanged writer altered the committed snapshot")
				}
				published, old = current, manifest
			}
		})
	}
}

func TestZeroCountOptionalMetadata(t *testing.T) {
	ctx := context.Background()
	opts := zeroCountOptions(t)
	st := openFixtureStore(t, "source.db")
	for _, identity := range []string{"", "test:zero-counts", ""} {
		data := store.SnapshotData{SourceIdentity: identity}
		if err := st.RestoreSnapshot(ctx, data, "/fixture", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if _, err := Push(ctx, st, opts); err != nil {
			t.Fatal(err)
		}
		zeroCountStable(t, st, opts, 0, 0)
		manifest := zeroCountManifest(t, opts, 0, 0)
		count, present := manifest.Counts["archive_metadata"]
		if present != (identity != "") || (present && count != 1) {
			t.Fatalf("identity=%q raw metadata count=%d present=%v", identity, count, present)
		}
		shards := 0
		for _, shard := range manifest.Shards {
			if shard.Table == "archive_metadata" {
				shards++
				if identity == "" || shard.Rows != 1 {
					t.Fatalf("unexpected optional metadata shard: %+v", shard)
				}
			}
		}
		if (identity == "" && shards != 0) || (identity != "" && shards != 1) {
			t.Fatalf("identity=%q metadata shards=%d", identity, shards)
		}
		target := openFixtureStore(t, "restored.db")
		zeroCountPull(t, target, opts)
		got, err := target.ExportAll(ctx)
		if err != nil || got.SourceIdentity != identity {
			t.Fatalf("restored identity=%q want=%q err=%v", got.SourceIdentity, identity, err)
		}
	}
}

func zeroCountData(messages, revisions int) store.SnapshotData {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	data := store.SnapshotData{}
	if messages != 0 {
		data.Chats = []store.Chat{{JID: "chat", Kind: "dm"}}
		data.Messages = []store.Message{{
			SourcePK: 1, EventID: "fixture-message", ChatJID: "chat", MessageID: "one",
			Timestamp: now, Text: "fixture",
		}}
	}
	if revisions != 0 {
		data.Revisions = []store.MessageRevision{{
			EventID: "fixture-revision", MessageEventID: "fixture-message", EventType: "edit",
			PayloadJSON: `{"text":"previous fixture"}`, EventAt: now, ObservedAt: now,
			EventSource: "fixture", Reason: "fixture edit",
		}}
	}
	return data
}

func zeroCountOptions(t *testing.T) Options {
	t.Helper()
	cfg := Config{Repo: filepath.Join(t.TempDir(), "repo"), Identity: filepath.Join(t.TempDir(), "age.key")}
	if err := os.Mkdir(cfg.Repo, 0o700); err != nil {
		t.Fatal(err)
	}
	recipient, err := EnsureIdentity(cfg.Identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Recipients = []string{recipient}
	configPath := filepath.Join(t.TempDir(), "backup.json")
	if err := SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	runGit(t, cfg.Repo, "init", "-b", "main")
	return Options{ConfigPath: configPath, Repo: cfg.Repo}
}

func zeroCountGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	data, err := scopeGit(context.Background(), repo, args...)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func zeroCountManifest(t *testing.T, opts Options, messages, revisions int) ckbackup.Manifest {
	t.Helper()
	cfg, err := ResolveOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ckbackup.ReadManifest(cfg.Repo)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int{"messages": messages, "message_revisions": revisions} {
		if got, ok := manifest.Counts[key]; !ok || got != want {
			t.Fatalf("raw count %s = %d, present=%v; want %d", key, got, ok, want)
		}
		seen := 0
		for _, shard := range manifest.Shards {
			if shard.Table != key {
				continue
			}
			seen++
			if want == 0 {
				plain, err := ckbackup.DecryptShardFile(crawlkitConfig(cfg), shard)
				if err != nil || len(plain) != 0 || shard.Rows != 0 || shard.Bytes <= 0 || shard.SHA256 != ckbackup.SHA256Hex(nil) {
					t.Fatalf("invalid typed empty %s shard: %+v, %v", key, shard, err)
				}
			}
		}
		if seen != 1 {
			t.Fatalf("%s shards = %d, want one", key, seen)
		}
	}
	return manifest
}

func zeroCountPublished(t *testing.T, opts Options) map[string]string {
	t.Helper()
	cfg, err := ResolveOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ckbackup.ReadManifest(cfg.Repo)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{"git-head": zeroCountGit(t, cfg.Repo, "rev-parse", "HEAD")}
	paths := []string{"manifest.json"}
	for _, shard := range manifest.Shards {
		paths = append(paths, shard.Path)
	}
	for _, file := range manifest.Files {
		paths = append(paths, file.Shard)
	}
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(cfg.Repo, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		out[path] = string(data)
	}
	return out
}

func zeroCountStable(t *testing.T, st *store.Store, opts Options, messages, revisions int) {
	t.Helper()
	before := zeroCountPublished(t, opts)
	for push := 2; push <= 3; push++ {
		result, err := Push(context.Background(), st, opts)
		if err != nil || result.Changed {
			t.Fatalf("unchanged push %d: %+v, %v", push, result, err)
		}
		zeroCountManifest(t, opts, messages, revisions)
		if !reflect.DeepEqual(before, zeroCountPublished(t, opts)) {
			t.Fatalf("push %d changed manifest timestamp/bytes, ciphertext references/bytes or Git HEAD", push)
		}
	}
}

func zeroCountPull(t *testing.T, target *store.Store, opts Options) {
	t.Helper()
	opts.Restore = true
	if _, err := Pull(context.Background(), target, opts); err != nil {
		t.Fatal(err)
	}
}
