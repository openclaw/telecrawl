package telegramdesktop

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	querymessages "github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
)

func TestTelegramMessageFileTreatsBrokenMediaAsNoFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		media tg.MessageMediaClass
	}{
		{"no media", nil},
		{"missing document", &tg.MessageMediaDocument{}},
		{"empty document", &tg.MessageMediaDocument{Document: &tg.DocumentEmpty{}}},
		{"missing photo", &tg.MessageMediaPhoto{}},
		{"empty photo", &tg.MessageMediaPhoto{Photo: &tg.PhotoEmpty{}}},
		{"missing webpage", &tg.MessageMediaWebPage{}},
		{"webpage without file", &tg.MessageMediaWebPage{Webpage: &tg.WebPage{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			elem := querymessages.Elem{Msg: &tg.Message{Media: tc.media}}
			if _, ok := telegramMessageFile(elem); ok {
				t.Fatal("expected no file for fileless media")
			}
			dir := t.TempDir()
			path, size, reason := downloadTelegramMessageMedia(context.Background(), nil, elem, dir, "fixture", ImportOptions{FetchMediaMaxBytes: 1024})
			if path != "" || size != 0 || reason != "unavailable" {
				t.Fatalf("download result = %q, %d, %q", path, size, reason)
			}
			if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
				t.Fatalf("fileless media created files: %v, %v", files, err)
			}
		})
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

func TestRemoteMediaFetchAllowedAtLimits(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	opts := ImportOptions{FetchMediaMaxAge: time.Hour, FetchMediaMaxBytes: 1024}
	for _, tc := range []struct {
		name    string
		age     time.Duration
		size    int64
		allowed bool
	}{
		{"below both limits", time.Hour - time.Second, 1023, true},
		{"exactly both limits", time.Hour, 1024, true},
		{"one second too old", time.Hour + time.Second, 1024, false},
		{"one byte too large", time.Hour, 1025, false},
		{"unknown size at age limit", time.Hour, 0, true},
		{"unknown size too old", time.Hour + time.Second, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			elem := querymessages.Elem{Msg: &tg.Message{
				Date:  int(now.Add(-tc.age).Unix()),
				Media: &tg.MessageMediaDocument{Document: &tg.Document{Size: tc.size}},
			}}
			if got := remoteMediaFetchAllowed(elem, opts, now); got != tc.allowed {
				t.Fatalf("allowed = %v, want %v for age %s and size %d", got, tc.allowed, tc.age, tc.size)
			}
		})
	}
}

func TestRemoteMediaFetchAllowedWebpageBounds(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const limit = 20 << 20
	smallPhoto := &tg.Photo{Sizes: []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "x", W: 800, H: 600, Size: limit},
	}}
	largePhoto := &tg.Photo{Sizes: []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "x", W: 800, H: 600, Size: limit + 1},
	}}
	for _, tc := range []struct {
		name     string
		document tg.DocumentClass
		photo    tg.PhotoClass
		allowed  bool
	}{
		{name: "large document", document: &tg.Document{Size: limit + 1}},
		{name: "large photo", photo: largePhoto},
		{name: "photo at limit", photo: smallPhoto, allowed: true},
		{name: "document takes precedence", document: &tg.Document{Size: limit}, photo: largePhoto, allowed: true},
		{name: "large document with small photo", document: &tg.Document{Size: limit + 1}, photo: smallPhoto},
		{name: "empty document falls back to photo", document: &tg.DocumentEmpty{}, photo: largePhoto},
		{name: "unknown document size", document: &tg.Document{}, photo: largePhoto, allowed: true},
		{name: "no attachment", allowed: true},
		{name: "progressive photo", photo: &tg.Photo{Sizes: []tg.PhotoSizeClass{
			&tg.PhotoSizeProgressive{Type: "x", W: 800, H: 600, Sizes: []int{1024, limit + 1}},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := &tg.WebPage{}
			if tc.document != nil {
				page.SetDocument(tc.document)
			}
			if tc.photo != nil {
				page.SetPhoto(tc.photo)
			}
			elem := querymessages.Elem{Msg: &tg.Message{
				Date:  int(now.Unix()),
				Media: &tg.MessageMediaWebPage{Webpage: page},
			}}
			if got := remoteMediaFetchAllowed(elem, ImportOptions{FetchMediaMaxBytes: limit}, now); got != tc.allowed {
				t.Fatalf("allowed = %v, want %v", got, tc.allowed)
			}
			if !remoteMediaFetchAllowed(elem, ImportOptions{}, now) {
				t.Fatal("unset bounds must allow every fetch")
			}
		})
	}
}

func TestDownloadTelegramMessageMediaEnforcesBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		age     time.Duration
		size    int64
		opts    ImportOptions
		blocked bool
	}{
		{name: "old attachment", age: 48 * time.Hour, size: 1, opts: ImportOptions{FetchMediaMaxAge: time.Hour}, blocked: true},
		{name: "large attachment", size: 1025, opts: ImportOptions{FetchMediaMaxBytes: 1024}, blocked: true},
		{name: "at size limit", size: 1024, opts: ImportOptions{FetchMediaMaxBytes: 1024}},
		{name: "unset limits", age: 365 * 24 * time.Hour, size: 1 << 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			raw := tg.NewClient(telegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
				calls.Add(1)
				return errors.New("fixture download stopped")
			}))
			elem := querymessages.Elem{Msg: &tg.Message{
				Date: int(time.Now().Add(-tc.age).Unix()),
				Media: &tg.MessageMediaDocument{Document: &tg.Document{
					ID:   1,
					Size: tc.size,
				}},
			}}
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			path, size, reason := downloadTelegramMessageMedia(ctx, raw, elem, dir, "fixture", tc.opts)
			if path != "" || size != 0 {
				t.Fatalf("unexpected downloaded file: %q, %d", path, size)
			}
			if tc.blocked {
				if calls.Load() != 0 || reason != "unavailable" {
					t.Fatalf("blocked attachment reached downloader: calls=%d reason=%q", calls.Load(), reason)
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 0 {
					t.Fatalf("blocked attachment created files: entries=%v err=%v", entries, err)
				}
			} else if calls.Load() == 0 || reason != "error" {
				t.Fatalf("eligible attachment never reached fixture downloader: calls=%d reason=%q", calls.Load(), reason)
			}
		})
	}
}
