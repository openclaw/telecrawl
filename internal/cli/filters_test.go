package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/telecrawl/internal/store"
)

func TestSearchQueryAndFlags(t *testing.T) {
	path := filterFixture(t)
	for _, args := range [][]string{
		{"fixture", "--chat", "200", "--topic", "7", "--limit", "1"},
		{"--chat", "200", "--topic", "7", "--limit", "1", "fixture"},
		{"fixture", "--chat=200", "--topic=7", "--pinned"},
		{"--chat", "200", "--topic", "7", "--", "-fixture"},
	} {
		t.Run(args[0]+args[1], func(t *testing.T) {
			var out bytes.Buffer
			argv := append([]string{"--json", "--db", path, "search"}, args...)
			if err := Run(t.Context(), argv, &out, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			var messages []store.Message
			if err := json.Unmarshal(out.Bytes(), &messages); err != nil {
				t.Fatal(err)
			}
			if len(messages) != 1 || messages[0].ChatJID != "200" || messages[0].TopicID != "7" {
				t.Fatalf("search result = %+v", messages)
			}
		})
	}
}

func TestSearchRejectsInvalidTrailingFlags(t *testing.T) {
	for _, args := range [][]string{
		{"fixture", "--chat"},
		{"fixture", "--unknown"},
		{"fixture", "extra"},
		{"fixture", "--limit", "bad"},
		{"fixture", "--from-me", "--from-them"},
		{"--", "fixture", "--chat", "200"},
	} {
		r := &runtime{}
		if _, err := r.messageFilter("search", args, true); err == nil || ExitCode(err) != 2 {
			t.Fatalf("args %v: want usage error, got %v", args, err)
		}
	}
}

func TestChatsCombinesFolderUnreadAndLimit(t *testing.T) {
	path := filterFixture(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--folder", "2", "--unread", "--limit", "1"}, "200"},
		{[]string{"--folder", "2", "--limit", "1"}, "100"},
		{[]string{"--unread", "--limit", "1"}, "300"},
	} {
		var out bytes.Buffer
		argv := append([]string{"--json", "--db", path, "chats"}, tc.args...)
		if err := Run(t.Context(), argv, &out, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		var chats []store.Chat
		if err := json.Unmarshal(out.Bytes(), &chats); err != nil {
			t.Fatal(err)
		}
		if len(chats) != 1 || chats[0].JID != tc.want {
			t.Fatalf("args %v: chats = %+v, want %s", tc.args, chats, tc.want)
		}
	}
}

func filterFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "filters.db")
	st, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err = st.ReplaceAll(t.Context(), store.ImportStats{SourceIdentity: "synthetic:filters", FinishedAt: now}, nil,
		[]store.Chat{
			{JID: "100", Kind: "chat", Name: "Read", LastMessageAt: now},
			{JID: "200", Kind: "chat", Name: "Unread in folder", UnreadCount: 1, LastMessageAt: now},
			{JID: "300", Kind: "chat", Name: "Unread outside folder", UnreadCount: 1, LastMessageAt: now.Add(time.Hour)},
		},
		[]store.Folder{{ID: "2", Title: "Synthetic"}},
		[]store.FolderChat{{FolderID: "2", ChatJID: "100", Position: 0}, {FolderID: "2", ChatJID: "200", Position: 1}}, nil,
		[]store.Message{
			{SourcePK: 1, ChatJID: "200", MessageID: "1", TopicID: "7", Text: "-fixture hello", Pinned: true, Timestamp: now},
			{SourcePK: 2, ChatJID: "200", MessageID: "2", TopicID: "8", Text: "fixture another topic", Timestamp: now},
			{SourcePK: 3, ChatJID: "300", MessageID: "3", TopicID: "7", Text: "fixture another chat", Timestamp: now},
		})
	if err != nil {
		t.Fatal(err)
	}
	return path
}
