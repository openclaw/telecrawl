package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotRoundTripPreservesTelegramStructure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 3, 17, 53, 0, time.UTC)
	data := SnapshotData{
		Contacts: []Contact{{
			JID:       "9",
			PeerType:  "user",
			Phone:     "+15551234567",
			FullName:  "Peter Example",
			FirstName: "Peter",
			LastName:  "Example",
			Username:  "peter",
			UpdatedAt: now,
		}},
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
			MetadataType:  "web_page",
			MetadataTitle: "GitHub",
			MetadataURL:   "https://github.com/openclaw/telecrawl",
			MetadataJSON:  `{"url":"https://github.com/openclaw/telecrawl"}`,
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
	if got := len(exported.Contacts); got != 1 {
		t.Fatalf("contacts = %d, want 1", got)
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
	if msg.ReplyToID != "17" || msg.ReactionsJSON == "" || msg.ForwardJSON == "" || msg.Views != 10 || !msg.Pinned || msg.MetadataType != "web_page" || msg.MetadataURL == "" {
		t.Fatalf("message metadata lost: %#v", msg)
	}
	restoredExport, err := restored.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(restoredExport.Contacts) != 1 || restoredExport.Contacts[0].Phone != "+15551234567" || restoredExport.Contacts[0].PeerType != "user" {
		t.Fatalf("contact lost: %#v", restoredExport.Contacts)
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

func TestOpenMigratesSchema2MessageMetadataColumns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema2.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
create table messages (
	rowid integer primary key autoincrement,
	source_pk integer not null unique,
	chat_jid text not null,
	chat_name text,
	msg_id text not null,
	sender_jid text,
	sender_name text,
	ts integer not null,
	from_me integer not null,
	text text,
	raw_type integer not null default 0,
	message_type text,
	media_type text,
	media_title text,
	media_path text,
	media_url text,
	media_size integer,
	starred integer not null default 0,
	topic_id text,
	reply_to_msg_id text,
	reply_to_chat_jid text,
	thread_id text,
	edit_ts integer,
	forward_json text,
	reactions_json text,
	views integer not null default 0,
	forwards integer not null default 0,
	replies_count integer not null default 0,
	pinned integer not null default 0
);
pragma user_version = 2;
`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st := openTestStore(t, path)
	cols, err := columns(ctx, st.db, "messages")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"metadata_type", "metadata_title", "metadata_url", "metadata_json"} {
		if !cols[name] {
			t.Fatalf("missing migrated column %q", name)
		}
	}
	var version int
	if err := st.db.QueryRowContext(ctx, "pragma user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
}

func TestOpenMigratesSchema1BeforeCreatingTopicIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
create table chats (
	id integer primary key,
	kind text not null,
	name text,
	username text,
	last_message_at integer,
	unread_count integer not null default 0,
	message_count integer not null default 0
);
create table contacts (
	jid text primary key,
	phone text,
	full_name text,
	first_name text,
	last_name text,
	business_name text,
	username text,
	lid text,
	about_text text,
	updated_at integer
);
create table messages (
	rowid integer primary key autoincrement,
	source_pk integer not null unique,
	chat_jid text not null,
	chat_name text,
	msg_id text not null,
	sender_jid text,
	sender_name text,
	ts integer not null,
	from_me integer not null,
	text text,
	raw_type integer not null default 0,
	message_type text,
	media_type text,
	media_title text,
	media_path text,
	media_url text,
	media_size integer,
	starred integer not null default 0
);
create index idx_messages_chat_ts on messages(chat_jid, ts);
pragma user_version = 1;
`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st := openTestStore(t, path)
	cols, err := columns(ctx, st.db, "messages")
	if err != nil {
		t.Fatal(err)
	}
	if !cols["topic_id"] {
		t.Fatal("missing migrated topic_id column")
	}
	var indexName string
	if err := st.db.QueryRowContext(ctx, `select name from sqlite_master where type='index' and name='idx_messages_chat_topic_ts'`).Scan(&indexName); err != nil {
		t.Fatal(err)
	}
	if indexName != "idx_messages_chat_topic_ts" {
		t.Fatalf("topic index = %q", indexName)
	}
	var version int
	if err := st.db.QueryRowContext(ctx, "pragma user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
}

func TestMessagesToleratesNullableOptionalFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTestStore(t, filepath.Join(t.TempDir(), "nullable-messages.db"))
	if _, err := st.db.ExecContext(ctx, `insert into messages(source_pk,chat_jid,msg_id,ts,from_me,raw_type,starred) values(?,?,?,?,?,?,?)`, 1, "42", "1", unix(time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)), 0, 0, 0); err != nil {
		t.Fatal(err)
	}

	messages, err := st.Messages(ctx, MessageFilter{ChatJID: "42", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if messages[0].EditTime.IsZero() == false {
		t.Fatalf("edit time = %v, want zero", messages[0].EditTime)
	}
	if messages[0].ChatName != "" || messages[0].TopicID != "" || messages[0].ForwardJSON != "" {
		t.Fatalf("nullable fields not normalized: %#v", messages[0])
	}
}

func TestUpsertChatPreservesUnrelatedChats(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 3, 17, 53, 0, time.UTC)
	later := now.Add(time.Hour)

	st := openTestStore(t, filepath.Join(t.TempDir(), "upsert.db"))

	chatA := Chat{JID: "-1001", Kind: "channel", Name: "Chat A", LastMessageAt: now, UnreadCount: 1, MessageCount: 1, FolderID: "1", Forum: false}
	chatB := Chat{JID: "-1002", Kind: "group", Name: "Chat B", LastMessageAt: now, UnreadCount: 3, MessageCount: 2, FolderID: "2", Forum: true}
	topicA := Topic{ChatJID: "-1001", TopicID: "1", Title: "Topic A", LastMessageAt: now}
	topicB := Topic{ChatJID: "-1002", TopicID: "2", Title: "Topic B", LastMessageAt: now}
	fcA := FolderChat{FolderID: "1", ChatJID: "-1001", Position: 0}
	fcB := FolderChat{FolderID: "2", ChatJID: "-1002", Position: 1}
	msgA := Message{SourcePK: 1, ChatJID: "-1001", ChatName: "Chat A", MessageID: "1", SenderJID: "10", SenderName: "Alice", Timestamp: now, Text: "hello a", MessageType: "Message"}
	msgB1 := Message{SourcePK: 2, ChatJID: "-1002", ChatName: "Chat B", MessageID: "1", SenderJID: "20", SenderName: "Bob", Timestamp: now, Text: "hello b1", MessageType: "Message"}
	msgB2 := Message{SourcePK: 3, ChatJID: "-1002", ChatName: "Chat B", MessageID: "2", SenderJID: "20", SenderName: "Bob", Timestamp: later, Text: "hello b2", MessageType: "Message"}

	initial := ImportStats{SourcePath: "tdata", DBPath: st.Path(), Chats: 2, Messages: 3, StartedAt: now, FinishedAt: now}
	if err := st.ReplaceAll(
		ctx, initial,
		nil,
		[]Chat{chatA, chatB},
		[]Folder{{ID: "1", Title: "F1"}, {ID: "2", Title: "F2"}},
		[]FolderChat{fcA, fcB},
		[]Topic{topicA, topicB},
		[]Message{msgA, msgB1, msgB2},
	); err != nil {
		t.Fatal(err)
	}

	updatedChatA := Chat{JID: "-1001", Kind: "channel", Name: "Chat A Updated", LastMessageAt: later, UnreadCount: 5, MessageCount: 1, Forum: false}
	updatedMsgA := Message{SourcePK: 4, ChatJID: "-1001", ChatName: "Chat A Updated", MessageID: "2", SenderJID: "10", SenderName: "Alice", Timestamp: later, Text: "updated a", MessageType: "Message", MediaType: "photo", MediaTitle: "pic.jpg"}

	upsertStats := ImportStats{SourcePath: "tdata", DBPath: st.Path(), Chats: 1, Messages: 1, MediaMessages: 1, StartedAt: later, FinishedAt: later}
	if err := st.UpsertChat(
		ctx, upsertStats, "-1001",
		nil,
		[]Chat{updatedChatA},
		nil, nil,
		nil,
		[]Message{updatedMsgA},
	); err != nil {
		t.Fatal(err)
	}

	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Chats != 2 {
		t.Fatalf("chats = %d, want 2 (chat B preserved)", status.Chats)
	}
	if status.Messages != 3 {
		t.Fatalf("messages = %d, want 3 (2 from B + 1 updated A)", status.Messages)
	}
	if status.MediaMessages != 1 {
		t.Fatalf("media_messages = %d, want 1", status.MediaMessages)
	}
	if status.LastImportAt != later {
		t.Fatalf("last_import_at = %v, want %v", status.LastImportAt, later)
	}

	chats, err := st.ListChats(ctx, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 {
		t.Fatalf("chats list = %d, want 2", len(chats))
	}
	foundA, foundB := false, false
	for _, c := range chats {
		switch c.JID {
		case "-1001":
			foundA = true
			if c.Name != "Chat A Updated" {
				t.Fatalf("chat A name = %q, want %q", c.Name, "Chat A Updated")
			}
			if c.FolderID != "1" {
				t.Fatalf("chat A folder_id = %q, want preserved folder 1", c.FolderID)
			}
		case "-1002":
			foundB = true
			if c.Name != "Chat B" {
				t.Fatalf("chat B name = %q, want %q", c.Name, "Chat B")
			}
		}
	}
	if !foundA || !foundB {
		t.Fatalf("missing chats: A=%v B=%v", foundA, foundB)
	}

	msgAAll, err := st.Messages(ctx, MessageFilter{ChatJID: "-1001", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgAAll) != 1 || msgAAll[0].Text != "updated a" {
		t.Fatalf("chat A messages = %d (text=%q), want 1 (updated a)", len(msgAAll), msgAAll[0].Text)
	}

	msgBAll, err := st.Messages(ctx, MessageFilter{ChatJID: "-1002", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgBAll) != 2 {
		t.Fatalf("chat B messages = %d, want 2 (preserved)", len(msgBAll))
	}

	folders, err := st.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 2 {
		t.Fatalf("folders = %d, want 2", len(folders))
	}

	fcsA, err := st.ChatsInFolder(ctx, "1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fcsA) != 1 || fcsA[0].JID != "-1001" {
		t.Fatalf("folder 1 chats = %v, want chat A preserved", fcsA)
	}

	fcs, err := st.ChatsInFolder(ctx, "2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fcs) != 1 || fcs[0].JID != "-1002" {
		t.Fatalf("folder 2 chats = %v, want chat B only", fcs)
	}

	searchA, err := st.Search(ctx, MessageFilter{Query: "updated", ChatJID: "-1001", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(searchA) != 1 {
		t.Fatalf("FTS 'updated' in chat A = %d, want 1", len(searchA))
	}

	searchB, err := st.Search(ctx, MessageFilter{Query: "hello", ChatJID: "-1002", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(searchB) != 2 {
		t.Fatalf("FTS 'hello' in chat B = %d, want 2 (preserved)", len(searchB))
	}

	searchOld, err := st.Search(ctx, MessageFilter{Query: "hello a", ChatJID: "-1001", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(searchOld) != 0 {
		t.Fatalf("FTS 'hello a' in chat A = %d, want 0 (old FTS removed)", len(searchOld))
	}

	var sourcePath string
	if err := st.db.QueryRowContext(ctx, `select value from sync_state where key='source_path'`).Scan(&sourcePath); err != nil {
		t.Fatal(err)
	}
	if sourcePath != "tdata" {
		t.Fatalf("source_path = %q, want %q", sourcePath, "tdata")
	}
}
