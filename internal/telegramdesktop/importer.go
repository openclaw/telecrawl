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

type ImportOptions struct {
	Path          string
	Python        string
	Session       string
	DialogsLimit  int
	MessagesLimit int
}

type ImportResult struct {
	Stats    store.ImportStats
	Chats    []store.Chat
	Messages []store.Message
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
	} `json:"chats"`
	Messages []struct {
		SourcePK    int64  `json:"source_pk"`
		ChatID      string `json:"chat_id"`
		ChatName    string `json:"chat_name"`
		MessageID   string `json:"message_id"`
		SenderID    string `json:"sender_id"`
		SenderName  string `json:"sender_name"`
		Timestamp   string `json:"timestamp"`
		FromMe      bool   `json:"from_me"`
		Text        string `json:"text"`
		MessageType string `json:"message_type"`
		MediaType   string `json:"media_type"`
		MediaTitle  string `json:"media_title"`
	} `json:"messages"`
}

func Import(ctx context.Context, opts ImportOptions, dbPath string) (ImportResult, error) {
	tdata := strings.TrimSpace(opts.Path)
	if tdata == "" {
		tdata = DefaultPath()
	}
	python, err := resolvePython(opts.Python)
	if err != nil {
		return ImportResult{}, err
	}
	session := strings.TrimSpace(opts.Session)
	if session == "" {
		session = defaultSessionPath(dbPath)
	}
	if err := os.MkdirAll(filepath.Dir(session), 0o700); err != nil {
		return ImportResult{}, err
	}
	script, cleanup, err := writeTempScript()
	if err != nil {
		return ImportResult{}, err
	}
	defer cleanup()

	args := []string{
		script,
		"--tdata", tdata,
		"--session", session,
		"--dialogs-limit", fmt.Sprint(opts.DialogsLimit),
		"--messages-limit", fmt.Sprint(opts.MessagesLimit),
	}
	cmd := exec.CommandContext(ctx, python, args...) // #nosec G204 -- python and args are explicit CLI configuration.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "ModuleNotFoundError") || strings.Contains(msg, "No module named") {
			return ImportResult{}, fmt.Errorf("python dependency missing: run `%s -m pip install opentele2 telethon`: %s", python, msg)
		}
		if msg != "" {
			return ImportResult{}, fmt.Errorf("telegram import failed: %w: %s", err, msg)
		}
		return ImportResult{}, fmt.Errorf("telegram import failed: %w", err)
	}
	var raw pyResult
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return ImportResult{}, fmt.Errorf("decode importer output: %w", err)
	}
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
		})
	}
	for _, m := range raw.Messages {
		msg := store.Message{
			SourcePK:    m.SourcePK,
			ChatJID:     m.ChatID,
			ChatName:    m.ChatName,
			MessageID:   m.MessageID,
			SenderJID:   m.SenderID,
			SenderName:  m.SenderName,
			Timestamp:   parseTime(m.Timestamp),
			FromMe:      m.FromMe,
			Text:        m.Text,
			MessageType: m.MessageType,
			MediaType:   m.MediaType,
			MediaTitle:  m.MediaTitle,
		}
		if msg.MediaType != "" {
			result.Stats.MediaMessages++
		}
		result.Messages = append(result.Messages, msg)
	}
	result.Stats.Chats = len(result.Chats)
	result.Stats.Messages = len(result.Messages)
	return result, nil
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

func writeTempScript() (string, func(), error) {
	dir, err := os.MkdirTemp("", "telecrawl-import-*")
	if err != nil {
		return "", func() {}, err
	}
	path := filepath.Join(dir, "import_tdata.py")
	if err := os.WriteFile(path, []byte(importScript), 0o600); err != nil {
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
