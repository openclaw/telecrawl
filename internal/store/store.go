package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ckstore "github.com/openclaw/crawlkit/store"
	_ "modernc.org/sqlite"
)

const schemaVersion = 5

type Store struct {
	db   *sql.DB
	path string
}

type ImportStats struct {
	SourcePath             string    `json:"source_path"`
	SourcePathCanonical    bool      `json:"-"`
	SourceIdentity         string    `json:"-"`
	AdoptSource            bool      `json:"-"`
	DBPath                 string    `json:"db_path"`
	Chats                  int       `json:"chats"`
	Messages               int       `json:"messages"`
	MediaMessages          int       `json:"media_messages"`
	MediaFiles             int       `json:"media_files"`
	MediaBytes             int64     `json:"media_bytes"`
	RemoteMediaCandidates  int       `json:"remote_media_candidates,omitempty"`
	RemoteMediaAttempted   int       `json:"remote_media_attempted,omitempty"`
	RemoteMediaDownloads   int       `json:"remote_media_downloads,omitempty"`
	RemoteMediaMissing     int       `json:"remote_media_missing,omitempty"`
	RemoteMediaUnavailable int       `json:"remote_media_unavailable,omitempty"`
	RemoteMediaTimeouts    int       `json:"remote_media_timeouts,omitempty"`
	RemoteMediaErrors      int       `json:"remote_media_errors,omitempty"`
	StartedAt              time.Time `json:"started_at"`
	FinishedAt             time.Time `json:"finished_at"`
}

type Status struct {
	DBPath         string    `json:"db_path"`
	Chats          int       `json:"chats"`
	UnreadChats    int       `json:"unread_chats"`
	UnreadMessages int       `json:"unread_messages"`
	Messages       int       `json:"messages"`
	MediaMessages  int       `json:"media_messages"`
	Folders        int       `json:"folders"`
	Topics         int       `json:"topics"`
	OldestMessage  time.Time `json:"oldest_message,omitzero"`
	NewestMessage  time.Time `json:"newest_message,omitzero"`
	LastImportAt   time.Time `json:"last_import_at,omitzero"`
	LastSource     string    `json:"last_source,omitempty"`
}

// Tombstone records an explicit source deletion. A missing row in an import is
// never enough to populate these fields.
type Tombstone struct {
	DeletedAt      time.Time `json:"deleted_at,omitzero"`
	DeletionSource string    `json:"deletion_source,omitempty"`
	DeletionReason string    `json:"deletion_reason,omitempty"`
}

type Chat struct {
	Tombstone
	JID           string    `json:"jid"`
	Kind          string    `json:"kind"`
	Name          string    `json:"name,omitempty"`
	Username      string    `json:"username,omitempty"`
	LastMessageAt time.Time `json:"last_message_at,omitzero"`
	UnreadCount   int       `json:"unread_count"`
	MessageCount  int       `json:"message_count"`
	FolderID      string    `json:"folder_id,omitempty"`
	Forum         bool      `json:"forum,omitempty"`
}

type Folder struct {
	Tombstone
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Emoticon  string `json:"emoticon,omitempty"`
	Color     int    `json:"color,omitempty"`
	FlagsJSON string `json:"flags_json,omitempty"`
}

type FolderChat struct {
	Tombstone
	FolderID string `json:"folder_id"`
	ChatJID  string `json:"chat_jid"`
	Position int    `json:"position"`
}

type Topic struct {
	Tombstone
	ChatJID              string    `json:"chat_jid"`
	TopicID              string    `json:"topic_id"`
	Title                string    `json:"title,omitempty"`
	TopMessageID         string    `json:"top_message_id,omitempty"`
	IconColor            int       `json:"icon_color,omitempty"`
	IconEmojiID          string    `json:"icon_emoji_id,omitempty"`
	UnreadCount          int       `json:"unread_count"`
	UnreadMentionsCount  int       `json:"unread_mentions_count"`
	UnreadReactionsCount int       `json:"unread_reactions_count"`
	Pinned               bool      `json:"pinned,omitempty"`
	Closed               bool      `json:"closed,omitempty"`
	Hidden               bool      `json:"hidden,omitempty"`
	LastMessageAt        time.Time `json:"last_message_at,omitzero"`
}

type Contact struct {
	Tombstone
	JID          string    `json:"jid"`
	PeerType     string    `json:"peer_type,omitempty"`
	Phone        string    `json:"phone,omitempty"`
	FullName     string    `json:"full_name,omitempty"`
	FirstName    string    `json:"first_name,omitempty"`
	LastName     string    `json:"last_name,omitempty"`
	BusinessName string    `json:"business_name,omitempty"`
	Username     string    `json:"username,omitempty"`
	LID          string    `json:"lid,omitempty"`
	AboutText    string    `json:"about_text,omitempty"`
	AvatarPath   string    `json:"avatar_path,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitzero"`
}

type Group struct {
	Tombstone
	JID       string    `json:"jid"`
	Name      string    `json:"name,omitempty"`
	OwnerJID  string    `json:"owner_jid,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

type GroupParticipant struct {
	Tombstone
	GroupJID    string `json:"group_jid"`
	UserJID     string `json:"user_jid"`
	ContactName string `json:"contact_name,omitempty"`
	FirstName   string `json:"first_name,omitempty"`
	IsAdmin     bool   `json:"is_admin,omitempty"`
	IsActive    bool   `json:"is_active,omitempty"`
}

type Message struct {
	Tombstone
	EventID       string    `json:"event_id,omitempty"`
	SourcePK      int64     `json:"source_pk"`
	ChatJID       string    `json:"chat_jid"`
	ChatName      string    `json:"chat_name,omitempty"`
	MessageID     string    `json:"message_id"`
	SenderJID     string    `json:"sender_jid,omitempty"`
	SenderName    string    `json:"sender_name,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
	EditTime      time.Time `json:"edit_timestamp,omitzero"`
	FromMe        bool      `json:"from_me"`
	Text          string    `json:"text,omitempty"`
	RawType       int       `json:"raw_type"`
	MessageType   string    `json:"message_type,omitempty"`
	MediaType     string    `json:"media_type,omitempty"`
	MediaTitle    string    `json:"media_title,omitempty"`
	MediaPath     string    `json:"media_path,omitempty"`
	MediaURL      string    `json:"media_url,omitempty"`
	MediaSize     int64     `json:"media_size,omitempty"`
	MetadataType  string    `json:"metadata_type,omitempty"`
	MetadataTitle string    `json:"metadata_title,omitempty"`
	MetadataURL   string    `json:"metadata_url,omitempty"`
	MetadataJSON  string    `json:"metadata_json,omitempty"`
	Starred       bool      `json:"starred,omitempty"`
	TopicID       string    `json:"topic_id,omitempty"`
	ReplyToID     string    `json:"reply_to_message_id,omitempty"`
	ReplyToChat   string    `json:"reply_to_chat_id,omitempty"`
	ThreadID      string    `json:"thread_id,omitempty"`
	ForwardJSON   string    `json:"forward_json,omitempty"`
	ReactionsJSON string    `json:"reactions_json,omitempty"`
	Views         int       `json:"views,omitempty"`
	Forwards      int       `json:"forwards,omitempty"`
	RepliesCount  int       `json:"replies_count,omitempty"`
	Pinned        bool      `json:"pinned,omitempty"`
	Snippet       string    `json:"snippet,omitempty"`
}

type MessageRevision struct {
	EventID            string    `json:"event_id"`
	MessageEventID     string    `json:"message_event_id"`
	EventType          string    `json:"event_type"`
	PayloadJSON        string    `json:"payload_json"`
	EventAt            time.Time `json:"event_at"`
	ObservedAt         time.Time `json:"observed_at"`
	EventSource        string    `json:"event_source,omitempty"`
	Reason             string    `json:"reason"`
	PredecessorEventID string    `json:"predecessor_event_id,omitempty"`
}

type MessageFilter struct {
	Query    string
	ChatJID  string
	Sender   string
	TopicID  string
	Limit    int
	After    *time.Time
	Before   *time.Time
	FromMe   *bool
	HasMedia bool
	Pinned   bool
	Asc      bool
}

const mediaRefBatchSize = 500

type MediaRef struct {
	SourcePK   int64
	MediaType  string
	MediaTitle string
	MediaPath  string
	MediaSize  int64
}

func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("db path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db, path: path}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, indexSQL); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("pragma user_version = %d", schemaVersion)); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path() string { return s.path }

func (s *Store) MergeAll(ctx context.Context, stats ImportStats, contacts []Contact, chats []Chat, folders []Folder, folderChats []FolderChat, topics []Topic, messages []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := ensureMergeSource(ctx, tx, stats, messages); err != nil {
		return err
	}
	if err := writeImport(ctx, tx, stats, contacts, chats, folders, folderChats, topics, messages, false, true, true); err != nil {
		return err
	}
	scope := newTombstoneScope(chats, folders, folderChats, topics, nil, nil, messages)
	if err := propagateTombstones(ctx, tx, scope); err != nil {
		return err
	}
	if err := recordPropagatedMessageDeletions(ctx, tx, stats.FinishedAt, scope); err != nil {
		return err
	}
	if err := pruneDeletedMessageFTS(ctx, tx, scope); err != nil {
		return err
	}
	if err := recomputeChatAggregates(ctx, tx, affectedChatJIDs(chats, topics, messages)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ValidateMergeSource(ctx context.Context, stats ImportStats, messages []Message) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer rollback(tx)
	return ensureMergeSource(ctx, tx, stats, messages)
}

func ensureMergeSource(ctx context.Context, tx *sql.Tx, stats ImportStats, _ []Message) error {
	sourcePath := strings.TrimSpace(stats.SourcePath)
	if !stats.SourcePathCanonical || !filepath.IsAbs(sourcePath) {
		return errors.New("refusing to merge without an absolute source path; use --restore")
	}
	sourceIdentity := strings.TrimSpace(stats.SourceIdentity)
	if sourceIdentity == "" {
		return errors.New("refusing to merge without a source identity; use --restore")
	}
	var storedIdentity string
	err := tx.QueryRowContext(ctx, `select value from sync_state where key='source_identity'`).Scan(&storedIdentity)
	if err == nil {
		if storedIdentity != sourceIdentity {
			return errors.New("refusing to merge a different Telegram source identity; use --restore")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if stats.AdoptSource {
		return nil
	}
	var storedSource string
	sourceErr := tx.QueryRowContext(ctx, `select value from sync_state where key='source_path'`).Scan(&storedSource)
	if sourceErr == nil {
		empty, err := sourceArchiveEmpty(ctx, tx)
		if err != nil {
			return err
		}
		if !empty {
			return errors.New("refusing to merge into legacy archive with unknown source identity; use --adopt-source or --restore")
		}
		return nil
	}
	if !errors.Is(sourceErr, sql.ErrNoRows) {
		return sourceErr
	}
	empty, err := sourceArchiveEmpty(ctx, tx)
	if err != nil {
		return err
	}
	if !empty && !stats.AdoptSource {
		return errors.New("refusing to merge into archive with unknown source; use --adopt-source or --restore")
	}
	return nil
}

func sourceArchiveEmpty(ctx context.Context, tx *sql.Tx) (bool, error) {
	var empty int
	err := tx.QueryRowContext(ctx, `select
		not exists(select 1 from chats) and
		not exists(select 1 from folders) and
		not exists(select 1 from folder_chats) and
		not exists(select 1 from topics) and
		not exists(select 1 from contacts) and
		not exists(select 1 from groups) and
		not exists(select 1 from group_participants) and
		not exists(select 1 from messages)`).Scan(&empty)
	return empty != 0, err
}

func (s *Store) ReplaceAll(ctx context.Context, stats ImportStats, contacts []Contact, chats []Chat, folders []Folder, folderChats []FolderChat, topics []Topic, messages []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, q := range []string{"delete from messages_fts", "delete from message_revisions", "delete from messages", "delete from topics", "delete from folder_chats", "delete from folders", "delete from chats", "delete from contacts", "delete from groups", "delete from group_participants", "delete from sync_state"} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if err := writeImport(ctx, tx, stats, contacts, chats, folders, folderChats, topics, messages, false, true, true); err != nil {
		return err
	}
	if err := propagateTombstones(ctx, tx, nil); err != nil {
		return err
	}
	if err := recordPropagatedMessageDeletions(ctx, tx, stats.FinishedAt, nil); err != nil {
		return err
	}
	if err := rebuildMessageFTS(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func writeImport(ctx context.Context, tx *sql.Tx, stats ImportStats, contacts []Contact, chats []Chat, folders []Folder, folderChats []FolderChat, topics []Topic, messages []Message, preserveTombstones, writeState, recordRevisions bool) error {
	messages = append([]Message(nil), messages...)
	if err := normalizeImportedMessageEventIDs(ctx, tx, messages); err != nil {
		return err
	}
	source := firstNonEmptyString(stats.SourceIdentity, stats.SourcePath)
	if err := insertContacts(ctx, tx, contacts, source, preserveTombstones); err != nil {
		return err
	}
	for _, c := range chats {
		if err := normalizeTombstone(&c.Tombstone, source, "explicit-chat-delete"); err != nil {
			return err
		}
		if !c.DeletedAt.IsZero() {
			updated, err := updateExistingTombstone(ctx, tx, "chats", "id=?", []any{parseInt64(c.JID)}, c.Tombstone, preserveTombstones)
			if err != nil {
				return err
			}
			if updated {
				continue
			}
		}
		deletedAt, deletionSource, deletionReason := tombstoneUpdate("chats", preserveTombstones)
		query := `insert into chats(id,kind,name,username,last_message_at,unread_count,message_count,folder_id,forum,deleted_at,deletion_source,deletion_reason) values(?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do update set kind=excluded.kind, name=excluded.name, username=excluded.username, last_message_at=excluded.last_message_at, unread_count=excluded.unread_count, message_count=excluded.message_count, folder_id=excluded.folder_id, forum=excluded.forum, deleted_at=` + deletedAt + `, deletion_source=` + deletionSource + `, deletion_reason=` + deletionReason + conflictUpdateWhere("chats", preserveTombstones)
		if _, err := tx.ExecContext(ctx, query,
			parseInt64(c.JID), c.Kind, c.Name, c.Username, unix(c.LastMessageAt), c.UnreadCount, c.MessageCount, c.FolderID, boolInt(c.Forum), nullableUnix(c.DeletedAt), nullableString(c.DeletionSource), nullableString(c.DeletionReason)); err != nil {
			return err
		}
	}
	for _, f := range folders {
		if err := normalizeTombstone(&f.Tombstone, source, "explicit-folder-delete"); err != nil {
			return err
		}
		if !f.DeletedAt.IsZero() {
			updated, err := updateExistingTombstone(ctx, tx, "folders", "id=?", []any{f.ID}, f.Tombstone, preserveTombstones)
			if err != nil {
				return err
			}
			if updated {
				continue
			}
		}
		deletedAt, deletionSource, deletionReason := tombstoneUpdate("folders", preserveTombstones)
		query := `insert into folders(id,title,emoticon,color,flags_json,deleted_at,deletion_source,deletion_reason) values(?,?,?,?,?,?,?,?) on conflict(id) do update set title=excluded.title, emoticon=excluded.emoticon, color=excluded.color, flags_json=excluded.flags_json, deleted_at=` + deletedAt + `, deletion_source=` + deletionSource + `, deletion_reason=` + deletionReason + conflictUpdateWhere("folders", preserveTombstones)
		if _, err := tx.ExecContext(ctx, query,
			f.ID, f.Title, f.Emoticon, f.Color, f.FlagsJSON, nullableUnix(f.DeletedAt), nullableString(f.DeletionSource), nullableString(f.DeletionReason)); err != nil {
			return err
		}
	}
	for _, fc := range folderChats {
		if err := normalizeTombstone(&fc.Tombstone, source, "explicit-folder-membership-delete"); err != nil {
			return err
		}
		if !fc.DeletedAt.IsZero() {
			updated, err := updateExistingTombstone(ctx, tx, "folder_chats", "folder_id=? and chat_jid=?", []any{fc.FolderID, fc.ChatJID}, fc.Tombstone, preserveTombstones)
			if err != nil {
				return err
			}
			if updated {
				continue
			}
		}
		deletedAt, deletionSource, deletionReason := tombstoneUpdate("folder_chats", preserveTombstones)
		query := `insert into folder_chats(folder_id,chat_jid,position,deleted_at,deletion_source,deletion_reason) values(?,?,?,?,?,?) on conflict(folder_id,chat_jid) do update set position=excluded.position, deleted_at=` + deletedAt + `, deletion_source=` + deletionSource + `, deletion_reason=` + deletionReason + conflictUpdateWhere("folder_chats", preserveTombstones)
		if _, err := tx.ExecContext(ctx, query,
			fc.FolderID, fc.ChatJID, fc.Position, nullableUnix(fc.DeletedAt), nullableString(fc.DeletionSource), nullableString(fc.DeletionReason)); err != nil {
			return err
		}
	}
	for _, t := range topics {
		if err := normalizeTombstone(&t.Tombstone, source, "explicit-topic-delete"); err != nil {
			return err
		}
		if !t.DeletedAt.IsZero() {
			updated, err := updateExistingTombstone(ctx, tx, "topics", "chat_jid=? and topic_id=?", []any{t.ChatJID, t.TopicID}, t.Tombstone, preserveTombstones)
			if err != nil {
				return err
			}
			if updated {
				continue
			}
		}
		deletedAt, deletionSource, deletionReason := tombstoneUpdate("topics", preserveTombstones)
		query := `insert into topics(chat_jid,topic_id,title,top_message_id,icon_color,icon_emoji_id,unread_count,unread_mentions_count,unread_reactions_count,pinned,closed,hidden,last_message_at,deleted_at,deletion_source,deletion_reason) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(chat_jid,topic_id) do update set title=excluded.title, top_message_id=excluded.top_message_id, icon_color=excluded.icon_color, icon_emoji_id=excluded.icon_emoji_id, unread_count=excluded.unread_count, unread_mentions_count=excluded.unread_mentions_count, unread_reactions_count=excluded.unread_reactions_count, pinned=excluded.pinned, closed=excluded.closed, hidden=excluded.hidden, last_message_at=excluded.last_message_at, deleted_at=` + deletedAt + `, deletion_source=` + deletionSource + `, deletion_reason=` + deletionReason + conflictUpdateWhere("topics", preserveTombstones)
		if _, err := tx.ExecContext(ctx, query,
			t.ChatJID, t.TopicID, t.Title, t.TopMessageID, t.IconColor, t.IconEmojiID, t.UnreadCount, t.UnreadMentionsCount, t.UnreadReactionsCount, boolInt(t.Pinned), boolInt(t.Closed), boolInt(t.Hidden), unix(t.LastMessageAt), nullableUnix(t.DeletedAt), nullableString(t.DeletionSource), nullableString(t.DeletionReason)); err != nil {
			return err
		}
	}
	// Snapshot merge filters message conflicts through the causal revision graph
	// before this point, so selected message rows are authoritative here too.
	if err := insertMessages(ctx, tx, messages, stats.FinishedAt, source, false, recordRevisions); err != nil {
		return err
	}
	if !writeState {
		return nil
	}
	now := stats.FinishedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	for key, value := range map[string]string{"last_import_at": now.Format(time.RFC3339Nano), "source_path": stats.SourcePath} {
		if _, err := tx.ExecContext(ctx, `insert into sync_state(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, key, value, unix(now)); err != nil {
			return err
		}
	}
	if stats.SourcePathCanonical {
		if strings.TrimSpace(stats.SourceIdentity) == "" {
			return errors.New("canonical source identity is required")
		}
		if _, err := tx.ExecContext(ctx, `insert into sync_state(key,value,updated_at) values('source_path_canonical','1',?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, unix(now)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into sync_state(key,value,updated_at) values('source_identity',?,?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, stats.SourceIdentity, unix(now)); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `delete from sync_state where key in ('source_path_canonical','source_identity')`); err != nil {
			return err
		}
	}
	return nil
}

func insertContacts(ctx context.Context, tx *sql.Tx, contacts []Contact, source string, preserveTombstones bool) error {
	for _, c := range contacts {
		if strings.TrimSpace(c.JID) == "" {
			continue
		}
		if err := normalizeTombstone(&c.Tombstone, source, "explicit-contact-delete"); err != nil {
			return err
		}
		if !c.DeletedAt.IsZero() {
			updated, err := updateExistingTombstone(ctx, tx, "contacts", "jid=?", []any{c.JID}, c.Tombstone, preserveTombstones)
			if err != nil {
				return err
			}
			if updated {
				continue
			}
		}
		deletedAt, deletionSource, deletionReason := tombstoneUpdate("contacts", preserveTombstones)
		query := `insert into contacts(jid,peer_type,phone,full_name,first_name,last_name,business_name,username,lid,about_text,avatar_path,updated_at,deleted_at,deletion_source,deletion_reason) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(jid) do update set peer_type=excluded.peer_type, phone=excluded.phone, full_name=excluded.full_name, first_name=excluded.first_name, last_name=excluded.last_name, business_name=excluded.business_name, username=excluded.username, lid=excluded.lid, about_text=excluded.about_text, avatar_path=excluded.avatar_path, updated_at=excluded.updated_at, deleted_at=` + deletedAt + `, deletion_source=` + deletionSource + `, deletion_reason=` + deletionReason + conflictUpdateWhere("contacts", preserveTombstones)
		if _, err := tx.ExecContext(ctx, query,
			c.JID, c.PeerType, c.Phone, c.FullName, c.FirstName, c.LastName, c.BusinessName, c.Username, c.LID, c.AboutText, c.AvatarPath, unix(c.UpdatedAt), nullableUnix(c.DeletedAt), nullableString(c.DeletionSource), nullableString(c.DeletionReason)); err != nil {
			return err
		}
	}
	return nil
}

func insertMessages(ctx context.Context, tx *sql.Tx, messages []Message, observedAt time.Time, source string, preserveTombstones, recordRevisions bool) error {
	for _, m := range messages {
		if err := normalizeMessage(&m, source); err != nil {
			return err
		}
		current, found, err := storedMessage(ctx, tx, m.EventID)
		if err != nil {
			return err
		}
		if found {
			if !m.DeletedAt.IsZero() {
				if !preserveTombstones || current.DeletedAt.IsZero() {
					current.Tombstone = m.Tombstone
				}
				m = current
			} else if preserveTombstones && !current.DeletedAt.IsZero() {
				m = current
			} else {
				m = mergeStoredMedia(current, m)
			}
		}
		if recordRevisions {
			revision, err := pendingMessageRevision(ctx, tx, m, observedAt, source)
			if err != nil {
				return err
			}
			if revision != nil {
				if err := insertMessageRevision(ctx, tx, *revision); err != nil {
					return err
				}
			}
		}
		deletedAt, deletionSource, deletionReason := tombstoneUpdate("messages", preserveTombstones)
		query := `insert into messages(event_id,source_pk,chat_jid,chat_name,msg_id,sender_jid,sender_name,ts,from_me,text,raw_type,message_type,media_type,media_title,media_path,media_url,media_size,metadata_type,metadata_title,metadata_url,metadata_json,starred,topic_id,reply_to_msg_id,reply_to_chat_jid,thread_id,edit_ts,forward_json,reactions_json,views,forwards,replies_count,pinned,deleted_at,deletion_source,deletion_reason) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(event_id) do update set source_pk=excluded.source_pk, chat_jid=excluded.chat_jid, chat_name=excluded.chat_name, msg_id=excluded.msg_id, sender_jid=excluded.sender_jid, sender_name=excluded.sender_name, ts=excluded.ts, from_me=excluded.from_me, text=excluded.text, raw_type=excluded.raw_type, message_type=excluded.message_type, media_type=case when excluded.media_type='' then messages.media_type else excluded.media_type end, media_title=case when excluded.media_title='' then messages.media_title else excluded.media_title end, media_path=case when excluded.media_path='' then messages.media_path else excluded.media_path end, media_url=case when excluded.media_url='' then messages.media_url else excluded.media_url end, media_size=case when excluded.media_path='' then messages.media_size else excluded.media_size end, metadata_type=excluded.metadata_type, metadata_title=excluded.metadata_title, metadata_url=excluded.metadata_url, metadata_json=excluded.metadata_json, starred=excluded.starred, topic_id=excluded.topic_id, reply_to_msg_id=excluded.reply_to_msg_id, reply_to_chat_jid=excluded.reply_to_chat_jid, thread_id=excluded.thread_id, edit_ts=excluded.edit_ts, forward_json=excluded.forward_json, reactions_json=excluded.reactions_json, views=excluded.views, forwards=excluded.forwards, replies_count=excluded.replies_count, pinned=excluded.pinned, deleted_at=` + deletedAt + `, deletion_source=` + deletionSource + `, deletion_reason=` + deletionReason + conflictUpdateWhere("messages", preserveTombstones)
		if _, err := tx.ExecContext(ctx, query,
			m.EventID, m.SourcePK, m.ChatJID, m.ChatName, m.MessageID, m.SenderJID, m.SenderName, unix(m.Timestamp), boolInt(m.FromMe), m.Text, m.RawType, m.MessageType, m.MediaType, m.MediaTitle, m.MediaPath, m.MediaURL, m.MediaSize, m.MetadataType, m.MetadataTitle, m.MetadataURL, m.MetadataJSON, boolInt(m.Starred), m.TopicID, m.ReplyToID, m.ReplyToChat, m.ThreadID, unix(m.EditTime), m.ForwardJSON, m.ReactionsJSON, m.Views, m.Forwards, m.RepliesCount, boolInt(m.Pinned), nullableUnix(m.DeletedAt), nullableString(m.DeletionSource), nullableString(m.DeletionReason)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `delete from messages_fts where rowid=(select rowid from messages where event_id=?)`, m.EventID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into messages_fts(rowid,text,chat,sender,media) select rowid,trim(coalesce(text,'')||' '||coalesce(media_title,'')||' '||coalesce(metadata_title,'')||' '||coalesce(metadata_url,'')),coalesce(chat_name,''),coalesce(sender_name,''),coalesce(media_type,'') from messages where event_id=? and deleted_at is null`, m.EventID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	out := Status{DBPath: s.path}
	for _, c := range []struct {
		dst *int
		q   string
	}{
		{&out.Chats, "select count(*) from chats where deleted_at is null"},
		{&out.UnreadChats, "select count(*) from chats where deleted_at is null and unread_count > 0"},
		{&out.UnreadMessages, "select coalesce(sum(unread_count), 0) from chats where deleted_at is null"},
		{&out.Messages, "select count(*) from messages where deleted_at is null"},
		{&out.MediaMessages, "select count(*) from messages where deleted_at is null and media_type <> ''"},
		{&out.Folders, "select count(*) from folders where deleted_at is null"},
		{&out.Topics, "select count(*) from topics where deleted_at is null"},
	} {
		if err := s.db.QueryRowContext(ctx, c.q).Scan(c.dst); err != nil {
			return out, err
		}
	}
	var oldest, newest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `select min(ts), max(ts) from messages where deleted_at is null`).Scan(&oldest, &newest); err != nil {
		return out, err
	}
	if oldest.Valid {
		out.OldestMessage = fromUnix(oldest.Int64)
	}
	if newest.Valid {
		out.NewestMessage = fromUnix(newest.Int64)
	}
	var lastImport string
	_ = s.db.QueryRowContext(ctx, `select value from sync_state where key='last_import_at'`).Scan(&lastImport)
	if t, err := time.Parse(time.RFC3339Nano, lastImport); err == nil {
		out.LastImportAt = t
	}
	_ = s.db.QueryRowContext(ctx, `select value from sync_state where key='source_path'`).Scan(&out.LastSource)
	return out, nil
}

func (s *Store) ListChats(ctx context.Context, limit int, unread bool) ([]Chat, error) {
	if limit <= 0 {
		limit = 50
	}
	where := "where deleted_at is null"
	if unread {
		where += " and unread_count > 0"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`select cast(id as text),kind,coalesce(name,''),coalesce(username,''),coalesce(last_message_at,0),unread_count,message_count,coalesce(folder_id,''),forum from chats %s order by last_message_at desc limit ?`, where), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Chat
	for rows.Next() {
		var c Chat
		var ts int64
		var forum int
		if err := rows.Scan(&c.JID, &c.Kind, &c.Name, &c.Username, &ts, &c.UnreadCount, &c.MessageCount, &c.FolderID, &forum); err != nil {
			return nil, err
		}
		c.LastMessageAt = fromUnix(ts)
		c.Forum = forum != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ListFolders(ctx context.Context) ([]Folder, error) {
	rows, err := s.db.QueryContext(ctx, `select id,coalesce(title,''),coalesce(emoticon,''),color,coalesce(flags_json,'') from folders where deleted_at is null order by cast(id as integer), title`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Title, &f.Emoticon, &f.Color, &f.FlagsJSON); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) ChatsInFolder(ctx context.Context, folderID string, limit int) ([]Chat, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `select cast(c.id as text),c.kind,coalesce(c.name,''),coalesce(c.username,''),coalesce(c.last_message_at,0),c.unread_count,c.message_count,coalesce(c.folder_id,''),c.forum
from folder_chats fc join chats c on cast(c.id as text)=fc.chat_jid
where fc.folder_id=? and fc.deleted_at is null and c.deleted_at is null
order by fc.position asc, c.last_message_at desc
limit ?`, folderID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Chat
	for rows.Next() {
		var c Chat
		var ts int64
		var forum int
		if err := rows.Scan(&c.JID, &c.Kind, &c.Name, &c.Username, &ts, &c.UnreadCount, &c.MessageCount, &c.FolderID, &forum); err != nil {
			return nil, err
		}
		c.LastMessageAt = fromUnix(ts)
		c.Forum = forum != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ListTopics(ctx context.Context, chatJID string, limit int) ([]Topic, error) {
	if strings.TrimSpace(chatJID) == "" {
		return nil, errors.New("chat id required")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `select chat_jid,topic_id,coalesce(title,''),coalesce(top_message_id,''),icon_color,coalesce(icon_emoji_id,''),unread_count,unread_mentions_count,unread_reactions_count,pinned,closed,hidden,coalesce(last_message_at,0)
from topics where chat_jid=? and deleted_at is null
order by pinned desc, last_message_at desc, cast(topic_id as integer) desc
limit ?`, chatJID, limit)
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

func (s *Store) Messages(ctx context.Context, filter MessageFilter) ([]Message, error) {
	return s.messages(ctx, filter, false)
}

func (s *Store) MediaRefs(ctx context.Context) ([]MediaRef, error) {
	return s.mediaRefs(ctx, mediaRefBatchSize)
}

func (s *Store) mediaRefs(ctx context.Context, batchSize int) ([]MediaRef, error) {
	if batchSize <= 0 {
		batchSize = mediaRefBatchSize
	}
	var out []MediaRef
	var lastRowID int64
	for {
		rows, err := s.db.QueryContext(ctx, `select rowid,source_pk,coalesce(media_type,''),coalesce(media_title,''),coalesce(media_path,''),coalesce(media_size,0)
			from messages
			where deleted_at is null and media_type <> '' and rowid > ?
			order by rowid limit ?`, lastRowID, batchSize)
		if err != nil {
			return nil, err
		}
		batch := make([]MediaRef, 0, batchSize)
		var rowID int64
		for rows.Next() {
			var ref MediaRef
			if err := rows.Scan(&rowID, &ref.SourcePK, &ref.MediaType, &ref.MediaTitle, &ref.MediaPath, &ref.MediaSize); err != nil {
				_ = rows.Close()
				return nil, err
			}
			batch = append(batch, ref)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return out, nil
		}
		out = append(out, batch...)
		lastRowID = rowID
		if len(batch) < batchSize {
			return out, nil
		}
	}
}

func (s *Store) Search(ctx context.Context, filter MessageFilter) ([]Message, error) {
	if strings.TrimSpace(filter.Query) == "" {
		return nil, errors.New("search query required")
	}
	return s.messages(ctx, filter, true)
}

func (s *Store) messages(ctx context.Context, filter MessageFilter, search bool) ([]Message, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	query := `select event_id,source_pk,chat_jid,coalesce(chat_name,''),msg_id,coalesce(sender_jid,''),coalesce(sender_name,''),ts,coalesce(edit_ts,0),from_me,coalesce(text,''),raw_type,coalesce(message_type,''),coalesce(media_type,''),coalesce(media_title,''),coalesce(media_path,''),coalesce(media_url,''),coalesce(media_size,0),coalesce(metadata_type,''),coalesce(metadata_title,''),coalesce(metadata_url,''),coalesce(metadata_json,''),starred,coalesce(topic_id,''),coalesce(reply_to_msg_id,''),coalesce(reply_to_chat_jid,''),coalesce(thread_id,''),coalesce(forward_json,''),coalesce(reactions_json,''),coalesce(views,0),coalesce(forwards,0),coalesce(replies_count,0),coalesce(pinned,0),'' from messages where deleted_at is null`
	args := []any{}
	prefix := ""
	if search {
		ftsQuery, err := ckstore.FTS5Terms(filter.Query, "")
		if err != nil {
			return nil, err
		}
		query = `select m.event_id,m.source_pk,m.chat_jid,coalesce(m.chat_name,''),m.msg_id,coalesce(m.sender_jid,''),coalesce(m.sender_name,''),m.ts,coalesce(m.edit_ts,0),m.from_me,coalesce(m.text,''),m.raw_type,coalesce(m.message_type,''),coalesce(m.media_type,''),coalesce(m.media_title,''),coalesce(m.media_path,''),coalesce(m.media_url,''),coalesce(m.media_size,0),coalesce(m.metadata_type,''),coalesce(m.metadata_title,''),coalesce(m.metadata_url,''),coalesce(m.metadata_json,''),m.starred,coalesce(m.topic_id,''),coalesce(m.reply_to_msg_id,''),coalesce(m.reply_to_chat_jid,''),coalesce(m.thread_id,''),coalesce(m.forward_json,''),coalesce(m.reactions_json,''),coalesce(m.views,0),coalesce(m.forwards,0),coalesce(m.replies_count,0),coalesce(m.pinned,0),snippet(messages_fts,0,'[',']','...',12) from messages_fts f join messages m on m.rowid=f.rowid where messages_fts match ? and m.deleted_at is null`
		args = append(args, ftsQuery)
		prefix = "m."
	}
	if filter.ChatJID != "" {
		query += " and " + prefix + "chat_jid = ?"
		args = append(args, filter.ChatJID)
	}
	if filter.Sender != "" {
		query += " and " + prefix + "sender_jid = ?"
		args = append(args, filter.Sender)
	}
	if filter.TopicID != "" {
		query += " and " + prefix + "topic_id = ?"
		args = append(args, filter.TopicID)
	}
	if filter.After != nil {
		query += " and " + prefix + "ts >= ?"
		args = append(args, unix(*filter.After))
	}
	if filter.Before != nil {
		query += " and " + prefix + "ts <= ?"
		args = append(args, unix(*filter.Before))
	}
	if filter.FromMe != nil {
		query += " and " + prefix + "from_me = ?"
		args = append(args, boolInt(*filter.FromMe))
	}
	if filter.HasMedia {
		query += " and " + prefix + "media_type <> ''"
	}
	if filter.Pinned {
		query += " and " + prefix + "pinned <> 0"
	}
	if search {
		query += " order by bm25(messages_fts) limit ?"
	} else if filter.Asc {
		query += " order by ts asc, source_pk asc limit ?"
	} else {
		query += " order by ts desc, source_pk desc limit ?"
	}
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Message
	for rows.Next() {
		var m Message
		var ts, editTS int64
		var fromMe, starred, pinned int
		if err := rows.Scan(&m.EventID, &m.SourcePK, &m.ChatJID, &m.ChatName, &m.MessageID, &m.SenderJID, &m.SenderName, &ts, &editTS, &fromMe, &m.Text, &m.RawType, &m.MessageType, &m.MediaType, &m.MediaTitle, &m.MediaPath, &m.MediaURL, &m.MediaSize, &m.MetadataType, &m.MetadataTitle, &m.MetadataURL, &m.MetadataJSON, &starred, &m.TopicID, &m.ReplyToID, &m.ReplyToChat, &m.ThreadID, &m.ForwardJSON, &m.ReactionsJSON, &m.Views, &m.Forwards, &m.RepliesCount, &pinned, &m.Snippet); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(ts)
		m.EditTime = fromUnix(editTS)
		m.FromMe = fromMe != 0
		m.Starred = starred != 0
		m.Pinned = pinned != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func migrate(ctx context.Context, db *sql.DB) error {
	var currentVersion int
	if err := db.QueryRowContext(ctx, `pragma user_version`).Scan(&currentVersion); err != nil {
		return err
	}
	if currentVersion > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than this telecrawl build supports (%d)", currentVersion, schemaVersion)
	}
	if currentVersion == schemaVersion {
		return nil
	}
	adds := map[string]map[string]string{
		"chats": {
			"folder_id":       "text",
			"forum":           "integer not null default 0",
			"deleted_at":      "integer",
			"deletion_source": "text",
			"deletion_reason": "text",
		},
		"folders": {
			"deleted_at":      "integer",
			"deletion_source": "text",
			"deletion_reason": "text",
		},
		"folder_chats": {
			"deleted_at":      "integer",
			"deletion_source": "text",
			"deletion_reason": "text",
		},
		"topics": {
			"deleted_at":      "integer",
			"deletion_source": "text",
			"deletion_reason": "text",
		},
		"messages": {
			"event_id":          "text",
			"topic_id":          "text",
			"reply_to_msg_id":   "text",
			"reply_to_chat_jid": "text",
			"thread_id":         "text",
			"edit_ts":           "integer",
			"forward_json":      "text",
			"reactions_json":    "text",
			"views":             "integer not null default 0",
			"forwards":          "integer not null default 0",
			"replies_count":     "integer not null default 0",
			"pinned":            "integer not null default 0",
			"metadata_type":     "text",
			"metadata_title":    "text",
			"metadata_url":      "text",
			"metadata_json":     "text",
			"deleted_at":        "integer",
			"deletion_source":   "text",
			"deletion_reason":   "text",
		},
		"message_revisions": {
			"predecessor_event_id": "text",
		},
		"contacts": {
			"peer_type":       "text",
			"avatar_path":     "text",
			"deleted_at":      "integer",
			"deletion_source": "text",
			"deletion_reason": "text",
		},
		"groups": {
			"deleted_at":      "integer",
			"deletion_source": "text",
			"deletion_reason": "text",
		},
		"group_participants": {
			"deleted_at":      "integer",
			"deletion_source": "text",
			"deletion_reason": "text",
		},
	}
	for table, defs := range adds {
		existing, err := columns(ctx, db, table)
		if err != nil {
			return err
		}
		for name, def := range defs {
			if existing[name] {
				continue
			}
			if _, err := db.ExecContext(ctx, fmt.Sprintf("alter table %s add column %s %s", table, name, def)); err != nil {
				return err
			}
		}
	}
	if _, err := db.ExecContext(ctx, `create index if not exists idx_messages_chat_msg on messages(chat_jid,msg_id)`); err != nil {
		return err
	}
	if err := backfillMessageEventIDs(ctx, db); err != nil {
		return err
	}
	if err := removeMessageSourcePKUniqueness(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `create unique index if not exists idx_messages_event_id on messages(event_id)`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `create index if not exists idx_message_revisions_message on message_revisions(message_event_id,event_at,event_id)`); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := seedMissingMessageBaselines(ctx, tx, time.Now().UTC(), "schema-v5-migration"); err != nil {
		return err
	}
	if err := propagateTombstones(ctx, tx, nil); err != nil {
		return err
	}
	if err := recordPropagatedMessageDeletions(ctx, tx, time.Now().UTC(), nil); err != nil {
		return err
	}
	if err := rebuildMessageFTS(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func removeMessageSourcePKUniqueness(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `pragma index_list(messages)`)
	if err != nil {
		return err
	}
	type indexInfo struct {
		name   string
		unique bool
	}
	var indexes []indexInfo
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return err
		}
		indexes = append(indexes, indexInfo{name: name, unique: unique != 0})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	uniqueSourcePK := false
	for _, index := range indexes {
		if !index.unique {
			continue
		}
		columns, err := db.QueryContext(ctx, `pragma index_info(`+strconv.Quote(index.name)+`)`)
		if err != nil {
			return err
		}
		var names []string
		for columns.Next() {
			var seq, cid int
			var name string
			if err := columns.Scan(&seq, &cid, &name); err != nil {
				_ = columns.Close()
				return err
			}
			names = append(names, name)
		}
		if err := columns.Close(); err != nil {
			return err
		}
		if err := columns.Err(); err != nil {
			return err
		}
		if len(names) == 1 && names[0] == "source_pk" {
			uniqueSourcePK = true
			break
		}
	}
	if !uniqueSourcePK {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.ExecContext(ctx, `create table messages_v5 (
		rowid integer primary key autoincrement,
		event_id text not null unique,
		source_pk integer not null,
		chat_jid text not null,
		chat_name text,
		msg_id text not null,
		sender_jid text,
		sender_name text,
		ts integer not null,
		from_me integer not null,
		text text,
		raw_type integer not null default 0,
		message_type text,
		media_type text,
		media_title text,
		media_path text,
		media_url text,
		media_size integer,
		metadata_type text,
		metadata_title text,
		metadata_url text,
		metadata_json text,
		starred integer not null default 0,
		topic_id text,
		reply_to_msg_id text,
		reply_to_chat_jid text,
		thread_id text,
		edit_ts integer,
		forward_json text,
		reactions_json text,
		views integer not null default 0,
		forwards integer not null default 0,
		replies_count integer not null default 0,
		pinned integer not null default 0,
		deleted_at integer,
		deletion_source text,
		deletion_reason text
	)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into messages_v5(rowid,event_id,source_pk,chat_jid,chat_name,msg_id,sender_jid,sender_name,ts,from_me,text,raw_type,message_type,media_type,media_title,media_path,media_url,media_size,metadata_type,metadata_title,metadata_url,metadata_json,starred,topic_id,reply_to_msg_id,reply_to_chat_jid,thread_id,edit_ts,forward_json,reactions_json,views,forwards,replies_count,pinned,deleted_at,deletion_source,deletion_reason)
		select rowid,event_id,source_pk,chat_jid,chat_name,msg_id,sender_jid,sender_name,ts,from_me,text,raw_type,message_type,media_type,media_title,media_path,media_url,media_size,metadata_type,metadata_title,metadata_url,metadata_json,starred,topic_id,reply_to_msg_id,reply_to_chat_jid,thread_id,edit_ts,forward_json,reactions_json,views,forwards,replies_count,pinned,deleted_at,deletion_source,deletion_reason from messages`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `drop table messages`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `alter table messages_v5 rename to messages`); err != nil {
		return err
	}
	return tx.Commit()
}

func backfillMessageEventIDs(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := backfillMessageEventIDsTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func backfillMessageEventIDsTx(ctx context.Context, tx *sql.Tx) error {
	const batchSize = 1000
	lastSourcePK := int64(-1 << 63)
	lastRowID := int64(0)
	for {
		rows, err := tx.QueryContext(ctx, `select rowid,source_pk,chat_jid,msg_id from messages
			where source_pk>? or (source_pk=? and rowid>?)
			order by source_pk,rowid limit ?`, lastSourcePK, lastSourcePK, lastRowID, batchSize)
		if err != nil {
			return err
		}
		type legacyMessage struct {
			rowID, sourcePK int64
			chatJID, msgID  string
		}
		batch := make([]legacyMessage, 0, batchSize)
		for rows.Next() {
			var message legacyMessage
			if err := rows.Scan(&message.rowID, &message.sourcePK, &message.chatJID, &message.msgID); err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, message)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, message := range batch {
			var familySize int
			if err := tx.QueryRowContext(ctx, `select count(*) from messages where chat_jid=? and msg_id=?`, message.chatJID, message.msgID).Scan(&familySize); err != nil {
				return err
			}
			eventID := stableMessageEventID(message.chatJID, message.msgID)
			if familySize > 1 {
				eventID = stableLegacyMessageEventID(message.chatJID, message.msgID, message.sourcePK, 0)
			}
			if _, err := tx.ExecContext(ctx, `update messages set event_id=? where rowid=? and (event_id is null or trim(event_id)='')`, eventID, message.rowID); err != nil {
				return err
			}
			lastSourcePK = message.sourcePK
			lastRowID = message.rowID
		}
	}
}

func columns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "pragma table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}

func fromUnix(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

func fromNullUnix(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return fromUnix(v.Int64)
}

func rollback(tx *sql.Tx) { _ = tx.Rollback() }

func parseInt64(s string) int64 {
	var out int64
	_, _ = fmt.Sscan(s, &out)
	return out
}
