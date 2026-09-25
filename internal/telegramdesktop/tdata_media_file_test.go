package telegramdesktop

import (
	"testing"
	"time"

	querymessages "github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
)

func TestTelegramMessageFileTreatsBrokenMediaAsNoFile(t *testing.T) {
	// A document message whose Document field is nil makes gotd's Elem.File
	// dereference a nil pointer; the importer must see "no file", not crash.
	elem := querymessages.Elem{Msg: &tg.Message{Media: &tg.MessageMediaDocument{}}}
	if _, ok := telegramMessageFile(elem); ok {
		t.Fatal("expected no file for a document message without a document")
	}
	empty := querymessages.Elem{Msg: &tg.Message{}}
	if _, ok := telegramMessageFile(empty); ok {
		t.Fatal("expected no file for a message without media")
	}
}

func TestRemoteMediaFetchAllowedHonoursBounds(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	recent := querymessages.Elem{Msg: &tg.Message{Date: int(now.Unix()) - 3600, Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 1, Size: 5 << 20}}}}
	old := querymessages.Elem{Msg: &tg.Message{Date: int(now.Unix()) - 30*24*3600, Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 2, Size: 5 << 20}}}}
	big := querymessages.Elem{Msg: &tg.Message{Date: int(now.Unix()) - 3600, Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 3, Size: 50 << 20}}}}

	unbounded := ImportOptions{}
	for _, elem := range []querymessages.Elem{recent, old, big} {
		if !remoteMediaFetchAllowed(elem, unbounded, now) {
			t.Fatal("unset bounds must allow every fetch")
		}
	}
	bounded := ImportOptions{FetchMediaMaxAge: 7 * 24 * time.Hour, FetchMediaMaxBytes: 20 << 20}
	if !remoteMediaFetchAllowed(recent, bounded, now) {
		t.Fatal("a recent small attachment must be fetched")
	}
	if remoteMediaFetchAllowed(old, bounded, now) {
		t.Fatal("an attachment older than the age bound must be skipped")
	}
	if remoteMediaFetchAllowed(big, bounded, now) {
		t.Fatal("an attachment above the size bound must be skipped")
	}
}
