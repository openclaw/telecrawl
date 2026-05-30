package telegramdesktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Path                    string
	Python                  string
	Session                 string
	DialogsLimit            int
	MessagesLimit           int
	ChatID                  string
	FetchMedia              bool
	ExistingMediaSourcePath string
	ExistingMediaRefs       []ExistingMediaRef
}

type ExistingMediaRef struct {
	SourcePK   int64  `json:"source_pk"`
	MediaType  string `json:"media_type,omitempty"`
	MediaTitle string `json:"media_title,omitempty"`
	MediaPath  string `json:"media_path"`
	MediaSize  int64  `json:"media_size,omitempty"`
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
	SourcePath  string `json:"source_path"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at"`
	RemoteMedia struct {
		Candidates  int `json:"candidates"`
		Attempted   int `json:"attempted"`
		Downloaded  int `json:"downloaded"`
		Missing     int `json:"missing"`
		Unavailable int `json:"unavailable"`
		Timeouts    int `json:"timeouts"`
		Errors      int `json:"errors"`
	} `json:"remote_media"`
	Chats []struct {
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
		MediaPath        string `json:"media_path"`
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
		if opts.FetchMedia {
			mediaTempDir, err := os.MkdirTemp("", "telecrawl-postbox-media-*")
			if err != nil {
				return ImportResult{}, err
			}
			defer func() { _ = os.RemoveAll(mediaTempDir) }()
			args = append(args, "--fetch-media", "--media-output-dir", mediaTempDir)
		}
		existingRefsPath, cleanupExistingRefs, err := writeExistingMediaRefs(opts, source.path)
		if err != nil {
			return ImportResult{}, err
		}
		defer cleanupExistingRefs()
		if existingRefsPath != "" {
			args = append(args, "--existing-media-refs", existingRefsPath)
		}
		if opts.ChatID != "" {
			args = append(args, "--chat", opts.ChatID)
		}
		deps := "pycryptodomex sqlcipher3"
		if opts.FetchMedia {
			deps += " telethon>=1.43.2"
		}
		raw, err := runPythonImporter(ctx, python, "import_postbox.py", importPostboxScript, args, deps)
		if err != nil {
			return ImportResult{}, err
		}
		result := decodeImportResult(raw, dbPath)
		if err := copyImportedMedia(result.Messages, mediaArchiveDir(dbPath), &result.Stats); err != nil {
			return ImportResult{}, err
		}
		return result, nil
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
	if opts.FetchMedia {
		mediaTempDir, err := os.MkdirTemp("", "telecrawl-tdata-media-*")
		if err != nil {
			return ImportResult{}, err
		}
		defer func() { _ = os.RemoveAll(mediaTempDir) }()
		args = append(args, "--fetch-media", "--media-output-dir", mediaTempDir)
	}
	if opts.ChatID != "" {
		args = append(args, "--chat", opts.ChatID)
	}
	raw, err := runPythonImporter(ctx, python, "import_tdata.py", importScript, args, "opentele2 telethon>=1.43.2")
	if err != nil {
		return ImportResult{}, err
	}
	result := decodeImportResult(raw, dbPath)
	if err := copyImportedMedia(result.Messages, mediaArchiveDir(dbPath), &result.Stats); err != nil {
		return ImportResult{}, err
	}
	return result, nil
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

func writeExistingMediaRefs(opts ImportOptions, sourcePath string) (string, func(), error) {
	cleanup := func() {}
	if !opts.FetchMedia || len(opts.ExistingMediaRefs) == 0 || !sameImportSourcePath(opts.ExistingMediaSourcePath, sourcePath) {
		return "", cleanup, nil
	}
	file, err := os.CreateTemp("", "telecrawl-existing-media-*.json")
	if err != nil {
		return "", cleanup, fmt.Errorf("create existing media refs: %w", err)
	}
	cleanup = func() { _ = os.Remove(file.Name()) }
	if err := json.NewEncoder(file).Encode(opts.ExistingMediaRefs); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write existing media refs: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close existing media refs: %w", err)
	}
	return file.Name(), cleanup, nil
}

func sameImportSourcePath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	leftAbs, err := filepath.Abs(filepath.Clean(left))
	if err != nil {
		return false
	}
	rightAbs, err := filepath.Abs(filepath.Clean(right))
	if err != nil {
		return false
	}
	return leftAbs == rightAbs
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
		installHint := pipInstallHint(deps)
		if strings.Contains(msg, "missing dependency:") {
			return pyResult{}, fmt.Errorf("python dependency missing: run `%s -m pip install %s`: %s", python, installHint, msg)
		}
		if strings.Contains(msg, "ModuleNotFoundError") || strings.Contains(msg, "No module named") {
			return pyResult{}, fmt.Errorf("python dependency missing: run `%s -m pip install %s`: %s", python, installHint, msg)
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

func pipInstallHint(deps string) string {
	fields := strings.Fields(deps)
	for i, dep := range fields {
		fields[i] = shellQuote(dep)
	}
	return strings.Join(fields, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"`$&;<>|()") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func decodeImportResult(raw pyResult, dbPath string) ImportResult {
	result := ImportResult{}
	started := parseTime(raw.StartedAt)
	finished := parseTime(raw.FinishedAt)
	result.Stats = store.ImportStats{
		SourcePath:             raw.SourcePath,
		DBPath:                 dbPath,
		RemoteMediaCandidates:  raw.RemoteMedia.Candidates,
		RemoteMediaAttempted:   raw.RemoteMedia.Attempted,
		RemoteMediaDownloads:   raw.RemoteMedia.Downloaded,
		RemoteMediaMissing:     raw.RemoteMedia.Missing,
		RemoteMediaUnavailable: raw.RemoteMedia.Unavailable,
		RemoteMediaTimeouts:    raw.RemoteMedia.Timeouts,
		RemoteMediaErrors:      raw.RemoteMedia.Errors,
		StartedAt:              started,
		FinishedAt:             finished,
	}
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
			MediaPath:     m.MediaPath,
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

func mediaArchiveDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "media")
}

func copyImportedMedia(messages []store.Message, archiveDir string, stats *store.ImportStats) error {
	type archivedMedia struct {
		path string
		size int64
	}
	copiedSources := make(map[string]archivedMedia)
	archivedFiles := make(map[string]int64)
	for i := range messages {
		sourcePath := strings.TrimSpace(messages[i].MediaPath)
		if sourcePath == "" {
			continue
		}
		archived, ok := copiedSources[sourcePath]
		if !ok {
			archivedPath, size, alreadyArchived, err := existingArchivedMedia(sourcePath, archiveDir)
			if err != nil {
				return err
			}
			if !alreadyArchived {
				archivedPath, size, err = copyMediaFile(sourcePath, archiveDir)
			}
			if err != nil {
				if isMediaSourceUnavailable(err) {
					copiedSources[sourcePath] = archivedMedia{}
					messages[i].MediaPath = ""
					messages[i].MediaSize = 0
					continue
				}
				return err
			}
			archived = archivedMedia{path: archivedPath, size: size}
			copiedSources[sourcePath] = archived
		}
		messages[i].MediaPath = archived.path
		messages[i].MediaSize = archived.size
		if archived.path == "" {
			continue
		}
		if _, ok := archivedFiles[archived.path]; !ok {
			archivedFiles[archived.path] = archived.size
		}
	}
	if stats != nil {
		for _, size := range archivedFiles {
			stats.MediaFiles++
			stats.MediaBytes += size
		}
	}
	return nil
}

func existingArchivedMedia(sourcePath, archiveDir string) (string, int64, bool, error) {
	sourceAbs, err := filepath.Abs(filepath.Clean(sourcePath))
	if err != nil {
		return "", 0, false, fmt.Errorf("resolve media source: %w", err)
	}
	archiveAbs, err := filepath.Abs(filepath.Clean(archiveDir))
	if err != nil {
		return "", 0, false, fmt.Errorf("resolve media archive: %w", err)
	}
	rel, err := filepath.Rel(archiveAbs, sourceAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", 0, false, nil
	}
	info, err := os.Stat(sourceAbs)
	if err != nil {
		return "", 0, false, nil
	}
	if info.IsDir() {
		return "", 0, false, mediaSourceUnavailableError{path: sourcePath, err: errors.New("is a directory")}
	}
	return sourceAbs, info.Size(), true, nil
}

type mediaSourceUnavailableError struct {
	path string
	err  error
}

func (e mediaSourceUnavailableError) Error() string {
	return fmt.Sprintf("read media %s: %v", e.path, e.err)
}

func (e mediaSourceUnavailableError) Unwrap() error {
	return e.err
}

func isMediaSourceUnavailable(err error) bool {
	var sourceErr mediaSourceUnavailableError
	return errors.As(err, &sourceErr)
}

func copyMediaFile(sourcePath, archiveDir string) (string, int64, error) {
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		return "", 0, fmt.Errorf("mkdir media archive: %w", err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", 0, mediaSourceUnavailableError{path: sourcePath, err: err}
	}
	defer func() { _ = source.Close() }()

	tmp, err := os.CreateTemp(archiveDir, ".media-*")
	if err != nil {
		return "", 0, fmt.Errorf("create media temp: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(tmp, hash), source)
	closeErr := tmp.Close()
	if copyErr != nil {
		return "", 0, fmt.Errorf("copy media %s: %w", sourcePath, copyErr)
	}
	if closeErr != nil {
		return "", 0, fmt.Errorf("close media temp: %w", closeErr)
	}

	digest := fmt.Sprintf("%x", hash.Sum(nil))
	finalDir := filepath.Join(archiveDir, digest[:2])
	if err := os.MkdirAll(finalDir, 0o700); err != nil {
		return "", 0, fmt.Errorf("mkdir media shard: %w", err)
	}
	finalPath := filepath.Join(finalDir, digest)
	if _, err := os.Stat(finalPath); err == nil {
		return finalPath, size, nil
	} else if !os.IsNotExist(err) {
		return "", 0, fmt.Errorf("stat media archive: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", 0, fmt.Errorf("archive media %s: %w", sourcePath, err)
	}
	removeTemp = false
	return finalPath, size, nil
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
