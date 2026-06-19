package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	ckstore "github.com/openclaw/crawlkit/store"
	_ "modernc.org/sqlite"
)

const schemaVersion = 4

type Store struct {
	db   *sql.DB
	path string
}

type ImportStats struct {
	SourcePath             string    `json:"source_path"`
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

type Chat struct {
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
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Emoticon  string `json:"emoticon,omitempty"`
	Color     int    `json:"color,omitempty"`
	FlagsJSON string `json:"flags_json,omitempty"`
}

type FolderChat struct {
	FolderID string `json:"folder_id"`
	ChatJID  string `json:"chat_jid"`
	Position int    `json:"position"`
}

type Topic struct {
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
	JID       string    `json:"jid"`
	Name      string    `json:"name,omitempty"`
	OwnerJID  string    `json:"owner_jid,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

type GroupParticipant struct {
	GroupJID    string `json:"group_jid"`
	UserJID     string `json:"user_jid"`
	ContactName string `json:"contact_name,omitempty"`
	FirstName   string `json:"first_name,omitempty"`
	IsAdmin     bool   `json:"is_admin,omitempty"`
	IsActive    bool   `json:"is_active,omitempty"`
}

type Message struct {
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

func (s *Store) UpsertChat(ctx context.Context, stats ImportStats, chatJID string, contacts []Contact, chats []Chat, folders []Folder, folderChats []FolderChat, topics []Topic, messages []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	chatID := parseInt64(chatJID)
	preserveFolderState := len(folders) == 0 && len(folderChats) == 0
	var existingFolderID string
	if preserveFolderState {
		_ = tx.QueryRowContext(ctx, `select coalesce(folder_id,'') from chats where id = ?`, chatID).Scan(&existingFolderID)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"delete from messages_fts where rowid in (select rowid from messages where chat_jid = ?)", []any{chatJID}},
		{"delete from messages where chat_jid = ?", []any{chatJID}},
		{"delete from topics where chat_jid = ?", []any{chatJID}},
		{"delete from chats where id = ?", []any{chatID}},
	} {
		if _, err := tx.ExecContext(ctx, q.sql, q.args...); err != nil {
			return err
		}
	}
	if !preserveFolderState {
		if _, err := tx.ExecContext(ctx, `delete from folder_chats where chat_jid = ?`, chatJID); err != nil {
			return err
		}
	}
	if err := insertContacts(ctx, tx, contacts); err != nil {
		return err
	}
	for _, c := range chats {
		if preserveFolderState && c.FolderID == "" {
			c.FolderID = existingFolderID
		}
		if _, err := tx.ExecContext(ctx, `insert into chats(id,kind,name,username,last_message_at,unread_count,message_count,folder_id,forum) values(?,?,?,?,?,?,?,?,?)`,
			parseInt64(c.JID), c.Kind, c.Name, c.Username, unix(c.LastMessageAt), c.UnreadCount, c.MessageCount, c.FolderID, boolInt(c.Forum)); err != nil {
			return err
		}
	}
	for _, f := range folders {
		if _, err := tx.ExecContext(ctx, `insert into folders(id,title,emoticon,color,flags_json) values(?,?,?,?,?) on conflict(id) do update set title=excluded.title, emoticon=excluded.emoticon, color=excluded.color, flags_json=excluded.flags_json`,
			f.ID, f.Title, f.Emoticon, f.Color, f.FlagsJSON); err != nil {
			return err
		}
	}
	for _, fc := range folderChats {
		if _, err := tx.ExecContext(ctx, `insert into folder_chats(folder_id,chat_jid,position) values(?,?,?)`,
			fc.FolderID, fc.ChatJID, fc.Position); err != nil {
			return err
		}
	}
	for _, t := range topics {
		if _, err := tx.ExecContext(ctx, `insert into topics(chat_jid,topic_id,title,top_message_id,icon_color,icon_emoji_id,unread_count,unread_mentions_count,unread_reactions_count,pinned,closed,hidden,last_message_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			t.ChatJID, t.TopicID, t.Title, t.TopMessageID, t.IconColor, t.IconEmojiID, t.UnreadCount, t.UnreadMentionsCount, t.UnreadReactionsCount, boolInt(t.Pinned), boolInt(t.Closed), boolInt(t.Hidden), unix(t.LastMessageAt)); err != nil {
			return err
		}
	}
	if err := insertMessages(ctx, tx, messages); err != nil {
		return err
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
	return tx.Commit()
}

func (s *Store) ReplaceAll(ctx context.Context, stats ImportStats, contacts []Contact, chats []Chat, folders []Folder, folderChats []FolderChat, topics []Topic, messages []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, q := range []string{"delete from messages_fts", "delete from messages", "delete from topics", "delete from folder_chats", "delete from folders", "delete from chats", "delete from contacts", "delete from groups", "delete from group_participants", "delete from sync_state"} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if err := insertContacts(ctx, tx, contacts); err != nil {
		return err
	}
	for _, c := range chats {
		if _, err := tx.ExecContext(ctx, `insert into chats(id,kind,name,username,last_message_at,unread_count,message_count,folder_id,forum) values(?,?,?,?,?,?,?,?,?)`,
			parseInt64(c.JID), c.Kind, c.Name, c.Username, unix(c.LastMessageAt), c.UnreadCount, c.MessageCount, c.FolderID, boolInt(c.Forum)); err != nil {
			return err
		}
	}
	for _, f := range folders {
		if _, err := tx.ExecContext(ctx, `insert into folders(id,title,emoticon,color,flags_json) values(?,?,?,?,?)`,
			f.ID, f.Title, f.Emoticon, f.Color, f.FlagsJSON); err != nil {
			return err
		}
	}
	for _, fc := range folderChats {
		if _, err := tx.ExecContext(ctx, `insert into folder_chats(folder_id,chat_jid,position) values(?,?,?)`,
			fc.FolderID, fc.ChatJID, fc.Position); err != nil {
			return err
		}
	}
	for _, t := range topics {
		if _, err := tx.ExecContext(ctx, `insert into topics(chat_jid,topic_id,title,top_message_id,icon_color,icon_emoji_id,unread_count,unread_mentions_count,unread_reactions_count,pinned,closed,hidden,last_message_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			t.ChatJID, t.TopicID, t.Title, t.TopMessageID, t.IconColor, t.IconEmojiID, t.UnreadCount, t.UnreadMentionsCount, t.UnreadReactionsCount, boolInt(t.Pinned), boolInt(t.Closed), boolInt(t.Hidden), unix(t.LastMessageAt)); err != nil {
			return err
		}
	}
	if err := insertMessages(ctx, tx, messages); err != nil {
		return err
	}
	now := stats.FinishedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	for key, value := range map[string]string{"last_import_at": now.Format(time.RFC3339Nano), "source_path": stats.SourcePath} {
		if _, err := tx.ExecContext(ctx, `insert into sync_state(key,value,updated_at) values(?,?,?)`, key, value, unix(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertContacts(ctx context.Context, tx *sql.Tx, contacts []Contact) error {
	for _, c := range contacts {
		if strings.TrimSpace(c.JID) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `insert into contacts(jid,peer_type,phone,full_name,first_name,last_name,business_name,username,lid,about_text,avatar_path,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?) on conflict(jid) do update set peer_type=excluded.peer_type, phone=excluded.phone, full_name=excluded.full_name, first_name=excluded.first_name, last_name=excluded.last_name, business_name=excluded.business_name, username=excluded.username, lid=excluded.lid, about_text=excluded.about_text, avatar_path=excluded.avatar_path, updated_at=excluded.updated_at`,
			c.JID, c.PeerType, c.Phone, c.FullName, c.FirstName, c.LastName, c.BusinessName, c.Username, c.LID, c.AboutText, c.AvatarPath, unix(c.UpdatedAt)); err != nil {
			return err
		}
	}
	return nil
}

func insertMessages(ctx context.Context, tx *sql.Tx, messages []Message) error {
	for _, m := range messages {
		if _, err := tx.ExecContext(ctx, `insert into messages(source_pk,chat_jid,chat_name,msg_id,sender_jid,sender_name,ts,from_me,text,raw_type,message_type,media_type,media_title,media_path,media_url,media_size,metadata_type,metadata_title,metadata_url,metadata_json,starred,topic_id,reply_to_msg_id,reply_to_chat_jid,thread_id,edit_ts,forward_json,reactions_json,views,forwards,replies_count,pinned) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			m.SourcePK, m.ChatJID, m.ChatName, m.MessageID, m.SenderJID, m.SenderName, unix(m.Timestamp), boolInt(m.FromMe), m.Text, m.RawType, m.MessageType, m.MediaType, m.MediaTitle, m.MediaPath, m.MediaURL, m.MediaSize, m.MetadataType, m.MetadataTitle, m.MetadataURL, m.MetadataJSON, boolInt(m.Starred), m.TopicID, m.ReplyToID, m.ReplyToChat, m.ThreadID, unix(m.EditTime), m.ForwardJSON, m.ReactionsJSON, m.Views, m.Forwards, m.RepliesCount, boolInt(m.Pinned)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into messages_fts(rowid,text,chat,sender,media) values((select rowid from messages where source_pk=?),?,?,?,?)`,
			m.SourcePK, strings.TrimSpace(m.Text+" "+m.MediaTitle+" "+m.MetadataTitle+" "+m.MetadataURL), m.ChatName, m.SenderName, m.MediaType); err != nil {
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
		{&out.Chats, "select count(*) from chats"},
		{&out.UnreadChats, "select count(*) from chats where unread_count > 0"},
		{&out.UnreadMessages, "select coalesce(sum(unread_count), 0) from chats"},
		{&out.Messages, "select count(*) from messages"},
		{&out.MediaMessages, "select count(*) from messages where media_type <> ''"},
		{&out.Folders, "select count(*) from folders"},
		{&out.Topics, "select count(*) from topics"},
	} {
		if err := s.db.QueryRowContext(ctx, c.q).Scan(c.dst); err != nil {
			return out, err
		}
	}
	var oldest, newest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `select min(ts), max(ts) from messages`).Scan(&oldest, &newest); err != nil {
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
	where := ""
	if unread {
		where = "where unread_count > 0"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`select cast(id as text),kind,name,username,last_message_at,unread_count,message_count,coalesce(folder_id,''),forum from chats %s order by last_message_at desc limit ?`, where), limit)
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
	rows, err := s.db.QueryContext(ctx, `select id,title,emoticon,color,flags_json from folders order by cast(id as integer), title`)
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
	rows, err := s.db.QueryContext(ctx, `select cast(c.id as text),c.kind,c.name,c.username,c.last_message_at,c.unread_count,c.message_count,coalesce(c.folder_id,''),c.forum
from folder_chats fc join chats c on cast(c.id as text)=fc.chat_jid
where fc.folder_id=?
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
	rows, err := s.db.QueryContext(ctx, `select chat_jid,topic_id,title,top_message_id,icon_color,icon_emoji_id,unread_count,unread_mentions_count,unread_reactions_count,pinned,closed,hidden,last_message_at
from topics where chat_jid=?
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
	query := `select source_pk,chat_jid,coalesce(chat_name,''),msg_id,coalesce(sender_jid,''),coalesce(sender_name,''),ts,coalesce(edit_ts,0),from_me,coalesce(text,''),raw_type,coalesce(message_type,''),coalesce(media_type,''),coalesce(media_title,''),coalesce(media_path,''),coalesce(media_url,''),coalesce(media_size,0),coalesce(metadata_type,''),coalesce(metadata_title,''),coalesce(metadata_url,''),coalesce(metadata_json,''),starred,coalesce(topic_id,''),coalesce(reply_to_msg_id,''),coalesce(reply_to_chat_jid,''),coalesce(thread_id,''),coalesce(forward_json,''),coalesce(reactions_json,''),coalesce(views,0),coalesce(forwards,0),coalesce(replies_count,0),coalesce(pinned,0),'' from messages where 1=1`
	args := []any{}
	prefix := ""
	if search {
		ftsQuery, err := ckstore.FTS5Terms(filter.Query, "")
		if err != nil {
			return nil, err
		}
		query = `select m.source_pk,m.chat_jid,coalesce(m.chat_name,''),m.msg_id,coalesce(m.sender_jid,''),coalesce(m.sender_name,''),m.ts,coalesce(m.edit_ts,0),m.from_me,coalesce(m.text,''),m.raw_type,coalesce(m.message_type,''),coalesce(m.media_type,''),coalesce(m.media_title,''),coalesce(m.media_path,''),coalesce(m.media_url,''),coalesce(m.media_size,0),coalesce(m.metadata_type,''),coalesce(m.metadata_title,''),coalesce(m.metadata_url,''),coalesce(m.metadata_json,''),m.starred,coalesce(m.topic_id,''),coalesce(m.reply_to_msg_id,''),coalesce(m.reply_to_chat_jid,''),coalesce(m.thread_id,''),coalesce(m.forward_json,''),coalesce(m.reactions_json,''),coalesce(m.views,0),coalesce(m.forwards,0),coalesce(m.replies_count,0),coalesce(m.pinned,0),snippet(messages_fts,0,'[',']','...',12) from messages_fts f join messages m on m.rowid=f.rowid where messages_fts match ?`
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
		if err := rows.Scan(&m.SourcePK, &m.ChatJID, &m.ChatName, &m.MessageID, &m.SenderJID, &m.SenderName, &ts, &editTS, &fromMe, &m.Text, &m.RawType, &m.MessageType, &m.MediaType, &m.MediaTitle, &m.MediaPath, &m.MediaURL, &m.MediaSize, &m.MetadataType, &m.MetadataTitle, &m.MetadataURL, &m.MetadataJSON, &starred, &m.TopicID, &m.ReplyToID, &m.ReplyToChat, &m.ThreadID, &m.ForwardJSON, &m.ReactionsJSON, &m.Views, &m.Forwards, &m.RepliesCount, &pinned, &m.Snippet); err != nil {
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
	adds := map[string]map[string]string{
		"chats": {
			"folder_id": "text",
			"forum":     "integer not null default 0",
		},
		"messages": {
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
		},
		"contacts": {
			"peer_type":   "text",
			"avatar_path": "text",
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
	return nil
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
func rollback(tx *sql.Tx) { _ = tx.Rollback() }

func parseInt64(s string) int64 {
	var out int64
	_, _ = fmt.Sscan(s, &out)
	return out
}
