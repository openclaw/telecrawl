package postbox

import (
	"context"
	"database/sql"
	"encoding/binary"
	"path/filepath"
	"testing"
)

func TestLoadAccountPeerIDFromAuthorizedState(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`create table t0(key integer primary key, value blob not null)`); err != nil {
		t.Fatal(err)
	}
	const peerID int64 = 36513321142
	inner := append([]byte{6}, []byte("peerId")...)
	inner = append(inner, 1)
	value := make([]byte, 8)
	binary.LittleEndian.PutUint64(value, uint64(peerID))
	inner = append(inner, value...)
	state := []byte{1, '_', 5}
	value = make([]byte, 8)
	binary.LittleEndian.PutUint32(value[:4], 1)
	binary.LittleEndian.PutUint32(value[4:], uint32(len(inner)))
	state = append(state, value...)
	state = append(state, inner...)
	if _, err := db.Exec(`insert into t0(key,value) values(2,?)`, state); err != nil {
		t.Fatal(err)
	}
	got, err := loadAccountPeerID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if got != "36513321142" {
		t.Fatalf("account peer id = %q, want 36513321142", got)
	}
}

func TestLoadAccountPeerIDAllowsUnsupportedStateFallback(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`create table t0(key integer primary key, value blob not null); insert into t0(key,value) values(2,x'00')`); err != nil {
		t.Fatal(err)
	}
	got, err := loadAccountPeerID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("account peer id = %q, want database-key fallback", got)
	}
}

func TestReadSourceRecordsSQLCipherFixture(t *testing.T) {
	keyAndSalt := make([]byte, 48)
	for i := range keyAndSalt {
		keyAndSalt[i] = byte(i)
	}
	source := Source{
		AccountID: "stable/account-a",
		DBPath:    filepath.Join("testdata", "sqlcipher_v4.db"),
	}
	records, err := ReadSourceRecordsWithOptions(context.Background(), source, keyAndSalt, false, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := records.Peers["100"]; got != "Fixture Person" {
		t.Fatalf("peer display = %q", got)
	}
	if len(records.Contacts) != 1 {
		t.Fatalf("contacts = %#v", records.Contacts)
	}
	if contact := records.Contacts[0]; contact.ID != "100" || contact.PeerType != "user" || contact.FullName != "Fixture Person" || contact.FirstName != "Fixture" || contact.LastName != "Person" {
		t.Fatalf("contact = %#v", contact)
	}
	if len(records.Messages) != 1 {
		t.Fatalf("messages = %#v", records.Messages)
	}
	msg := records.Messages[0]
	if msg.ChatID != "100" || msg.ChatName != "Fixture Person" || msg.MessageID != "0:1" {
		t.Fatalf("message identity = %#v", msg)
	}
	if msg.Text != "fixture hello" || msg.SenderID != "4242" || msg.Timestamp != "2015-01-16T10:40:00Z" {
		t.Fatalf("message content = %#v", msg)
	}
	if msg.MediaType != "photo_or_video" || msg.MediaPath != "" || len(msg.ReferencedMediaIDs) != 1 || msg.ReferencedMediaIDs[0].ID != 123456789 {
		t.Fatalf("message media = %#v", msg)
	}
	if msg.SourcePK != SourcePK("stable/account-a", 100, 0, 1, false) {
		t.Fatalf("source pk = %d", msg.SourcePK)
	}
}

func TestReadSourceRecordsOptionsMatchFullFixture(t *testing.T) {
	keyAndSalt := make([]byte, 48)
	for i := range keyAndSalt {
		keyAndSalt[i] = byte(i)
	}
	source := Source{
		AccountID: "stable/account-a",
		DBPath:    filepath.Join("testdata", "sqlcipher_v4.db"),
	}
	full, err := ReadSourceRecordsWithOptions(context.Background(), source, keyAndSalt, false, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	limited, err := ReadSourceRecordsWithOptions(context.Background(), source, keyAndSalt, false, ReadOptions{
		DialogsLimit:  1,
		MessagesLimit: 1,
		ChatID:        "100",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Messages) != len(full.Messages) || limited.Messages[0].SourcePK != full.Messages[0].SourcePK || limited.Messages[0].Text != full.Messages[0].Text {
		t.Fatalf("limited messages = %#v, full = %#v", limited.Messages, full.Messages)
	}
	if len(limited.Contacts) != len(full.Contacts) || limited.Contacts[0].ID != full.Contacts[0].ID {
		t.Fatalf("limited contacts = %#v, full = %#v", limited.Contacts, full.Contacts)
	}
}
