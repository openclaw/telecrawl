package telegramdesktop

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/session/tdesktop"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	querymessages "github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"github.com/openclaw/telecrawl/internal/store"
	"golang.org/x/crypto/blake2b"
)

const (
	telegramDesktopAPIID   = 2040
	telegramDesktopAPIHash = "b18441a1ff607e10a989891a5462e627" // gitleaks:allow
	tdataBatchSize         = 100
)

var errTDataStop = errors.New("stop tdata iteration")

type tdataImportSession struct {
	raw          *tg.Client
	selfID       int64
	opts         ImportOptions
	sourcePath   string
	mediaTempDir string
	existingRefs map[int64]ExistingMediaRef
	remoteMedia  postboxRemoteMediaStats
}

type tdataDialog struct {
	elem     dialogs.Elem
	chatID   string
	chatName string
	kind     string
	username string
	folderID string
	forum    bool
}

func importTDataGo(ctx context.Context, sourcePath string, opts ImportOptions, dbPath, mediaTempDir string) (ImportResult, error) {
	started := time.Now().UTC()
	accounts, err := tdesktop.Read(sourcePath, nil)
	if err != nil {
		return ImportResult{}, fmt.Errorf("read Telegram Desktop tdata: %w", err)
	}
	if len(accounts) == 0 {
		return ImportResult{}, errors.New("no Telegram Desktop accounts found")
	}
	data, err := session.TDesktopSession(accounts[0])
	if err != nil {
		return ImportResult{}, fmt.Errorf("read Telegram Desktop session: %w", err)
	}
	storage := &session.StorageMemory{}
	if err := (&session.Loader{Storage: storage}).Save(ctx, data); err != nil {
		return ImportResult{}, fmt.Errorf("store Telegram Desktop session: %w", err)
	}
	client := telegram.NewClient(telegramDesktopAPIID, telegramDesktopAPIHash, telegram.Options{
		SessionStorage: storage,
		NoUpdates:      true,
		AllowCDN:       true,
		Middlewares:    []telegram.Middleware{newTelegramFloodWaitPolicy(opts.Progress)},
		Device: telegram.DeviceConfig{
			DeviceModel:    "Desktop",
			SystemVersion:  "Windows 11",
			AppVersion:     "6.5 x64",
			SystemLangCode: "en-US",
			LangPack:       "tdesktop",
			LangCode:       "en",
		},
	})

	var result ImportResult
	err = client.Run(ctx, func(ctx context.Context) error {
		self, err := client.Self(ctx)
		if err != nil {
			return fmt.Errorf("telegram session is not authorized: %w", err)
		}
		importer := &tdataImportSession{
			raw:          tg.NewClient(client),
			selfID:       self.ID,
			opts:         opts,
			sourcePath:   sourcePath,
			mediaTempDir: mediaTempDir,
			existingRefs: tdataExistingMediaRefs(opts, sourcePath),
		}
		result, err = importer.importAccount(ctx)
		return err
	})
	if err != nil {
		return ImportResult{}, err
	}
	result.Stats.SourcePath = sourcePath
	result.Stats.DBPath = dbPath
	result.Stats.StartedAt = started
	result.Stats.FinishedAt = time.Now().UTC()
	result.Stats.Chats = len(result.Chats)
	result.Stats.Messages = len(result.Messages)
	return result, nil
}

func (s *tdataImportSession) importAccount(ctx context.Context) (ImportResult, error) {
	dialogRows, err := s.loadDialogs(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	if chatID := strings.TrimSpace(s.opts.ChatID); chatID != "" {
		dialogRows = filterTDataDialogs(dialogRows, chatID)
		if len(dialogRows) == 0 {
			return ImportResult{}, fmt.Errorf("could not resolve chat: %s", chatID)
		}
	}

	var result ImportResult
	if strings.TrimSpace(s.opts.ChatID) == "" {
		result.Folders, result.FolderChats = s.loadFolders(ctx)
	}
	for _, row := range dialogRows {
		result.Topics = append(result.Topics, s.loadTopics(ctx, row)...)
		messages, err := s.loadMessages(ctx, row)
		if err != nil {
			return ImportResult{}, err
		}
		chat := store.Chat{
			JID:           row.chatID,
			Kind:          row.kind,
			Name:          row.chatName,
			Username:      row.username,
			UnreadCount:   tdataDialogUnreadCount(row.elem.Dialog),
			MessageCount:  len(messages),
			FolderID:      row.folderID,
			Forum:         row.forum,
			LastMessageAt: latestTDataMessageTime(messages),
		}
		result.Chats = append(result.Chats, chat)
		result.Messages = append(result.Messages, messages...)
	}
	sort.Slice(result.Messages, func(i, j int) bool {
		if result.Messages[i].Timestamp.Equal(result.Messages[j].Timestamp) {
			return result.Messages[i].SourcePK < result.Messages[j].SourcePK
		}
		return result.Messages[i].Timestamp.Before(result.Messages[j].Timestamp)
	})
	result.Stats = store.ImportStats{
		MediaMessages:          countTDataMediaMessages(result.Messages),
		RemoteMediaCandidates:  s.remoteMedia.Candidates,
		RemoteMediaAttempted:   s.remoteMedia.Attempted,
		RemoteMediaDownloads:   s.remoteMedia.Downloaded,
		RemoteMediaMissing:     s.remoteMedia.Missing,
		RemoteMediaUnavailable: s.remoteMedia.Unavailable,
		RemoteMediaTimeouts:    s.remoteMedia.Timeouts,
		RemoteMediaErrors:      s.remoteMedia.Errors,
	}
	return result, nil
}

func (s *tdataImportSession) loadDialogs(ctx context.Context) ([]tdataDialog, error) {
	var out []tdataDialog
	seen := make(map[string]struct{})
	limit := s.opts.DialogsLimit
	chatFilter := strings.TrimSpace(s.opts.ChatID)
	if chatFilter != "" {
		limit = 0
	}
	count := 0
	err := query.GetDialogs(s.raw).BatchSize(tdataBatchSize).ForEach(ctx, func(ctx context.Context, elem dialogs.Elem) error {
		if elem.Deleted() {
			return nil
		}
		chatID := tdataPeerIDString(elem.Dialog.GetPeer(), s.selfID)
		if chatID == "" {
			return nil
		}
		if _, ok := seen[chatID]; ok {
			return nil
		}
		seen[chatID] = struct{}{}
		info := tdataPeerInfo(elem.Dialog.GetPeer(), elem.Entities, s.selfID)
		folderID := tdataDialogFolderID(elem.Dialog)
		if chatFilter != "" && !tdataChatFilterMatches(chatID, chatFilter) {
			return nil
		}
		out = append(out, tdataDialog{
			elem:     elem,
			chatID:   chatID,
			chatName: firstNonEmpty(info.name, chatID),
			kind:     firstNonEmpty(info.kind, "unknown"),
			username: info.username,
			folderID: folderID,
			forum:    info.forum,
		})
		count++
		if chatFilter != "" {
			return errTDataStop
		}
		if limit > 0 && count >= limit {
			return errTDataStop
		}
		return nil
	})
	if errors.Is(err, errTDataStop) {
		err = nil
	}
	return out, err
}

func tdataDialogUnreadCount(dialog tg.DialogClass) int {
	if concrete, ok := dialog.(*tg.Dialog); ok {
		return concrete.UnreadCount
	}
	return 0
}

func tdataDialogFolderID(dialog tg.DialogClass) string {
	if concrete, ok := dialog.(*tg.Dialog); ok {
		if id, ok := concrete.GetFolderID(); ok {
			return strconv.Itoa(id)
		}
	}
	return ""
}

func filterTDataDialogs(dialogs []tdataDialog, chatID string) []tdataDialog {
	var out []tdataDialog
	for _, row := range dialogs {
		if tdataChatFilterMatches(row.chatID, chatID) {
			out = append(out, row)
		}
	}
	return out
}

func tdataChatFilterMatches(chatID, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return false
	}
	for _, alias := range tdataChatIDAliases(chatID) {
		if alias == filter {
			return true
		}
	}
	return false
}

func tdataChatIDAliases(chatID string) []string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil
	}
	seen := make(map[string]struct{})
	add := func(value string) {
		if value == "" {
			return
		}
		seen[value] = struct{}{}
	}
	add(chatID)
	add(strings.TrimPrefix(chatID, "-"))
	if raw, ok := strings.CutPrefix(chatID, "-100"); ok {
		add(raw)
		trimmed := strings.TrimLeft(raw, "0")
		if trimmed == "" {
			trimmed = "0"
		}
		add(trimmed)
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (s *tdataImportSession) loadMessages(ctx context.Context, row tdataDialog) ([]store.Message, error) {
	limit := s.opts.MessagesLimit
	count := 0
	var out []store.Message
	err := row.elem.Messages(s.raw).BatchSize(tdataBatchSize).ForEach(ctx, func(ctx context.Context, elem querymessages.Elem) error {
		msg := elem.Msg
		if msg.GetID() == 0 {
			return nil
		}
		converted := s.convertMessage(ctx, row, elem)
		out = append(out, converted)
		count++
		if limit > 0 && count >= limit {
			return errTDataStop
		}
		return nil
	})
	if errors.Is(err, errTDataStop) {
		err = nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].SourcePK < out[j].SourcePK
		}
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	return out, err
}

func (s *tdataImportSession) convertMessage(ctx context.Context, row tdataDialog, elem querymessages.Elem) store.Message {
	msg := elem.Msg
	msgID := msg.GetID()
	replyTo, replyThread, replyChat, topicID := tdataReplyFields(msg, s.selfID)
	if topicID == "" {
		if svc, ok := msg.(*tg.MessageService); ok {
			if _, ok := svc.Action.(*tg.MessageActionTopicCreate); ok {
				topicID = strconv.Itoa(msgID)
			}
		}
	}
	sourcePK := stableTDataSourcePK(row.chatID, msgID)
	mediaType := tdataMediaType(msg)
	mediaTitle := tdataMediaTitle(msg)
	mediaSize := tdataMediaSize(msg)
	mediaPath := ""
	if s.opts.FetchMedia && mediaType != "" {
		if ref, ok := s.existingRefs[sourcePK]; ok {
			mediaPath = ref.MediaPath
			mediaSize = ref.MediaSize
			if mediaType == "" {
				mediaType = ref.MediaType
			}
			if mediaTitle == "" {
				mediaTitle = ref.MediaTitle
			}
		} else {
			s.remoteMedia.Candidates++
			s.remoteMedia.Attempted++
			path, size, reason := s.downloadMessageMedia(ctx, elem, row.chatID)
			if path != "" {
				mediaPath = path
				mediaSize = size
				s.remoteMedia.Downloaded++
			} else {
				s.remoteMedia.Missing++
				switch reason {
				case "timeout":
					s.remoteMedia.Timeouts++
				case "error":
					s.remoteMedia.Errors++
				default:
					s.remoteMedia.Unavailable++
				}
			}
		}
	}
	fromID, fromName := s.senderInfo(msg, elem.Entities)
	return store.Message{
		SourcePK:      sourcePK,
		ChatJID:       row.chatID,
		ChatName:      row.chatName,
		MessageID:     strconv.Itoa(msgID),
		TopicID:       topicID,
		ReplyToID:     replyTo,
		ThreadID:      replyThread,
		ReplyToChat:   replyChat,
		SenderJID:     fromID,
		SenderName:    fromName,
		Timestamp:     unixTime(msg.GetDate()),
		EditTime:      tdataEditTime(msg),
		FromMe:        msg.GetOut(),
		Text:          tdataMessageText(msg),
		MessageType:   tdataMessageType(msg),
		MediaType:     mediaType,
		MediaTitle:    mediaTitle,
		MediaPath:     mediaPath,
		MediaSize:     mediaSize,
		Views:         tdataViews(msg),
		Forwards:      tdataForwards(msg),
		RepliesCount:  tdataRepliesCount(msg),
		Pinned:        tdataPinned(msg),
		ForwardJSON:   tdataForwardJSON(msg),
		ReactionsJSON: tdataReactionsJSON(msg),
	}
}

func (s *tdataImportSession) senderInfo(msg tg.NotEmptyMessage, ents peer.Entities) (string, string) {
	from, ok := msg.GetFromID()
	if !ok {
		return "", ""
	}
	id := tdataPeerIDString(from, s.selfID)
	info := tdataPeerInfo(from, ents, s.selfID)
	return id, info.name
}

func (s *tdataImportSession) downloadMessageMedia(ctx context.Context, elem querymessages.Elem, chatID string) (string, int64, string) {
	return downloadTelegramMessageMedia(ctx, s.raw, elem, s.mediaTempDir, fmt.Sprintf("%s:%d", chatID, elem.Msg.GetID()))
}

func downloadTelegramMessageMedia(ctx context.Context, raw *tg.Client, elem querymessages.Elem, mediaTempDir, key string) (string, int64, string) {
	if strings.TrimSpace(mediaTempDir) == "" {
		return "", 0, "unavailable"
	}
	file, ok := telegramMessageFile(elem)
	if !ok {
		return "", 0, "unavailable"
	}
	messageDir := filepath.Join(mediaTempDir, fmt.Sprintf("%x", sha256Bytes(key)))
	if err := os.MkdirAll(messageDir, 0o700); err != nil {
		return "", 0, "error"
	}
	name := filepath.Base(strings.TrimSpace(file.Name))
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "media"
	}
	outputPath := filepath.Join(messageDir, name)
	// Reserve the retry budget so a valid flood wait cannot consume the transfer deadline.
	downloadCtx, cancel := context.WithTimeout(ctx, telegramMediaDownloadTimeout)
	defer cancel()
	if _, err := downloader.NewDownloader().WithAllowCDN(true).Download(raw, file.Location).ToPath(downloadCtx, outputPath); err != nil {
		if errors.Is(downloadCtx.Err(), context.DeadlineExceeded) {
			return "", 0, "timeout"
		}
		return "", 0, "error"
	}
	info, err := os.Stat(outputPath)
	if err != nil || info.Size() <= 0 {
		return "", 0, "unavailable"
	}
	return outputPath, info.Size(), ""
}

func telegramMessageFile(elem querymessages.Elem) (querymessages.File, bool) {
	if file, ok := elem.File(); ok {
		return file, true
	}
	msg, ok := elem.Msg.(*tg.Message)
	if !ok {
		return querymessages.File{}, false
	}
	media, ok := msg.Media.(*tg.MessageMediaWebPage)
	if !ok {
		return querymessages.File{}, false
	}
	page, ok := media.Webpage.(*tg.WebPage)
	if !ok {
		return querymessages.File{}, false
	}
	if rawDocument, ok := page.GetDocument(); ok {
		if doc, ok := rawDocument.AsNotEmpty(); ok {
			return querymessages.File{
				Name:     firstNonEmpty(tdataDocumentFilename(doc), tdataDocumentAudioTitle(doc), fmt.Sprintf("doc%d", doc.ID)),
				MIMEType: doc.MimeType,
				Location: doc.AsInputDocumentFileLocation(""),
			}, true
		}
	}
	if rawPhoto, ok := page.GetPhoto(); ok {
		if photo, ok := rawPhoto.AsNotEmpty(); ok {
			thumbSize := tdataLargestPhotoThumbSize(photo)
			if thumbSize == "" {
				return querymessages.File{}, false
			}
			return querymessages.File{
				Name:     fmt.Sprintf("photo%d_%s.jpg", photo.ID, time.Unix(int64(photo.Date), 0).Format("2006-01-02_15-04-05")),
				MIMEType: "image/jpeg",
				Location: &tg.InputPhotoFileLocation{
					ID:            photo.ID,
					AccessHash:    photo.AccessHash,
					FileReference: photo.FileReference,
					ThumbSize:     thumbSize,
				},
			}, true
		}
	}
	return querymessages.File{}, false
}

func tdataLargestPhotoThumbSize(photo *tg.Photo) string {
	var bestType string
	var bestArea int
	for _, rawSize := range photo.Sizes {
		size, ok := rawSize.(interface {
			GetW() int
			GetH() int
			GetType() string
		})
		if !ok {
			continue
		}
		area := size.GetW() * size.GetH()
		if area > bestArea {
			bestArea = area
			bestType = size.GetType()
		}
	}
	return bestType
}

func (s *tdataImportSession) loadFolders(ctx context.Context) ([]store.Folder, []store.FolderChat) {
	result, err := s.raw.MessagesGetDialogFilters(ctx)
	if err != nil || result == nil {
		return nil, nil
	}
	memberships := make(map[string]map[string]struct{})
	var folders []store.Folder
	for _, filter := range result.GetFilters() {
		folder, explicit := tdataFolder(filter, s.selfID)
		if folder.ID == "" {
			continue
		}
		folders = append(folders, folder)
		if len(explicit) > 0 {
			set := memberships[folder.ID]
			if set == nil {
				set = make(map[string]struct{})
				memberships[folder.ID] = set
			}
			for _, chatID := range explicit {
				set[chatID] = struct{}{}
			}
		}
		if id, err := strconv.Atoi(folder.ID); err == nil && id != 0 {
			_ = query.GetDialogs(s.raw).FolderID(id).BatchSize(tdataBatchSize).ForEach(ctx, func(ctx context.Context, elem dialogs.Elem) error {
				chatID := tdataPeerIDString(elem.Dialog.GetPeer(), s.selfID)
				if chatID == "" {
					return nil
				}
				set := memberships[folder.ID]
				if set == nil {
					set = make(map[string]struct{})
					memberships[folder.ID] = set
				}
				set[chatID] = struct{}{}
				return nil
			})
		}
	}
	var folderChats []store.FolderChat
	for folderID, chats := range memberships {
		ids := make([]string, 0, len(chats))
		for chatID := range chats {
			ids = append(ids, chatID)
		}
		sort.Strings(ids)
		for position, chatID := range ids {
			folderChats = append(folderChats, store.FolderChat{
				FolderID: folderID,
				ChatJID:  chatID,
				Position: position,
			})
		}
	}
	sort.Slice(folders, func(i, j int) bool {
		return numericStringLess(folders[i].ID, folders[j].ID)
	})
	sort.Slice(folderChats, func(i, j int) bool {
		if folderChats[i].FolderID == folderChats[j].FolderID {
			return folderChats[i].Position < folderChats[j].Position
		}
		return numericStringLess(folderChats[i].FolderID, folderChats[j].FolderID)
	})
	return folders, folderChats
}

func (s *tdataImportSession) loadTopics(ctx context.Context, row tdataDialog) []store.Topic {
	if !row.forum {
		return nil
	}
	var out []store.Topic
	seen := make(map[int]struct{})
	req := tg.MessagesGetForumTopicsRequest{
		Peer:  row.elem.Peer,
		Limit: tdataBatchSize,
	}
	for {
		result, err := s.raw.MessagesGetForumTopics(ctx, &req)
		if err != nil || result == nil || len(result.Topics) == 0 {
			return out
		}
		for _, rawTopic := range result.Topics {
			topic, ok := rawTopic.(*tg.ForumTopic)
			if !ok || topic.ID == 0 {
				continue
			}
			if _, ok := seen[topic.ID]; ok {
				continue
			}
			seen[topic.ID] = struct{}{}
			iconEmojiID := ""
			if id, ok := topic.GetIconEmojiID(); ok {
				iconEmojiID = strconv.FormatInt(id, 10)
			}
			out = append(out, store.Topic{
				ChatJID:              row.chatID,
				TopicID:              strconv.Itoa(topic.ID),
				Title:                topic.Title,
				TopMessageID:         strconv.Itoa(topic.TopMessage),
				IconColor:            topic.IconColor,
				IconEmojiID:          iconEmojiID,
				UnreadCount:          topic.UnreadCount,
				UnreadMentionsCount:  topic.UnreadMentionsCount,
				UnreadReactionsCount: topic.UnreadReactionsCount,
				Pinned:               topic.Pinned,
				Closed:               topic.Closed,
				Hidden:               topic.Hidden,
				LastMessageAt:        unixTime(topic.Date),
			})
		}
		last, ok := result.Topics[len(result.Topics)-1].(*tg.ForumTopic)
		if !ok || len(result.Topics) < tdataBatchSize {
			return out
		}
		req.OffsetTopic = last.ID
		req.OffsetID = last.TopMessage
		req.OffsetDate = last.Date
	}
}

type tdataPeerDetails struct {
	kind     string
	name     string
	username string
	forum    bool
}

func tdataPeerInfo(peerID tg.PeerClass, ents peer.Entities, selfID int64) tdataPeerDetails {
	switch p := peerID.(type) {
	case *tg.PeerUser:
		if p.UserID == selfID {
			return tdataUserInfo(&tg.User{ID: selfID, Self: true})
		}
		if user, ok := ents.User(p.UserID); ok {
			return tdataUserInfo(user)
		}
		return tdataPeerDetails{kind: "user", name: strconv.FormatInt(p.UserID, 10)}
	case *tg.PeerChat:
		if chat, ok := ents.Chat(p.ChatID); ok {
			return tdataPeerDetails{kind: "group", name: chat.Title}
		}
		return tdataPeerDetails{kind: "group", name: strconv.FormatInt(p.ChatID, 10)}
	case *tg.PeerChannel:
		if channel, ok := ents.Channel(p.ChannelID); ok {
			username, _ := channel.GetUsername()
			return tdataPeerDetails{kind: "channel", name: channel.Title, username: username, forum: channel.GetForum()}
		}
		return tdataPeerDetails{kind: "channel", name: strconv.FormatInt(p.ChannelID, 10)}
	default:
		return tdataPeerDetails{}
	}
}

func tdataUserInfo(user *tg.User) tdataPeerDetails {
	first, _ := user.GetFirstName()
	last, _ := user.GetLastName()
	username, _ := user.GetUsername()
	name := strings.TrimSpace(first + " " + last)
	if name == "" {
		name = username
	}
	if name == "" && user.ID != 0 {
		name = strconv.FormatInt(user.ID, 10)
	}
	return tdataPeerDetails{kind: "user", name: name, username: username}
}

func tdataPeerIDString(peerID tg.PeerClass, selfID int64) string {
	if value, ok := tdataPeerID(peerID, selfID); ok {
		return strconv.FormatInt(value, 10)
	}
	return ""
}

func tdataPeerID(peerID tg.PeerClass, selfID int64) (int64, bool) {
	switch p := peerID.(type) {
	case *tg.PeerUser:
		return p.UserID, p.UserID != 0
	case *tg.PeerChat:
		return -p.ChatID, p.ChatID != 0
	case *tg.PeerChannel:
		return -1000000000000 - p.ChannelID, p.ChannelID != 0
	default:
		return 0, false
	}
}

func tdataInputPeerIDString(peerID tg.InputPeerClass, selfID int64) string {
	switch p := peerID.(type) {
	case *tg.InputPeerSelf:
		if selfID != 0 {
			return strconv.FormatInt(selfID, 10)
		}
	case *tg.InputPeerUser:
		return strconv.FormatInt(p.UserID, 10)
	case *tg.InputPeerChat:
		return strconv.FormatInt(-p.ChatID, 10)
	case *tg.InputPeerChannel:
		return strconv.FormatInt(-1000000000000-p.ChannelID, 10)
	}
	return ""
}

func stableTDataSourcePK(chatID string, messageID int) int64 {
	h, _ := blake2b.New(8, nil)
	_, _ = fmt.Fprintf(h, "%s:%d", chatID, messageID)
	value := int64(binary.BigEndian.Uint64(h.Sum(nil)) & ((1 << 63) - 1))
	if value == 0 {
		return 1
	}
	return value
}

func tdataMessageType(msg tg.NotEmptyMessage) string {
	switch msg.(type) {
	case *tg.Message:
		return "Message"
	case *tg.MessageService:
		return "MessageService"
	default:
		return msg.TypeName()
	}
}

func tdataMessageText(msg tg.NotEmptyMessage) string {
	if m, ok := msg.(*tg.Message); ok {
		return m.Message
	}
	return ""
}

func tdataMediaType(msg tg.NotEmptyMessage) string {
	m, ok := msg.(*tg.Message)
	if !ok || m.Media == nil {
		return ""
	}
	name := m.Media.TypeName()
	name = strings.TrimPrefix(name, "messageMedia")
	return strings.ToLower(name)
}

func tdataMediaTitle(msg tg.NotEmptyMessage) string {
	m, ok := msg.(*tg.Message)
	if !ok || m.Media == nil {
		return ""
	}
	switch media := m.Media.(type) {
	case *tg.MessageMediaDocument:
		doc, ok := media.Document.AsNotEmpty()
		if !ok {
			return ""
		}
		return firstNonEmpty(tdataDocumentFilename(doc), tdataDocumentAudioTitle(doc))
	case *tg.MessageMediaWebPage:
		if page, ok := media.Webpage.(*tg.WebPage); ok {
			return firstNonEmpty(optionalString(page.GetTitle()), optionalString(page.GetSiteName()), page.URL)
		}
	}
	return ""
}

func tdataMediaSize(msg tg.NotEmptyMessage) int64 {
	m, ok := msg.(*tg.Message)
	if !ok {
		return 0
	}
	media, ok := m.Media.(*tg.MessageMediaDocument)
	if !ok {
		return 0
	}
	doc, ok := media.Document.AsNotEmpty()
	if !ok {
		return 0
	}
	return doc.Size
}

func tdataDocumentFilename(doc *tg.Document) string {
	for _, attr := range doc.Attributes {
		if filename, ok := attr.(*tg.DocumentAttributeFilename); ok {
			return filename.FileName
		}
	}
	return ""
}

func tdataDocumentAudioTitle(doc *tg.Document) string {
	for _, attr := range doc.Attributes {
		if audio, ok := attr.(*tg.DocumentAttributeAudio); ok {
			title, _ := audio.GetTitle()
			return title
		}
	}
	return ""
}

func optionalString(value string, ok bool) string {
	if !ok {
		return ""
	}
	return value
}

func tdataReplyFields(msg tg.NotEmptyMessage, selfID int64) (replyTo, threadID, replyChat, topicID string) {
	rawReply, ok := msg.GetReplyTo()
	if !ok {
		return "", "", "", ""
	}
	reply, ok := rawReply.(*tg.MessageReplyHeader)
	if !ok {
		return "", "", "", ""
	}
	if value, ok := reply.GetReplyToMsgID(); ok && value != 0 {
		replyTo = strconv.Itoa(value)
	}
	if value, ok := reply.GetReplyToTopID(); ok && value != 0 {
		threadID = strconv.Itoa(value)
		topicID = threadID
	} else if reply.GetForumTopic() && replyTo != "" {
		topicID = replyTo
	}
	if peerID, ok := reply.GetReplyToPeerID(); ok {
		replyChat = tdataPeerIDString(peerID, selfID)
	}
	return replyTo, threadID, replyChat, topicID
}

func tdataEditTime(msg tg.NotEmptyMessage) time.Time {
	if m, ok := msg.(*tg.Message); ok {
		if value, ok := m.GetEditDate(); ok {
			return unixTime(value)
		}
	}
	return time.Time{}
}

func tdataViews(msg tg.NotEmptyMessage) int {
	if m, ok := msg.(*tg.Message); ok {
		value, _ := m.GetViews()
		return value
	}
	return 0
}

func tdataForwards(msg tg.NotEmptyMessage) int {
	if m, ok := msg.(*tg.Message); ok {
		value, _ := m.GetForwards()
		return value
	}
	return 0
}

func tdataRepliesCount(msg tg.NotEmptyMessage) int {
	if m, ok := msg.(*tg.Message); ok {
		replies, ok := m.GetReplies()
		if ok {
			return replies.Replies
		}
	}
	return 0
}

func tdataPinned(msg tg.NotEmptyMessage) bool {
	if m, ok := msg.(*tg.Message); ok {
		return m.Pinned
	}
	return false
}

func tdataForwardJSON(msg tg.NotEmptyMessage) string {
	if m, ok := msg.(*tg.Message); ok {
		if value, ok := m.GetFwdFrom(); ok {
			return tdataJSON(value)
		}
	}
	return ""
}

func tdataReactionsJSON(msg tg.NotEmptyMessage) string {
	switch m := msg.(type) {
	case *tg.Message:
		if value, ok := m.GetReactions(); ok {
			return tdataJSON(value)
		}
	case *tg.MessageService:
		if value, ok := m.GetReactions(); ok {
			return tdataJSON(value)
		}
	}
	return ""
}

func tdataJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil || string(data) == "null" || string(data) == "{}" {
		return ""
	}
	return string(data)
}

func tdataFolder(filter tg.DialogFilterClass, selfID int64) (store.Folder, []string) {
	switch f := filter.(type) {
	case *tg.DialogFilter:
		flags := tdataFolderFlags(f)
		flagsJSON, _ := json.Marshal(flags)
		emoticon, _ := f.GetEmoticon()
		color, _ := f.GetColor()
		return store.Folder{
			ID:        strconv.Itoa(f.ID),
			Title:     f.Title.Text,
			Emoticon:  emoticon,
			Color:     color,
			FlagsJSON: string(flagsJSON),
		}, tdataFilterPeerIDs(selfID, f.PinnedPeers, f.IncludePeers)
	case *tg.DialogFilterChatlist:
		emoticon, _ := f.GetEmoticon()
		color, _ := f.GetColor()
		return store.Folder{
			ID:       strconv.Itoa(f.ID),
			Title:    f.Title.Text,
			Emoticon: emoticon,
			Color:    color,
		}, tdataFilterPeerIDs(selfID, f.PinnedPeers, f.IncludePeers)
	default:
		return store.Folder{}, nil
	}
}

func tdataFolderFlags(f *tg.DialogFilter) map[string]bool {
	return map[string]bool{
		"contacts":         f.GetContacts(),
		"non_contacts":     f.GetNonContacts(),
		"groups":           f.GetGroups(),
		"broadcasts":       f.GetBroadcasts(),
		"bots":             f.GetBots(),
		"exclude_muted":    f.GetExcludeMuted(),
		"exclude_read":     f.GetExcludeRead(),
		"exclude_archived": f.GetExcludeArchived(),
	}
}

func tdataFilterPeerIDs(selfID int64, lists ...[]tg.InputPeerClass) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, peers := range lists {
		for _, peerID := range peers {
			id := tdataInputPeerIDString(peerID, selfID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

func tdataExistingMediaRefs(opts ImportOptions, sourcePath string) map[int64]ExistingMediaRef {
	if !opts.FetchMedia || len(opts.ExistingMediaRefs) == 0 || !sameImportSourcePath(opts.ExistingMediaSourcePath, sourcePath) {
		return nil
	}
	refs := make(map[int64]ExistingMediaRef)
	for _, ref := range opts.ExistingMediaRefs {
		if strings.TrimSpace(ref.MediaPath) != "" {
			refs[ref.SourcePK] = ref
		}
	}
	return refs
}

func latestTDataMessageTime(messages []store.Message) time.Time {
	var latest time.Time
	for _, msg := range messages {
		if msg.Timestamp.After(latest) {
			latest = msg.Timestamp
		}
	}
	return latest
}

func countTDataMediaMessages(messages []store.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.MediaType != "" {
			count++
		}
	}
	return count
}

func unixTime(value int) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(value), 0).UTC()
}

func sha256Bytes(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func numericStringLess(left, right string) bool {
	leftN, leftErr := strconv.Atoi(left)
	rightN, rightErr := strconv.Atoi(right)
	if leftErr == nil && rightErr == nil {
		return leftN < rightN
	}
	return left < right
}
