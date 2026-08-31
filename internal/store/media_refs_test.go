package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestMediaRefsPagesPastOneBatch(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, filepath.Join(t.TempDir(), "media-refs-batch.db"))
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	const rows = 5
	chat := Chat{JID: "100", Kind: "chat", Name: "media", LastMessageAt: now, MessageCount: rows}
	messages := make([]Message, rows)
	for i := range messages {
		pk := int64(i + 1)
		messages[i] = Message{
			SourcePK:   pk,
			ChatJID:    "100",
			ChatName:   "media",
			MessageID:  fmt.Sprintf("%d", i+1),
			Timestamp:  now.Add(time.Duration(i) * time.Minute),
			Text:       fmt.Sprintf("payload %d", i+1),
			SenderName: "alice",
			MediaType:  "photo",
			MediaTitle: fmt.Sprintf("pic-%d", i+1),
			MediaPath:  fmt.Sprintf("/archive/pic-%d.jpg", i+1),
			MediaSize:  int64(100 + i),
		}
	}
	if err := st.ReplaceAll(ctx, ImportStats{SourcePath: t.TempDir(), SourceIdentity: "test:media-refs", FinishedAt: now}, nil, []Chat{chat}, nil, nil, nil, messages); err != nil {
		t.Fatal(err)
	}

	refs, err := st.mediaRefs(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != rows {
		t.Fatalf("media refs = %d, want %d across batches of 2", len(refs), rows)
	}
	seen := make(map[int64]MediaRef, len(refs))
	for _, ref := range refs {
		if ref.MediaType != "photo" || ref.MediaPath == "" {
			t.Fatalf("media ref missing media columns: %+v", ref)
		}
		seen[ref.SourcePK] = ref
	}
	for i := 1; i <= rows; i++ {
		ref, ok := seen[int64(i)]
		if !ok {
			t.Fatalf("missing source_pk %d", i)
		}
		wantPath := fmt.Sprintf("/archive/pic-%d.jpg", i)
		if ref.MediaPath != wantPath || ref.MediaSize != int64(99+i) {
			t.Fatalf("ref %d = path %q size %d, want %q/%d", i, ref.MediaPath, ref.MediaSize, wantPath, 99+i)
		}
	}
}

func TestMediaRefsSkipsDeletedAndNonMediaRows(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, filepath.Join(t.TempDir(), "media-refs-filter.db"))
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	chat := Chat{JID: "100", Kind: "chat", Name: "media", LastMessageAt: now, MessageCount: 3}
	messages := []Message{
		{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "photo", MediaType: "photo", MediaPath: "/archive/keep.jpg", MediaSize: 10},
		{SourcePK: 2, ChatJID: "100", MessageID: "2", Timestamp: now.Add(time.Minute), Text: "text only"},
		{SourcePK: 3, ChatJID: "100", MessageID: "3", Timestamp: now.Add(2 * time.Minute), Text: "gone", MediaType: "photo", MediaPath: "/archive/gone.jpg", MediaSize: 11},
	}
	if err := st.ReplaceAll(ctx, ImportStats{SourcePath: t.TempDir(), SourceIdentity: "test:media-filter", FinishedAt: now}, nil, []Chat{chat}, nil, nil, nil, messages); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `update messages set deleted_at=? where source_pk=3`, unix(now)); err != nil {
		t.Fatal(err)
	}

	refs, err := st.MediaRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].SourcePK != 1 || refs[0].MediaPath != "/archive/keep.jpg" {
		t.Fatalf("media refs = %+v, want only live photo", refs)
	}
}

func TestMediaRefBatchSizeIsBounded(t *testing.T) {
	if mediaRefBatchSize <= 0 || mediaRefBatchSize > 10_000 {
		t.Fatalf("mediaRefBatchSize = %d, want a small page size", mediaRefBatchSize)
	}
	if mediaRefBatchSize == int(^uint(0)>>1) {
		t.Fatal("mediaRefBatchSize must not be MaxInt")
	}
}

func TestMediaRefsHonorsCanceledContext(t *testing.T) {
	st := openTestStore(t, filepath.Join(t.TempDir(), "media-refs-ctx.db"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := st.MediaRefs(ctx)
	if err == nil {
		t.Fatal("error = nil, want canceled context")
	}
}
