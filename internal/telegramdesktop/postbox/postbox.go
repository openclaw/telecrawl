package postbox

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	incomingFlag = 4

	resourceTypeCloudPhotoSize    = 1226791958
	resourceTypeCloudDocumentSize = -2129249780
	resourceTypeCloudDocument     = 486562374
	resourceTypeCloudPeerPhoto    = 923090569
	resourceTypeLocalFile         = 711798229
	resourceTypeLocalFileRef      = 1868491758
)

var mediaTags = []struct {
	bit   uint32
	label string
}{
	{1 << 0, "photo_or_video"},
	{1 << 1, "file"},
	{1 << 2, "music"},
	{1 << 3, "web_page"},
	{1 << 4, "voice_or_instant_video"},
	{1 << 7, "gif"},
	{1 << 8, "photo"},
	{1 << 9, "video"},
}

type Message struct {
	Flags              uint32
	Tags               uint32
	AuthorID           int64
	HasAuthorID        bool
	Text               string
	EmbeddedMediaCount int
	EmbeddedMedia      []any
	ReferencedMediaIDs []MediaRef
}

type MediaRef struct {
	Namespace int32
	ID        int64
}

func ReadMessage(value []byte) (*Message, error) {
	reader := newByteReader(value)
	version, err := reader.int8()
	if err != nil {
		return nil, err
	}
	if version != 0 {
		return nil, nil
	}
	if _, err := reader.uint32(); err != nil {
		return nil, err
	}
	if _, err := reader.uint32(); err != nil {
		return nil, err
	}
	dataFlags, err := reader.uint8()
	if err != nil {
		return nil, err
	}
	if dataFlags&(1<<0) != 0 {
		if _, err := reader.int64(); err != nil {
			return nil, err
		}
	}
	if dataFlags&(1<<1) != 0 {
		if _, err := reader.uint32(); err != nil {
			return nil, err
		}
	}
	if dataFlags&(1<<2) != 0 {
		if _, err := reader.int64(); err != nil {
			return nil, err
		}
	}
	if dataFlags&(1<<3) != 0 {
		if _, err := reader.uint32(); err != nil {
			return nil, err
		}
	}
	if dataFlags&(1<<4) != 0 {
		if _, err := reader.uint32(); err != nil {
			return nil, err
		}
	}
	if dataFlags&(1<<5) != 0 {
		if _, err := reader.int64(); err != nil {
			return nil, err
		}
		if _, err := reader.int64(); err != nil {
			return nil, err
		}
	}
	flags, err := reader.uint32()
	if err != nil {
		return nil, err
	}
	tags, err := reader.uint32()
	if err != nil {
		return nil, err
	}
	if err := readForwardInfo(reader); err != nil {
		return nil, err
	}
	var authorID int64
	var hasAuthorID bool
	hasAuthorFlag, err := reader.int8()
	if err != nil {
		return nil, err
	}
	if hasAuthorFlag == 1 {
		authorID, err = reader.int64()
		if err != nil {
			return nil, err
		}
		hasAuthorID = true
	}
	text, err := reader.string()
	if err != nil {
		return nil, err
	}
	attrCount, err := reader.int32()
	if err != nil {
		return nil, err
	}
	if attrCount < 0 {
		return nil, fmt.Errorf("negative message attribute count")
	}
	for range int(attrCount) {
		if _, err := reader.bytes(); err != nil {
			return nil, err
		}
	}
	embeddedCount, err := reader.int32()
	if err != nil {
		return nil, err
	}
	if embeddedCount < 0 {
		return nil, fmt.Errorf("negative embedded media count")
	}
	embeddedMedia := make([]any, 0, embeddedCount)
	for range int(embeddedCount) {
		raw, err := reader.bytes()
		if err != nil {
			return nil, err
		}
		decoded, err := DecodeObject(raw)
		if err == nil && decoded != nil {
			embeddedMedia = append(embeddedMedia, decoded)
		}
	}
	refCount, err := reader.int32()
	if err != nil {
		return nil, err
	}
	if refCount < 0 {
		return nil, fmt.Errorf("negative referenced media count")
	}
	refs := make([]MediaRef, 0, refCount)
	for range int(refCount) {
		namespace, err := reader.int32()
		if err != nil {
			return nil, err
		}
		id, err := reader.int64()
		if err != nil {
			return nil, err
		}
		refs = append(refs, MediaRef{Namespace: namespace, ID: id})
	}
	return &Message{
		Flags:              flags,
		Tags:               tags,
		AuthorID:           authorID,
		HasAuthorID:        hasAuthorID,
		Text:               text,
		EmbeddedMediaCount: int(embeddedCount),
		EmbeddedMedia:      embeddedMedia,
		ReferencedMediaIDs: refs,
	}, nil
}

func readForwardInfo(reader *byteReader) error {
	flags, err := reader.int8()
	if err != nil {
		return err
	}
	if flags == 0 {
		return nil
	}
	if _, err := reader.int64(); err != nil {
		return err
	}
	if _, err := reader.int32(); err != nil {
		return err
	}
	if flags&(1<<1) != 0 {
		if _, err := reader.int64(); err != nil {
			return err
		}
	}
	if flags&(1<<2) != 0 {
		if _, err := reader.int64(); err != nil {
			return err
		}
		if _, err := reader.int32(); err != nil {
			return err
		}
		if _, err := reader.int32(); err != nil {
			return err
		}
	}
	if flags&(1<<3) != 0 {
		if _, err := reader.string(); err != nil {
			return err
		}
	}
	if flags&(1<<4) != 0 {
		if _, err := reader.string(); err != nil {
			return err
		}
	}
	if flags&(1<<5) != 0 {
		if _, err := reader.int32(); err != nil {
			return err
		}
	}
	return nil
}

func PeerDisplay(peer map[string]any) string {
	first := cleanText(peer["fn"])
	last := cleanText(peer["ln"])
	if first != "" || last != "" {
		return strings.TrimSpace(first + " " + last)
	}
	if title := cleanText(peer["t"]); title != "" {
		return title
	}
	if username := cleanText(peer["un"]); username != "" {
		return "@" + username
	}
	return ""
}

func MediaTypeFor(msg *Message) string {
	for _, tag := range mediaTags {
		if msg.Tags&tag.bit != 0 {
			return tag.label
		}
	}
	if msg.EmbeddedMediaCount > 0 || len(msg.ReferencedMediaIDs) > 0 {
		return "media"
	}
	return ""
}

func MediaTitleFor(msg *Message) string {
	for _, item := range msg.EmbeddedMedia {
		if found := firstNestedString(item, "fn"); found != "" {
			return found
		}
	}
	return ""
}

func firstNestedString(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if found := cleanText(typed[key]); found != "" {
			return found
		}
		for _, k := range orderedObjectKeys(typed) {
			if found := firstNestedString(typed[k], key); found != "" {
				return found
			}
		}
	case []any:
		for _, nested := range typed {
			if found := firstNestedString(nested, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func MediaResourceIDs(value any) []string {
	seen := make(map[string]struct{})
	var ids []string
	var visit func(any)
	visit = func(item any) {
		switch typed := item.(type) {
		case map[string]any:
			resourceType, ok := int64Value(typed["@type"])
			if ok {
				if id := mediaResourceID(resourceType, typed); id != "" {
					if _, exists := seen[id]; !exists {
						seen[id] = struct{}{}
						ids = append(ids, id)
					}
				}
			}
			for _, key := range orderedObjectKeys(typed) {
				visit(typed[key])
			}
		case []any:
			for _, nested := range typed {
				visit(nested)
			}
		}
	}
	visit(value)
	return ids
}

func orderedObjectKeys(item map[string]any) []string {
	if rawOrder, ok := item[objectOrderKey].([]string); ok {
		keys := make([]string, 0, len(rawOrder)+1)
		seen := make(map[string]struct{}, len(item))
		for _, key := range rawOrder {
			if key == objectOrderKey {
				continue
			}
			if _, ok := item[key]; ok {
				keys = append(keys, key)
				seen[key] = struct{}{}
			}
		}
		if _, ok := item["@type"]; ok {
			keys = append(keys, "@type")
			seen["@type"] = struct{}{}
		}
		for key := range item {
			if key == objectOrderKey {
				continue
			}
			if _, ok := seen[key]; !ok {
				keys = append(keys, key)
			}
		}
		return keys
	}
	keys := make([]string, 0, len(item))
	for key := range item {
		if key != objectOrderKey {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func mediaResourceID(resourceType int64, item map[string]any) string {
	switch resourceType {
	case resourceTypeCloudPhotoSize:
		return fmt.Sprintf("telegram-cloud-photo-size-%s-%s-%s", stringValue(item["d"]), stringValue(item["i"]), stringValue(item["s"]))
	case resourceTypeCloudDocumentSize:
		return fmt.Sprintf("telegram-cloud-document-size-%s-%s-%s", stringValue(item["d"]), stringValue(item["i"]), stringValue(item["s"]))
	case resourceTypeCloudDocument:
		return fmt.Sprintf("telegram-cloud-document-%s-%s", stringValue(item["d"]), stringValue(item["f"]))
	case resourceTypeCloudPeerPhoto:
		return fmt.Sprintf("telegram-peer-photo-size-%s-%s-%s-%s", stringValue(item["d"]), stringValue(item["s"]), stringValue(item["v"]), stringValue(item["l"]))
	case resourceTypeLocalFile:
		return fmt.Sprintf("telegram-local-file-%s", stringValue(item["f"]))
	case resourceTypeLocalFileRef:
		return fmt.Sprintf("local-file-%s", stringValue(item["r"]))
	default:
		return ""
	}
}

func CachedMediaFor(msg *Message, mediaRoot string) (string, int64) {
	var candidates []cacheCandidate
	for _, item := range msg.EmbeddedMedia {
		for _, resourceID := range MediaResourceIDs(item) {
			for _, path := range cachedMediaPaths(resourceID, mediaRoot) {
				if info, err := os.Stat(path); err == nil && !info.IsDir() {
					candidates = append(candidates, cacheCandidate{path: path, size: info.Size()})
				}
			}
		}
	}
	return largestCacheCandidate(candidates)
}

func CachedPeerAvatarPath(peer map[string]any, mediaRoot string) string {
	photos, _ := peer["ph"].([]any)
	var candidates []cacheCandidate
	for _, photo := range photos {
		for _, resourceID := range MediaResourceIDs(photo) {
			for _, path := range cachedMediaPaths(resourceID, mediaRoot) {
				if info, err := os.Stat(path); err == nil && !info.IsDir() {
					candidates = append(candidates, cacheCandidate{path: path, size: info.Size()})
				}
			}
		}
	}
	path, _ := largestCacheCandidate(candidates)
	return path
}

type cacheCandidate struct {
	path string
	size int64
}

func largestCacheCandidate(candidates []cacheCandidate) (string, int64) {
	if len(candidates) == 0 {
		return "", 0
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].size == candidates[j].size {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].size > candidates[j].size
	})
	return candidates[0].path, candidates[0].size
}

func cachedMediaPaths(resourceID, mediaRoot string) []string {
	var paths []string
	exact := filepath.Join(mediaRoot, resourceID)
	if isCompleteCacheFile(exact, resourceID) {
		paths = append(paths, exact)
	}
	for _, path := range mediaCacheIndex(mediaRoot)[resourceID] {
		if path != exact {
			paths = append(paths, path)
		}
	}
	return paths
}

func mediaCacheIndex(mediaRoot string) map[string][]string {
	index := make(map[string][]string)
	entries, err := os.ReadDir(mediaRoot)
	if err != nil {
		return index
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.Contains(name, ".") || strings.Contains(name, "_partial") || strings.HasSuffix(name, ".meta") {
			continue
		}
		resourceID := strings.TrimSuffix(name, filepath.Ext(name))
		if resourceID != "" {
			index[resourceID] = append(index[resourceID], filepath.Join(mediaRoot, name))
		}
	}
	return index
}

func isCompleteCacheFile(path, resourceID string) bool {
	name := filepath.Base(path)
	if name != resourceID && !strings.HasPrefix(name, resourceID+".") {
		return false
	}
	if strings.Contains(name, "_partial") || strings.HasSuffix(name, ".meta") {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func PeerStoreID(accountID string, peerID int64, multiAccount bool) string {
	if !multiAccount {
		return strconv.FormatInt(peerID, 10)
	}
	return strconv.FormatInt(stableInt("postbox-account", accountID, peerID), 10)
}

func SourcePK(accountID string, peerID int64, namespace, messageID int32, multiAccount bool) int64 {
	if !multiAccount {
		return stableInt(peerID, namespace, messageID)
	}
	return stableInt("postbox-message", accountID, peerID, namespace, messageID)
}

func stableInt(parts ...any) int64 {
	text := make([]string, len(parts))
	for i, part := range parts {
		text[i] = fmt.Sprint(part)
	}
	sum := sha256.Sum256([]byte(strings.Join(text, ":")))
	return int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
}

type Row map[string]any

func ApplyLimits(messages []Row, dialogsLimit, messagesLimit int) []Row {
	byChat := make(map[string][]Row)
	for _, msg := range messages {
		chatID := stringValue(msg["chat_id"])
		byChat[chatID] = append(byChat[chatID], msg)
	}
	type rankedChat struct {
		chatID string
		rows   []Row
		maxTS  int64
	}
	ranked := make([]rankedChat, 0, len(byChat))
	for chatID, rows := range byChat {
		var maxTS int64
		for _, row := range rows {
			if ts, _ := int64Value(row["_ts"]); ts > maxTS {
				maxTS = ts
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
	var out []Row
	for _, chat := range ranked {
		rows := append([]Row(nil), chat.rows...)
		sortRows(rows)
		if messagesLimit > 0 && messagesLimit < len(rows) {
			rows = rows[len(rows)-messagesLimit:]
		}
		out = append(out, rows...)
	}
	sortRows(out)
	return out
}

func FilterChat(messages []Row, chatID string) []Row {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return messages
	}
	out := make([]Row, 0, len(messages))
	for _, msg := range messages {
		if stringValue(msg["chat_id"]) == chatID || stringValue(msg["_raw_chat_id"]) == chatID {
			out = append(out, msg)
		}
	}
	return out
}

func sortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		leftTS, _ := int64Value(rows[i]["_ts"])
		rightTS, _ := int64Value(rows[j]["_ts"])
		if leftTS == rightTS {
			leftPK, _ := int64Value(rows[i]["source_pk"])
			rightPK, _ := int64Value(rows[j]["source_pk"])
			return leftPK < rightPK
		}
		return leftTS < rightTS
	})
}

func AccountDirRecordID(accountDir string) (int64, error) {
	if !strings.HasPrefix(accountDir, "account-") {
		return 0, fmt.Errorf("invalid account directory: %s", accountDir)
	}
	unsigned, err := strconv.ParseUint(strings.TrimPrefix(accountDir, "account-"), 10, 64)
	if err != nil {
		return 0, err
	}
	return int64(unsigned), nil
}

func PostboxPeerToTelegramID(peerID int64) (int64, bool) {
	namespace, rawID, ok := PostboxPeerParts(peerID)
	if !ok {
		return 0, false
	}
	switch namespace {
	case 0:
		return rawID, true
	case 1:
		return -rawID, true
	case 2:
		return -1_000_000_000_000 - rawID, true
	default:
		return 0, false
	}
}

func PostboxPeerParts(peerID int64) (namespace int64, rawID int64, ok bool) {
	data := uint64(peerID)
	legacyNamespaceBits := (data >> 32) & 0xffffffff
	idLowBits := data & 0xffffffff
	if legacyNamespaceBits == 0x7fffffff && idLowBits == 0 {
		return 0, 0, false
	}
	namespace = int64((data >> 32) & 0x7)
	idHighBits := ((data >> 35) & 0xffffffff) << 32
	if idHighBits == 0 && namespace == 3 {
		return namespace, int64(int32(idLowBits)), true
	}
	return namespace, int64(idHighBits | idLowBits), true
}

func cleanText(value any) string {
	return strings.TrimSpace(stringValue(value))
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func int64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint32:
		return int64(typed), true
	case uint64:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case string:
		value, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return value, err == nil
	case json.Number:
		value, err := typed.Int64()
		return value, err == nil
	default:
		return 0, false
	}
}
