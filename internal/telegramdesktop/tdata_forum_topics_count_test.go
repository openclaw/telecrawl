package telegramdesktop

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
)

// The server can advertise fewer topics than it returns (count 6, 7 topics) and
// repeat already seen topics on the next page; that is the end of the list.
func TestCollectForumTopicsStopsAfterAdvertisedCount(t *testing.T) {
	calls := 0
	topics, err := collectForumTopics(context.Background(), "chat-1", 0, func(_ context.Context, _ *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
		calls++
		if calls > 2 {
			t.Fatal("too many requests")
		}
		page := advancingForumTopicPage(0)
		page.Count = 6
		if calls == 1 {
			page.Topics, page.Messages = page.Topics[:7], page.Messages[:7]
		} else {
			page.Topics, page.Messages = page.Topics[6:7], page.Messages[6:7]
		}
		return page, nil
	})
	if err != nil || len(topics) != 7 || calls != 2 {
		t.Fatalf("calls=%d topics=%d error=%v", calls, len(topics), err)
	}
}
