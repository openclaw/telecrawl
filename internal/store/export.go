package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

type SnapshotData struct {
	SourceIdentity string
	Contacts       []Contact
	Chats          []Chat
	Folders        []Folder
	FolderChats    []FolderChat
	Groups         []Group
	Participants   []GroupParticipant
	Topics         []Topic
	Messages       []Message
	Revisions      []MessageRevision
}

// NormalizeLegacyEventIDs upgrades pre-v5 snapshots in memory. Legacy
// archives keyed messages by source_pk, so duplicate Telegram identities are
// disambiguated deterministically instead of making an otherwise valid backup
// unrestorable.
func (d *SnapshotData) NormalizeLegacyEventIDs() {
	normalizeLegacyMessageEventIDs(d.Messages)
}

func normalizeLegacyMessageEventIDs(messages []Message) {
	type telegramKey struct {
		chatJID   string
		messageID string
	}
	used := make(map[string]struct{}, len(messages))
	missingCounts := make(map[telegramKey]int)
	for _, message := range messages {
		if message.EventID != "" {
			used[message.EventID] = struct{}{}
		} else {
			missingCounts[telegramKey{chatJID: message.ChatJID, messageID: message.MessageID}]++
		}
	}
	indexes := make([]int, 0, len(messages))
	for i := range messages {
		if messages[i].EventID == "" {
			indexes = append(indexes, i)
		}
	}
	sort.SliceStable(indexes, func(i, j int) bool {
		left, right := messages[indexes[i]], messages[indexes[j]]
		if left.SourcePK != right.SourcePK {
			return left.SourcePK < right.SourcePK
		}
		if left.ChatJID != right.ChatJID {
			return left.ChatJID < right.ChatJID
		}
		return left.MessageID < right.MessageID
	})
	for _, index := range indexes {
		message := &messages[index]
		key := telegramKey{chatJID: message.ChatJID, messageID: message.MessageID}
		eventID := stableMessageEventID(message.ChatJID, message.MessageID)
		if missingCounts[key] > 1 {
			eventID = stableLegacyMessageEventID(message.ChatJID, message.MessageID, message.SourcePK, 0)
		}
		for suffix := 1; ; suffix++ {
			if _, exists := used[eventID]; !exists {
				break
			}
			eventID = stableLegacyMessageEventID(message.ChatJID, message.MessageID, message.SourcePK, suffix)
		}
		message.EventID = eventID
		used[eventID] = struct{}{}
	}
}

func normalizeImportedMessageEventIDs(ctx context.Context, tx *sql.Tx, messages []Message) error {
	used := make(map[string]struct{}, len(messages))
	type telegramKey struct {
		chatJID   string
		messageID string
	}
	groups := make(map[telegramKey][]int)
	for i := range messages {
		if messages[i].EventID != "" {
			used[messages[i].EventID] = struct{}{}
			continue
		}
		key := telegramKey{chatJID: messages[i].ChatJID, messageID: messages[i].MessageID}
		groups[key] = append(groups[key], i)
	}
	indexes := make([]int, 0, len(messages))
	for _, group := range groups {
		indexes = append(indexes, group...)
	}
	sort.SliceStable(indexes, func(i, j int) bool {
		left, right := messages[indexes[i]], messages[indexes[j]]
		if left.ChatJID != right.ChatJID {
			return left.ChatJID < right.ChatJID
		}
		if left.MessageID != right.MessageID {
			return left.MessageID < right.MessageID
		}
		return left.SourcePK < right.SourcePK
	})
	for _, index := range indexes {
		message := &messages[index]
		key := telegramKey{chatJID: message.ChatJID, messageID: message.MessageID}
		rows, err := tx.QueryContext(ctx, `select event_id,source_pk from messages where chat_jid=? and msg_id=? order by event_id`, message.ChatJID, message.MessageID)
		if err != nil {
			return err
		}
		type existingIdentity struct {
			eventID  string
			sourcePK int64
		}
		var existing []existingIdentity
		for rows.Next() {
			var identity existingIdentity
			if err := rows.Scan(&identity.eventID, &identity.sourcePK); err != nil {
				_ = rows.Close()
				return err
			}
			existing = append(existing, identity)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(existing) == 1 && len(groups[key]) == 1 {
			message.EventID = existing[0].eventID
			used[message.EventID] = struct{}{}
			continue
		}
		for _, identity := range existing {
			if identity.sourcePK == message.SourcePK {
				message.EventID = identity.eventID
				used[message.EventID] = struct{}{}
				break
			}
		}
		if message.EventID != "" {
			continue
		}
		candidate := stableMessageEventID(message.ChatJID, message.MessageID)
		if len(groups[key]) > 1 || len(existing) > 1 {
			candidate = stableLegacyMessageEventID(message.ChatJID, message.MessageID, message.SourcePK, 0)
		}
		for suffix := 1; ; suffix++ {
			_, batchCollision := used[candidate]
			var databaseCollision int
			if err := tx.QueryRowContext(ctx, `select exists(select 1 from messages where event_id=?)`, candidate).Scan(&databaseCollision); err != nil {
				return err
			}
			if !batchCollision && databaseCollision == 0 {
				break
			}
			candidate = stableLegacyMessageEventID(message.ChatJID, message.MessageID, message.SourcePK, suffix)
		}
		message.EventID = candidate
		used[candidate] = struct{}{}
	}
	return nil
}

func (d SnapshotData) Validate() error {
	events := map[string]struct{}{}
	for _, message := range d.Messages {
		if message.SourcePK == 0 {
			return fmt.Errorf("message with empty source_pk")
		}
		eventID := message.EventID
		if eventID == "" {
			eventID = stableLegacyMessageEventID(message.ChatJID, message.MessageID, message.SourcePK, 0)
		}
		if _, ok := events[eventID]; ok {
			return fmt.Errorf("duplicate message event_id %s", eventID)
		}
		events[eventID] = struct{}{}
	}
	revisions := map[string]struct{}{}
	for _, revision := range d.Revisions {
		if revision.EventID == "" || revision.MessageEventID == "" {
			return fmt.Errorf("message revision with empty event identity")
		}
		if _, ok := revisions[revision.EventID]; ok {
			return fmt.Errorf("duplicate message revision event_id %s", revision.EventID)
		}
		revisions[revision.EventID] = struct{}{}
	}
	return nil
}

func (s *Store) ExportAll(ctx context.Context) (SnapshotData, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SnapshotData{}, err
	}
	defer rollback(tx)
	var sourceIdentity string
	if err := tx.QueryRowContext(ctx, `select value from sync_state where key='source_identity'`).Scan(&sourceIdentity); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SnapshotData{}, err
	}
	contacts, err := queryAllContacts(ctx, tx)
	if err != nil {
		return SnapshotData{}, err
	}
	chats, err := queryAllChats(ctx, tx)
	if err != nil {
		return SnapshotData{}, err
	}
	folders, err := queryAllFolders(ctx, tx)
	if err != nil {
		return SnapshotData{}, err
	}
	folderChats, err := queryAllFolderChats(ctx, tx)
	if err != nil {
		return SnapshotData{}, err
	}
	topics, err := queryAllTopics(ctx, tx)
	if err != nil {
		return SnapshotData{}, err
	}
	groups, err := queryAllGroups(ctx, tx)
	if err != nil {
		return SnapshotData{}, err
	}
	participants, err := queryAllGroupParticipants(ctx, tx)
	if err != nil {
		return SnapshotData{}, err
	}
	messages, err := queryAllMessages(ctx, tx)
	if err != nil {
		return SnapshotData{}, err
	}
	revisions, err := queryAllMessageRevisions(ctx, tx)
	if err != nil {
		return SnapshotData{}, err
	}
	return SnapshotData{SourceIdentity: sourceIdentity, Contacts: contacts, Chats: chats, Folders: folders, FolderChats: folderChats, Groups: groups, Participants: participants, Topics: topics, Messages: messages, Revisions: revisions}, nil
}

func (s *Store) ImportSnapshot(ctx context.Context, data SnapshotData, sourcePath string, finishedAt time.Time) error {
	data.NormalizeLegacyEventIDs()
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	stats := ImportStats{SourcePath: sourcePath, SourceIdentity: data.SourceIdentity, DBPath: s.Path(), Chats: len(data.Chats), Messages: len(data.Messages), StartedAt: finishedAt, FinishedAt: finishedAt}
	for _, message := range data.Messages {
		if message.MediaType != "" || message.MediaPath != "" || message.MediaURL != "" {
			stats.MediaMessages++
		}
	}
	return s.MergeSnapshot(ctx, data, stats)
}

func (s *Store) ListContacts(ctx context.Context, limit int) ([]Contact, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.contacts(ctx, limit)
}

func (s *Store) ExportContacts(ctx context.Context) ([]Contact, error) {
	query := `select jid,coalesce(peer_type,''),coalesce(phone,''),coalesce(full_name,''),coalesce(first_name,''),coalesce(last_name,''),coalesce(business_name,''),coalesce(username,''),coalesce(lid,''),coalesce(about_text,''),coalesce(avatar_path,''),coalesce(updated_at,0)
from contacts c
where c.deleted_at is null and (
   exists (select 1 from chats ch where cast(ch.id as text)=c.jid and ch.deleted_at is null)
   or exists (select 1 from messages m where (m.chat_jid=c.jid or m.sender_jid=c.jid) and m.deleted_at is null)
)
order by jid`
	return s.queryContacts(ctx, query, nil)
}

func queryAllContacts(ctx context.Context, q snapshotQueryer) ([]Contact, error) {
	rows, err := q.QueryContext(ctx, `select jid,coalesce(peer_type,''),coalesce(phone,''),coalesce(full_name,''),coalesce(first_name,''),coalesce(last_name,''),coalesce(business_name,''),coalesce(username,''),coalesce(lid,''),coalesce(about_text,''),coalesce(avatar_path,''),coalesce(updated_at,0),deleted_at,coalesce(deletion_source,''),coalesce(deletion_reason,'') from contacts order by jid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Contact
	for rows.Next() {
		var contact Contact
		var updatedAt int64
		var deletedAt sql.NullInt64
		if err := rows.Scan(&contact.JID, &contact.PeerType, &contact.Phone, &contact.FullName, &contact.FirstName, &contact.LastName, &contact.BusinessName, &contact.Username, &contact.LID, &contact.AboutText, &contact.AvatarPath, &updatedAt, &deletedAt, &contact.DeletionSource, &contact.DeletionReason); err != nil {
			return nil, err
		}
		contact.UpdatedAt = fromUnix(updatedAt)
		contact.DeletedAt = fromNullUnix(deletedAt)
		out = append(out, contact)
	}
	return out, rows.Err()
}

func (s *Store) contacts(ctx context.Context, limit int) ([]Contact, error) {
	query := `select jid,coalesce(peer_type,''),coalesce(phone,''),coalesce(full_name,''),coalesce(first_name,''),coalesce(last_name,''),coalesce(business_name,''),coalesce(username,''),coalesce(lid,''),coalesce(about_text,''),coalesce(avatar_path,''),coalesce(updated_at,0) from contacts where deleted_at is null order by jid`
	args := []any{}
	if limit > 0 {
		query += " limit ?"
		args = append(args, limit)
	}
	return s.queryContacts(ctx, query, args)
}

func (s *Store) queryContacts(ctx context.Context, query string, args []any) ([]Contact, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Contact
	for rows.Next() {
		var c Contact
		var updatedAt int64
		if err := rows.Scan(&c.JID, &c.PeerType, &c.Phone, &c.FullName, &c.FirstName, &c.LastName, &c.BusinessName, &c.Username, &c.LID, &c.AboutText, &c.AvatarPath, &updatedAt); err != nil {
			return nil, err
		}
		c.UpdatedAt = fromUnix(updatedAt)
		out = append(out, c)
	}
	return out, rows.Err()
}

func queryAllFolderChats(ctx context.Context, q snapshotQueryer) ([]FolderChat, error) {
	rows, err := q.QueryContext(ctx, `select folder_id,chat_jid,position,deleted_at,coalesce(deletion_source,''),coalesce(deletion_reason,'') from folder_chats order by folder_id, position, chat_jid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FolderChat
	for rows.Next() {
		var fc FolderChat
		var deletedAt sql.NullInt64
		if err := rows.Scan(&fc.FolderID, &fc.ChatJID, &fc.Position, &deletedAt, &fc.DeletionSource, &fc.DeletionReason); err != nil {
			return nil, err
		}
		fc.DeletedAt = fromNullUnix(deletedAt)
		out = append(out, fc)
	}
	return out, rows.Err()
}

func queryAllTopics(ctx context.Context, q snapshotQueryer) ([]Topic, error) {
	rows, err := q.QueryContext(ctx, `select chat_jid,topic_id,coalesce(title,''),coalesce(top_message_id,''),icon_color,coalesce(icon_emoji_id,''),unread_count,unread_mentions_count,unread_reactions_count,pinned,closed,hidden,coalesce(last_message_at,0),deleted_at,coalesce(deletion_source,''),coalesce(deletion_reason,'') from topics order by chat_jid, cast(topic_id as integer)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Topic
	for rows.Next() {
		var t Topic
		var ts int64
		var deletedAt sql.NullInt64
		var pinned, closed, hidden int
		if err := rows.Scan(&t.ChatJID, &t.TopicID, &t.Title, &t.TopMessageID, &t.IconColor, &t.IconEmojiID, &t.UnreadCount, &t.UnreadMentionsCount, &t.UnreadReactionsCount, &pinned, &closed, &hidden, &ts, &deletedAt, &t.DeletionSource, &t.DeletionReason); err != nil {
			return nil, err
		}
		t.Pinned = pinned != 0
		t.Closed = closed != 0
		t.Hidden = hidden != 0
		t.LastMessageAt = fromUnix(ts)
		t.DeletedAt = fromNullUnix(deletedAt)
		out = append(out, t)
	}
	return out, rows.Err()
}
