package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func stableMessageEventID(chatJID, messageID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(chatJID) + "\x00" + strings.TrimSpace(messageID)))
	return "telegram-message-" + hex.EncodeToString(sum[:])
}

func stableLegacyMessageEventID(chatJID, messageID string, sourcePK int64, suffix int) string {
	discriminator := messageID + "\x00source-pk:" + strconv.FormatInt(sourcePK, 10)
	if suffix > 0 {
		discriminator += ":" + strconv.Itoa(suffix)
	}
	return stableMessageEventID(chatJID, discriminator)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func nullableUnix(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return unix(value)
}

func canonicalArchiveTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return time.Unix(value.Unix(), 0).UTC()
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func tombstoneUpdate(table string, preserve bool) (deletedAt, source, reason string) {
	if !preserve {
		return "excluded.deleted_at", "excluded.deletion_source", "excluded.deletion_reason"
	}
	return "coalesce(" + table + ".deleted_at,excluded.deleted_at)",
		"case when " + table + ".deleted_at is not null then " + table + ".deletion_source else excluded.deletion_source end",
		"case when " + table + ".deleted_at is not null then " + table + ".deletion_reason else excluded.deletion_reason end"
}

func conflictUpdateWhere(table string, preserveTombstones bool) string {
	if !preserveTombstones {
		return ""
	}
	return " where " + table + ".deleted_at is null"
}

func updateExistingTombstone(ctx context.Context, tx *sql.Tx, table, keyClause string, keyArgs []any, tombstone Tombstone, preserveTombstones bool) (bool, error) {
	if preserveTombstones {
		var exists int
		query := `select exists(select 1 from ` + table + ` where ` + keyClause + `)`
		if err := tx.QueryRowContext(ctx, query, keyArgs...).Scan(&exists); err != nil {
			return false, err
		}
		if exists != 0 {
			return true, nil
		}
	}
	query := `update ` + table + ` set deleted_at=?,deletion_source=?,deletion_reason=? where ` + keyClause
	args := []any{nullableUnix(tombstone.DeletedAt), nullableString(tombstone.DeletionSource), nullableString(tombstone.DeletionReason)}
	args = append(args, keyArgs...)
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func storedMessage(ctx context.Context, tx *sql.Tx, eventID string) (Message, bool, error) {
	row := tx.QueryRowContext(ctx, `select event_id,source_pk,chat_jid,coalesce(chat_name,''),msg_id,coalesce(sender_jid,''),coalesce(sender_name,''),ts,coalesce(edit_ts,0),from_me,coalesce(text,''),raw_type,coalesce(message_type,''),coalesce(media_type,''),coalesce(media_title,''),coalesce(media_path,''),coalesce(media_url,''),coalesce(media_size,0),coalesce(metadata_type,''),coalesce(metadata_title,''),coalesce(metadata_url,''),coalesce(metadata_json,''),starred,coalesce(topic_id,''),coalesce(reply_to_msg_id,''),coalesce(reply_to_chat_jid,''),coalesce(thread_id,''),coalesce(forward_json,''),coalesce(reactions_json,''),coalesce(views,0),coalesce(forwards,0),coalesce(replies_count,0),coalesce(pinned,0),deleted_at,coalesce(deletion_source,''),coalesce(deletion_reason,'') from messages where event_id=?`, eventID)
	message, err := scanSnapshotMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}
	return message, err == nil, err
}

func mergeStoredMedia(current, incoming Message) Message {
	if incoming.MediaType == "" {
		incoming.MediaType = current.MediaType
	}
	if incoming.MediaTitle == "" {
		incoming.MediaTitle = current.MediaTitle
	}
	if incoming.MediaPath == "" {
		incoming.MediaPath = current.MediaPath
		incoming.MediaSize = current.MediaSize
	}
	if incoming.MediaURL == "" {
		incoming.MediaURL = current.MediaURL
	}
	return incoming
}

func preserveDestinationMedia(current, incoming Message) Message {
	if current.MediaPath != "" {
		incoming.MediaPath = current.MediaPath
		incoming.MediaSize = current.MediaSize
	}
	return incoming
}

func normalizeTombstone(t *Tombstone, source, reason string) error {
	t.DeletedAt = canonicalArchiveTime(t.DeletedAt)
	if t.DeletedAt.IsZero() {
		if strings.TrimSpace(t.DeletionSource) != "" || strings.TrimSpace(t.DeletionReason) != "" {
			return errors.New("tombstone source/reason requires deleted_at")
		}
		return nil
	}
	if strings.TrimSpace(t.DeletionSource) == "" {
		t.DeletionSource = strings.TrimSpace(source)
	}
	if strings.TrimSpace(t.DeletionSource) == "" {
		t.DeletionSource = "unknown"
	}
	if strings.TrimSpace(t.DeletionReason) == "" {
		t.DeletionReason = reason
	}
	if strings.TrimSpace(t.DeletionReason) == "" {
		t.DeletionReason = "explicit-source-delete"
	}
	return nil
}

func normalizeMessage(message *Message, source string) error {
	if strings.TrimSpace(message.ChatJID) == "" || strings.TrimSpace(message.MessageID) == "" {
		return errors.New("message chat_jid and message_id are required")
	}
	if strings.TrimSpace(message.EventID) == "" {
		message.EventID = stableLegacyMessageEventID(message.ChatJID, message.MessageID, message.SourcePK, 0)
	}
	message.Timestamp = canonicalArchiveTime(message.Timestamp)
	message.EditTime = canonicalArchiveTime(message.EditTime)
	return normalizeTombstone(&message.Tombstone, source, "explicit-message-delete")
}

func messageRevisionPayload(message Message) (string, error) {
	message.Timestamp = canonicalArchiveTime(message.Timestamp)
	message.EditTime = canonicalArchiveTime(message.EditTime)
	message.DeletedAt = canonicalArchiveTime(message.DeletedAt)
	payload, err := json.Marshal(map[string]any{
		"chat_jid":            message.ChatJID,
		"chat_name":           message.ChatName,
		"message_id":          message.MessageID,
		"sender_jid":          message.SenderJID,
		"sender_name":         message.SenderName,
		"timestamp":           message.Timestamp,
		"edit_timestamp":      message.EditTime,
		"from_me":             message.FromMe,
		"text":                message.Text,
		"raw_type":            message.RawType,
		"message_type":        message.MessageType,
		"media_type":          message.MediaType,
		"media_title":         message.MediaTitle,
		"media_url":           message.MediaURL,
		"metadata_type":       message.MetadataType,
		"metadata_title":      message.MetadataTitle,
		"metadata_url":        message.MetadataURL,
		"metadata_json":       message.MetadataJSON,
		"starred":             message.Starred,
		"topic_id":            message.TopicID,
		"reply_to_message_id": message.ReplyToID,
		"reply_to_chat_id":    message.ReplyToChat,
		"thread_id":           message.ThreadID,
		"forward_json":        message.ForwardJSON,
		"reactions_json":      message.ReactionsJSON,
		"views":               message.Views,
		"forwards":            message.Forwards,
		"replies_count":       message.RepliesCount,
		"pinned":              message.Pinned,
		"deleted_at":          message.DeletedAt,
		"deletion_source":     message.DeletionSource,
		"deletion_reason":     message.DeletionReason,
	})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func newMessageRevision(message Message, payload, eventType string, eventAt, observedAt time.Time, source, reason, predecessorEventID string) MessageRevision {
	eventSource := strings.TrimSpace(source)
	if eventAt.IsZero() {
		eventAt = message.Timestamp
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	eventAt = canonicalArchiveTime(eventAt)
	observedAt = canonicalArchiveTime(observedAt)
	if eventSource == "" {
		eventSource = "unknown"
	}
	return MessageRevision{
		EventID:            stableRevisionEventID(message.EventID, eventType, eventAt, payload, predecessorEventID),
		MessageEventID:     message.EventID,
		EventType:          eventType,
		PayloadJSON:        string(payload),
		EventAt:            eventAt.UTC(),
		ObservedAt:         observedAt.UTC(),
		EventSource:        eventSource,
		Reason:             reason,
		PredecessorEventID: predecessorEventID,
	}
}

func stableRevisionEventID(messageEventID, eventType string, eventAt time.Time, payload, predecessorEventID string) string {
	payloadSum := sha256.Sum256([]byte(payload))
	identity := strings.Join([]string{
		messageEventID,
		eventType,
		strconv.FormatInt(eventAt.UTC().UnixNano(), 10),
		hex.EncodeToString(payloadSum[:]),
		predecessorEventID,
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "telegram-event-" + hex.EncodeToString(sum[:])
}

func insertMessageRevision(ctx context.Context, tx *sql.Tx, revision MessageRevision) error {
	if strings.TrimSpace(revision.EventID) == "" || strings.TrimSpace(revision.MessageEventID) == "" {
		return errors.New("message revision event identity is required")
	}
	if strings.TrimSpace(revision.EventType) == "" || strings.TrimSpace(revision.PayloadJSON) == "" || strings.TrimSpace(revision.Reason) == "" {
		return errors.New("message revision type, payload, and reason are required")
	}
	if !json.Valid([]byte(revision.PayloadJSON)) {
		return errors.New("message revision payload must be valid JSON")
	}
	_, err := tx.ExecContext(ctx, `insert into message_revisions(event_id,message_event_id,event_type,payload_json,event_at,observed_at,event_source,reason,predecessor_event_id)
		values(?,?,?,?,?,?,?,?,?) on conflict(event_id) do nothing`,
		revision.EventID, revision.MessageEventID, revision.EventType, revision.PayloadJSON,
		unix(revision.EventAt), unix(revision.ObservedAt), revision.EventSource, revision.Reason, nullableString(revision.PredecessorEventID))
	return err
}

func messageRevisionPredecessor(ctx context.Context, tx *sql.Tx, messageEventID, targetPayload string) (revisionOrder, int, error) {
	type revisionCandidate struct {
		payload string
		order   revisionOrder
	}
	rows, err := tx.QueryContext(ctx, `select payload_json,event_at,observed_at,event_id,event_type,coalesce(predecessor_event_id,'') from message_revisions where message_event_id=?`, messageEventID)
	if err != nil {
		return revisionOrder{}, 0, err
	}
	var candidates []revisionCandidate
	predecessors := make(map[string]string)
	for rows.Next() {
		var candidate revisionCandidate
		var eventAt, observedAt int64
		if err := rows.Scan(&candidate.payload, &eventAt, &observedAt, &candidate.order.eventID, &candidate.order.eventType, &candidate.order.predecessorEventID); err != nil {
			_ = rows.Close()
			return revisionOrder{}, 0, err
		}
		candidate.order.eventAt = fromUnix(eventAt)
		candidate.order.observedAt = fromUnix(observedAt)
		predecessors[candidate.order.eventID] = candidate.order.predecessorEventID
		candidates = append(candidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return revisionOrder{}, 0, err
	}
	if err := rows.Err(); err != nil {
		return revisionOrder{}, 0, err
	}
	var predecessor revisionOrder
	hasPredecessor := false
	for _, candidate := range candidates {
		if targetPayload != "" && candidate.payload != targetPayload {
			continue
		}
		if !hasPredecessor || preferRevisionOrder(candidate.order, predecessor, predecessors) {
			predecessor = candidate.order
			hasPredecessor = true
		}
	}
	if !hasPredecessor {
		for _, candidate := range candidates {
			if !hasPredecessor || preferRevisionOrder(candidate.order, predecessor, predecessors) {
				predecessor = candidate.order
				hasPredecessor = true
			}
		}
	}
	return predecessor, len(candidates), nil
}

func pendingMessageRevision(ctx context.Context, tx *sql.Tx, message Message, observedAt time.Time, source string) (*MessageRevision, error) {
	payload, err := messageRevisionPayload(message)
	if err != nil {
		return nil, err
	}
	canonical, hasCanonical, err := storedMessage(ctx, tx, message.EventID)
	if err != nil {
		return nil, err
	}
	canonicalPayload := ""
	if hasCanonical {
		canonicalPayload, err = messageRevisionPayload(canonical)
		if err != nil {
			return nil, err
		}
		if canonicalPayload == payload {
			return nil, nil
		}
	}
	predecessor, revisionCount, err := messageRevisionPredecessor(ctx, tx, message.EventID, canonicalPayload)
	if err != nil {
		return nil, err
	}
	hasPrevious := hasCanonical || revisionCount > 0
	eventType := "message_created"
	eventAt := message.Timestamp
	reason := "telegram-message-observed"
	eventSource := source
	if !message.DeletedAt.IsZero() {
		eventType = "message_deleted"
		eventAt = message.DeletedAt
		reason = message.DeletionReason
		eventSource = message.DeletionSource
	} else if hasPrevious {
		eventType = "message_edited"
		reason = "observable-payload-change"
		if !message.EditTime.IsZero() {
			eventAt = message.EditTime
			reason = "telegram-edit"
		}
	}
	revision := newMessageRevision(message, payload, eventType, eventAt, observedAt, eventSource, reason, predecessor.eventID)
	return &revision, nil
}

func seedMissingMessageBaselines(ctx context.Context, tx *sql.Tx, observedAt time.Time, reason string) error {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	const batchSize = 500
	lastRowID := int64(0)
	for {
		rowIDs, err := tx.QueryContext(ctx, `select m.rowid from messages m
			where m.rowid>? and not exists(select 1 from message_revisions r where r.message_event_id=m.event_id)
			order by m.rowid limit ?`, lastRowID, batchSize)
		if err != nil {
			return err
		}
		var firstRowID, finalRowID int64
		for rowIDs.Next() {
			var rowID int64
			if err := rowIDs.Scan(&rowID); err != nil {
				_ = rowIDs.Close()
				return err
			}
			if firstRowID == 0 {
				firstRowID = rowID
			}
			finalRowID = rowID
		}
		if err := rowIDs.Close(); err != nil {
			return err
		}
		if err := rowIDs.Err(); err != nil {
			return err
		}
		if firstRowID == 0 {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `select event_id,source_pk,chat_jid,coalesce(chat_name,''),msg_id,coalesce(sender_jid,''),coalesce(sender_name,''),ts,coalesce(edit_ts,0),from_me,coalesce(text,''),raw_type,coalesce(message_type,''),coalesce(media_type,''),coalesce(media_title,''),coalesce(media_path,''),coalesce(media_url,''),coalesce(media_size,0),coalesce(metadata_type,''),coalesce(metadata_title,''),coalesce(metadata_url,''),coalesce(metadata_json,''),starred,coalesce(topic_id,''),coalesce(reply_to_msg_id,''),coalesce(reply_to_chat_jid,''),coalesce(thread_id,''),coalesce(forward_json,''),coalesce(reactions_json,''),coalesce(views,0),coalesce(forwards,0),coalesce(replies_count,0),coalesce(pinned,0),deleted_at,coalesce(deletion_source,''),coalesce(deletion_reason,'')
			from messages m where m.rowid between ? and ? and not exists(select 1 from message_revisions r where r.message_event_id=m.event_id) order by m.rowid`, firstRowID, finalRowID)
		if err != nil {
			return err
		}
		messages := make([]Message, 0, batchSize)
		for rows.Next() {
			message, err := scanSnapshotMessage(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			messages = append(messages, message)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, message := range messages {
			payload, err := messageRevisionPayload(message)
			if err != nil {
				return err
			}
			eventType := "message_observed"
			eventAt := message.Timestamp
			eventSource := reason
			eventReason := reason
			if !message.DeletedAt.IsZero() {
				eventType = "message_deleted"
				eventAt = message.DeletedAt
				eventSource = firstNonEmptyString(message.DeletionSource, reason)
				eventReason = firstNonEmptyString(message.DeletionReason, reason)
			}
			revision := newMessageRevision(message, payload, eventType, eventAt, observedAt, eventSource, eventReason, "")
			if err := insertMessageRevision(ctx, tx, revision); err != nil {
				return err
			}
		}
		lastRowID = finalRowID
	}
}

type tombstoneTopicKey struct {
	chatJID string
	topicID string
}

type tombstoneScope struct {
	chats   map[string]struct{}
	folders map[string]struct{}
	topics  map[tombstoneTopicKey]struct{}
	groups  map[string]struct{}
}

func newTombstoneScope(chats []Chat, folders []Folder, folderChats []FolderChat, topics []Topic, groups []Group, participants []GroupParticipant, messages []Message) *tombstoneScope {
	scope := &tombstoneScope{
		chats:   make(map[string]struct{}),
		folders: make(map[string]struct{}),
		topics:  make(map[tombstoneTopicKey]struct{}),
		groups:  make(map[string]struct{}),
	}
	for _, chat := range chats {
		scope.chats[chat.JID] = struct{}{}
	}
	for _, folder := range folders {
		scope.folders[folder.ID] = struct{}{}
	}
	for _, membership := range folderChats {
		scope.folders[membership.FolderID] = struct{}{}
		scope.chats[membership.ChatJID] = struct{}{}
	}
	for _, topic := range topics {
		scope.chats[topic.ChatJID] = struct{}{}
		scope.topics[tombstoneTopicKey{chatJID: topic.ChatJID, topicID: topic.TopicID}] = struct{}{}
	}
	for _, group := range groups {
		scope.groups[group.JID] = struct{}{}
	}
	for _, participant := range participants {
		scope.groups[participant.GroupJID] = struct{}{}
	}
	for _, message := range messages {
		scope.chats[message.ChatJID] = struct{}{}
		if message.TopicID != "" {
			scope.topics[tombstoneTopicKey{chatJID: message.ChatJID, topicID: message.TopicID}] = struct{}{}
		}
	}
	return scope
}

func installTombstoneScope(ctx context.Context, tx *sql.Tx, scope *tombstoneScope) error {
	for _, statement := range []string{
		`create temporary table if not exists telecrawl_affected_chats(jid text primary key) without rowid`,
		`create temporary table if not exists telecrawl_affected_folders(id text primary key) without rowid`,
		`create temporary table if not exists telecrawl_affected_topics(chat_jid text,topic_id text,primary key(chat_jid,topic_id)) without rowid`,
		`create temporary table if not exists telecrawl_affected_groups(jid text primary key) without rowid`,
		`delete from telecrawl_affected_chats`,
		`delete from telecrawl_affected_folders`,
		`delete from telecrawl_affected_topics`,
		`delete from telecrawl_affected_groups`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for chatJID := range scope.chats {
		if _, err := tx.ExecContext(ctx, `insert or ignore into telecrawl_affected_chats(jid) values(?)`, chatJID); err != nil {
			return err
		}
	}
	for folderID := range scope.folders {
		if _, err := tx.ExecContext(ctx, `insert or ignore into telecrawl_affected_folders(id) values(?)`, folderID); err != nil {
			return err
		}
	}
	for topic := range scope.topics {
		if _, err := tx.ExecContext(ctx, `insert or ignore into telecrawl_affected_topics(chat_jid,topic_id) values(?,?)`, topic.chatJID, topic.topicID); err != nil {
			return err
		}
	}
	for groupJID := range scope.groups {
		if _, err := tx.ExecContext(ctx, `insert or ignore into telecrawl_affected_groups(jid) values(?)`, groupJID); err != nil {
			return err
		}
	}
	return nil
}

func propagateTombstones(ctx context.Context, tx *sql.Tx, scope *tombstoneScope) error {
	conditions := make([]string, 6)
	if scope != nil {
		if err := installTombstoneScope(ctx, tx, scope); err != nil {
			return err
		}
		conditions = []string{
			` and exists(select 1 from telecrawl_affected_folders a where a.id=folder_chats.folder_id)`,
			` and exists(select 1 from telecrawl_affected_chats a where a.jid=folder_chats.chat_jid)`,
			` and exists(select 1 from telecrawl_affected_chats a where a.jid=topics.chat_jid)`,
			` and messages.chat_jid in (select jid from telecrawl_affected_chats)`,
			` and (messages.chat_jid,messages.topic_id) in (select chat_jid,topic_id from telecrawl_affected_topics)`,
			` and exists(select 1 from telecrawl_affected_groups a where a.jid=group_participants.group_jid)`,
		}
	}
	statements := []string{
		`update folder_chats set
			deleted_at=coalesce(deleted_at,(select deleted_at from folders where folders.id=folder_chats.folder_id)),
			deletion_source=coalesce(deletion_source,(select deletion_source from folders where folders.id=folder_chats.folder_id)),
			deletion_reason=coalesce(deletion_reason,'parent_folder_deleted')
		where deleted_at is null` + conditions[0] + ` and exists(select 1 from folders where folders.id=folder_chats.folder_id and deleted_at is not null)`,
		`update folder_chats set
			deleted_at=coalesce(deleted_at,(select deleted_at from chats where cast(chats.id as text)=folder_chats.chat_jid)),
			deletion_source=coalesce(deletion_source,(select deletion_source from chats where cast(chats.id as text)=folder_chats.chat_jid)),
			deletion_reason=coalesce(deletion_reason,'parent_chat_deleted')
		where deleted_at is null` + conditions[1] + ` and exists(select 1 from chats where cast(chats.id as text)=folder_chats.chat_jid and deleted_at is not null)`,
		`update topics set
			deleted_at=coalesce(deleted_at,(select deleted_at from chats where cast(chats.id as text)=topics.chat_jid)),
			deletion_source=coalesce(deletion_source,(select deletion_source from chats where cast(chats.id as text)=topics.chat_jid)),
			deletion_reason=coalesce(deletion_reason,'parent_chat_deleted')
		where deleted_at is null` + conditions[2] + ` and exists(select 1 from chats where cast(chats.id as text)=topics.chat_jid and deleted_at is not null)`,
		`update messages set
			deleted_at=coalesce(deleted_at,(select deleted_at from chats where cast(chats.id as text)=messages.chat_jid)),
			deletion_source=coalesce(deletion_source,(select deletion_source from chats where cast(chats.id as text)=messages.chat_jid)),
			deletion_reason=coalesce(deletion_reason,'parent_chat_deleted')
		where deleted_at is null` + conditions[3] + ` and exists(select 1 from chats where cast(chats.id as text)=messages.chat_jid and deleted_at is not null)`,
		`update messages set
			deleted_at=coalesce(deleted_at,(select deleted_at from topics where topics.chat_jid=messages.chat_jid and topics.topic_id=messages.topic_id)),
			deletion_source=coalesce(deletion_source,(select deletion_source from topics where topics.chat_jid=messages.chat_jid and topics.topic_id=messages.topic_id)),
			deletion_reason=coalesce(deletion_reason,'parent_topic_deleted')
		where deleted_at is null and coalesce(topic_id,'')<>''` + conditions[4] + ` and exists(select 1 from topics where topics.chat_jid=messages.chat_jid and topics.topic_id=messages.topic_id and deleted_at is not null)`,
		`update group_participants set
			deleted_at=coalesce(deleted_at,(select deleted_at from groups where groups.jid=group_participants.group_jid)),
			deletion_source=coalesce(deletion_source,(select deletion_source from groups where groups.jid=group_participants.group_jid)),
			deletion_reason=coalesce(deletion_reason,'parent_group_deleted')
		where deleted_at is null` + conditions[5] + ` and exists(select 1 from groups where groups.jid=group_participants.group_jid and deleted_at is not null)`,
	}
	for i, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("propagate tombstones step %d: %w", i+1, err)
		}
	}
	return nil
}

func recordPropagatedMessageDeletions(ctx context.Context, tx *sql.Tx, observedAt time.Time, scope *tombstoneScope) error {
	scopeFilter := ""
	if scope != nil {
		scopeFilter = ` and m.event_id in (
			select candidate.event_id from telecrawl_affected_chats a join messages candidate on candidate.chat_jid=a.jid
			union
			select candidate.event_id from telecrawl_affected_topics a join messages candidate on candidate.chat_jid=a.chat_jid and candidate.topic_id=a.topic_id
		)`
	}
	type deletion struct {
		rowID          int64
		messageEventID string
		deletedAt      int64
		source         string
		reason         string
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	observedAt = canonicalArchiveTime(observedAt)
	const batchSize = 500
	lastRowID := int64(0)
	for {
		rows, err := tx.QueryContext(ctx, `select m.rowid,m.event_id,m.deleted_at,coalesce(m.deletion_source,''),coalesce(m.deletion_reason,'')
			from messages m
			where m.rowid>? and m.deleted_at is not null`+scopeFilter+`
			order by m.rowid limit ?`, lastRowID, batchSize)
		if err != nil {
			return err
		}
		pending := make([]deletion, 0, batchSize)
		for rows.Next() {
			var value deletion
			if err := rows.Scan(&value.rowID, &value.messageEventID, &value.deletedAt, &value.source, &value.reason); err != nil {
				_ = rows.Close()
				return err
			}
			pending = append(pending, value)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		for _, value := range pending {
			if value.source == "" {
				value.source = "unknown"
			}
			if value.reason == "" {
				value.reason = "parent-entity-deleted"
			}
			predecessor, _, err := messageRevisionPredecessor(ctx, tx, value.messageEventID, "")
			if err != nil {
				return err
			}
			eventAt := fromUnix(value.deletedAt)
			if predecessor.eventType == "message_deleted" && predecessor.eventAt.Equal(eventAt) {
				continue
			}
			payload, err := json.Marshal(map[string]any{
				"deleted_at":       fromUnix(value.deletedAt),
				"deletion_reason":  value.reason,
				"deletion_source":  value.source,
				"message_event_id": value.messageEventID,
			})
			if err != nil {
				return err
			}
			revision := MessageRevision{
				EventID:            stableRevisionEventID(value.messageEventID, "message_deleted", eventAt, string(payload), predecessor.eventID),
				MessageEventID:     value.messageEventID,
				EventType:          "message_deleted",
				PayloadJSON:        string(payload),
				EventAt:            eventAt,
				ObservedAt:         observedAt,
				EventSource:        value.source,
				Reason:             value.reason,
				PredecessorEventID: predecessor.eventID,
			}
			if err := insertMessageRevision(ctx, tx, revision); err != nil {
				return err
			}
		}
		lastRowID = pending[len(pending)-1].rowID
	}
}

func affectedChatJIDs(chats []Chat, topics []Topic, messages []Message) []string {
	seen := make(map[string]struct{}, len(chats)+len(topics)+len(messages))
	for _, chat := range chats {
		seen[chat.JID] = struct{}{}
	}
	for _, topic := range topics {
		seen[topic.ChatJID] = struct{}{}
	}
	for _, message := range messages {
		seen[message.ChatJID] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for chatJID := range seen {
		if strings.TrimSpace(chatJID) != "" {
			out = append(out, chatJID)
		}
	}
	return out
}

func recomputeChatAggregates(ctx context.Context, tx *sql.Tx, chatJIDs []string) error {
	for _, chatJID := range chatJIDs {
		if _, err := tx.ExecContext(ctx, `update chats set
			message_count=(select count(*) from messages where messages.chat_jid=? and messages.deleted_at is null),
			last_message_at=(select max(messages.ts) from messages where messages.chat_jid=? and messages.deleted_at is null)
			where chats.deleted_at is null and id=?`, chatJID, chatJID, parseInt64(chatJID)); err != nil {
			return err
		}
	}
	return nil
}

func rebuildMessageFTS(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `delete from messages_fts`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `insert into messages_fts(rowid,text,chat,sender,media)
		select rowid,trim(coalesce(text,'')||' '||coalesce(media_title,'')||' '||coalesce(metadata_title,'')||' '||coalesce(metadata_url,'')),coalesce(chat_name,''),coalesce(sender_name,''),coalesce(media_type,'')
		from messages where deleted_at is null`)
	return err
}

func pruneDeletedMessageFTS(ctx context.Context, tx *sql.Tx, scope *tombstoneScope) error {
	scopeFilter := ""
	if scope != nil {
		scopeFilter = ` and messages.event_id in (
			select candidate.event_id from telecrawl_affected_chats a join messages candidate on candidate.chat_jid=a.jid
			union
			select candidate.event_id from telecrawl_affected_topics a join messages candidate on candidate.chat_jid=a.chat_jid and candidate.topic_id=a.topic_id
		)`
	}
	_, err := tx.ExecContext(ctx, `delete from messages_fts where rowid in (select rowid from messages where deleted_at is not null`+scopeFilter+`)`)
	return err
}
