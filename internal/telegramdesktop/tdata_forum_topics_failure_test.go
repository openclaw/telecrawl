package telegramdesktop

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func TestCollectForumTopicsRejectsIncompleteStall(t *testing.T) {
	page := repeatedForumTopicPage(1000, 2000)
	page.Count = 400
	calls := 0
	topics, err := collectForumTopics(context.Background(), "chat-1", 0, func(context.Context, *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
		calls++
		if calls > 3 {
			t.Fatal("pagination did not stop")
		}
		return page, nil
	})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("calls=%d topics=%d advertised=400 error=%v; incomplete pagination must fail", calls, len(topics), err)
	}
}

func TestCollectForumTopicsMissingTopMessage(t *testing.T) {
	for _, empty := range []bool{false, true} {
		calls := 0
		topics, err := collectForumTopics(context.Background(), "chat-1", 0, func(_ context.Context, req *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
			calls++
			if calls == 2 {
				if req.OffsetID != 0 || req.OffsetDate != 1000 {
					t.Fatalf("missing-message cursor=%+v", req)
				}
				return &tg.MessagesForumTopics{}, nil
			}
			if calls > 2 {
				t.Fatal("too many requests")
			}
			page := repeatedForumTopicPage(1000, 2000)
			page.Messages = nil
			if empty {
				page.Messages = []tg.MessageClass{&tg.MessageEmpty{ID: 1100}}
			}
			return page, nil
		})
		if err != nil || len(topics) != 100 {
			t.Fatalf("topics=%d error=%v", len(topics), err)
		}
	}
}

func TestCollectForumTopicsSkipsDeletedCursor(t *testing.T) {
	calls := 0
	topics, err := collectForumTopics(context.Background(), "chat-1", 0, func(_ context.Context, req *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
		calls++
		if calls == 2 {
			if req.OffsetTopic != 99 || req.OffsetID != 1099 || req.OffsetDate != 2000 {
				t.Fatalf("cursor=%+v", req)
			}
			return &tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{&tg.ForumTopicDeleted{ID: 100}}}, nil
		}
		if calls > 2 {
			t.Fatal("too many requests")
		}
		page := repeatedForumTopicPage(1000, 2000)
		page.Topics[99] = &tg.ForumTopicDeleted{ID: 100}
		return page, nil
	})
	if err != nil || len(topics) != 99 || calls != 2 {
		t.Fatalf("calls=%d topics=%d error=%v", calls, len(topics), err)
	}
}

func TestCollectForumTopicsContinuesShortPage(t *testing.T) {
	calls := 0
	topics, err := collectForumTopics(context.Background(), "chat-1", 0, func(_ context.Context, req *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
		calls++
		if calls == 3 {
			return &tg.MessagesForumTopics{}, nil
		}
		if calls > 3 {
			t.Fatal("too many requests")
		}
		if calls == 2 && req.OffsetTopic != 50 {
			t.Fatalf("offset=%d", req.OffsetTopic)
		}
		page := advancingForumTopicPage(req.OffsetTopic)
		page.Count = 150
		if calls == 1 {
			page.Topics = page.Topics[:50]
		}
		return page, nil
	})
	if err != nil || len(topics) != 150 || calls != 3 {
		t.Fatalf("calls=%d topics=%d error=%v", calls, len(topics), err)
	}
}

func TestCollectForumTopicsRejectsCycle(t *testing.T) {
	calls := 0
	_, err := collectForumTopics(context.Background(), "chat-1", 0, func(_ context.Context, _ *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
		calls++
		if calls > 3 {
			t.Fatal("cycle did not stop")
		}
		page := advancingForumTopicPage(((calls - 1) % 2) * 100)
		page.Count = 400
		return page, nil
	})
	if err == nil || calls != 3 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestCollectForumTopicsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := collectForumTopics(ctx, "chat-1", 0, func(context.Context, *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
		t.Fatal("RPC after cancellation")
		return nil, nil
	})
	if err != context.Canceled {
		t.Fatalf("error=%v", err)
	}
}

func TestCollectForumTopicsIgnoresApproximateCount(t *testing.T) {
	for _, count := range []int{50, 400} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			calls := 0
			topics, err := collectForumTopics(context.Background(), "chat-1", 0, func(_ context.Context, req *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
				calls++
				if calls > 3 {
					t.Fatal("too many requests")
				}
				page := advancingForumTopicPage(req.OffsetTopic)
				page.Count = count
				if calls == 2 {
					page.Topics = page.Topics[:1]
				}
				if calls == 3 {
					page.Topics = nil
				}
				return page, nil
			})
			if err != nil || len(topics) != 101 || calls != 3 {
				t.Fatalf("calls=%d topics=%d error=%v", calls, len(topics), err)
			}
		})
	}
}
