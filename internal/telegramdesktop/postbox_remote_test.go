package telegramdesktop

import (
	"context"
	"reflect"
	"testing"
	"time"

	postboxpkg "github.com/openclaw/telecrawl/internal/telegramdesktop/postbox"
)

func TestPostboxRemoteMediaSkipsOldCandidates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		age, limit time.Duration
		candidates int
	}{
		{"old", 48 * time.Hour, time.Hour, 0},
		{"recent", time.Minute, time.Hour, 1},
		{"unlimited", 48 * time.Hour, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []postboxpkg.MessageRecord{{
				AccountID: "fixture", RawChatID: 42, MessageID: "0:1",
				TS: time.Now().Add(-tc.age).Unix(), Text: "preserve fixture metadata",
				MediaType: "photo", MediaTitle: "fixture.jpg",
				ReferencedMediaIDs: []postboxpkg.MediaRef{{}},
			}}
			before := messages[0]
			stats := downloadPostboxRemoteMedia(context.Background(), messages, nil, t.TempDir(), ImportOptions{FetchMediaMaxAge: tc.limit})
			if stats.Candidates != tc.candidates || stats.Attempted != 0 {
				t.Fatalf("stats = %+v, want %d candidates before session setup", stats, tc.candidates)
			}
			if !reflect.DeepEqual(messages[0], before) {
				t.Fatalf("metadata changed: %+v", messages[0])
			}
		})
	}
}

func TestPostboxRemoteMediaCandidateAgeBoundary(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		name       string
		age        time.Duration
		candidates int
	}{
		{"one second below limit", time.Hour - time.Second, 1},
		{"exactly at limit", time.Hour, 1},
		{"one second above limit", time.Hour + time.Second, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []postboxpkg.MessageRecord{{
				AccountID: "fixture", RawChatID: 42, MessageID: "0:1",
				TS: now.Add(-tc.age).Unix(), Text: "preserve fixture metadata",
				MediaType: "photo", MediaTitle: "fixture.jpg",
				ReferencedMediaIDs: []postboxpkg.MediaRef{{}},
			}}
			before := messages[0]
			indexes := postboxRemoteMediaCandidateIndexes(messages, ImportOptions{FetchMediaMaxAge: time.Hour}, now)
			if len(indexes) != tc.candidates {
				t.Fatalf("candidate indexes = %v, want %d candidates at age %s", indexes, tc.candidates, tc.age)
			}
			if !reflect.DeepEqual(messages[0], before) {
				t.Fatalf("metadata changed: %+v", messages[0])
			}
		})
	}
}
