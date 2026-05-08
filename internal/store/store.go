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

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

type Store struct {
	db   *sql.DB
	path string
}

type ImportStats struct {
	SourcePath    string    `json:"source_path"`
	DBPath        string    `json:"db_path"`
	Chats         int       `json:"chats"`
	Messages      int       `json:"messages"`
	MediaMessages int       `json:"media_messages"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
}

type Status struct {
	DBPath         string    `json:"db_path"`
	Chats          int       `json:"chats"`
	UnreadChats    int       `json:"unread_chats"`
	UnreadMessages int       `json:"unread_messages"`
	Messages       int       `json:"messages"`
	MediaMessages  int       `json:"media_messages"`
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
}

type Contact struct {
	JID          string    `json:"jid"`
	Phone        string    `json:"phone,omitempty"`
	FullName     string    `json:"full_name,omitempty"`
	FirstName    string    `json:"first_name,omitempty"`
	LastName     string    `json:"last_name,omitempty"`
	BusinessName string    `json:"business_name,omitempty"`
	Username     string    `json:"username,omitempty"`
	LID          string    `json:"lid,omitempty"`
	AboutText    string    `json:"about_text,omitempty"`
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
	SourcePK    int64     `json:"source_pk"`
	ChatJID     string    `json:"chat_jid"`
	ChatName    string    `json:"chat_name,omitempty"`
	MessageID   string    `json:"message_id"`
	SenderJID   string    `json:"sender_jid,omitempty"`
	SenderName  string    `json:"sender_name,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
	FromMe      bool      `json:"from_me"`
	Text        string    `json:"text,omitempty"`
	RawType     int       `json:"raw_type"`
	MessageType string    `json:"message_type,omitempty"`
	MediaType   string    `json:"media_type,omitempty"`
	MediaTitle  string    `json:"media_title,omitempty"`
	MediaPath   string    `json:"media_path,omitempty"`
	MediaURL    string    `json:"media_url,omitempty"`
	MediaSize   int64     `json:"media_size,omitempty"`
	Starred     bool      `json:"starred,omitempty"`
	Snippet     string    `json:"snippet,omitempty"`
}

type MessageFilter struct {
	Query    string
	ChatJID  string
	Sender   string
	Limit    int
	After    *time.Time
	Before   *time.Time
	FromMe   *bool
	HasMedia bool
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
	if _, err := db.ExecContext(ctx, fmt.Sprintf("pragma user_version = %d", schemaVersion)); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path() string { return s.path }

func (s *Store) ReplaceAll(ctx context.Context, stats ImportStats, chats []Chat, messages []Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, q := range []string{"delete from messages_fts", "delete from messages", "delete from chats", "delete from contacts", "delete from groups", "delete from group_participants", "delete from sync_state"} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	for _, c := range chats {
		if _, err := tx.ExecContext(ctx, `insert into chats(id,kind,name,username,last_message_at,unread_count,message_count) values(?,?,?,?,?,?,?)`,
			parseInt64(c.JID), c.Kind, c.Name, c.Username, unix(c.LastMessageAt), c.UnreadCount, c.MessageCount); err != nil {
			return err
		}
	}
	for _, m := range messages {
		if _, err := tx.ExecContext(ctx, `insert into messages(source_pk,chat_jid,chat_name,msg_id,sender_jid,sender_name,ts,from_me,text,raw_type,message_type,media_type,media_title,media_path,media_url,media_size,starred) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			m.SourcePK, m.ChatJID, m.ChatName, m.MessageID, m.SenderJID, m.SenderName, unix(m.Timestamp), boolInt(m.FromMe), m.Text, m.RawType, m.MessageType, m.MediaType, m.MediaTitle, m.MediaPath, m.MediaURL, m.MediaSize, boolInt(m.Starred)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into messages_fts(rowid,text,chat,sender,media) values((select rowid from messages where source_pk=?),?,?,?,?)`,
			m.SourcePK, strings.TrimSpace(m.Text+" "+m.MediaTitle), m.ChatName, m.SenderName, m.MediaType); err != nil {
			return err
		}
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
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`select cast(id as text),kind,name,username,last_message_at,unread_count,message_count from chats %s order by last_message_at desc limit ?`, where), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Chat
	for rows.Next() {
		var c Chat
		var ts int64
		if err := rows.Scan(&c.JID, &c.Kind, &c.Name, &c.Username, &ts, &c.UnreadCount, &c.MessageCount); err != nil {
			return nil, err
		}
		c.LastMessageAt = fromUnix(ts)
		out = append(out, c)
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
	query := `select source_pk,chat_jid,chat_name,msg_id,sender_jid,sender_name,ts,from_me,text,raw_type,message_type,media_type,media_title,media_path,media_url,media_size,starred,'' from messages where 1=1`
	args := []any{}
	prefix := ""
	if search {
		query = `select m.source_pk,m.chat_jid,m.chat_name,m.msg_id,m.sender_jid,m.sender_name,m.ts,m.from_me,m.text,m.raw_type,m.message_type,m.media_type,m.media_title,m.media_path,m.media_url,m.media_size,m.starred,snippet(messages_fts,0,'[',']','...',12) from messages_fts f join messages m on m.rowid=f.rowid where messages_fts match ?`
		args = append(args, filter.Query)
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
		var ts int64
		var fromMe, starred int
		if err := rows.Scan(&m.SourcePK, &m.ChatJID, &m.ChatName, &m.MessageID, &m.SenderJID, &m.SenderName, &ts, &fromMe, &m.Text, &m.RawType, &m.MessageType, &m.MediaType, &m.MediaTitle, &m.MediaPath, &m.MediaURL, &m.MediaSize, &starred, &m.Snippet); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(ts)
		m.FromMe = fromMe != 0
		m.Starred = starred != 0
		out = append(out, m)
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
