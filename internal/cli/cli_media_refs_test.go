package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/telecrawl/internal/store"
	"github.com/openclaw/telecrawl/internal/telegramdesktop"
)

func TestMediaRefCacheDoesNotRescanAfterFileRemoved(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "telecrawl.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Unix(1_800_000_000, 0).UTC()
	sourcePath := t.TempDir()
	archivedData := []byte("cached-media")
	archivedPath := writeContentAddressedMedia(t, filepath.Join(filepath.Dir(dbPath), "media"), archivedData)
	first := telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: sourcePath, SourceIdentity: "test:cache", StartedAt: now, FinishedAt: now},
		Chats: []store.Chat{{JID: "100", Kind: "chat", Name: "saved media", LastMessageAt: now, MessageCount: 1}},
		Messages: []store.Message{{
			SourcePK:   9,
			ChatJID:    "100",
			ChatName:   "saved media",
			MessageID:  "0:9",
			Timestamp:  now,
			Text:       "keep this body out of the media scan",
			MediaType:  "photo",
			MediaTitle: "cached",
			MediaPath:  archivedPath,
			MediaSize:  int64(len(archivedData)),
		}},
	}
	if err := storeImportResult(ctx, st, &first, "", false); err != nil {
		t.Fatal(err)
	}
	archivedPath = first.Messages[0].MediaPath

	cache := &mediaRefCache{}
	_, refs, err := cache.get(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if refs[9].MediaPath != archivedPath {
		t.Fatalf("first scan = %+v, want path %q", refs[9], archivedPath)
	}
	if err := os.Remove(archivedPath); err != nil {
		t.Fatal(err)
	}

	_, refs, err = cache.get(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if refs[9].MediaPath != archivedPath {
		t.Fatalf("cached scan dropped media after file was removed: %+v", refs[9])
	}
	if cache.loads != 1 {
		t.Fatalf("loads = %d, want 1", cache.loads)
	}
}

func TestPreserveExistingMediaRefsUsesImportCache(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "telecrawl.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Unix(1_800_000_000, 0).UTC()
	sourcePath := t.TempDir()
	archivedData := []byte("preserve-cache")
	archivedPath := writeContentAddressedMedia(t, filepath.Join(filepath.Dir(dbPath), "media"), archivedData)
	first := telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: sourcePath, SourceIdentity: "test:preserve-cache", StartedAt: now, FinishedAt: now},
		Chats: []store.Chat{{JID: "100", Kind: "chat", Name: "saved media", LastMessageAt: now, MessageCount: 1}},
		Messages: []store.Message{{
			SourcePK:  9,
			ChatJID:   "100",
			ChatName:  "saved media",
			MessageID: "0:9",
			Timestamp: now,
			MediaType: "photo",
			MediaPath: archivedPath,
			MediaSize: int64(len(archivedData)),
		}},
	}
	if err := storeImportResult(ctx, st, &first, "", false); err != nil {
		t.Fatal(err)
	}
	archivedPath = first.Messages[0].MediaPath

	cache := &mediaRefCache{}
	incoming := []store.Message{{
		SourcePK:  9,
		ChatJID:   "100",
		ChatName:  "saved media",
		MessageID: "0:9",
		Timestamp: now,
	}}
	if err := preserveExistingMediaRefs(ctx, st, sourcePath, incoming, true, cache); err != nil {
		t.Fatal(err)
	}
	if incoming[0].MediaPath != archivedPath || incoming[0].MediaType != "photo" {
		t.Fatalf("first preserve = %+v, want cached media", incoming[0])
	}
	if err := os.Remove(archivedPath); err != nil {
		t.Fatal(err)
	}

	second := []store.Message{{
		SourcePK:  9,
		ChatJID:   "100",
		ChatName:  "saved media",
		MessageID: "0:9",
		Timestamp: now,
	}}
	if err := preserveExistingMediaRefs(ctx, st, sourcePath, second, true, cache); err != nil {
		t.Fatal(err)
	}
	if second[0].MediaPath != archivedPath {
		t.Fatalf("second preserve lost cached media path: %+v", second[0])
	}
}

func TestExistingMediaRefsLoadsEveryMediaRow(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "telecrawl.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Unix(1_800_000_000, 0).UTC()
	sourcePath := t.TempDir()
	const rows = 60
	messages := make([]store.Message, rows)
	chats := []store.Chat{{JID: "100", Kind: "chat", Name: "bulk", LastMessageAt: now, MessageCount: rows}}
	mediaRoot := filepath.Join(filepath.Dir(dbPath), "media")
	for i := range messages {
		data := []byte{byte(i), byte(i + 1)}
		path := writeContentAddressedMedia(t, mediaRoot, data)
		messages[i] = store.Message{
			SourcePK:  int64(i + 1),
			ChatJID:   "100",
			ChatName:  "bulk",
			MessageID: filepath.Base(path),
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Text:      "body",
			MediaType: "photo",
			MediaPath: path,
			MediaSize: int64(len(data)),
		}
	}
	first := telegramdesktop.ImportResult{
		Stats:    store.ImportStats{SourcePath: sourcePath, SourceIdentity: "test:bulk-media", StartedAt: now, FinishedAt: now},
		Chats:    chats,
		Messages: messages,
	}
	if err := storeImportResult(ctx, st, &first, "", false); err != nil {
		t.Fatal(err)
	}

	_, refs, err := existingMediaRefs(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != rows {
		t.Fatalf("existing media refs = %d, want %d", len(refs), rows)
	}
}
