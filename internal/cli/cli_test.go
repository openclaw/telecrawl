package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/telecrawl/internal/store"
	"github.com/openclaw/telecrawl/internal/telegramdesktop"
)

func TestDepsInstallPackagesKeepTDataPathIndependent(t *testing.T) {
	got := depsInstallPackages()
	want := []string{"opentele2", "telethon>=1.43.2"}
	if !slices.Equal(got, want) {
		t.Fatalf("deps = %v, want %v", got, want)
	}
	if slices.Contains(got, "pycryptodomex") || slices.Contains(got, "sqlcipher3") {
		t.Fatalf("tdata deps should not require Postbox packages: %v", got)
	}
	if want := []string{"pycryptodomex", "sqlcipher3", "telethon>=1.43.2"}; !slices.Equal(postboxDepsInstallPackages(), want) {
		t.Fatalf("postbox deps = %v, want %v", postboxDepsInstallPackages(), want)
	}
}

func TestStoreImportResultUpsertsReturnedAccountScopedChats(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "telecrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	full := accountScopedImportResult("old")
	if err := storeImportResult(ctx, st, &full, ""); err != nil {
		t.Fatal(err)
	}
	partial := accountScopedImportResult("new")
	if err := storeImportResult(ctx, st, &partial, "100"); err != nil {
		t.Fatal(err)
	}

	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Chats != 2 || status.Messages != 2 {
		t.Fatalf("status = chats %d messages %d, want 2/2", status.Chats, status.Messages)
	}
	messages, err := st.Messages(ctx, store.MessageFilter{Limit: 10, Asc: true})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{messages[0].Text, messages[1].Text}
	want := []string{"new a", "new b"}
	if !slices.Equal(got, want) {
		t.Fatalf("messages = %v, want %v", got, want)
	}
}

func TestStoreImportResultPreservesArchivedMediaOnReimport(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "telecrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Unix(1_800_000_000, 0).UTC()
	archivedPath := filepath.Join(t.TempDir(), "media", "abc")
	if err := os.MkdirAll(filepath.Dir(archivedPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivedPath, []byte("archived"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: "postbox", StartedAt: now, FinishedAt: now},
		Chats: []store.Chat{{JID: "100", Kind: "chat", Name: "saved media", LastMessageAt: now, MessageCount: 1}},
		Messages: []store.Message{{
			SourcePK:  9,
			ChatJID:   "100",
			ChatName:  "saved media",
			MessageID: "0:9",
			Timestamp: now,
			MediaType: "photo",
			MediaPath: archivedPath,
			MediaSize: 123,
		}},
	}
	if err := storeImportResult(ctx, st, &first, ""); err != nil {
		t.Fatal(err)
	}

	second := telegramdesktop.ImportResult{
		Stats: first.Stats,
		Chats: first.Chats,
		Messages: []store.Message{{
			SourcePK:  9,
			ChatJID:   "100",
			ChatName:  "saved media",
			MessageID: "0:9",
			Timestamp: now,
		}},
	}
	if err := storeImportResult(ctx, st, &second, ""); err != nil {
		t.Fatal(err)
	}
	if second.Stats.MediaMessages != 1 || second.Stats.MediaFiles != 1 || second.Stats.MediaBytes != 123 {
		t.Fatalf("refreshed stats = %+v, want preserved media stats", second.Stats)
	}

	messages, err := st.Messages(ctx, store.MessageFilter{HasMedia: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if messages[0].MediaPath != archivedPath || messages[0].MediaSize != 123 {
		t.Fatalf("media ref = path %q size %d, want %q/123", messages[0].MediaPath, messages[0].MediaSize, archivedPath)
	}
	if messages[0].MediaType != "photo" {
		t.Fatalf("media type = %q, want preserved photo", messages[0].MediaType)
	}
	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.MediaMessages != 1 {
		t.Fatalf("media_messages = %d, want 1", status.MediaMessages)
	}

	otherSource := telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: "other-postbox", StartedAt: now, FinishedAt: now},
		Chats: first.Chats,
		Messages: []store.Message{{
			SourcePK:  9,
			ChatJID:   "100",
			ChatName:  "saved media",
			MessageID: "0:9",
			Timestamp: now,
			MediaType: "photo",
		}},
	}
	if err := storeImportResult(ctx, st, &otherSource, ""); err != nil {
		t.Fatal(err)
	}
	messages, err = st.Messages(ctx, store.MessageFilter{HasMedia: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages after source switch = %d, want 1", len(messages))
	}
	if messages[0].MediaPath != "" || messages[0].MediaSize != 0 {
		t.Fatalf("media ref crossed source boundary: path %q size %d", messages[0].MediaPath, messages[0].MediaSize)
	}
}

func TestPrintImportStatsIncludesMediaArchiveStats(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	now := time.Unix(1_800_000_000, 0).UTC()
	r := &runtime{stdout: &out}

	if err := r.print(store.ImportStats{
		SourcePath:    "postbox",
		DBPath:        "/tmp/telecrawl.db",
		Chats:         2,
		Messages:      3,
		MediaMessages: 2,
		MediaFiles:    1,
		MediaBytes:    1234,
		StartedAt:     now,
		FinishedAt:    now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"media_files: 1\n", "media_bytes: 1234\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "remote_media_downloads:") || strings.Contains(out.String(), "remote_media_missing:") {
		t.Fatalf("zero remote media stats should be omitted:\n%s", out.String())
	}
}

func TestPrintImportStatsIncludesRemoteMediaWhenUsed(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	now := time.Unix(1_800_000_000, 0).UTC()
	r := &runtime{stdout: &out}

	if err := r.print(store.ImportStats{
		SourcePath:             "postbox",
		DBPath:                 "/tmp/telecrawl.db",
		RemoteMediaCandidates:  4,
		RemoteMediaAttempted:   3,
		RemoteMediaDownloads:   2,
		RemoteMediaMissing:     1,
		RemoteMediaUnavailable: 1,
		RemoteMediaTimeouts:    0,
		RemoteMediaErrors:      0,
		StartedAt:              now,
		FinishedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"remote_media_candidates: 4\n",
		"remote_media_attempted: 3\n",
		"remote_media_downloads: 2\n",
		"remote_media_missing: 1\n",
		"remote_media_unavailable: 1\n",
		"remote_media_timeouts: 0\n",
		"remote_media_errors: 0\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestPrintImportStatsIncludesRemoteMediaDiagnosticsWithoutDownloads(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	now := time.Unix(1_800_000_000, 0).UTC()
	r := &runtime{stdout: &out}

	if err := r.print(store.ImportStats{
		SourcePath:             "postbox",
		DBPath:                 "/tmp/telecrawl.db",
		RemoteMediaCandidates:  4,
		RemoteMediaAttempted:   4,
		RemoteMediaUnavailable: 4,
		StartedAt:              now,
		FinishedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"remote_media_candidates: 4\n",
		"remote_media_attempted: 4\n",
		"remote_media_downloads: 0\n",
		"remote_media_missing: 0\n",
		"remote_media_unavailable: 4\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestUsageDocumentsMediaFetchOptIn(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	printUsage(&out)
	if !strings.Contains(out.String(), "--fetch-media") {
		t.Fatalf("usage should document media fetch opt-in:\n%s", out.String())
	}
}

func accountScopedImportResult(label string) telegramdesktop.ImportResult {
	now := time.Unix(1_800_000_000, 0).UTC()
	return telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: "postbox", StartedAt: now, FinishedAt: now},
		Chats: []store.Chat{
			{JID: "111", Kind: "chat", Name: "account a", LastMessageAt: now, MessageCount: 1},
			{JID: "222", Kind: "chat", Name: "account b", LastMessageAt: now, MessageCount: 1},
		},
		Messages: []store.Message{
			{SourcePK: 1, ChatJID: "111", ChatName: "account a", MessageID: "0:1", Timestamp: now, Text: label + " a"},
			{SourcePK: 2, ChatJID: "222", ChatName: "account b", MessageID: "0:1", Timestamp: now, Text: label + " b"},
		},
	}
}
