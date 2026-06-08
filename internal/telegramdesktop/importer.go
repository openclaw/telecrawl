package telegramdesktop

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openclaw/telecrawl/internal/store"
	postboxpkg "github.com/openclaw/telecrawl/internal/telegramdesktop/postbox"
)

type ImportOptions struct {
	Path                    string
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
	Contacts    []store.Contact
	Chats       []store.Chat
	Folders     []store.Folder
	FolderChats []store.FolderChat
	Topics      []store.Topic
	Messages    []store.Message
}

func Import(ctx context.Context, opts ImportOptions, dbPath string) (ImportResult, error) {
	source := resolveImportSource(strings.TrimSpace(opts.Path))
	var mediaTempDir string
	if opts.FetchMedia {
		var err error
		mediaTempDir, err = os.MkdirTemp("", "telecrawl-telegram-media-*")
		if err != nil {
			return ImportResult{}, err
		}
		defer func() { _ = os.RemoveAll(mediaTempDir) }()
	}
	if source.postbox {
		result, err := importPostboxGo(ctx, source.path, opts, dbPath, mediaTempDir)
		if err != nil {
			return ImportResult{}, err
		}
		archiveDir := mediaArchiveDir(dbPath)
		if err := copyImportedContactAvatars(result.Contacts, archiveDir); err != nil {
			return ImportResult{}, err
		}
		if err := copyImportedMedia(result.Messages, archiveDir, &result.Stats); err != nil {
			return ImportResult{}, err
		}
		return result, nil
	}

	result, err := importTDataGo(ctx, source.path, opts, dbPath, mediaTempDir)
	if err != nil {
		return ImportResult{}, err
	}
	archiveDir := mediaArchiveDir(dbPath)
	if err := copyImportedContactAvatars(result.Contacts, archiveDir); err != nil {
		return ImportResult{}, err
	}
	if err := copyImportedMedia(result.Messages, archiveDir, &result.Stats); err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

func importPostboxGo(ctx context.Context, sourcePath string, opts ImportOptions, dbPath, mediaTempDir string) (ImportResult, error) {
	started := time.Now().UTC()
	sources, err := postboxpkg.DiscoverSources(sourcePath)
	if err != nil {
		return ImportResult{}, err
	}
	if len(sources) == 0 {
		return ImportResult{}, errors.New("no Telegram for macOS Postbox account databases found")
	}
	multiAccount := len(sources) > 1
	allPeers := make(map[string]string)
	allContacts := make(map[string]store.Contact)
	byIdentity := make(map[string]postboxpkg.MessageRecord)
	for _, source := range sources {
		key, err := postboxpkg.ReadTempKey(source.KeyPath, postboxpkg.DefaultPasscodes)
		if err != nil {
			return ImportResult{}, fmt.Errorf("read postbox tempkey %s: %w", source.KeyPath, err)
		}
		records, err := postboxpkg.ReadSourceRecordsWithOptions(ctx, source, key, multiAccount, postboxpkg.ReadOptions{
			DialogsLimit:  opts.DialogsLimit,
			MessagesLimit: opts.MessagesLimit,
			ChatID:        opts.ChatID,
		})
		if err != nil {
			return ImportResult{}, fmt.Errorf("read postbox records %s: %w", source.DBPath, err)
		}
		for id, display := range records.Peers {
			allPeers[id] = display
		}
		for _, contact := range records.Contacts {
			allContacts[contact.ID] = store.Contact{
				JID:          contact.ID,
				PeerType:     contact.PeerType,
				Phone:        contact.Phone,
				FullName:     contact.FullName,
				FirstName:    contact.FirstName,
				LastName:     contact.LastName,
				BusinessName: contact.BusinessName,
				Username:     contact.Username,
				LID:          contact.LID,
				AboutText:    contact.AboutText,
				AvatarPath:   contact.AvatarPath,
				UpdatedAt:    parseTime(contact.UpdatedAt),
			}
		}
		for _, msg := range records.Messages {
			byIdentity[postboxMessageIdentity(msg)] = msg
		}
	}
	messages := make([]postboxpkg.MessageRecord, 0, len(byIdentity))
	for _, msg := range byIdentity {
		messages = append(messages, msg)
	}
	messages = filterPostboxChat(messages, opts.ChatID)
	if opts.ChatID != "" && len(messages) == 0 {
		return ImportResult{}, fmt.Errorf("could not find chat in Postbox cache: %s", opts.ChatID)
	}
	messages = applyPostboxLimits(messages, opts.DialogsLimit, opts.MessagesLimit)
	sharePostboxDuplicateMedia(messages)
	sharePostboxResourceMedia(messages)
	if applyPostboxExistingMediaRefs(messages, opts, sourcePath) > 0 {
		sharePostboxDuplicateMedia(messages)
		sharePostboxResourceMedia(messages)
	}
	remoteMedia := postboxRemoteMediaStats{Downloaded: 0, Missing: 0}
	if opts.FetchMedia {
		remoteMedia = downloadPostboxRemoteMedia(ctx, messages, sources, mediaTempDir)
	}
	sharePostboxDuplicateMedia(messages)
	sharePostboxResourceMedia(messages)
	cleared := clearPostboxPlaceholderMedia(messages)
	_ = cleared
	postboxpkg.AttachMessageMetadata(messages)

	contacts := make([]store.Contact, 0, len(allContacts))
	if opts.ChatID != "" {
		contacts = filterPostboxContactsForMessages(allContacts, messages)
	} else {
		for _, contact := range allContacts {
			contacts = append(contacts, contact)
		}
	}
	sort.Slice(contacts, func(i, j int) bool {
		return contacts[i].JID < contacts[j].JID
	})

	result := ImportResult{Contacts: contacts}
	chats := make(map[string]*store.Chat)
	for _, msg := range messages {
		if msg.MediaType != "" {
			result.Stats.MediaMessages++
		}
		result.Messages = append(result.Messages, store.Message{
			SourcePK:      msg.SourcePK,
			ChatJID:       msg.ChatID,
			ChatName:      msg.ChatName,
			MessageID:     msg.MessageID,
			SenderJID:     msg.SenderID,
			SenderName:    msg.SenderName,
			Timestamp:     parseTime(msg.Timestamp),
			FromMe:        msg.FromMe,
			Text:          msg.Text,
			MessageType:   msg.MessageType,
			MediaType:     msg.MediaType,
			MediaTitle:    msg.MediaTitle,
			MediaPath:     msg.MediaPath,
			MediaSize:     msg.MediaSize,
			MetadataType:  msg.MetadataType,
			MetadataTitle: msg.MetadataTitle,
			MetadataURL:   msg.MetadataURL,
			MetadataJSON:  msg.MetadataJSON,
		})
		chat := chats[msg.ChatID]
		if chat == nil {
			chat = &store.Chat{
				JID:           msg.ChatID,
				Kind:          "chat",
				Name:          firstNonEmpty(msg.ChatName, allPeers[msg.ChatID]),
				LastMessageAt: parseTime(msg.Timestamp),
			}
			chats[msg.ChatID] = chat
		}
		chat.MessageCount++
		if ts := parseTime(msg.Timestamp); ts.After(chat.LastMessageAt) {
			chat.LastMessageAt = ts
		}
	}
	for _, chat := range chats {
		result.Chats = append(result.Chats, *chat)
	}
	sort.Slice(result.Chats, func(i, j int) bool {
		return result.Chats[i].LastMessageAt.After(result.Chats[j].LastMessageAt)
	})
	finished := time.Now().UTC()
	result.Stats.SourcePath = sourcePath
	result.Stats.DBPath = dbPath
	result.Stats.Chats = len(result.Chats)
	result.Stats.Messages = len(result.Messages)
	result.Stats.RemoteMediaCandidates = remoteMedia.Candidates
	result.Stats.RemoteMediaAttempted = remoteMedia.Attempted
	result.Stats.RemoteMediaDownloads = remoteMedia.Downloaded
	result.Stats.RemoteMediaMissing = remoteMedia.Missing
	result.Stats.RemoteMediaUnavailable = remoteMedia.Unavailable
	result.Stats.RemoteMediaTimeouts = remoteMedia.Timeouts
	result.Stats.RemoteMediaErrors = remoteMedia.Errors
	result.Stats.StartedAt = started
	result.Stats.FinishedAt = finished
	return result, nil
}

func postboxMessageIdentity(msg postboxpkg.MessageRecord) string {
	return strings.Join([]string{msg.AccountID, msg.ChatID, msg.MessageID}, "\x00")
}

func filterPostboxChat(messages []postboxpkg.MessageRecord, chatID string) []postboxpkg.MessageRecord {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return messages
	}
	out := make([]postboxpkg.MessageRecord, 0, len(messages))
	for _, msg := range messages {
		if msg.ChatID == chatID || strconv.FormatInt(msg.RawChatID, 10) == chatID {
			out = append(out, msg)
		}
	}
	return out
}

func applyPostboxLimits(messages []postboxpkg.MessageRecord, dialogsLimit, messagesLimit int) []postboxpkg.MessageRecord {
	byChat := make(map[string][]postboxpkg.MessageRecord)
	for _, msg := range messages {
		byChat[msg.ChatID] = append(byChat[msg.ChatID], msg)
	}
	type rankedChat struct {
		chatID string
		rows   []postboxpkg.MessageRecord
		maxTS  int64
	}
	ranked := make([]rankedChat, 0, len(byChat))
	for chatID, rows := range byChat {
		var maxTS int64
		for _, row := range rows {
			if row.TS > maxTS {
				maxTS = row.TS
			}
		}
		ranked = append(ranked, rankedChat{chatID: chatID, rows: rows, maxTS: maxTS})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].maxTS == ranked[j].maxTS {
			return ranked[i].chatID < ranked[j].chatID
		}
		return ranked[i].maxTS > ranked[j].maxTS
	})
	if dialogsLimit > 0 && dialogsLimit < len(ranked) {
		ranked = ranked[:dialogsLimit]
	}
	var out []postboxpkg.MessageRecord
	for _, chat := range ranked {
		rows := append([]postboxpkg.MessageRecord(nil), chat.rows...)
		sortPostboxMessages(rows)
		if messagesLimit > 0 && messagesLimit < len(rows) {
			rows = rows[len(rows)-messagesLimit:]
		}
		out = append(out, rows...)
	}
	sortPostboxMessages(out)
	return out
}

func sortPostboxMessages(messages []postboxpkg.MessageRecord) {
	sort.Slice(messages, func(i, j int) bool {
		if messages[i].TS == messages[j].TS {
			return messages[i].SourcePK < messages[j].SourcePK
		}
		return messages[i].TS < messages[j].TS
	})
}

func filterPostboxContactsForMessages(contacts map[string]store.Contact, messages []postboxpkg.MessageRecord) []store.Contact {
	peerIDs := make(map[string]struct{})
	for _, msg := range messages {
		if msg.ChatID != "" {
			peerIDs[msg.ChatID] = struct{}{}
		}
		if msg.SenderID != "" {
			peerIDs[msg.SenderID] = struct{}{}
		}
	}
	out := make([]store.Contact, 0, len(peerIDs))
	for id := range peerIDs {
		if contact, ok := contacts[id]; ok {
			out = append(out, contact)
		}
	}
	return out
}

func applyPostboxExistingMediaRefs(messages []postboxpkg.MessageRecord, opts ImportOptions, sourcePath string) int {
	if !opts.FetchMedia || len(opts.ExistingMediaRefs) == 0 || !sameImportSourcePath(opts.ExistingMediaSourcePath, sourcePath) {
		return 0
	}
	refs := make(map[int64]ExistingMediaRef, len(opts.ExistingMediaRefs))
	for _, ref := range opts.ExistingMediaRefs {
		if strings.TrimSpace(ref.MediaPath) != "" {
			refs[ref.SourcePK] = ref
		}
	}
	restored := 0
	for i := range messages {
		if messages[i].MediaPath != "" {
			continue
		}
		ref, ok := refs[messages[i].SourcePK]
		if !ok || strings.TrimSpace(ref.MediaPath) == "" {
			continue
		}
		messages[i].MediaPath = ref.MediaPath
		messages[i].MediaSize = ref.MediaSize
		if messages[i].MediaType == "" {
			messages[i].MediaType = ref.MediaType
		}
		if messages[i].MediaTitle == "" {
			messages[i].MediaTitle = ref.MediaTitle
		}
		restored++
	}
	return restored
}

type postboxRemoteMediaStats struct {
	Candidates  int
	Attempted   int
	Downloaded  int
	Missing     int
	Unavailable int
	Timeouts    int
	Errors      int
}

func postboxRemoteMediaCandidates(messages []postboxpkg.MessageRecord) []postboxpkg.MessageRecord {
	var candidates []postboxpkg.MessageRecord
	for _, msg := range messages {
		if msg.MediaPath != "" || msg.MediaType == "" {
			continue
		}
		if !postboxHasRemoteMediaIdentity(msg) || postboxCloudMediaKey(msg) == nil {
			continue
		}
		candidates = append(candidates, msg)
	}
	return candidates
}

func postboxRemoteMediaMissingCount(messages []postboxpkg.MessageRecord) int {
	seen := make(map[string]struct{})
	for _, msg := range messages {
		if key := postboxDuplicateMediaKey(msg); key != "" {
			seen[key] = struct{}{}
		}
	}
	return len(seen)
}

func postboxHasRemoteMediaIdentity(msg postboxpkg.MessageRecord) bool {
	return len(postboxMessageResourceIDs(msg)) > 0 || len(msg.ReferencedMediaIDs) > 0
}

func postboxMessageResourceIDs(msg postboxpkg.MessageRecord) []string {
	var ids []string
	seen := make(map[string]struct{})
	for _, item := range msg.EmbeddedMedia {
		for _, id := range postboxpkg.MediaResourceIDs(item) {
			if _, ok := seen[id]; !ok {
				ids = append(ids, id)
				seen[id] = struct{}{}
			}
		}
	}
	return ids
}

type postboxCloudKey struct {
	PeerID    int64
	MessageID int64
}

func postboxCloudMediaKey(msg postboxpkg.MessageRecord) *postboxCloudKey {
	parts := strings.SplitN(msg.MessageID, ":", 2)
	if len(parts) != 2 || parts[0] != "0" {
		return nil
	}
	messageID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || messageID <= 0 {
		return nil
	}
	peerID, ok := postboxpkg.PostboxPeerToTelegramID(msg.RawChatID)
	if !ok {
		return nil
	}
	return &postboxCloudKey{PeerID: peerID, MessageID: messageID}
}

func postboxDuplicateMediaKey(msg postboxpkg.MessageRecord) string {
	key := postboxCloudMediaKey(msg)
	if key == nil {
		return ""
	}
	return fmt.Sprintf("%s:%d:%d:%s:%s:%s", msg.AccountID, key.PeerID, key.MessageID, msg.Timestamp, msg.MediaType, msg.MediaTitle)
}

func sharePostboxDuplicateMedia(messages []postboxpkg.MessageRecord) int {
	known := make(map[string]ExistingMediaRef)
	for _, msg := range messages {
		key := postboxDuplicateMediaKey(msg)
		if key == "" || msg.MediaPath == "" {
			continue
		}
		if _, ok := known[key]; !ok {
			known[key] = ExistingMediaRef{MediaPath: msg.MediaPath, MediaSize: msg.MediaSize}
		}
	}
	filled := 0
	for i := range messages {
		if messages[i].MediaPath != "" || messages[i].MediaType == "" {
			continue
		}
		key := postboxDuplicateMediaKey(messages[i])
		ref, ok := known[key]
		if !ok {
			continue
		}
		messages[i].MediaPath = ref.MediaPath
		messages[i].MediaSize = ref.MediaSize
		filled++
	}
	return filled
}

func sharePostboxResourceMedia(messages []postboxpkg.MessageRecord) int {
	known := make(map[string]ExistingMediaRef)
	for _, msg := range messages {
		if msg.MediaPath == "" {
			continue
		}
		for _, resourceID := range postboxMessageResourceIDs(msg) {
			previous, ok := known[resourceID]
			if !ok || msg.MediaSize > previous.MediaSize {
				known[resourceID] = ExistingMediaRef{MediaPath: msg.MediaPath, MediaSize: msg.MediaSize}
			}
		}
	}
	filled := 0
	for i := range messages {
		if messages[i].MediaPath != "" || messages[i].MediaType == "" {
			continue
		}
		var best ExistingMediaRef
		for _, resourceID := range postboxMessageResourceIDs(messages[i]) {
			ref, ok := known[resourceID]
			if ok && ref.MediaSize > best.MediaSize {
				best = ref
			}
		}
		if best.MediaPath == "" {
			continue
		}
		messages[i].MediaPath = best.MediaPath
		messages[i].MediaSize = best.MediaSize
		filled++
	}
	return filled
}

func clearPostboxPlaceholderMedia(messages []postboxpkg.MessageRecord) int {
	cleared := 0
	for i := range messages {
		if messages[i].MediaPath != "" || (messages[i].MediaType != "web_page" && messages[i].MediaType != "media") {
			continue
		}
		messages[i].MediaType = ""
		messages[i].MediaTitle = ""
		messages[i].MediaSize = 0
		cleared++
	}
	return cleared
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
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

func copyImportedContactAvatars(contacts []store.Contact, archiveDir string) error {
	copiedSources := make(map[string]string)
	for i := range contacts {
		sourcePath := strings.TrimSpace(contacts[i].AvatarPath)
		if sourcePath == "" {
			continue
		}
		archivedPath, ok := copiedSources[sourcePath]
		if !ok {
			path, _, alreadyArchived, err := existingArchivedMedia(sourcePath, archiveDir)
			if err != nil {
				return err
			}
			if !alreadyArchived {
				path, _, err = copyMediaFile(sourcePath, archiveDir)
			}
			if err != nil {
				if isMediaSourceUnavailable(err) {
					copiedSources[sourcePath] = ""
					contacts[i].AvatarPath = ""
					continue
				}
				return err
			}
			archivedPath = path
			copiedSources[sourcePath] = archivedPath
		}
		contacts[i].AvatarPath = archivedPath
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
