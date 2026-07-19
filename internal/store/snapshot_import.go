package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

func (s *Store) MergeSnapshot(ctx context.Context, data SnapshotData, stats ImportStats) error {
	data.NormalizeLegacyEventIDs()
	stats.SourceIdentity = data.SourceIdentity
	return s.importSnapshot(ctx, data, stats, false)
}

func (s *Store) RestoreSnapshot(ctx context.Context, data SnapshotData, sourcePath string, finishedAt time.Time) error {
	data.NormalizeLegacyEventIDs()
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	stats := snapshotStats(data, sourcePath, s.Path(), finishedAt)
	stats.SourceIdentity = data.SourceIdentity
	return s.importSnapshot(ctx, data, stats, true)
}

func snapshotStats(data SnapshotData, sourcePath, dbPath string, finishedAt time.Time) ImportStats {
	stats := ImportStats{SourcePath: sourcePath, DBPath: dbPath, Chats: len(data.Chats), Messages: len(data.Messages), StartedAt: finishedAt, FinishedAt: finishedAt}
	for _, message := range data.Messages {
		if message.DeletedAt.IsZero() && (message.MediaType != "" || message.MediaPath != "" || message.MediaURL != "") {
			stats.MediaMessages++
		}
	}
	return stats
}

func (s *Store) importSnapshot(ctx context.Context, data SnapshotData, stats ImportStats, restore bool) error {
	source := firstNonEmptyString(stats.SourceIdentity, stats.SourcePath)
	for i := range data.Messages {
		if err := normalizeMessage(&data.Messages[i], source); err != nil {
			return err
		}
	}
	for i := range data.Revisions {
		data.Revisions[i].EventAt = canonicalArchiveTime(data.Revisions[i].EventAt)
		data.Revisions[i].ObservedAt = canonicalArchiveTime(data.Revisions[i].ObservedAt)
	}
	if err := data.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if !restore {
		if err := ensureSnapshotMergeSource(ctx, tx, data.SourceIdentity); err != nil {
			return err
		}
	}
	if restore {
		for _, query := range []string{
			"delete from messages_fts",
			"delete from message_revisions",
			"delete from messages",
			"delete from topics",
			"delete from folder_chats",
			"delete from folders",
			"delete from chats",
			"delete from contacts",
			"delete from group_participants",
			"delete from groups",
			"delete from sync_state",
		} {
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return err
			}
		}
	}
	if !restore {
		if err := reconcileSnapshotMessageEventIDs(ctx, tx, &data); err != nil {
			return err
		}
	}
	messages := data.Messages
	if !restore {
		messages, err = mergeableSnapshotMessages(ctx, tx, data.Messages, data.Revisions)
		if err != nil {
			return err
		}
	}
	if err := writeImport(ctx, tx, stats, data.Contacts, data.Chats, data.Folders, data.FolderChats, data.Topics, messages, !restore, restore, false); err != nil {
		return err
	}
	if err := insertGroups(ctx, tx, data.Groups, firstNonEmptyString(stats.SourceIdentity, stats.SourcePath), !restore); err != nil {
		return err
	}
	if err := insertGroupParticipants(ctx, tx, data.Participants, firstNonEmptyString(stats.SourceIdentity, stats.SourcePath), !restore); err != nil {
		return err
	}
	for _, revision := range data.Revisions {
		if err := insertMessageRevision(ctx, tx, revision); err != nil {
			return err
		}
	}
	if err := seedMissingMessageBaselines(ctx, tx, stats.FinishedAt, "snapshot-baseline"); err != nil {
		return err
	}
	var scope *tombstoneScope
	if !restore {
		scope = newTombstoneScope(data.Chats, data.Folders, data.FolderChats, data.Topics, data.Groups, data.Participants, data.Messages)
	}
	if err := propagateTombstones(ctx, tx, scope); err != nil {
		return err
	}
	if err := recordPropagatedMessageDeletions(ctx, tx, stats.FinishedAt, scope); err != nil {
		return err
	}
	if !restore {
		if err := recomputeChatAggregates(ctx, tx, affectedChatJIDs(data.Chats, data.Topics, data.Messages)); err != nil {
			return err
		}
	}
	if err := storeSnapshotSourceIdentity(ctx, tx, data.SourceIdentity, stats.FinishedAt); err != nil {
		return err
	}
	if restore {
		if err := rebuildMessageFTS(ctx, tx); err != nil {
			return err
		}
	} else if err := pruneDeletedMessageFTS(ctx, tx, scope); err != nil {
		return err
	}
	return tx.Commit()
}

func reconcileSnapshotMessageEventIDs(ctx context.Context, tx *sql.Tx, data *SnapshotData) error {
	type telegramKey struct {
		chatJID   string
		messageID string
	}
	incomingFamilies := make(map[telegramKey]int)
	for _, message := range data.Messages {
		incomingFamilies[telegramKey{chatJID: message.ChatJID, messageID: message.MessageID}]++
	}
	aliases := make(map[string]string)
	for i := range data.Messages {
		message := &data.Messages[i]
		var exact int
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from messages where event_id=?)`, message.EventID).Scan(&exact); err != nil {
			return err
		}
		if exact != 0 {
			continue
		}
		rows, err := tx.QueryContext(ctx, `select event_id,source_pk from messages where chat_jid=? and msg_id=? order by event_id`, message.ChatJID, message.MessageID)
		if err != nil {
			return err
		}
		type identity struct {
			eventID  string
			sourcePK int64
		}
		var matches []identity
		for rows.Next() {
			var match identity
			if err := rows.Scan(&match.eventID, &match.sourcePK); err != nil {
				_ = rows.Close()
				return err
			}
			matches = append(matches, match)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		resolved := ""
		key := telegramKey{chatJID: message.ChatJID, messageID: message.MessageID}
		if len(matches) == 1 && incomingFamilies[key] == 1 {
			resolved = matches[0].eventID
		} else {
			for _, match := range matches {
				if match.sourcePK == message.SourcePK {
					if resolved != "" {
						resolved = ""
						break
					}
					resolved = match.eventID
				}
			}
		}
		if resolved != "" {
			aliases[message.EventID] = resolved
			message.EventID = resolved
		}
	}
	if len(aliases) == 0 {
		return nil
	}
	revisionsByID := make(map[string]MessageRevision, len(data.Revisions))
	for _, revision := range data.Revisions {
		revisionsByID[revision.EventID] = revision
	}
	remappedRevisionIDs := make(map[string]string)
	visiting := make(map[string]bool)
	var remapRevisionID func(string) string
	remapRevisionID = func(eventID string) string {
		if eventID == "" {
			return ""
		}
		if remapped, ok := remappedRevisionIDs[eventID]; ok {
			return remapped
		}
		revision, ok := revisionsByID[eventID]
		if !ok || visiting[eventID] {
			return eventID
		}
		messageEventID, changed := aliases[revision.MessageEventID]
		if !changed {
			remappedRevisionIDs[eventID] = eventID
			return eventID
		}
		visiting[eventID] = true
		predecessor := remapRevisionID(revision.PredecessorEventID)
		delete(visiting, eventID)
		remapped := stableRevisionEventID(messageEventID, revision.EventType, revision.EventAt, revision.PayloadJSON, predecessor)
		remappedRevisionIDs[eventID] = remapped
		return remapped
	}
	for i := range data.Revisions {
		revision := &data.Revisions[i]
		messageEventID, changed := aliases[revision.MessageEventID]
		if !changed {
			continue
		}
		oldEventID := revision.EventID
		revision.MessageEventID = messageEventID
		revision.PredecessorEventID = remapRevisionID(revision.PredecessorEventID)
		revision.EventID = remapRevisionID(oldEventID)
	}
	return nil
}

type revisionOrder struct {
	eventAt            time.Time
	observedAt         time.Time
	eventID            string
	eventType          string
	predecessorEventID string
}

func revisionDescendsFrom(eventID, ancestorID string, predecessors map[string]string) bool {
	if eventID == "" || ancestorID == "" || eventID == ancestorID {
		return false
	}
	seen := make(map[string]struct{})
	for current := eventID; current != ""; current = predecessors[current] {
		if current == ancestorID {
			return true
		}
		if _, exists := seen[current]; exists {
			return false
		}
		seen[current] = struct{}{}
	}
	return false
}

func preferRevisionOrder(candidate, current revisionOrder, predecessors map[string]string) bool {
	if revisionDescendsFrom(candidate.eventID, current.eventID, predecessors) {
		return true
	}
	if revisionDescendsFrom(current.eventID, candidate.eventID, predecessors) {
		return false
	}
	if comparison := compareRevisionOrder(candidate, current); comparison != 0 {
		return comparison > 0
	}
	return candidate.eventID > current.eventID
}

func compareRevisionOrder(left, right revisionOrder) int {
	if !left.eventAt.Equal(right.eventAt) {
		if left.eventAt.After(right.eventAt) {
			return 1
		}
		return -1
	}
	return 0
}

func canonicalRevisionOrder(order revisionOrder, message Message) revisionOrder {
	if message.EditTime.After(order.eventAt) {
		order.eventAt = message.EditTime
	} else if message.EditTime.IsZero() && order.eventType == "message_edited" && order.observedAt.After(order.eventAt) {
		order.eventAt = order.observedAt
	}
	return order
}

func mergeableSnapshotMessages(ctx context.Context, tx *sql.Tx, incoming []Message, revisions []MessageRevision) ([]Message, error) {
	if len(incoming) == 0 {
		return incoming, nil
	}
	if _, err := tx.ExecContext(ctx, `create temporary table if not exists snapshot_incoming_events(event_id text primary key) without rowid`); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `delete from snapshot_incoming_events`); err != nil {
		return nil, err
	}
	for _, message := range incoming {
		if _, err := tx.ExecContext(ctx, `insert or ignore into snapshot_incoming_events(event_id) values(?)`, message.EventID); err != nil {
			return nil, err
		}
	}
	defer func() { _, _ = tx.ExecContext(ctx, `drop table if exists snapshot_incoming_events`) }()

	existing := make(map[string]Message)
	rows, err := tx.QueryContext(ctx, `select m.event_id,m.source_pk,m.chat_jid,coalesce(m.chat_name,''),m.msg_id,coalesce(m.sender_jid,''),coalesce(m.sender_name,''),m.ts,coalesce(m.edit_ts,0),m.from_me,coalesce(m.text,''),m.raw_type,coalesce(m.message_type,''),coalesce(m.media_type,''),coalesce(m.media_title,''),coalesce(m.media_path,''),coalesce(m.media_url,''),coalesce(m.media_size,0),coalesce(m.metadata_type,''),coalesce(m.metadata_title,''),coalesce(m.metadata_url,''),coalesce(m.metadata_json,''),m.starred,coalesce(m.topic_id,''),coalesce(m.reply_to_msg_id,''),coalesce(m.reply_to_chat_jid,''),coalesce(m.thread_id,''),coalesce(m.forward_json,''),coalesce(m.reactions_json,''),coalesce(m.views,0),coalesce(m.forwards,0),coalesce(m.replies_count,0),coalesce(m.pinned,0),m.deleted_at,coalesce(m.deletion_source,''),coalesce(m.deletion_reason,'')
		from messages m join snapshot_incoming_events i on i.event_id=m.event_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		message, err := scanSnapshotMessage(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		existing[message.EventID] = message
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type revisionKey struct {
		messageEventID string
		payloadJSON    string
	}
	predecessors := make(map[string]string)
	existingOrders := make(map[revisionKey]revisionOrder)
	existingTips := make(map[string]revisionOrder)
	rows, err = tx.QueryContext(ctx, `select r.message_event_id,r.payload_json,r.event_at,r.observed_at,r.event_id,r.event_type,coalesce(r.predecessor_event_id,'')
		from message_revisions r join snapshot_incoming_events i on i.event_id=r.message_event_id order by r.rowid`)
	if err != nil {
		return nil, err
	}
	type storedRevisionOrder struct {
		key   revisionKey
		order revisionOrder
	}
	var storedOrders []storedRevisionOrder
	for rows.Next() {
		var key revisionKey
		var eventAt, observedAt int64
		var order revisionOrder
		if err := rows.Scan(&key.messageEventID, &key.payloadJSON, &eventAt, &observedAt, &order.eventID, &order.eventType, &order.predecessorEventID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		order.eventAt = fromUnix(eventAt)
		order.observedAt = fromUnix(observedAt)
		predecessors[order.eventID] = order.predecessorEventID
		storedOrders = append(storedOrders, storedRevisionOrder{key: key, order: order})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, revision := range revisions {
		predecessors[revision.EventID] = revision.PredecessorEventID
	}
	for _, stored := range storedOrders {
		if previous, ok := existingOrders[stored.key]; !ok || preferRevisionOrder(stored.order, previous, predecessors) {
			existingOrders[stored.key] = stored.order
		}
		if previous, ok := existingTips[stored.key.messageEventID]; !ok || preferRevisionOrder(stored.order, previous, predecessors) {
			existingTips[stored.key.messageEventID] = stored.order
		}
	}

	incomingOrders := make(map[revisionKey]revisionOrder)
	incomingTips := make(map[string]revisionOrder)
	for _, revision := range revisions {
		key := revisionKey{messageEventID: revision.MessageEventID, payloadJSON: revision.PayloadJSON}
		order := revisionOrder{eventAt: revision.EventAt, observedAt: revision.ObservedAt, eventID: revision.EventID, eventType: revision.EventType, predecessorEventID: revision.PredecessorEventID}
		if previous, ok := incomingOrders[key]; !ok || preferRevisionOrder(order, previous, predecessors) {
			incomingOrders[key] = order
		}
		if previous, ok := incomingTips[revision.MessageEventID]; !ok || preferRevisionOrder(order, previous, predecessors) {
			incomingTips[revision.MessageEventID] = order
		}
	}

	merged := make([]Message, 0, len(incoming))
	for _, message := range incoming {
		current, found := existing[message.EventID]
		if !found {
			merged = append(merged, message)
			continue
		}
		message = preserveDestinationMedia(current, message)
		currentPayload, err := messageRevisionPayload(current)
		if err != nil {
			return nil, err
		}
		incomingPayload, err := messageRevisionPayload(message)
		if err != nil {
			return nil, err
		}
		if currentPayload == incomingPayload {
			merged = append(merged, message)
			continue
		}
		currentOrder, currentOK := existingOrders[revisionKey{messageEventID: current.EventID, payloadJSON: currentPayload}]
		incomingOrder, incomingOK := incomingOrders[revisionKey{messageEventID: message.EventID, payloadJSON: incomingPayload}]
		if !currentOK {
			currentOrder, currentOK = existingTips[current.EventID]
		}
		if !incomingOK && !message.DeletedAt.IsZero() {
			incomingOrder, incomingOK = incomingTips[message.EventID]
		}
		if currentOK {
			currentOrder = canonicalRevisionOrder(currentOrder, current)
		}
		if incomingOK {
			incomingOrder = canonicalRevisionOrder(incomingOrder, message)
		}
		if !current.DeletedAt.IsZero() && message.DeletedAt.IsZero() {
			if currentOK && incomingOK && revisionDescendsFrom(incomingOrder.eventID, currentOrder.eventID, predecessors) {
				merged = append(merged, message)
			}
			continue
		}
		switch {
		case currentOK && incomingOK:
			if preferRevisionOrder(incomingOrder, currentOrder, predecessors) {
				merged = append(merged, message)
			}
		case incomingOK:
			currentAt := current.EditTime
			if currentAt.IsZero() {
				currentAt = current.Timestamp
			}
			if incomingOrder.eventAt.After(currentAt) {
				merged = append(merged, message)
			}
		default:
			incomingAt := message.EditTime
			if incomingAt.IsZero() {
				incomingAt = message.Timestamp
			}
			currentAt := current.EditTime
			if currentAt.IsZero() {
				currentAt = current.Timestamp
			}
			if incomingAt.After(currentAt) {
				merged = append(merged, message)
			}
		}
	}
	return merged, nil
}

func ensureSnapshotMergeSource(ctx context.Context, tx *sql.Tx, incomingIdentity string) error {
	incomingIdentity = strings.TrimSpace(incomingIdentity)
	var storedIdentity string
	err := tx.QueryRowContext(ctx, `select value from sync_state where key='source_identity'`).Scan(&storedIdentity)
	if err == nil {
		if incomingIdentity == "" {
			return errors.New("refusing to merge a legacy backup without a Telegram source identity; use --restore")
		}
		if storedIdentity != incomingIdentity {
			return errors.New("refusing to merge a backup from a different Telegram source identity; use --restore")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	empty, err := sourceArchiveEmpty(ctx, tx)
	if err != nil {
		return err
	}
	if !empty {
		if incomingIdentity == "" {
			return errors.New("refusing to merge a legacy backup into an archive with unknown Telegram source identity; use --restore")
		}
		return errors.New("refusing to merge a backup into an archive with unknown Telegram source identity; use --restore")
	}
	return nil
}

func storeSnapshotSourceIdentity(ctx context.Context, tx *sql.Tx, sourceIdentity string, observedAt time.Time) error {
	sourceIdentity = strings.TrimSpace(sourceIdentity)
	if sourceIdentity == "" {
		return nil
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	_, err := tx.ExecContext(ctx, `insert into sync_state(key,value,updated_at) values('source_identity',?,?) on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at`, sourceIdentity, unix(observedAt))
	return err
}

func insertGroups(ctx context.Context, tx *sql.Tx, groups []Group, source string, preserveTombstones bool) error {
	deletedAt, deletionSource, deletionReason := tombstoneUpdate("groups", preserveTombstones)
	query := `insert into groups(jid,name,owner_jid,created_at,deleted_at,deletion_source,deletion_reason) values(?,?,?,?,?,?,?)
		on conflict(jid) do update set name=excluded.name,owner_jid=excluded.owner_jid,created_at=excluded.created_at,deleted_at=` + deletedAt + `,deletion_source=` + deletionSource + `,deletion_reason=` + deletionReason + conflictUpdateWhere("groups", preserveTombstones)
	for _, group := range groups {
		if err := normalizeTombstone(&group.Tombstone, source, "explicit-group-delete"); err != nil {
			return err
		}
		if !group.DeletedAt.IsZero() {
			updated, err := updateExistingTombstone(ctx, tx, "groups", "jid=?", []any{group.JID}, group.Tombstone, preserveTombstones)
			if err != nil {
				return err
			}
			if updated {
				continue
			}
		}
		if _, err := tx.ExecContext(ctx, query, group.JID, group.Name, group.OwnerJID, unix(group.CreatedAt), nullableUnix(group.DeletedAt), nullableString(group.DeletionSource), nullableString(group.DeletionReason)); err != nil {
			return err
		}
	}
	return nil
}

func insertGroupParticipants(ctx context.Context, tx *sql.Tx, participants []GroupParticipant, source string, preserveTombstones bool) error {
	deletedAt, deletionSource, deletionReason := tombstoneUpdate("group_participants", preserveTombstones)
	query := `insert into group_participants(group_jid,user_jid,contact_name,first_name,is_admin,is_active,deleted_at,deletion_source,deletion_reason) values(?,?,?,?,?,?,?,?,?)
		on conflict(group_jid,user_jid) do update set contact_name=excluded.contact_name,first_name=excluded.first_name,is_admin=excluded.is_admin,is_active=excluded.is_active,deleted_at=` + deletedAt + `,deletion_source=` + deletionSource + `,deletion_reason=` + deletionReason + conflictUpdateWhere("group_participants", preserveTombstones)
	for _, participant := range participants {
		if err := normalizeTombstone(&participant.Tombstone, source, "explicit-group-participant-delete"); err != nil {
			return err
		}
		if !participant.DeletedAt.IsZero() {
			updated, err := updateExistingTombstone(ctx, tx, "group_participants", "group_jid=? and user_jid=?", []any{participant.GroupJID, participant.UserJID}, participant.Tombstone, preserveTombstones)
			if err != nil {
				return err
			}
			if updated {
				continue
			}
		}
		if _, err := tx.ExecContext(ctx, query, participant.GroupJID, participant.UserJID, participant.ContactName, participant.FirstName, boolInt(participant.IsAdmin), boolInt(participant.IsActive), nullableUnix(participant.DeletedAt), nullableString(participant.DeletionSource), nullableString(participant.DeletionReason)); err != nil {
			return err
		}
	}
	return nil
}
