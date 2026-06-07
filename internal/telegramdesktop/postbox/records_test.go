package postbox

import (
	"context"
	"path/filepath"
	"testing"
)

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
