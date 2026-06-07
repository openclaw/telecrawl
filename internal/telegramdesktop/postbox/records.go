package postbox

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Records struct {
	Peers    map[string]string
	Contacts []Contact
	Messages []MessageRecord
}

type ReadOptions struct {
	DialogsLimit  int
	MessagesLimit int
	ChatID        string
}

type Contact struct {
	ID           string
	PeerType     string
	Phone        string
	FullName     string
	FirstName    string
	LastName     string
	BusinessName string
	Username     string
	LID          string
	AboutText    string
	AvatarPath   string
	UpdatedAt    string
}

type MessageRecord struct {
	AccountID          string
	AccessHash         int64
	RawChatID          int64
	TS                 int64
	SourcePK           int64
	ChatID             string
	ChatName           string
	MessageID          string
	SenderID           string
	SenderName         string
	Timestamp          string
	FromMe             bool
	Text               string
	MessageType        string
	MediaType          string
	MediaTitle         string
	MediaPath          string
	MediaSize          int64
	MetadataType       string
	MetadataTitle      string
	MetadataURL        string
	MetadataJSON       string
	EmbeddedMedia      []any
	ReferencedMediaIDs []MediaRef
}

type PeerRecord struct {
	Display    string
	AccessHash int64
	FirstName  string
	LastName   string
	Title      string
	Username   string
	Phone      string
	AvatarPath string
}

func ReadSourceRecords(ctx context.Context, source Source, keyAndSalt []byte, multiAccount bool) (Records, error) {
	return ReadSourceRecordsWithOptions(ctx, source, keyAndSalt, multiAccount, ReadOptions{})
}

func ReadSourceRecordsWithOptions(ctx context.Context, source Source, keyAndSalt []byte, multiAccount bool, opts ReadOptions) (Records, error) {
	db, cleanup, err := OpenDecryptedDB(ctx, source.DBPath, keyAndSalt)
	if err != nil {
		return Records{}, err
	}
	defer cleanup()
	defer func() { _ = db.Close() }()
	return ReadSourceRecordsDBWithOptions(ctx, source, db, multiAccount, opts)
}

func OpenDecryptedDB(ctx context.Context, dbPath string, keyAndSalt []byte) (*sql.DB, func(), error) {
	encrypted, err := os.ReadFile(dbPath)
	if err != nil {
		return nil, func() {}, err
	}
	plain, err := DecryptSQLCipherV4(encrypted, keyAndSalt)
	if err != nil {
		return nil, func() {}, err
	}
	defer zeroBytes(plain)
	disableSQLiteWALHeader(plain)
	db, err := sql.Open("sqlite", fmt.Sprintf("file:telecrawl-postbox-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		return nil, func() {}, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, func() {}, err
	}
	err = conn.Raw(func(driverConn any) error {
		deserializer, ok := driverConn.(interface{ Deserialize([]byte) error })
		if !ok {
			return errors.New("sqlite driver does not support in-memory deserialize")
		}
		return deserializer.Deserialize(plain)
	})
	if closeErr := conn.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = db.Close()
		return nil, func() {}, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, func() {}, err
	}
	return db, func() {}, nil
}

func zeroBytes(data []byte) {
	for i := range data {
		data[i] = 0
	}
}

func disableSQLiteWALHeader(data []byte) {
	if len(data) < 20 || string(data[:len(sqliteHeader)]) != sqliteHeader {
		return
	}
	if data[18] == 2 {
		data[18] = 1
	}
	if data[19] == 2 {
		data[19] = 1
	}
}

func ReadSourceRecordsDB(ctx context.Context, source Source, db *sql.DB, multiAccount bool) (Records, error) {
	return ReadSourceRecordsDBWithOptions(ctx, source, db, multiAccount, ReadOptions{})
}

func ReadSourceRecordsDBWithOptions(ctx context.Context, source Source, db *sql.DB, multiAccount bool, opts ReadOptions) (Records, error) {
	mediaRoot := filepath.Join(filepath.Dir(filepath.Dir(source.DBPath)), "media")
	rawPeerRecords, err := LoadPeerRecords(ctx, db, mediaRoot)
	if err != nil {
		return Records{}, err
	}
	rawPeers := make(map[int64]string, len(rawPeerRecords))
	peers := make(map[string]string, len(rawPeerRecords))
	contacts := make([]Contact, 0, len(rawPeerRecords))
	for peerID, peer := range rawPeerRecords {
		rawPeers[peerID] = peer.Display
		storeID := PeerStoreID(source.AccountID, peerID, multiAccount)
		peers[storeID] = peer.Display
		if contact, ok := contactForPeer(source.AccountID, peerID, peer, multiAccount); ok {
			contacts = append(contacts, contact)
		}
	}
	sort.Slice(contacts, func(i, j int) bool {
		return contacts[i].ID < contacts[j].ID
	})

	messages, err := LoadMessageRecords(ctx, db, source, rawPeerRecords, rawPeers, mediaRoot, multiAccount, opts)
	if err != nil {
		return Records{}, err
	}
	return Records{Peers: peers, Contacts: contacts, Messages: messages}, nil
}

func LoadMessageRecords(ctx context.Context, db *sql.DB, source Source, rawPeerRecords map[int64]PeerRecord, rawPeers map[int64]string, mediaRoot string, multiAccount bool, opts ReadOptions) ([]MessageRecord, error) {
	if limitedReadOptions(opts) {
		keys, err := selectMessageKeys(ctx, db, source, multiAccount, opts)
		if err != nil {
			return nil, err
		}
		return loadSelectedMessageRecords(ctx, db, source, rawPeerRecords, rawPeers, mediaRoot, multiAccount, keys)
	}
	rows, err := db.QueryContext(ctx, "SELECT key, value FROM t7 ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var messages []MessageRecord
	for rows.Next() {
		var keyBlob []byte
		var value []byte
		if err := rows.Scan(&keyBlob, &value); err != nil {
			return nil, err
		}
		record, ok := decodeMessageRecord(source, rawPeerRecords, rawPeers, mediaRoot, multiAccount, keyBlob, value)
		if ok {
			messages = append(messages, record)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func loadSelectedMessageRecords(ctx context.Context, db *sql.DB, source Source, rawPeerRecords map[int64]PeerRecord, rawPeers map[int64]string, mediaRoot string, multiAccount bool, keys []messageKey) ([]MessageRecord, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	stmt, err := db.PrepareContext(ctx, "SELECT value FROM t7 WHERE key = ?")
	if err != nil {
		return nil, err
	}
	defer func() { _ = stmt.Close() }()
	messages := make([]MessageRecord, 0, len(keys))
	for _, key := range keys {
		var value []byte
		if err := stmt.QueryRowContext(ctx, key.raw).Scan(&value); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return nil, err
		}
		record, ok := decodeMessageRecord(source, rawPeerRecords, rawPeers, mediaRoot, multiAccount, key.raw, value)
		if ok {
			messages = append(messages, record)
		}
	}
	return messages, nil
}

func decodeMessageRecord(source Source, rawPeerRecords map[int64]PeerRecord, rawPeers map[int64]string, mediaRoot string, multiAccount bool, keyBlob []byte, value []byte) (MessageRecord, bool) {
	key, ok := parseMessageKey(source, multiAccount, keyBlob)
	if !ok {
		return MessageRecord{}, false
	}
	msg, err := ReadMessage(value)
	if err != nil || msg == nil {
		return MessageRecord{}, false
	}
	chatID := PeerStoreID(source.AccountID, key.peerID, multiAccount)
	chatName := rawPeers[key.peerID]
	incoming := msg.Flags&incomingFlag != 0
	mediaType := MediaTypeFor(msg)
	mediaPath, mediaSize := CachedMediaFor(msg, mediaRoot)
	var senderID, senderName string
	if msg.HasAuthorID && msg.AuthorID != 0 {
		senderID = PeerStoreID(source.AccountID, msg.AuthorID, multiAccount)
		senderName = rawPeers[msg.AuthorID]
	} else if incoming {
		senderID = chatID
		senderName = chatName
	}
	return MessageRecord{
		AccountID:          source.AccountID,
		AccessHash:         rawPeerRecords[key.peerID].AccessHash,
		RawChatID:          key.peerID,
		TS:                 key.timestamp,
		SourcePK:           key.sourcePK,
		ChatID:             chatID,
		ChatName:           chatName,
		MessageID:          fmt.Sprintf("%d:%d", key.namespace, key.messageID),
		SenderID:           senderID,
		SenderName:         senderName,
		Timestamp:          iso(key.timestamp),
		FromMe:             !incoming,
		Text:               msg.Text,
		MessageType:        "message",
		MediaType:          mediaType,
		MediaTitle:         MediaTitleFor(msg),
		MediaPath:          mediaPath,
		MediaSize:          mediaSize,
		EmbeddedMedia:      msg.EmbeddedMedia,
		ReferencedMediaIDs: msg.ReferencedMediaIDs,
	}, true
}

type messageKey struct {
	raw       []byte
	peerID    int64
	namespace int32
	timestamp int64
	messageID int32
	sourcePK  int64
}

func limitedReadOptions(opts ReadOptions) bool {
	return opts.DialogsLimit > 0 || opts.MessagesLimit > 0 || strings.TrimSpace(opts.ChatID) != ""
}

func selectMessageKeys(ctx context.Context, db *sql.DB, source Source, multiAccount bool, opts ReadOptions) ([]messageKey, error) {
	rows, err := db.QueryContext(ctx, "SELECT key FROM t7 ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	byPeer := make(map[int64][]messageKey)
	for rows.Next() {
		var keyBlob []byte
		if err := rows.Scan(&keyBlob); err != nil {
			return nil, err
		}
		key, ok := parseMessageKey(source, multiAccount, keyBlob)
		if !ok || !messageKeyMatchesChat(source, multiAccount, key, opts.ChatID) {
			continue
		}
		byPeer[key.peerID] = append(byPeer[key.peerID], key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	type rankedPeer struct {
		peerID  int64
		rows    []messageKey
		maxTS   int64
		storeID string
	}
	ranked := make([]rankedPeer, 0, len(byPeer))
	for peerID, rows := range byPeer {
		var maxTS int64
		for _, row := range rows {
			if row.timestamp > maxTS {
				maxTS = row.timestamp
			}
		}
		ranked = append(ranked, rankedPeer{
			peerID:  peerID,
			rows:    rows,
			maxTS:   maxTS,
			storeID: PeerStoreID(source.AccountID, peerID, multiAccount),
		})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].maxTS == ranked[j].maxTS {
			return ranked[i].storeID < ranked[j].storeID
		}
		return ranked[i].maxTS > ranked[j].maxTS
	})
	if opts.DialogsLimit > 0 && opts.DialogsLimit < len(ranked) {
		ranked = ranked[:opts.DialogsLimit]
	}
	var selected []messageKey
	for _, peer := range ranked {
		rows := append([]messageKey(nil), peer.rows...)
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].timestamp == rows[j].timestamp {
				return rows[i].sourcePK < rows[j].sourcePK
			}
			return rows[i].timestamp < rows[j].timestamp
		})
		if opts.MessagesLimit > 0 && opts.MessagesLimit < len(rows) {
			rows = rows[len(rows)-opts.MessagesLimit:]
		}
		selected = append(selected, rows...)
	}
	sort.Slice(selected, func(i, j int) bool {
		return bytes.Compare(selected[i].raw, selected[j].raw) < 0
	})
	return selected, nil
}

func parseMessageKey(source Source, multiAccount bool, keyBlob []byte) (messageKey, bool) {
	if len(keyBlob) < 20 {
		return messageKey{}, false
	}
	raw := append([]byte(nil), keyBlob...)
	peerID := int64(binary.BigEndian.Uint64(raw[:8]))
	namespace := int32(binary.BigEndian.Uint32(raw[8:12]))
	timestamp := int64(int32(binary.BigEndian.Uint32(raw[12:16])))
	messageID := int32(binary.BigEndian.Uint32(raw[16:20]))
	return messageKey{
		raw:       raw,
		peerID:    peerID,
		namespace: namespace,
		timestamp: timestamp,
		messageID: messageID,
		sourcePK:  SourcePK(source.AccountID, peerID, namespace, messageID, multiAccount),
	}, true
}

func messageKeyMatchesChat(source Source, multiAccount bool, key messageKey, chatID string) bool {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return true
	}
	return chatID == PeerStoreID(source.AccountID, key.peerID, multiAccount) ||
		chatID == strconv.FormatInt(key.peerID, 10)
}

func LoadPeerRecords(ctx context.Context, db *sql.DB, mediaRoot string) (map[int64]PeerRecord, error) {
	rows, err := db.QueryContext(ctx, "SELECT key, value FROM t2")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	peers := make(map[int64]PeerRecord)
	for rows.Next() {
		var key int64
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		peer, err := DecodeObject(value)
		if err != nil || peer == nil {
			continue
		}
		record := PeerRecord{
			Display:    PeerDisplay(peer),
			AccessHash: peerAccessHash(peer),
			FirstName:  cleanText(peer["fn"]),
			LastName:   cleanText(peer["ln"]),
			Title:      cleanText(peer["t"]),
			Username:   cleanText(peer["un"]),
			Phone:      cleanText(peer["p"]),
			AvatarPath: CachedPeerAvatarPath(peer, mediaRoot),
		}
		if record.Display != "" || record.AccessHash != 0 || record.FirstName != "" || record.LastName != "" || record.Title != "" || record.Username != "" || record.Phone != "" || record.AvatarPath != "" {
			peers[key] = record
		}
	}
	return peers, rows.Err()
}

func contactForPeer(accountID string, peerID int64, peer PeerRecord, multiAccount bool) (Contact, bool) {
	peerType := PeerTypeForID(peerID)
	fullName := cleanText(firstNonEmpty(peer.Display, peer.Title))
	if fullName == "" && peer.FirstName == "" && peer.LastName == "" && peer.Username == "" && peer.Phone == "" && peer.AvatarPath == "" && peerType == "unknown" {
		return Contact{}, false
	}
	return Contact{
		ID:         PeerStoreID(accountID, peerID, multiAccount),
		PeerType:   peerType,
		Phone:      peer.Phone,
		FullName:   fullName,
		FirstName:  peer.FirstName,
		LastName:   peer.LastName,
		Username:   peer.Username,
		AvatarPath: peer.AvatarPath,
	}, true
}

func PeerTypeForID(peerID int64) string {
	namespace, _, ok := PostboxPeerParts(peerID)
	if !ok {
		return "unknown"
	}
	switch namespace {
	case 0:
		return "user"
	case 1:
		return "group"
	case 2:
		return "channel"
	case 3:
		return "secret_chat"
	default:
		return "unknown"
	}
}

func peerAccessHash(peer map[string]any) int64 {
	value, _ := int64Value(peer["ah"])
	return value
}

func iso(ts int64) string {
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = cleanText(value); value != "" {
			return value
		}
	}
	return ""
}
