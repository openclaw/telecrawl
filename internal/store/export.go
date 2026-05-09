package store

import (
	"context"
	"fmt"
	"time"
)

type SnapshotData struct {
	Contacts     []Contact
	Chats        []Chat
	Folders      []Folder
	FolderChats  []FolderChat
	Groups       []Group
	Participants []GroupParticipant
	Topics       []Topic
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
	folders, err := s.ListFolders(ctx)
	if err != nil {
		return SnapshotData{}, err
	}
	folderChats, err := s.allFolderChats(ctx)
	if err != nil {
		return SnapshotData{}, err
	}
	topics, err := s.allTopics(ctx)
	if err != nil {
		return SnapshotData{}, err
	}
	messages, err := s.Messages(ctx, MessageFilter{Limit: int(^uint(0) >> 1), Asc: true})
	if err != nil {
		return SnapshotData{}, err
	}
	return SnapshotData{Chats: chats, Folders: folders, FolderChats: folderChats, Topics: topics, Messages: messages}, nil
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
	return s.ReplaceAll(ctx, stats, data.Chats, data.Folders, data.FolderChats, data.Topics, data.Messages)
}

func (s *Store) allFolderChats(ctx context.Context) ([]FolderChat, error) {
	rows, err := s.db.QueryContext(ctx, `select folder_id,chat_jid,position from folder_chats order by folder_id, position, chat_jid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FolderChat
	for rows.Next() {
		var fc FolderChat
		if err := rows.Scan(&fc.FolderID, &fc.ChatJID, &fc.Position); err != nil {
			return nil, err
		}
		out = append(out, fc)
	}
	return out, rows.Err()
}

func (s *Store) allTopics(ctx context.Context) ([]Topic, error) {
	rows, err := s.db.QueryContext(ctx, `select chat_jid,topic_id,title,top_message_id,icon_color,icon_emoji_id,unread_count,unread_mentions_count,unread_reactions_count,pinned,closed,hidden,last_message_at from topics order by chat_jid, cast(topic_id as integer)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Topic
	for rows.Next() {
		var t Topic
		var ts int64
		var pinned, closed, hidden int
		if err := rows.Scan(&t.ChatJID, &t.TopicID, &t.Title, &t.TopMessageID, &t.IconColor, &t.IconEmojiID, &t.UnreadCount, &t.UnreadMentionsCount, &t.UnreadReactionsCount, &pinned, &closed, &hidden, &ts); err != nil {
			return nil, err
		}
		t.Pinned = pinned != 0
		t.Closed = closed != 0
		t.Hidden = hidden != 0
		t.LastMessageAt = fromUnix(ts)
		out = append(out, t)
	}
	return out, rows.Err()
}
