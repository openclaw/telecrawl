package telegramdesktop

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
)

func TestCollectForumTopicsStopsAfterAdvertisedCount(t *testing.T) {
	for _, tt := range []struct {
		name           string
		firstCount     int
		secondCount    int
		repeatedIndex  int
		newTopic       bool
		wantIncomplete bool
	}{
		{name: "under-count", firstCount: 6, secondCount: 6, repeatedIndex: 6},
		{name: "exact-count", firstCount: 7, secondCount: 7, repeatedIndex: 6},
		{name: "different-cursor", firstCount: 6, secondCount: 6},
		{name: "retain-count", firstCount: 7, repeatedIndex: 6},
		{name: "below-count", firstCount: 8, secondCount: 8, repeatedIndex: 6, wantIncomplete: true},
		{name: "unknown-count", repeatedIndex: 6, wantIncomplete: true},
		{name: "increased-count", firstCount: 7, secondCount: 8, repeatedIndex: 6, wantIncomplete: true},
		{name: "new-topic-repeated-cursor", firstCount: 7, secondCount: 7, repeatedIndex: 6, newTopic: true, wantIncomplete: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			topics, err := collectForumTopics(context.Background(), "chat-1", 0, func(_ context.Context, _ *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
				calls++
				if calls > 2 {
					t.Fatal("too many requests")
				}
				page := advancingForumTopicPage(0)
				page.Count = tt.firstCount
				if calls == 1 {
					page.Topics, page.Messages = page.Topics[:7], page.Messages[:7]
				} else {
					page.Count = tt.secondCount
					repeated := page.Topics[tt.repeatedIndex]
					if tt.newTopic {
						page.Topics = []tg.ForumTopicClass{page.Topics[7], repeated}
					} else {
						page.Topics = []tg.ForumTopicClass{repeated}
					}
				}
				return page, nil
			})
			if (err != nil) != tt.wantIncomplete || (tt.wantIncomplete && !errors.Is(err, errForumTopicsIncomplete)) {
				t.Fatalf("error=%v, wantIncomplete=%v", err, tt.wantIncomplete)
			}
			wantTopics := 7
			if tt.newTopic {
				wantTopics++
			}
			if len(topics) != wantTopics || calls != 2 {
				t.Fatalf("calls=%d topics=%d, want 2 calls and %d topics", calls, len(topics), wantTopics)
			}
		})
	}
}
