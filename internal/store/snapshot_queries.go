package store

import (
	"context"
	"database/sql"
)

type rowScanner interface {
	Scan(dest ...any) error
}

type snapshotQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func scanSnapshotMessage(row rowScanner) (Message, error) {
	var message Message
	var ts, editTS int64
	var fromMe, starred, pinned int
	var deletedAt sql.NullInt64
	if err := row.Scan(&message.EventID, &message.SourcePK, &message.ChatJID, &message.ChatName, &message.MessageID, &message.SenderJID, &message.SenderName, &ts, &editTS, &fromMe, &message.Text, &message.RawType, &message.MessageType, &message.MediaType, &message.MediaTitle, &message.MediaPath, &message.MediaURL, &message.MediaSize, &message.MetadataType, &message.MetadataTitle, &message.MetadataURL, &message.MetadataJSON, &starred, &message.TopicID, &message.ReplyToID, &message.ReplyToChat, &message.ThreadID, &message.ForwardJSON, &message.ReactionsJSON, &message.Views, &message.Forwards, &message.RepliesCount, &pinned, &deletedAt, &message.DeletionSource, &message.DeletionReason); err != nil {
		return Message{}, err
	}
	message.Timestamp = fromUnix(ts)
	message.EditTime = fromUnix(editTS)
	message.FromMe = fromMe != 0
	message.Starred = starred != 0
	message.Pinned = pinned != 0
	message.DeletedAt = fromNullUnix(deletedAt)
	return message, nil
}

func queryAllChats(ctx context.Context, q snapshotQueryer) ([]Chat, error) {
	rows, err := q.QueryContext(ctx, `select cast(id as text),kind,coalesce(name,''),coalesce(username,''),coalesce(last_message_at,0),unread_count,message_count,coalesce(folder_id,''),forum,deleted_at,coalesce(deletion_source,''),coalesce(deletion_reason,'') from chats order by id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Chat
	for rows.Next() {
		var chat Chat
		var lastMessageAt int64
		var forum int
		var deletedAt sql.NullInt64
		if err := rows.Scan(&chat.JID, &chat.Kind, &chat.Name, &chat.Username, &lastMessageAt, &chat.UnreadCount, &chat.MessageCount, &chat.FolderID, &forum, &deletedAt, &chat.DeletionSource, &chat.DeletionReason); err != nil {
			return nil, err
		}
		chat.LastMessageAt = fromUnix(lastMessageAt)
		chat.Forum = forum != 0
		chat.DeletedAt = fromNullUnix(deletedAt)
		out = append(out, chat)
	}
	return out, rows.Err()
}

func queryAllFolders(ctx context.Context, q snapshotQueryer) ([]Folder, error) {
	rows, err := q.QueryContext(ctx, `select id,coalesce(title,''),coalesce(emoticon,''),color,coalesce(flags_json,''),deleted_at,coalesce(deletion_source,''),coalesce(deletion_reason,'') from folders order by id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Folder
	for rows.Next() {
		var folder Folder
		var deletedAt sql.NullInt64
		if err := rows.Scan(&folder.ID, &folder.Title, &folder.Emoticon, &folder.Color, &folder.FlagsJSON, &deletedAt, &folder.DeletionSource, &folder.DeletionReason); err != nil {
			return nil, err
		}
		folder.DeletedAt = fromNullUnix(deletedAt)
		out = append(out, folder)
	}
	return out, rows.Err()
}

func queryAllGroups(ctx context.Context, q snapshotQueryer) ([]Group, error) {
	rows, err := q.QueryContext(ctx, `select jid,coalesce(name,''),coalesce(owner_jid,''),coalesce(created_at,0),deleted_at,coalesce(deletion_source,''),coalesce(deletion_reason,'') from groups order by jid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Group
	for rows.Next() {
		var group Group
		var createdAt int64
		var deletedAt sql.NullInt64
		if err := rows.Scan(&group.JID, &group.Name, &group.OwnerJID, &createdAt, &deletedAt, &group.DeletionSource, &group.DeletionReason); err != nil {
			return nil, err
		}
		group.CreatedAt = fromUnix(createdAt)
		group.DeletedAt = fromNullUnix(deletedAt)
		out = append(out, group)
	}
	return out, rows.Err()
}

func queryAllGroupParticipants(ctx context.Context, q snapshotQueryer) ([]GroupParticipant, error) {
	rows, err := q.QueryContext(ctx, `select group_jid,user_jid,coalesce(contact_name,''),coalesce(first_name,''),is_admin,is_active,deleted_at,coalesce(deletion_source,''),coalesce(deletion_reason,'') from group_participants order by group_jid,user_jid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []GroupParticipant
	for rows.Next() {
		var participant GroupParticipant
		var admin, active int
		var deletedAt sql.NullInt64
		if err := rows.Scan(&participant.GroupJID, &participant.UserJID, &participant.ContactName, &participant.FirstName, &admin, &active, &deletedAt, &participant.DeletionSource, &participant.DeletionReason); err != nil {
			return nil, err
		}
		participant.IsAdmin = admin != 0
		participant.IsActive = active != 0
		participant.DeletedAt = fromNullUnix(deletedAt)
		out = append(out, participant)
	}
	return out, rows.Err()
}

func queryAllMessages(ctx context.Context, q snapshotQueryer) ([]Message, error) {
	rows, err := q.QueryContext(ctx, `select event_id,source_pk,chat_jid,coalesce(chat_name,''),msg_id,coalesce(sender_jid,''),coalesce(sender_name,''),ts,coalesce(edit_ts,0),from_me,coalesce(text,''),raw_type,coalesce(message_type,''),coalesce(media_type,''),coalesce(media_title,''),coalesce(media_path,''),coalesce(media_url,''),coalesce(media_size,0),coalesce(metadata_type,''),coalesce(metadata_title,''),coalesce(metadata_url,''),coalesce(metadata_json,''),starred,coalesce(topic_id,''),coalesce(reply_to_msg_id,''),coalesce(reply_to_chat_jid,''),coalesce(thread_id,''),coalesce(forward_json,''),coalesce(reactions_json,''),coalesce(views,0),coalesce(forwards,0),coalesce(replies_count,0),coalesce(pinned,0),deleted_at,coalesce(deletion_source,''),coalesce(deletion_reason,'') from messages order by ts,event_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Message
	for rows.Next() {
		message, err := scanSnapshotMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, rows.Err()
}

func queryAllMessageRevisions(ctx context.Context, q snapshotQueryer) ([]MessageRevision, error) {
	rows, err := q.QueryContext(ctx, `select event_id,message_event_id,event_type,payload_json,event_at,observed_at,coalesce(event_source,''),reason,coalesce(predecessor_event_id,'') from message_revisions order by event_at,event_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []MessageRevision
	for rows.Next() {
		var revision MessageRevision
		var eventAt, observedAt int64
		if err := rows.Scan(&revision.EventID, &revision.MessageEventID, &revision.EventType, &revision.PayloadJSON, &eventAt, &observedAt, &revision.EventSource, &revision.Reason, &revision.PredecessorEventID); err != nil {
			return nil, err
		}
		revision.EventAt = fromUnix(eventAt)
		revision.ObservedAt = fromUnix(observedAt)
		out = append(out, revision)
	}
	return out, rows.Err()
}
