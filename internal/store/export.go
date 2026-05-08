package store

import (
	"context"
	"fmt"
	"time"
)

type SnapshotData struct {
	Contacts     []Contact
	Chats        []Chat
	Groups       []Group
	Participants []GroupParticipant
	Messages     []Message
}

func (d SnapshotData) Validate() error {
	seen := map[int64]struct{}{}
	for _, message := range d.Messages {
		if message.SourcePK == 0 {
			return fmt.Errorf("message with empty source_pk")
		}
		if _, ok := seen[message.SourcePK]; ok {
			return fmt.Errorf("duplicate message source_pk %d", message.SourcePK)
		}
		seen[message.SourcePK] = struct{}{}
	}
	return nil
}

func (s *Store) ExportAll(ctx context.Context) (SnapshotData, error) {
	chats, err := s.ListChats(ctx, int(^uint(0)>>1), false)
	if err != nil {
		return SnapshotData{}, err
	}
	messages, err := s.Messages(ctx, MessageFilter{Limit: int(^uint(0) >> 1), Asc: true})
	if err != nil {
		return SnapshotData{}, err
	}
	return SnapshotData{Chats: chats, Messages: messages}, nil
}

func (s *Store) ImportSnapshot(ctx context.Context, data SnapshotData, sourcePath string, finishedAt time.Time) error {
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	stats := ImportStats{SourcePath: sourcePath, DBPath: s.Path(), Chats: len(data.Chats), Messages: len(data.Messages), StartedAt: finishedAt, FinishedAt: finishedAt}
	for _, message := range data.Messages {
		if message.MediaType != "" || message.MediaPath != "" || message.MediaURL != "" {
			stats.MediaMessages++
		}
	}
	return s.ReplaceAll(ctx, stats, data.Chats, data.Messages)
}
