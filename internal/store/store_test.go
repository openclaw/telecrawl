package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotRoundTripPreservesTelegramStructure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 3, 17, 53, 0, time.UTC)
	data := SnapshotData{
		Chats: []Chat{{
			JID:           "-10042",
			Kind:          "channel",
			Name:          "coding",
			LastMessageAt: now,
			UnreadCount:   1,
			MessageCount:  2,
			FolderID:      "2",
			Forum:         true,
		}},
		Folders: []Folder{{
			ID:        "2",
			Title:     "Clawd",
			Emoticon:  "laptop",
			Color:     3,
			FlagsJSON: `{"groups":true}`,
		}},
		FolderChats: []FolderChat{{
			FolderID: "2",
			ChatJID:  "-10042",
			Position: 0,
		}},
		Topics: []Topic{{
			ChatJID:              "-10042",
			TopicID:              "17",
			Title:                "General",
			TopMessageID:         "17",
			IconColor:            0x6fb9f0,
			UnreadCount:          1,
			UnreadMentionsCount:  1,
			UnreadReactionsCount: 1,
			Pinned:               true,
			LastMessageAt:        now,
		}},
		Messages: []Message{{
			SourcePK:      1,
			ChatJID:       "-10042",
			ChatName:      "coding",
			MessageID:     "18",
			TopicID:       "17",
			ReplyToID:     "17",
			ThreadID:      "17",
			SenderJID:     "9",
			SenderName:    "Peter",
			Timestamp:     now,
			EditTime:      now.Add(time.Minute),
			Text:          "yo",
			MessageType:   "Message",
			MediaType:     "webpage",
			MediaTitle:    "GitHub",
			MediaSize:     123,
			ForwardJSON:   `{"from_name":"someone"}`,
			ReactionsJSON: `{"results":[]}`,
			Views:         10,
			Forwards:      2,
			RepliesCount:  3,
			Pinned:        true,
		}},
	}

	source := openTestStore(t, filepath.Join(t.TempDir(), "source.db"))
	if err := source.ImportSnapshot(ctx, data, "tdata", now); err != nil {
		t.Fatal(err)
	}
	exported, err := source.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(exported.Folders); got != 1 {
		t.Fatalf("folders = %d, want 1", got)
	}
	if got := len(exported.FolderChats); got != 1 {
		t.Fatalf("folder chats = %d, want 1", got)
	}
	if got := len(exported.Topics); got != 1 {
		t.Fatalf("topics = %d, want 1", got)
	}

	restored := openTestStore(t, filepath.Join(t.TempDir(), "restored.db"))
	if err := restored.ImportSnapshot(ctx, exported, "backup", now); err != nil {
		t.Fatal(err)
	}
	chats, err := restored.ChatsInFolder(ctx, "2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Name != "coding" || !chats[0].Forum {
		t.Fatalf("folder chats = %#v", chats)
	}
	topics, err := restored.ListTopics(ctx, "-10042", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 || topics[0].TopicID != "17" || !topics[0].Pinned {
		t.Fatalf("topics = %#v", topics)
	}
	messages, err := restored.Messages(ctx, MessageFilter{ChatJID: "-10042", TopicID: "17", Pinned: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	msg := messages[0]
	if msg.ReplyToID != "17" || msg.ReactionsJSON == "" || msg.ForwardJSON == "" || msg.Views != 10 || !msg.Pinned {
		t.Fatalf("message metadata lost: %#v", msg)
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return st
}
