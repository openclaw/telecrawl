package telegramdesktop

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/gotd/td/tg"
)

func TestCollectForumTopicsStopsWhenOffsetsDoNotAdvance(t *testing.T) {
	page := repeatedForumTopicPage(1000, 2000)
	calls := 0
	topics, err := collectForumTopics(context.Background(), "chat-1", 0, func(_ context.Context, _ *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
		calls++
		if calls > 5 {
			t.Fatalf("pager called %d times; forum topic paging did not stop on a repeated page", calls)
		}
		return page, nil
	})
	if err != nil {
		t.Fatalf("collectForumTopics: %v", err)
	}
	if calls != 2 {
		t.Fatalf("pager calls = %d, want 2 (first page plus one repeated page)", calls)
	}
	if len(topics) != tdataBatchSize {
		t.Fatalf("topics = %d, want %d unique topics", len(topics), tdataBatchSize)
	}
	if topics[0].ChatJID != "chat-1" || topics[0].TopicID != "1" {
		t.Fatalf("first topic = %+v", topics[0])
	}
}

func TestCollectForumTopicsPagesByTopMessageDate(t *testing.T) {
	first := repeatedForumTopicPage(1000, 2000)
	var secondReq tg.MessagesGetForumTopicsRequest
	calls := 0
	_, err := collectForumTopics(context.Background(), "chat-1", 0, func(_ context.Context, req *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		secondReq = *req
		return &tg.MessagesForumTopics{
			Topics: []tg.ForumTopicClass{
				&tg.ForumTopic{ID: 101, Date: 900, Title: "next", TopMessage: 1201},
			},
			Messages: []tg.MessageClass{
				&tg.Message{ID: 1201, Date: 1900},
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("collectForumTopics: %v", err)
	}
	if secondReq.OffsetDate != 2000 {
		t.Fatalf("OffsetDate = %d, want 2000 (date of top_message, not topic.date=%d)", secondReq.OffsetDate, 1000)
	}
	if secondReq.OffsetTopic != tdataBatchSize {
		t.Fatalf("OffsetTopic = %d, want %d", secondReq.OffsetTopic, tdataBatchSize)
	}
	if secondReq.OffsetID != 1000+tdataBatchSize {
		t.Fatalf("OffsetID = %d, want %d", secondReq.OffsetID, 1000+tdataBatchSize)
	}
}

func TestCollectForumTopicsPagesByTopicDateWhenOrderedByCreateDate(t *testing.T) {
	first := repeatedForumTopicPage(1000, 2000)
	first.OrderByCreateDate = true
	var secondReq tg.MessagesGetForumTopicsRequest
	calls := 0
	_, err := collectForumTopics(context.Background(), "chat-1", 0, func(_ context.Context, req *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		secondReq = *req
		return &tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{
			&tg.ForumTopic{ID: 101, Date: 900, Title: "next", TopMessage: 1201},
		}}, nil
	})
	if err != nil {
		t.Fatalf("collectForumTopics: %v", err)
	}
	if secondReq.OffsetDate != 1000 {
		t.Fatalf("OffsetDate = %d, want 1000 (topic.date when order_by_create_date is set)", secondReq.OffsetDate)
	}
}

func TestCollectForumTopicsReturnsRPCError(t *testing.T) {
	want := errors.New("FLOOD_WAIT_30")
	_, err := collectForumTopics(context.Background(), "chat-1", 0, func(context.Context, *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestCollectForumTopicsCapsPages(t *testing.T) {
	const maxPages = 3
	calls := 0
	_, err := collectForumTopics(context.Background(), "chat-1", maxPages, func(_ context.Context, req *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
		calls++
		if calls > maxPages+2 {
			t.Fatalf("pager called %d times; page cap %d was ignored", calls, maxPages)
		}
		return advancingForumTopicPage(req.OffsetTopic), nil
	})
	if err != nil {
		t.Fatalf("collectForumTopics: %v", err)
	}
	if calls != maxPages {
		t.Fatalf("pager calls = %d, want page cap %d", calls, maxPages)
	}
}

func TestCollectForumTopicsSkipsNonForum(t *testing.T) {
	session := &tdataImportSession{}
	got, err := session.loadTopics(context.Background(), tdataDialog{forum: false, chatID: "chat-1"})
	if err != nil {
		t.Fatalf("loadTopics: %v", err)
	}
	if got != nil {
		t.Fatalf("loadTopics(non-forum) = %#v, want nil", got)
	}
}

func repeatedForumTopicPage(topicDate, messageDate int) *tg.MessagesForumTopics {
	topics := make([]tg.ForumTopicClass, tdataBatchSize)
	messages := make([]tg.MessageClass, tdataBatchSize)
	for i := 0; i < tdataBatchSize; i++ {
		id := i + 1
		topics[i] = &tg.ForumTopic{
			ID:         id,
			Date:       topicDate,
			Title:      "topic-" + strconv.Itoa(id),
			TopMessage: 1000 + id,
		}
		messages[i] = &tg.Message{
			ID:   1000 + id,
			Date: messageDate,
		}
	}
	return &tg.MessagesForumTopics{
		Count:    tdataBatchSize,
		Topics:   topics,
		Messages: messages,
	}
}

func advancingForumTopicPage(offsetTopic int) *tg.MessagesForumTopics {
	topics := make([]tg.ForumTopicClass, tdataBatchSize)
	messages := make([]tg.MessageClass, tdataBatchSize)
	for i := 0; i < tdataBatchSize; i++ {
		id := offsetTopic + i + 1
		topics[i] = &tg.ForumTopic{
			ID:         id,
			Date:       1000 + id,
			Title:      fmt.Sprintf("topic-%d", id),
			TopMessage: 10000 + id,
		}
		messages[i] = &tg.Message{
			ID:   10000 + id,
			Date: 2000 + id,
		}
	}
	return &tg.MessagesForumTopics{
		Count:    offsetTopic + tdataBatchSize,
		Topics:   topics,
		Messages: messages,
	}
}
