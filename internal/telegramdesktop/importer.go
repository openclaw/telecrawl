package telegramdesktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/telecrawl/internal/store"
)

//go:embed scripts/import_tdata.py
var importScript string

//go:embed scripts/import_postbox.py
var importPostboxScript string

type ImportOptions struct {
	Path          string
	Python        string
	Session       string
	DialogsLimit  int
	MessagesLimit int
	ChatID        string
}

type ImportResult struct {
	Stats       store.ImportStats
	Chats       []store.Chat
	Folders     []store.Folder
	FolderChats []store.FolderChat
	Topics      []store.Topic
	Messages    []store.Message
}

type pyResult struct {
	SourcePath string `json:"source_path"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	Chats      []struct {
		ID            string `json:"id"`
		Kind          string `json:"kind"`
		Name          string `json:"name"`
		Username      string `json:"username"`
		LastMessageAt string `json:"last_message_at"`
		UnreadCount   int    `json:"unread_count"`
		MessageCount  int    `json:"message_count"`
		FolderID      string `json:"folder_id"`
		Forum         bool   `json:"forum"`
	} `json:"chats"`
	Folders []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Emoticon  string `json:"emoticon"`
		Color     int    `json:"color"`
		FlagsJSON string `json:"flags_json"`
	} `json:"folders"`
	FolderChats []struct {
		FolderID string `json:"folder_id"`
		ChatID   string `json:"chat_id"`
		Position int    `json:"position"`
	} `json:"folder_chats"`
	Topics []struct {
		ChatID               string `json:"chat_id"`
		TopicID              string `json:"topic_id"`
		Title                string `json:"title"`
		TopMessageID         string `json:"top_message_id"`
		IconColor            int    `json:"icon_color"`
		IconEmojiID          string `json:"icon_emoji_id"`
		UnreadCount          int    `json:"unread_count"`
		UnreadMentionsCount  int    `json:"unread_mentions_count"`
		UnreadReactionsCount int    `json:"unread_reactions_count"`
		Pinned               bool   `json:"pinned"`
		Closed               bool   `json:"closed"`
		Hidden               bool   `json:"hidden"`
		LastMessageAt        string `json:"last_message_at"`
	} `json:"topics"`
	Messages []struct {
		SourcePK         int64  `json:"source_pk"`
		ChatID           string `json:"chat_id"`
		ChatName         string `json:"chat_name"`
		MessageID        string `json:"message_id"`
		TopicID          string `json:"topic_id"`
		ReplyToMessageID string `json:"reply_to_message_id"`
		ThreadID         string `json:"thread_id"`
		ReplyToChatID    string `json:"reply_to_chat_id"`
		SenderID         string `json:"sender_id"`
		SenderName       string `json:"sender_name"`
		Timestamp        string `json:"timestamp"`
		EditTimestamp    string `json:"edit_timestamp"`
		FromMe           bool   `json:"from_me"`
		Text             string `json:"text"`
		MessageType      string `json:"message_type"`
		MediaType        string `json:"media_type"`
		MediaTitle       string `json:"media_title"`
		MediaSize        int64  `json:"media_size"`
		Views            int    `json:"views"`
		Forwards         int    `json:"forwards"`
		RepliesCount     int    `json:"replies_count"`
		Pinned           bool   `json:"pinned"`
		ForwardJSON      string `json:"forward_json"`
		ReactionsJSON    string `json:"reactions_json"`
	} `json:"messages"`
}

func Import(ctx context.Context, opts ImportOptions, dbPath string) (ImportResult, error) {
	python, err := resolvePython(opts.Python)
	if err != nil {
		return ImportResult{}, err
	}
	source := resolveImportSource(strings.TrimSpace(opts.Path))
	if source.postbox {
		args := []string{
			"--source", source.path,
			"--dialogs-limit", fmt.Sprint(opts.DialogsLimit),
			"--messages-limit", fmt.Sprint(opts.MessagesLimit),
		}
		if opts.ChatID != "" {
			args = append(args, "--chat", opts.ChatID)
		}
		raw, err := runPythonImporter(ctx, python, "import_postbox.py", importPostboxScript, args, "pycryptodomex sqlcipher3")
		if err != nil {
			return ImportResult{}, err
		}
		return decodeImportResult(raw, dbPath), nil
	}

	session := strings.TrimSpace(opts.Session)
	if session == "" {
		session = defaultSessionPath(dbPath)
	}
	if err := os.MkdirAll(filepath.Dir(session), 0o700); err != nil {
		return ImportResult{}, err
	}
	args := []string{
		"--tdata", source.path,
		"--session", session,
		"--dialogs-limit", fmt.Sprint(opts.DialogsLimit),
		"--messages-limit", fmt.Sprint(opts.MessagesLimit),
	}
	if opts.ChatID != "" {
		args = append(args, "--chat", opts.ChatID)
	}
	raw, err := runPythonImporter(ctx, python, "import_tdata.py", importScript, args, "opentele2 telethon")
	if err != nil {
		return ImportResult{}, err
	}
	return decodeImportResult(raw, dbPath), nil
}

type importSource struct {
	path    string
	postbox bool
}

func resolveImportSource(path string) importSource {
	return resolveImportSourcePaths(path, DefaultPath(), DefaultPostboxPath())
}

func resolveImportSourcePaths(path, tdesktop, postbox string) importSource {
	if path != "" {
		return importSource{path: path, postbox: LooksLikePostbox(path)}
	}
	if info, err := os.Stat(tdesktop); err == nil && info.IsDir() {
		return importSource{path: tdesktop}
	}
	if info, err := os.Stat(postbox); err == nil && info.IsDir() {
		return importSource{path: postbox, postbox: true}
	}
	return importSource{path: tdesktop}
}

func runPythonImporter(ctx context.Context, python, scriptName, scriptContent string, args []string, deps string) (pyResult, error) {
	script, cleanup, err := writeTempScript(scriptName, scriptContent)
	if err != nil {
		return pyResult{}, err
	}
	defer cleanup()

	argv := append([]string{script}, args...)
	cmd := exec.CommandContext(ctx, python, argv...) // #nosec G204 -- python and args are explicit CLI configuration.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "missing dependency:") {
			return pyResult{}, fmt.Errorf("python dependency missing: run `%s -m pip install %s`: %s", python, deps, msg)
		}
		if strings.Contains(msg, "ModuleNotFoundError") || strings.Contains(msg, "No module named") {
			return pyResult{}, fmt.Errorf("python dependency missing: run `%s -m pip install %s`: %s", python, deps, msg)
		}
		if msg != "" {
			return pyResult{}, fmt.Errorf("telegram import failed: %w: %s", err, msg)
		}
		return pyResult{}, fmt.Errorf("telegram import failed: %w", err)
	}
	var raw pyResult
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return pyResult{}, fmt.Errorf("decode importer output: %w", err)
	}
	return raw, nil
}

func decodeImportResult(raw pyResult, dbPath string) ImportResult {
	result := ImportResult{}
	started := parseTime(raw.StartedAt)
	finished := parseTime(raw.FinishedAt)
	result.Stats = store.ImportStats{SourcePath: raw.SourcePath, DBPath: dbPath, StartedAt: started, FinishedAt: finished}
	for _, c := range raw.Chats {
		result.Chats = append(result.Chats, store.Chat{
			JID:           c.ID,
			Kind:          c.Kind,
			Name:          c.Name,
			Username:      c.Username,
			LastMessageAt: parseTime(c.LastMessageAt),
			UnreadCount:   c.UnreadCount,
			MessageCount:  c.MessageCount,
			FolderID:      c.FolderID,
			Forum:         c.Forum,
		})
	}
	for _, f := range raw.Folders {
		result.Folders = append(result.Folders, store.Folder{
			ID:        f.ID,
			Title:     f.Title,
			Emoticon:  f.Emoticon,
			Color:     f.Color,
			FlagsJSON: f.FlagsJSON,
		})
	}
	for _, fc := range raw.FolderChats {
		result.FolderChats = append(result.FolderChats, store.FolderChat{
			FolderID: fc.FolderID,
			ChatJID:  fc.ChatID,
			Position: fc.Position,
		})
	}
	for _, t := range raw.Topics {
		result.Topics = append(result.Topics, store.Topic{
			ChatJID:              t.ChatID,
			TopicID:              t.TopicID,
			Title:                t.Title,
			TopMessageID:         t.TopMessageID,
			IconColor:            t.IconColor,
			IconEmojiID:          t.IconEmojiID,
			UnreadCount:          t.UnreadCount,
			UnreadMentionsCount:  t.UnreadMentionsCount,
			UnreadReactionsCount: t.UnreadReactionsCount,
			Pinned:               t.Pinned,
			Closed:               t.Closed,
			Hidden:               t.Hidden,
			LastMessageAt:        parseTime(t.LastMessageAt),
		})
	}
	for _, m := range raw.Messages {
		msg := store.Message{
			SourcePK:      m.SourcePK,
			ChatJID:       m.ChatID,
			ChatName:      m.ChatName,
			MessageID:     m.MessageID,
			TopicID:       m.TopicID,
			ReplyToID:     m.ReplyToMessageID,
			ReplyToChat:   m.ReplyToChatID,
			ThreadID:      m.ThreadID,
			SenderJID:     m.SenderID,
			SenderName:    m.SenderName,
			Timestamp:     parseTime(m.Timestamp),
			EditTime:      parseTime(m.EditTimestamp),
			FromMe:        m.FromMe,
			Text:          m.Text,
			MessageType:   m.MessageType,
			MediaType:     m.MediaType,
			MediaTitle:    m.MediaTitle,
			MediaSize:     m.MediaSize,
			Views:         m.Views,
			Forwards:      m.Forwards,
			RepliesCount:  m.RepliesCount,
			Pinned:        m.Pinned,
			ForwardJSON:   m.ForwardJSON,
			ReactionsJSON: m.ReactionsJSON,
		}
		if msg.MediaType != "" {
			result.Stats.MediaMessages++
		}
		result.Messages = append(result.Messages, msg)
	}
	result.Stats.Chats = len(result.Chats)
	result.Stats.Messages = len(result.Messages)
	return result
}

func resolvePython(configured string) (string, error) {
	if strings.TrimSpace(configured) != "" {
		return configured, nil
	}
	if env := strings.TrimSpace(os.Getenv("TELECRAWL_PYTHON")); env != "" {
		return env, nil
	}
	candidates := []string{
		filepath.Join(defaultBaseDir(), "venv", "bin", "python"),
		filepath.Join("/tmp", "telecrawl-opentele311", "bin", "python"),
		filepath.Join(os.TempDir(), "telecrawl-opentele311", "bin", "python"),
		"python3.11",
		"python3.12",
		"python3",
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
		if strings.HasPrefix(candidate, string(filepath.Separator)) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", errors.New("python not found; install python3.11 or set TELECRAWL_PYTHON")
}

func defaultSessionPath(dbPath string) string {
	sum := sha256.Sum256([]byte(dbPath))
	return filepath.Join(defaultBaseDir(), "sessions", fmt.Sprintf("tdata-%x.session", sum[:6]))
}

func defaultBaseDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".telecrawl")
}

func writeTempScript(name, content string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "telecrawl-import-*")
	if err != nil {
		return "", func() {}, err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, err
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
