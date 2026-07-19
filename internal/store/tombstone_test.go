package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAllCanonicalEntitiesExposeTombstoneMetadata(t *testing.T) {
	t.Parallel()
	st := openTestStore(t, filepath.Join(t.TempDir(), "schema.db"))
	for _, table := range []string{"chats", "folders", "folder_chats", "topics", "contacts", "groups", "group_participants", "messages"} {
		columns, err := columns(context.Background(), st.db, table)
		if err != nil {
			t.Fatal(err)
		}
		for _, column := range []string{"deleted_at", "deletion_source", "deletion_reason"} {
			if !columns[column] {
				t.Fatalf("%s missing %s", table, column)
			}
		}
	}
	messageColumns, err := columns(context.Background(), st.db, "messages")
	if err != nil {
		t.Fatal(err)
	}
	if !messageColumns["event_id"] {
		t.Fatal("messages missing stable event_id")
	}
	for _, index := range []string{"idx_messages_source_identity", "idx_message_revisions_message"} {
		var exists int
		if err := st.db.QueryRowContext(context.Background(), `select exists(select 1 from sqlite_master where type='index' and name=?)`, index).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists == 0 {
			t.Fatalf("missing import/migration support index %s", index)
		}
	}
	revisionColumns, err := columns(context.Background(), st.db, "message_revisions")
	if err != nil {
		t.Fatal(err)
	}
	if !revisionColumns["predecessor_event_id"] {
		t.Fatal("message_revisions missing causal predecessor_event_id")
	}
}

func TestSnapshotMergePreservesDestinationRowsAndTombstonesUntilRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	deletedAt := now.Add(time.Minute)
	st := openTestStore(t, filepath.Join(t.TempDir(), "snapshot-merge.db"))
	destination := SnapshotData{
		SourceIdentity: "test:telegram",
		Chats:          []Chat{{JID: "100", Kind: "chat", Name: "destination"}},
		Messages: []Message{
			{SourcePK: 1, ChatJID: "100", MessageID: "local", Timestamp: now, Text: "destination only"},
			{Tombstone: Tombstone{DeletedAt: deletedAt, DeletionSource: "telegram", DeletionReason: "explicit-message-delete"}, SourcePK: 2, ChatJID: "100", MessageID: "shared", Timestamp: now, Text: "deleted locally"},
		},
	}
	if err := st.RestoreSnapshot(ctx, destination, "fixture:destination", now); err != nil {
		t.Fatal(err)
	}
	incoming := SnapshotData{
		SourceIdentity: "test:telegram",
		Chats:          []Chat{{JID: "100", Kind: "chat", Name: "snapshot"}},
		Messages: []Message{
			{SourcePK: 2, ChatJID: "100", MessageID: "shared", Timestamp: now, Text: "stale live snapshot"},
			{SourcePK: 3, ChatJID: "100", MessageID: "new", Timestamp: now.Add(2 * time.Minute), Text: "snapshot new"},
		},
	}
	if err := st.ImportSnapshot(ctx, incoming, "fixture:snapshot", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	live, err := st.Messages(ctx, MessageFilter{ChatJID: "100", Limit: 10, Asc: true})
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 0, len(live))
	for _, message := range live {
		texts = append(texts, message.Text)
	}
	if !slices.Contains(texts, "destination only") || !slices.Contains(texts, "snapshot new") || slices.Contains(texts, "stale live snapshot") {
		t.Fatalf("default merge live messages = %v", texts)
	}
	exported, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Messages) != 3 {
		t.Fatalf("canonical messages = %d, want 3", len(exported.Messages))
	}
	var shared Message
	for _, message := range exported.Messages {
		if message.MessageID == "shared" {
			shared = message
		}
	}
	if shared.DeletedAt != deletedAt || shared.DeletionReason != "explicit-message-delete" {
		t.Fatalf("destination tombstone was not preserved: %#v", shared)
	}
	if shared.Text != "deleted locally" {
		t.Fatalf("destination tombstone payload changed to %q", shared.Text)
	}
	chats, err := st.ListChats(ctx, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].MessageCount != 2 || !chats[0].LastMessageAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("merged chat aggregates = %#v, want two live messages ending at snapshot new", chats)
	}
	if err := st.RestoreSnapshot(ctx, incoming, "fixture:snapshot", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	live, err = st.Messages(ctx, MessageFilter{ChatJID: "100", Limit: 10, Asc: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 || slices.ContainsFunc(live, func(message Message) bool { return message.MessageID == "local" }) {
		t.Fatalf("restore messages = %#v, want exact incoming snapshot", live)
	}
}

func TestSnapshotMergeRejectsUnknownAndDifferentTelegramAccounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	st := openTestStore(t, filepath.Join(t.TempDir(), "account-boundary.db"))
	base := SnapshotData{
		SourceIdentity: "test:account-a",
		Chats:          []Chat{{JID: "100", Kind: "chat"}},
		Messages:       []Message{{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "account a"}},
	}
	if err := st.RestoreSnapshot(ctx, base, "fixture:a", now); err != nil {
		t.Fatal(err)
	}
	for name, incoming := range map[string]SnapshotData{
		"legacy":    {Messages: []Message{{SourcePK: 2, ChatJID: "100", MessageID: "2", Timestamp: now, Text: "unknown"}}},
		"different": {SourceIdentity: "test:account-b", Messages: []Message{{SourcePK: 2, ChatJID: "100", MessageID: "2", Timestamp: now, Text: "account b"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := st.ImportSnapshot(ctx, incoming, "fixture:"+name, now); err == nil || !strings.Contains(err.Error(), "--restore") {
				t.Fatalf("merge error = %v, want account boundary requiring --restore", err)
			}
		})
	}
}

func TestLegacySnapshotDisambiguatesDuplicateTelegramIdentityAndSourcePKCollisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	data := SnapshotData{
		SourceIdentity: "test:telegram",
		Chats:          []Chat{{JID: "100", Kind: "chat"}},
		Messages: []Message{
			{SourcePK: 10, ChatJID: "100", MessageID: "duplicate", Timestamp: now, Text: "first legacy row"},
			{SourcePK: 20, ChatJID: "100", MessageID: "duplicate", Timestamp: now.Add(time.Second), Text: "second legacy row"},
		},
	}
	st := openTestStore(t, filepath.Join(t.TempDir(), "legacy-snapshot.db"))
	if err := st.RestoreSnapshot(ctx, data, "fixture:legacy", now); err != nil {
		t.Fatal(err)
	}
	merge := SnapshotData{
		SourceIdentity: "test:telegram",
		Messages:       []Message{{SourcePK: 10, ChatJID: "100", MessageID: "different", Timestamp: now.Add(2 * time.Second), Text: "same source pk, distinct event"}},
	}
	if err := st.ImportSnapshot(ctx, merge, "fixture:merge", now); err != nil {
		t.Fatal(err)
	}
	var rows, eventIDs int
	if err := st.db.QueryRowContext(ctx, `select count(*),count(distinct event_id) from messages`).Scan(&rows, &eventIDs); err != nil {
		t.Fatal(err)
	}
	if rows != 3 || eventIDs != 3 {
		t.Fatalf("messages=%d event_ids=%d, want three lossless rows", rows, eventIDs)
	}
}

func TestSnapshotValidationAllowsV5SourcePKCollisions(t *testing.T) {
	t.Parallel()
	data := SnapshotData{Messages: []Message{
		{EventID: "event-a", SourcePK: 7, ChatJID: "100", MessageID: "a"},
		{EventID: "event-b", SourcePK: 7, ChatJID: "100", MessageID: "b"},
	}}
	if err := data.Validate(); err != nil {
		t.Fatalf("valid v5 source_pk collision rejected: %v", err)
	}
}

func TestDirectImportDisambiguatesDuplicateTelegramIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: "test:telegram", FinishedAt: now}
	st := openTestStore(t, filepath.Join(t.TempDir(), "direct-duplicates.db"))
	messages := []Message{
		{SourcePK: 20, ChatJID: "100", MessageID: "duplicate", Timestamp: now, Text: "source pk 20"},
		{SourcePK: 10, ChatJID: "100", MessageID: "duplicate", Timestamp: now, Text: "source pk 10"},
	}
	if err := st.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, messages); err != nil {
		t.Fatal(err)
	}
	exported, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Messages) != 2 || exported.Messages[0].EventID == exported.Messages[1].EventID {
		t.Fatalf("direct import collapsed duplicate semantic rows: %#v", exported.Messages)
	}
	eventIDs := map[int64]string{}
	for _, message := range exported.Messages {
		eventIDs[message.SourcePK] = message.EventID
		if message.SourcePK == 10 && message.EventID != stableLegacyMessageEventID("100", "duplicate", 10, 0) {
			t.Fatalf("lowest source_pk event = %q", message.EventID)
		}
	}
	partial := messages[0]
	partial.Text = "source pk 20 updated"
	stats.FinishedAt = now.Add(time.Minute)
	if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{partial}); err != nil {
		t.Fatal(err)
	}
	exported, err = st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Messages) != 2 {
		t.Fatalf("partial merge changed duplicate family size: %#v", exported.Messages)
	}
	for _, message := range exported.Messages {
		if message.EventID != eventIDs[message.SourcePK] {
			t.Fatalf("partial merge remapped source_pk %d from %q to %q", message.SourcePK, eventIDs[message.SourcePK], message.EventID)
		}
		if message.SourcePK == 10 && message.Text != "source pk 10" {
			t.Fatalf("partial merge overwrote base duplicate: %#v", message)
		}
	}
	discoveryStore := openTestStore(t, filepath.Join(t.TempDir(), "partial-discovery.db"))
	if err := discoveryStore.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, []Message{messages[0]}); err != nil {
		t.Fatal(err)
	}
	if err := discoveryStore.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{messages[1]}); err != nil {
		t.Fatal(err)
	}
	discovered, err := discoveryStore.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered.Messages) != 1 || discovered.Messages[0].EventID != stableMessageEventID("100", "duplicate") || discovered.Messages[0].SourcePK != 10 {
		t.Fatalf("single-row discovery should reconcile source-pk drift by Telegram identity: %#v", discovered.Messages)
	}
}

func TestDirectImportReconcilesSourcePKDriftByTelegramIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: "test:telegram", FinishedAt: now}
	st := openTestStore(t, filepath.Join(t.TempDir(), "source-pk-drift.db"))
	message := Message{SourcePK: 10, ChatJID: "100", MessageID: "7", Timestamp: now, Text: "first cache"}
	if err := st.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	message.SourcePK = 9001
	message.Text = "rebuilt cache"
	message.EditTime = now.Add(time.Minute)
	stats.FinishedAt = now.Add(2 * time.Minute)
	if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	exported, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Messages) != 1 || exported.Messages[0].EventID != stableMessageEventID("100", "7") || exported.Messages[0].SourcePK != 9001 || exported.Messages[0].Text != "rebuilt cache" {
		t.Fatalf("source-pk drift split Telegram identity: %#v", exported.Messages)
	}
	message.SourcePK = 42
	message.Tombstone = Tombstone{DeletedAt: now.Add(3 * time.Minute), DeletionSource: "telegram", DeletionReason: "explicit-delete"}
	stats.FinishedAt = now.Add(4 * time.Minute)
	if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	exported, err = st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Messages) != 1 || exported.Messages[0].DeletedAt.IsZero() || exported.Messages[0].Text != "rebuilt cache" {
		t.Fatalf("source-pk-drifted deletion missed or erased canonical row: %#v", exported.Messages)
	}
}

func TestSnapshotMergeKeepsNewerDestinationMessageRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	identity := "test:telegram"
	st := openTestStore(t, filepath.Join(t.TempDir(), "newer-destination.db"))
	original := Message{EventID: stableMessageEventID("100", "1"), SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "snapshot edit A"}
	if err := st.RestoreSnapshot(ctx, SnapshotData{
		SourceIdentity: identity,
		Chats:          []Chat{{JID: "100", Kind: "chat"}},
		Messages:       []Message{original},
	}, "fixture:old", now); err != nil {
		t.Fatal(err)
	}
	oldSnapshot, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newer := original
	newer.Text = "destination edit B"
	newer.EditTime = now.Add(2 * time.Minute)
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: identity, FinishedAt: now.Add(3 * time.Minute)}
	if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{newer}); err != nil {
		t.Fatal(err)
	}
	if err := st.ImportSnapshot(ctx, oldSnapshot, "fixture:stale-backup", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	messages, err := st.Messages(ctx, MessageFilter{ChatJID: "100", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Text != "destination edit B" {
		t.Fatalf("stale snapshot regressed canonical message: %#v", messages)
	}
	var revisions int
	if err := st.db.QueryRowContext(ctx, `select count(*) from message_revisions where message_event_id=?`, original.EventID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 2 {
		t.Fatalf("merged revision history count = %d, want original + newer", revisions)
	}
}

func TestSnapshotMergeDoesNotReapplyAncestorDeletionAfterResurrection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	identity := "test:telegram"
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: identity, FinishedAt: now}
	base := Message{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "live"}
	source := openTestStore(t, filepath.Join(t.TempDir(), "stale-delete-source.db"))
	if err := source.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, []Message{base}); err != nil {
		t.Fatal(err)
	}
	deleted := base
	deleted.Tombstone = Tombstone{DeletedAt: now.Add(time.Minute), DeletionSource: "telegram", DeletionReason: "explicit-delete"}
	stats.FinishedAt = now.Add(2 * time.Minute)
	if err := source.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{deleted}); err != nil {
		t.Fatal(err)
	}
	staleDeleted, err := source.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	destination := openTestStore(t, filepath.Join(t.TempDir(), "resurrected-destination.db"))
	if err := destination.RestoreSnapshot(ctx, staleDeleted, "fixture:deleted", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	resurrected := base
	resurrected.Text = "authoritatively resurrected"
	resurrected.EditTime = now.Add(4 * time.Minute)
	stats.FinishedAt = now.Add(5 * time.Minute)
	if err := destination.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{resurrected}); err != nil {
		t.Fatal(err)
	}
	if err := destination.ImportSnapshot(ctx, staleDeleted, "fixture:stale-delete", now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	messages, err := destination.Messages(ctx, MessageFilter{ChatJID: "100", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Text != resurrected.Text {
		t.Fatalf("ancestor backup deletion regressed live descendant: %#v", messages)
	}
}

func TestSnapshotMergeAppliesCausalMessageResurrection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	identity := "test:telegram"
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: identity, FinishedAt: now}
	base := Message{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "live"}
	source := openTestStore(t, filepath.Join(t.TempDir(), "resurrection-source.db"))
	if err := source.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, []Message{base}); err != nil {
		t.Fatal(err)
	}
	deleted := base
	deleted.Tombstone = Tombstone{DeletedAt: now.Add(time.Minute), DeletionSource: "telegram", DeletionReason: "explicit-delete"}
	stats.FinishedAt = now.Add(2 * time.Minute)
	if err := source.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{deleted}); err != nil {
		t.Fatal(err)
	}
	deletedSnapshot, err := source.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resurrected := base
	resurrected.Text = "resurrected descendant"
	resurrected.EditTime = now.Add(3 * time.Minute)
	stats.FinishedAt = now.Add(4 * time.Minute)
	if err := source.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{resurrected}); err != nil {
		t.Fatal(err)
	}
	liveSnapshot, err := source.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	destination := openTestStore(t, filepath.Join(t.TempDir(), "resurrection-destination.db"))
	if err := destination.RestoreSnapshot(ctx, deletedSnapshot, "fixture:deleted", now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := destination.ImportSnapshot(ctx, liveSnapshot, "fixture:live-descendant", now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	messages, err := destination.Messages(ctx, MessageFilter{ChatJID: "100", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Text != resurrected.Text {
		t.Fatalf("causal live descendant did not supersede tombstone: %#v", messages)
	}
}

func TestSnapshotMergeReconcilesCanonicalIDWithDuplicateFamily(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	identity := "test:telegram"
	destination := openTestStore(t, filepath.Join(t.TempDir(), "duplicate-family-destination.db"))
	legacy := SnapshotData{SourceIdentity: identity, Chats: []Chat{{JID: "100", Kind: "chat"}}, Messages: []Message{
		{SourcePK: 10, ChatJID: "100", MessageID: "duplicate", Timestamp: now, Text: "legacy ten"},
		{SourcePK: 20, ChatJID: "100", MessageID: "duplicate", Timestamp: now, Text: "legacy twenty"},
	}}
	if err := destination.RestoreSnapshot(ctx, legacy, "fixture:legacy-family", now); err != nil {
		t.Fatal(err)
	}
	source := openTestStore(t, filepath.Join(t.TempDir(), "canonical-family-source.db"))
	canonical := Message{SourcePK: 20, ChatJID: "100", MessageID: "duplicate", Timestamp: now, EditTime: now.Add(time.Minute), Text: "fresh twenty"}
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: identity, FinishedAt: now.Add(2 * time.Minute)}
	if err := source.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, []Message{canonical}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := destination.ImportSnapshot(ctx, snapshot, "fixture:canonical-family", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	exported, err := destination.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Messages) != 2 {
		t.Fatalf("canonical/discriminated merge created duplicate family member: %#v", exported.Messages)
	}
	for _, message := range exported.Messages {
		if message.SourcePK == 20 && (message.EventID != stableLegacyMessageEventID("100", "duplicate", 20, 0) || message.Text != canonical.Text) {
			t.Fatalf("canonical snapshot did not attach to source-pk family member: %#v", message)
		}
	}
}

func TestSnapshotMergeKeepsLiveParentOverStaleTombstone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	identity := "test:telegram"
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: identity, FinishedAt: now}
	chat := Chat{JID: "100", Kind: "chat", Name: "live parent"}
	message := Message{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "live child"}
	source := openTestStore(t, filepath.Join(t.TempDir(), "stale-parent-source.db"))
	if err := source.ReplaceAll(ctx, stats, nil, []Chat{chat}, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	deleted := chat
	deleted.Tombstone = Tombstone{DeletedAt: now.Add(time.Minute), DeletionSource: "telegram", DeletionReason: "explicit-chat-delete"}
	stats.FinishedAt = now.Add(2 * time.Minute)
	if err := source.MergeAll(ctx, stats, nil, []Chat{deleted}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	staleSnapshot, err := source.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	destination := openTestStore(t, filepath.Join(t.TempDir(), "live-parent-destination.db"))
	if err := destination.RestoreSnapshot(ctx, staleSnapshot, "fixture:deleted-parent", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	stats.FinishedAt = now.Add(4 * time.Minute)
	if err := destination.MergeAll(ctx, stats, nil, []Chat{chat}, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := destination.ImportSnapshot(ctx, staleSnapshot, "fixture:stale-parent", now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	chats, err := destination.ListChats(ctx, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := destination.Messages(ctx, MessageFilter{ChatJID: "100", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || len(messages) != 1 {
		t.Fatalf("stale parent tombstone hid resurrected family: chats=%#v messages=%#v", chats, messages)
	}
}

func TestSnapshotMergeFollowsEqualTimeRevisionAncestry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	identity := "test:telegram"
	base := Message{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "A"}
	chat := Chat{JID: "100", Kind: "chat"}
	makeStore := func(name string) *Store {
		st := openTestStore(t, filepath.Join(t.TempDir(), name))
		stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: identity, FinishedAt: now}
		if err := st.ReplaceAll(ctx, stats, nil, []Chat{chat}, nil, nil, nil, []Message{base}); err != nil {
			t.Fatal(err)
		}
		return st
	}
	source := makeStore("causal-source.db")
	destination := makeStore("causal-destination.db")
	editTime := now.Add(time.Minute)
	editB := base
	editB.Text = "B"
	editB.EditTime = editTime
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: identity, FinishedAt: now.Add(2 * time.Minute)}
	for _, st := range []*Store{source, destination} {
		if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{editB}); err != nil {
			t.Fatal(err)
		}
	}
	editC := editB
	editC.Text = "C"
	editC.ReactionsJSON = `[{"emoji":"heart"}]`
	stats.FinishedAt = now.Add(3 * time.Minute)
	if err := source.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{editC}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := destination.ImportSnapshot(ctx, snapshot, "fixture:causal", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	messages, err := destination.Messages(ctx, MessageFilter{ChatJID: "100", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Text != "C" {
		t.Fatalf("equal-time descendant revision did not advance canonical payload: %#v", messages)
	}
}

func TestRevisionGraphSelectionIsIndependentOfInsertionOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	identity := "test:telegram"
	base := Message{EventID: stableMessageEventID("100", "1"), SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "A", EditTime: now.Add(time.Minute)}
	st := openTestStore(t, filepath.Join(t.TempDir(), "causal-row-order.db"))
	if err := st.RestoreSnapshot(ctx, SnapshotData{SourceIdentity: identity, Chats: []Chat{{JID: "100", Kind: "chat"}}, Messages: []Message{base}}, "fixture:base", now); err != nil {
		t.Fatal(err)
	}
	payloadA, err := messageRevisionPayload(base)
	if err != nil {
		t.Fatal(err)
	}
	versionB := base
	versionB.Text = "B"
	payloadB, err := messageRevisionPayload(versionB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `delete from message_revisions`); err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	for _, revision := range []MessageRevision{
		{EventID: "rev-a-return", MessageEventID: base.EventID, EventType: "message_edited", PayloadJSON: payloadA, EventAt: base.EditTime, ObservedAt: now.Add(4 * time.Minute), EventSource: identity, Reason: "return-to-a", PredecessorEventID: "rev-b"},
		{EventID: "rev-b", MessageEventID: base.EventID, EventType: "message_edited", PayloadJSON: payloadB, EventAt: base.EditTime, ObservedAt: now.Add(3 * time.Minute), EventSource: identity, Reason: "to-b", PredecessorEventID: "rev-a-first"},
		{EventID: "rev-a-first", MessageEventID: base.EventID, EventType: "message_observed", PayloadJSON: payloadA, EventAt: base.EditTime, ObservedAt: now.Add(2 * time.Minute), EventSource: identity, Reason: "first-a"},
	} {
		if err := insertMessageRevision(ctx, tx, revision); err != nil {
			t.Fatal(err)
		}
	}
	tip, _, err := messageRevisionPredecessor(ctx, tx, base.EventID, payloadA)
	if err != nil {
		t.Fatal(err)
	}
	if tip.eventID != "rev-a-return" {
		t.Fatalf("causal tip = %q, want descendant rev-a-return", tip.eventID)
	}
	merged, err := mergeableSnapshotMessages(ctx, tx, []Message{versionB}, []MessageRevision{{EventID: "rev-b", MessageEventID: base.EventID, EventType: "message_edited", PayloadJSON: payloadB, EventAt: base.EditTime, ObservedAt: now.Add(3 * time.Minute), EventSource: identity, Reason: "to-b", PredecessorEventID: "rev-a-first"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 0 {
		t.Fatalf("row order made stale ancestor replace causal descendant: %#v", merged)
	}
}

func TestSnapshotMergePreservesDestinationLocalMedia(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	identity := "test:telegram"
	base := Message{EventID: stableMessageEventID("100", "1"), SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "before", MediaType: "document", MediaTitle: "archive.pdf", MediaPath: "/destination/archive.pdf", MediaSize: 42}
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: identity, FinishedAt: now}
	destination := openTestStore(t, filepath.Join(t.TempDir(), "media-destination.db"))
	if err := destination.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, []Message{base}); err != nil {
		t.Fatal(err)
	}
	source := openTestStore(t, filepath.Join(t.TempDir(), "media-source.db"))
	remote := base
	remote.MediaPath = "/source/archive.pdf"
	remote.MediaSize = 99
	if err := source.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, []Message{remote}); err != nil {
		t.Fatal(err)
	}
	remote.Text = "after"
	remote.EditTime = now.Add(time.Minute)
	stats.FinishedAt = now.Add(2 * time.Minute)
	if err := source.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{remote}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := destination.ImportSnapshot(ctx, snapshot, "fixture:remote-media", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	exported, err := destination.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Messages) != 1 || exported.Messages[0].Text != "after" || exported.Messages[0].MediaPath != base.MediaPath || exported.Messages[0].MediaSize != base.MediaSize {
		t.Fatalf("snapshot merge did not preserve destination-local media: %#v", exported.Messages)
	}
}

func TestSnapshotMergeKeepsInitiallyObservedEditedDestination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	identity := "test:telegram"
	eventID := stableMessageEventID("100", "1")
	source := openTestStore(t, filepath.Join(t.TempDir(), "older-source.db"))
	base := Message{EventID: eventID, SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "created"}
	if err := source.RestoreSnapshot(ctx, SnapshotData{SourceIdentity: identity, Chats: []Chat{{JID: "100", Kind: "chat"}}, Messages: []Message{base}}, "fixture:created", now); err != nil {
		t.Fatal(err)
	}
	older := base
	older.Text = "older edit A"
	older.EditTime = now.Add(2 * time.Minute)
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: identity, FinishedAt: now.Add(2 * time.Minute)}
	if err := source.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{older}); err != nil {
		t.Fatal(err)
	}
	olderSnapshot, err := source.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}

	destination := openTestStore(t, filepath.Join(t.TempDir(), "initially-edited-destination.db"))
	newer := base
	newer.Text = "initially observed edit B"
	newer.EditTime = now.Add(3 * time.Minute)
	if err := destination.RestoreSnapshot(ctx, SnapshotData{SourceIdentity: identity, Chats: []Chat{{JID: "100", Kind: "chat"}}, Messages: []Message{newer}}, "fixture:newer", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := destination.ImportSnapshot(ctx, olderSnapshot, "fixture:older-backup", now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	messages, err := destination.Messages(ctx, MessageFilter{ChatJID: "100", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Text != "initially observed edit B" {
		t.Fatalf("older revision outranked edited destination baseline: %#v", messages)
	}
}

func TestLateLegacyObservationDoesNotOutrankTelegramEdit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	identity := "test:telegram"
	base := Message{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "legacy stale payload"}
	legacy := openTestStore(t, filepath.Join(t.TempDir(), "late-legacy.db"))
	if err := legacy.RestoreSnapshot(ctx, SnapshotData{SourceIdentity: identity, Chats: []Chat{{JID: "100", Kind: "chat"}}, Messages: []Message{base}}, "fixture:legacy", now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	lateObservedSnapshot, err := legacy.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	destination := openTestStore(t, filepath.Join(t.TempDir(), "telegram-edit.db"))
	if err := destination.RestoreSnapshot(ctx, SnapshotData{SourceIdentity: identity, Chats: []Chat{{JID: "100", Kind: "chat"}}, Messages: []Message{base}}, "fixture:base", now); err != nil {
		t.Fatal(err)
	}
	edited := base
	edited.Text = "real Telegram edit"
	edited.EditTime = now.Add(time.Hour)
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: identity, FinishedAt: now.Add(2 * time.Hour)}
	if err := destination.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{edited}); err != nil {
		t.Fatal(err)
	}
	if err := destination.ImportSnapshot(ctx, lateObservedSnapshot, "fixture:late-legacy", now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	messages, err := destination.Messages(ctx, MessageFilter{ChatJID: "100", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Text != "real Telegram edit" {
		t.Fatalf("late legacy observation outranked Telegram edit: %#v", messages)
	}
}

func TestRevisionPayloadMatchesMediaPreservedCanonicalMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: "test:telegram", FinishedAt: now}
	message := Message{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "before", MediaType: "document", MediaTitle: "archive.pdf", MediaPath: "/local/archive.pdf", MediaURL: "telegram://archive", MediaSize: 42}
	st := openTestStore(t, filepath.Join(t.TempDir(), "media-revision.db"))
	if err := st.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	changed := message
	changed.Text = "after"
	changed.EditTime = now.Add(time.Minute)
	changed.MediaType = ""
	changed.MediaTitle = ""
	changed.MediaPath = ""
	changed.MediaURL = ""
	changed.MediaSize = 0
	stats.FinishedAt = now.Add(2 * time.Minute)
	if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{changed}); err != nil {
		t.Fatal(err)
	}
	exported, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	canonical := exported.Messages[0]
	if canonical.MediaType != message.MediaType || canonical.MediaTitle != message.MediaTitle || canonical.MediaPath != message.MediaPath || canonical.MediaURL != message.MediaURL || canonical.MediaSize != message.MediaSize {
		t.Fatalf("canonical media was not preserved: %#v", canonical)
	}
	wantPayload, err := messageRevisionPayload(canonical)
	if err != nil {
		t.Fatal(err)
	}
	var latestPayload string
	if err := st.db.QueryRowContext(ctx, `select payload_json from message_revisions where message_event_id=? order by observed_at desc,rowid desc limit 1`, canonical.EventID).Scan(&latestPayload); err != nil {
		t.Fatal(err)
	}
	if latestPayload != wantPayload {
		t.Fatalf("revision payload does not match stored canonical message\nlatest: %s\nwant:   %s", latestPayload, wantPayload)
	}
}

func TestAggregateRepairDoesNotRewriteTombstonedChatPayload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	deletedAt := now.Add(time.Hour)
	st := openTestStore(t, filepath.Join(t.TempDir(), "tombstoned-chat-aggregate.db"))
	data := SnapshotData{
		SourceIdentity: "test:telegram",
		Chats: []Chat{{
			Tombstone:     Tombstone{DeletedAt: deletedAt, DeletionSource: "telegram", DeletionReason: "explicit-chat-delete"},
			JID:           "100",
			Kind:          "chat",
			Name:          "last known chat",
			MessageCount:  17,
			LastMessageAt: now,
		}},
	}
	if err := st.RestoreSnapshot(ctx, data, "fixture:deleted", deletedAt); err != nil {
		t.Fatal(err)
	}
	if err := st.ImportSnapshot(ctx, SnapshotData{SourceIdentity: "test:telegram"}, "fixture:empty", deletedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	exported, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Chats) != 1 || exported.Chats[0].MessageCount != 17 || !exported.Chats[0].LastMessageAt.Equal(now) || exported.Chats[0].Name != "last known chat" {
		t.Fatalf("tombstoned chat payload changed during aggregate repair: %#v", exported.Chats)
	}
}

func TestIDOnlyTombstonesPreserveLastKnownEntityPayloads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	identity := "test:telegram"
	st := openTestStore(t, filepath.Join(t.TempDir(), "id-only-tombstones.db"))
	live := SnapshotData{
		SourceIdentity: identity,
		Chats:          []Chat{{JID: "100", Kind: "group", Name: "known chat", MessageCount: 1, LastMessageAt: now}},
		Folders:        []Folder{{ID: "1", Title: "known folder"}},
		FolderChats:    []FolderChat{{FolderID: "1", ChatJID: "100", Position: 9}},
		Topics:         []Topic{{ChatJID: "100", TopicID: "7", Title: "known topic"}},
		Contacts:       []Contact{{JID: "user", FullName: "Known Contact"}},
		Groups:         []Group{{JID: "group", Name: "known group"}},
		Participants:   []GroupParticipant{{GroupJID: "group", UserJID: "user", ContactName: "Known Participant", IsActive: true}},
		Messages:       []Message{{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "last known message"}},
	}
	if err := st.RestoreSnapshot(ctx, live, "fixture:live", now); err != nil {
		t.Fatal(err)
	}
	deletedAt := now.Add(time.Minute)
	tombstone := Tombstone{DeletedAt: deletedAt, DeletionSource: "telegram", DeletionReason: "explicit-delete"}
	deleted := SnapshotData{
		SourceIdentity: identity,
		Chats:          []Chat{{Tombstone: tombstone, JID: "100"}},
		Folders:        []Folder{{Tombstone: tombstone, ID: "1"}},
		FolderChats:    []FolderChat{{Tombstone: tombstone, FolderID: "1", ChatJID: "100"}},
		Topics:         []Topic{{Tombstone: tombstone, ChatJID: "100", TopicID: "7"}},
		Contacts:       []Contact{{Tombstone: tombstone, JID: "user"}},
		Groups:         []Group{{Tombstone: tombstone, JID: "group"}},
		Participants:   []GroupParticipant{{Tombstone: tombstone, GroupJID: "group", UserJID: "user"}},
		Messages:       []Message{{Tombstone: tombstone, SourcePK: 1, ChatJID: "100", MessageID: "1"}},
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stats := snapshotStats(deleted, "fixture:deleted", st.Path(), deletedAt)
	stats.SourceIdentity = identity
	if err := writeImport(ctx, tx, stats, deleted.Contacts, deleted.Chats, deleted.Folders, deleted.FolderChats, deleted.Topics, deleted.Messages, false, false, true); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := insertGroups(ctx, tx, deleted.Groups, identity, false); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := insertGroupParticipants(ctx, tx, deleted.Participants, identity, false); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := propagateTombstones(ctx, tx, nil); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := recordPropagatedMessageDeletions(ctx, tx, deletedAt, nil); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	exported, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Chats[0].Name != "known chat" || exported.Chats[0].Kind != "group" || exported.Chats[0].MessageCount != 1 ||
		exported.Folders[0].Title != "known folder" || exported.FolderChats[0].Position != 9 || exported.Topics[0].Title != "known topic" ||
		exported.Contacts[0].FullName != "Known Contact" || exported.Groups[0].Name != "known group" ||
		exported.Participants[0].ContactName != "Known Participant" || exported.Messages[0].Text != "last known message" {
		t.Fatalf("ID-only tombstone erased last-known payload: %#v", exported)
	}
	for _, deletedAt := range []time.Time{exported.Chats[0].DeletedAt, exported.Folders[0].DeletedAt, exported.FolderChats[0].DeletedAt, exported.Topics[0].DeletedAt, exported.Contacts[0].DeletedAt, exported.Groups[0].DeletedAt, exported.Participants[0].DeletedAt, exported.Messages[0].DeletedAt} {
		if !deletedAt.Equal(tombstone.DeletedAt) {
			t.Fatalf("tombstone time = %v, want %v", deletedAt, tombstone.DeletedAt)
		}
	}
}

func TestBaselineSeedingUsesBoundedBatches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	st := openTestStore(t, filepath.Join(t.TempDir(), "baseline-batches.db"))
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	for i := 1; i <= 1201; i++ {
		messageID := fmt.Sprintf("%d", i)
		if _, err := tx.ExecContext(ctx, `insert into messages(event_id,source_pk,chat_jid,msg_id,ts,from_me,raw_type,starred) values(?,?,?,?,?,?,?,?)`, stableMessageEventID("100", messageID), i, "100", messageID, unix(now), 0, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := seedMissingMessageBaselines(ctx, tx, now, "test-batch"); err != nil {
		t.Fatal(err)
	}
	var revisions int
	if err := tx.QueryRowContext(ctx, `select count(*) from message_revisions`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 1201 {
		t.Fatalf("baseline revisions = %d, want 1201", revisions)
	}
}

func TestRevisionIdentityKeepsUntimestampedAndSameTimestampEdits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: "test:telegram", FinishedAt: now}
	message := Message{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "original"}
	st := openTestStore(t, filepath.Join(t.TempDir(), "same-time-revisions.db"))
	if err := st.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	editTime := now.Add(time.Minute)
	for i, text := range []string{"same time one", "same time two"} {
		changed := message
		changed.Text = text
		changed.EditTime = editTime
		stats.FinishedAt = now.Add(time.Duration(i+2) * time.Minute)
		if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{changed}); err != nil {
			t.Fatal(err)
		}
	}
	untimestamped := message
	untimestamped.Text = "observable without edit timestamp"
	untimestamped.ReactionsJSON = `[{"emoji":"+1"}]`
	stats.FinishedAt = now.Add(5 * time.Minute)
	if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{untimestamped}); err != nil {
		t.Fatal(err)
	}
	if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{untimestamped}); err != nil {
		t.Fatal(err)
	}
	backToEarlierPayload := message
	backToEarlierPayload.Text = "same time one"
	backToEarlierPayload.EditTime = editTime
	stats.FinishedAt = now.Add(6 * time.Minute)
	if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{backToEarlierPayload}); err != nil {
		t.Fatal(err)
	}
	if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{backToEarlierPayload}); err != nil {
		t.Fatal(err)
	}
	var edits, uniqueEdits int
	if err := st.db.QueryRowContext(ctx, `select count(*),count(distinct event_id) from message_revisions where event_type='message_edited'`).Scan(&edits, &uniqueEdits); err != nil {
		t.Fatal(err)
	}
	if edits != 4 || uniqueEdits != 4 {
		t.Fatalf("edited revisions=%d unique=%d, want four distinct transitions with retries deduped", edits, uniqueEdits)
	}
	var predecessors int
	if err := st.db.QueryRowContext(ctx, `select count(*) from message_revisions where coalesce(predecessor_event_id,'')<>''`).Scan(&predecessors); err != nil {
		t.Fatal(err)
	}
	if predecessors != 4 {
		t.Fatalf("causal predecessor links = %d, want one for each edit transition", predecessors)
	}
}

func TestRevisionPayloadCanonicalizesDatabaseTimes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	location := time.FixedZone("fixture", 5*60*60+30*60)
	inputTime := time.Date(2026, 7, 18, 17, 30, 0, 987654321, location)
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: "test:telegram", FinishedAt: inputTime}
	message := Message{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: inputTime, Text: "canonical time"}
	st := openTestStore(t, filepath.Join(t.TempDir(), "canonical-times.db"))
	if err := st.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	exported, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantPayload, err := messageRevisionPayload(exported.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := st.db.QueryRowContext(ctx, `select payload_json from message_revisions where message_event_id=?`, exported.Messages[0].EventID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != wantPayload || !exported.Messages[0].Timestamp.Equal(time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("revision/canonical time mismatch: timestamp=%s payload=%s want=%s", exported.Messages[0].Timestamp, payload, wantPayload)
	}
	stats.FinishedAt = inputTime.Add(time.Minute)
	if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	var revisions int
	if err := st.db.QueryRowContext(ctx, `select count(*) from message_revisions`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 1 {
		t.Fatalf("equivalent timestamp retry created %d revisions, want 1", revisions)
	}
}

func TestParentTombstonesPropagateAndAuthoritativeRowsResurrect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	sourcePath := t.TempDir()
	stats := ImportStats{SourcePath: sourcePath, SourcePathCanonical: true, SourceIdentity: "test:telegram", FinishedAt: now}
	chat := Chat{JID: "100", Kind: "chat", Name: "parent", FolderID: "1"}
	folder := Folder{ID: "1", Title: "folder"}
	membership := FolderChat{FolderID: "1", ChatJID: "100"}
	topic := Topic{ChatJID: "100", TopicID: "7", Title: "topic"}
	message := Message{SourcePK: 1, ChatJID: "100", MessageID: "9", TopicID: "7", Timestamp: now, Text: "searchable child"}
	st := openTestStore(t, filepath.Join(t.TempDir(), "propagation.db"))
	if err := st.ReplaceAll(ctx, stats, nil, []Chat{chat}, []Folder{folder}, []FolderChat{membership}, []Topic{topic}, []Message{message}); err != nil {
		t.Fatal(err)
	}
	deletedAt := now.Add(time.Minute)
	deletedChat := chat
	deletedChat.Tombstone = Tombstone{DeletedAt: deletedAt, DeletionSource: "telegram", DeletionReason: "explicit-chat-delete"}
	stats.FinishedAt = deletedAt
	if err := st.MergeAll(ctx, stats, nil, []Chat{deletedChat}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		table, where, reason string
	}{
		{"folder_chats", "chat_jid='100'", "parent_chat_deleted"},
		{"topics", "chat_jid='100' and topic_id='7'", "parent_chat_deleted"},
		{"messages", "chat_jid='100' and msg_id='9'", "parent_chat_deleted"},
	} {
		var gotDeletedAt sql.NullInt64
		var reason string
		if err := st.db.QueryRowContext(ctx, "select deleted_at,coalesce(deletion_reason,'') from "+check.table+" where "+check.where).Scan(&gotDeletedAt, &reason); err != nil {
			t.Fatal(err)
		}
		if !gotDeletedAt.Valid || reason != check.reason {
			t.Fatalf("%s tombstone = %v %q, want %q", check.table, gotDeletedAt, reason, check.reason)
		}
	}
	search, err := st.Search(ctx, MessageFilter{Query: "searchable", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(search) != 0 {
		t.Fatalf("tombstoned child remained searchable: %#v", search)
	}
	var deleteEvents int
	if err := st.db.QueryRowContext(ctx, `select count(*) from message_revisions where event_type='message_deleted' and message_event_id=?`, stableMessageEventID("100", "9")).Scan(&deleteEvents); err != nil {
		t.Fatal(err)
	}
	if deleteEvents != 1 {
		t.Fatalf("message delete revisions = %d, want 1", deleteEvents)
	}
	stats.FinishedAt = deletedAt.Add(time.Minute)
	if err := st.MergeAll(ctx, stats, nil, []Chat{chat}, []Folder{folder}, []FolderChat{membership}, []Topic{topic}, []Message{message}); err != nil {
		t.Fatal(err)
	}
	search, err = st.Search(ctx, MessageFilter{Query: "searchable", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(search) != 1 {
		t.Fatalf("authoritative resurrection search = %#v, want one live message", search)
	}
}

func TestParentDeletionRevisionPropagationSpansMultipleBatches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: "test:telegram", FinishedAt: now}
	messages := make([]Message, 1001)
	for i := range messages {
		messages[i] = Message{SourcePK: int64(i + 1), ChatJID: "100", MessageID: strconv.Itoa(i + 1), Timestamp: now, Text: "child"}
	}
	st := openTestStore(t, filepath.Join(t.TempDir(), "propagation-batches.db"))
	if err := st.ReplaceAll(ctx, stats, nil, []Chat{{JID: "100", Kind: "chat"}}, nil, nil, nil, messages); err != nil {
		t.Fatal(err)
	}
	stats.FinishedAt = now.Add(2 * time.Minute)
	deleted := Chat{JID: "100", Kind: "chat", Tombstone: Tombstone{DeletedAt: now.Add(time.Minute), DeletionSource: "telegram", DeletionReason: "explicit-chat-delete"}}
	if err := st.MergeAll(ctx, stats, nil, []Chat{deleted}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	var deletedMessages, deleteRevisions int
	if err := st.db.QueryRowContext(ctx, `select count(*) from messages where deleted_at is not null`).Scan(&deletedMessages); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `select count(*) from message_revisions where event_type='message_deleted'`).Scan(&deleteRevisions); err != nil {
		t.Fatal(err)
	}
	if deletedMessages != len(messages) || deleteRevisions != len(messages) {
		t.Fatalf("batched propagation messages=%d revisions=%d want=%d", deletedMessages, deleteRevisions, len(messages))
	}
}

func TestRepeatedParentPropagationRecordsNewCausalDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: "test:telegram", FinishedAt: now}
	chat := Chat{JID: "100", Kind: "chat"}
	message := Message{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "child"}
	st := openTestStore(t, filepath.Join(t.TempDir(), "repeat-parent-delete.db"))
	if err := st.ReplaceAll(ctx, stats, nil, []Chat{chat}, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	deletedAt := now.Add(time.Minute)
	chat.Tombstone = Tombstone{DeletedAt: deletedAt, DeletionSource: "telegram", DeletionReason: "explicit-chat-delete"}
	stats.FinishedAt = now.Add(2 * time.Minute)
	if err := st.MergeAll(ctx, stats, nil, []Chat{chat}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	message.Text = "transient authoritative resurrection"
	message.EditTime = now.Add(3 * time.Minute)
	stats.FinishedAt = now.Add(4 * time.Minute)
	if err := st.MergeAll(ctx, stats, nil, nil, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	var deleteRevisions int
	if err := st.db.QueryRowContext(ctx, `select count(*) from message_revisions where message_event_id=? and event_type='message_deleted'`, stableMessageEventID("100", "1")).Scan(&deleteRevisions); err != nil {
		t.Fatal(err)
	}
	if deleteRevisions != 2 {
		t.Fatalf("repeated parent propagation recorded %d delete revisions, want 2 causal transitions", deleteRevisions)
	}
	tx, err := st.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	tip, _, err := messageRevisionPredecessor(ctx, tx, stableMessageEventID("100", "1"), "")
	if err != nil {
		t.Fatal(err)
	}
	if tip.eventType != "message_deleted" || !tip.eventAt.Equal(deletedAt) {
		t.Fatalf("causal tip after repeated propagation = %#v, want parent deletion", tip)
	}
}

func TestFolderTopicAndGroupTombstonesPropagateToOwnedRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	st := openTestStore(t, filepath.Join(t.TempDir(), "owned-rows.db"))
	live := SnapshotData{
		SourceIdentity: "test:telegram",
		Chats:          []Chat{{JID: "100", Kind: "chat", Name: "chat"}},
		Folders:        []Folder{{ID: "1", Title: "folder"}},
		FolderChats:    []FolderChat{{FolderID: "1", ChatJID: "100"}},
		Groups:         []Group{{JID: "group", Name: "group"}},
		Participants:   []GroupParticipant{{GroupJID: "group", UserJID: "user", IsActive: true}},
		Topics:         []Topic{{ChatJID: "100", TopicID: "7", Title: "topic"}},
		Messages:       []Message{{SourcePK: 1, ChatJID: "100", MessageID: "9", TopicID: "7", Timestamp: now, Text: "owned"}},
	}
	if err := st.RestoreSnapshot(ctx, live, "fixture:live", now); err != nil {
		t.Fatal(err)
	}
	deletedAt := now.Add(time.Minute)
	deleted := SnapshotData{
		SourceIdentity: "test:telegram",
		Folders:        []Folder{{Tombstone: Tombstone{DeletedAt: deletedAt, DeletionSource: "telegram", DeletionReason: "explicit-folder-delete"}, ID: "1", Title: "folder"}},
		Groups:         []Group{{Tombstone: Tombstone{DeletedAt: deletedAt, DeletionSource: "telegram", DeletionReason: "explicit-group-delete"}, JID: "group", Name: "group"}},
		Topics:         []Topic{{Tombstone: Tombstone{DeletedAt: deletedAt, DeletionSource: "telegram", DeletionReason: "explicit-topic-delete"}, ChatJID: "100", TopicID: "7", Title: "topic"}},
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stats := snapshotStats(deleted, "fixture:deleted", st.Path(), deletedAt)
	stats.SourceIdentity = "test:telegram"
	if err := writeImport(ctx, tx, stats, nil, nil, deleted.Folders, nil, deleted.Topics, nil, false, false, true); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := insertGroups(ctx, tx, deleted.Groups, stats.SourceIdentity, false); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := propagateTombstones(ctx, tx, nil); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := recordPropagatedMessageDeletions(ctx, tx, deletedAt, nil); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		table, where, reason string
	}{
		{"folder_chats", "folder_id='1' and chat_jid='100'", "parent_folder_deleted"},
		{"messages", "chat_jid='100' and msg_id='9'", "parent_topic_deleted"},
		{"group_participants", "group_jid='group' and user_jid='user'", "parent_group_deleted"},
	} {
		var reason string
		if err := st.db.QueryRowContext(ctx, "select coalesce(deletion_reason,'') from "+check.table+" where "+check.where).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		if reason != check.reason {
			t.Fatalf("%s reason = %q, want %q", check.table, reason, check.reason)
		}
	}
}

func TestMessageRevisionEventsAreStableAcrossEditsDeletesAndRetries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	stats := ImportStats{SourcePath: t.TempDir(), SourcePathCanonical: true, SourceIdentity: "test:telegram", FinishedAt: now}
	chat := Chat{JID: "100", Kind: "chat", Name: "chat"}
	message := Message{SourcePK: 11, ChatJID: "100", MessageID: "22", Timestamp: now, Text: "original"}
	st := openTestStore(t, filepath.Join(t.TempDir(), "revisions.db"))
	if err := st.ReplaceAll(ctx, stats, nil, []Chat{chat}, nil, nil, nil, []Message{message}); err != nil {
		t.Fatal(err)
	}
	edited := message
	edited.Text = "edited"
	edited.EditTime = now.Add(time.Minute)
	stats.FinishedAt = now.Add(2 * time.Minute)
	if err := st.MergeAll(ctx, stats, nil, []Chat{chat}, nil, nil, nil, []Message{edited}); err != nil {
		t.Fatal(err)
	}
	if err := st.MergeAll(ctx, stats, nil, []Chat{chat}, nil, nil, nil, []Message{edited}); err != nil {
		t.Fatal(err)
	}
	deleted := edited
	deleted.Tombstone = Tombstone{DeletedAt: now.Add(3 * time.Minute), DeletionSource: "telegram", DeletionReason: "explicit-message-delete"}
	stats.FinishedAt = now.Add(4 * time.Minute)
	if err := st.MergeAll(ctx, stats, nil, []Chat{chat}, nil, nil, nil, []Message{deleted}); err != nil {
		t.Fatal(err)
	}
	if err := st.MergeAll(ctx, stats, nil, []Chat{chat}, nil, nil, nil, []Message{deleted}); err != nil {
		t.Fatal(err)
	}
	revisions, err := queryAllMessageRevisions(ctx, st.db)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 3 {
		t.Fatalf("revision events = %#v, want created + edited + deleted", revisions)
	}
	wantTypes := []string{"message_created", "message_edited", "message_deleted"}
	gotTypes := make([]string, 0, len(revisions))
	ids := make(map[string]struct{}, len(revisions))
	for _, revision := range revisions {
		gotTypes = append(gotTypes, revision.EventType)
		ids[revision.EventID] = struct{}{}
		if revision.MessageEventID != stableMessageEventID("100", "22") {
			t.Fatalf("message event identity changed: %#v", revision)
		}
	}
	if !slices.Equal(gotTypes, wantTypes) || len(ids) != 3 {
		t.Fatalf("revision types=%v unique_ids=%d", gotTypes, len(ids))
	}
	var canonicalRows int
	if err := st.db.QueryRowContext(ctx, `select count(*) from messages where event_id=?`, stableMessageEventID("100", "22")).Scan(&canonicalRows); err != nil {
		t.Fatal(err)
	}
	if canonicalRows != 1 {
		t.Fatalf("canonical rows = %d, want stable single identity", canonicalRows)
	}
}
