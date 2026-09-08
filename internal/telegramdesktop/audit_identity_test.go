package telegramdesktop

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openclaw/telecrawl/internal/store"
	postboxpkg "github.com/openclaw/telecrawl/internal/telegramdesktop/postbox"
	"golang.org/x/crypto/pbkdf2"
)

func TestAuditPostboxRequiresVerifiedAccountBeforeWrites(t *testing.T) {
	ctx := context.Background()
	root, lane, account := makePostboxFixture(t)
	archiveDir := t.TempDir()
	dbPath := filepath.Join(archiveDir, "archive.db")
	mediaDir := filepath.Join(archiveDir, "media")
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(mediaDir, "preserved")
	if err := os.WriteFile(sentinel, []byte("prior media"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	verified, err := Import(ctx, ImportOptions{Path: root}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MergeAll(ctx, verified.Stats, verified.Contacts, verified.Chats, verified.Folders, verified.FolderChats, verified.Topics, verified.Messages); err != nil {
		t.Fatal(err)
	}
	before, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Sequential directories deliberately reuse the same lane key.
	next := filepath.Join(lane, "account-456")
	if err := os.Rename(account, next); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(next, "postbox", "db", "db_sqlite")
	for _, tc := range []struct {
		name  string
		state []byte
	}{
		{"missing", nil},
		{"malformed", []byte{0}},
		{"zero", auditAccountState(0)},
		{"negative", auditAccountState(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auditWriteAccountState(t, db, tc.state)
			sourceBefore, err := os.ReadFile(db)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateImportIdentity(ctx, root); err == nil {
				t.Fatal("CLI identity preflight accepted an unverified source")
			}
			result, err := Import(ctx, ImportOptions{Path: root}, dbPath)
			if err == nil || !strings.Contains(err.Error(), "verified account peer identity") {
				t.Fatalf("unverified source accepted: %+v %v", result.Stats, err)
			}
			after, err := st.ExportAll(ctx)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("archive changed: %+v %v", after, err)
			}
			sourceAfter, err := os.ReadFile(db)
			if err != nil || !bytes.Equal(sourceAfter, sourceBefore) {
				t.Fatal("source was modified")
			}
			entries, err := os.ReadDir(mediaDir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "preserved" {
				t.Fatalf("media changed: %v %v", entries, err)
			}
		})
	}
}

func TestAuditPostboxHistoricalKeyBindingRemainsUnchanged(t *testing.T) {
	ctx := context.Background()
	root, _, _ := makePostboxFixture(t)
	dbPath := filepath.Join(t.TempDir(), "archive.db")
	result, err := Import(ctx, ImportOptions{Path: root}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	key := make([]byte, 48)
	for i := range key {
		key[i] = byte(i)
	}
	old := result.Stats
	old.SourceIdentity = sourceIdentity("postbox", fmt.Sprintf("database-key:%x", sha256.Sum256(key)))
	if err := st.MergeAll(ctx, old, result.Contacts, result.Chats, result.Folders, result.FolderChats, result.Topics, result.Messages); err != nil {
		t.Fatal(err)
	}
	before, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result.Stats.AdoptSource = true
	if err := st.MergeAll(ctx, result.Stats, result.Contacts, result.Chats, result.Folders, result.FolderChats, result.Topics, result.Messages); err == nil {
		t.Fatal("silently rebound historical key-derived identity")
	}
	after, err := st.ExportAll(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("historical archive changed")
	}
}

func TestAuditPostboxVerifiedMultiAccountIdentity(t *testing.T) {
	root, lane, account := makePostboxFixture(t)
	other := filepath.Join(lane, "account-456", "postbox", "db", "db_sqlite")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(other, filepath.Join(account, "postbox", "db", "db_sqlite")); err != nil {
		t.Fatal(err)
	}
	auditWriteAccountState(t, other, auditAccountState(8484))
	result, err := Import(context.Background(), ImportOptions{Path: root}, filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.SourceIdentity != sourceIdentity("postbox", "peer:4242", "peer:8484") || len(result.Messages) != 2 {
		t.Fatalf("verified multi-account identity changed: %+v", result.Stats)
	}
	if result.Messages[0].SourcePK == result.Messages[1].SourcePK || result.Messages[0].ChatJID == result.Messages[1].ChatJID {
		t.Fatal("multi-account namespaces collided")
	}
}

func TestAuditPostboxVerifiedIdentitySurvivesMoveAndRejectsAccountSwitch(t *testing.T) {
	ctx := context.Background()
	root, lane, account := makePostboxFixture(t)
	dbPath := filepath.Join(t.TempDir(), "archive.db")
	first, err := Import(ctx, ImportOptions{Path: root}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.Stats.SourceIdentity != sourceIdentity("postbox", "peer:4242") {
		t.Fatalf("verified identity algorithm changed: %q", first.Stats.SourceIdentity)
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.MergeAll(ctx, first.Stats, first.Contacts, first.Chats, first.Folders, first.FolderChats, first.Topics, first.Messages); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "moved-source")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	again, err := Import(ctx, ImportOptions{Path: moved}, dbPath)
	if err != nil || again.Stats.SourceIdentity != first.Stats.SourceIdentity {
		t.Fatalf("moved verified source: %+v %v", again.Stats, err)
	}
	if err := st.MergeAll(ctx, again.Stats, again.Contacts, again.Chats, again.Folders, again.FolderChats, again.Topics, again.Messages); err != nil {
		t.Fatal(err)
	}
	lane = filepath.Join(moved, filepath.Base(lane))
	account = filepath.Join(lane, filepath.Base(account))
	next := filepath.Join(lane, "account-456")
	if err := os.Rename(account, next); err != nil {
		t.Fatal(err)
	}
	auditWriteAccountState(t, filepath.Join(next, "postbox", "db", "db_sqlite"), auditAccountState(8484))
	other, err := Import(ctx, ImportOptions{Path: moved}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if other.Stats.SourceIdentity == first.Stats.SourceIdentity {
		t.Fatal("shared lane key merged distinct verified identities")
	}
	if err := st.MergeAll(ctx, other.Stats, other.Contacts, other.Chats, other.Folders, other.FolderChats, other.Topics, other.Messages); err == nil {
		t.Fatal("accepted a different verified account into the prior archive")
	}
}

func auditAccountState(peer int64) []byte {
	inner := append([]byte{6}, []byte("peerId")...)
	inner = append(inner, 1)
	inner = binary.LittleEndian.AppendUint64(inner, uint64(peer))
	state := []byte{1, '_', 5}
	state = binary.LittleEndian.AppendUint32(state, 1)
	state = binary.LittleEndian.AppendUint32(state, uint32(len(inner)))
	return append(state, inner...)
}

// Modify only owned synthetic fixtures through the actual SQLite driver, then
// re-encrypt using the existing fixture's SQLCipher page layout. No WAL proof.
func auditWriteAccountState(t *testing.T, path string, state []byte) {
	t.Helper()
	key := make([]byte, 48)
	for i := range key {
		key[i] = byte(i)
	}
	db, cleanup, err := postboxpkg.OpenDecryptedDB(context.Background(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS t0(key INTEGER PRIMARY KEY, value BLOB NOT NULL); DELETE FROM t0 WHERE key=2`); err != nil {
		t.Fatal(err)
	}
	if state != nil {
		if _, err := db.Exec(`INSERT INTO t0 VALUES(2,?)`, state); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var plain []byte
	err = conn.Raw(func(c any) error {
		var err error
		plain, err = c.(interface{ Serialize() ([]byte, error) }).Serialize()
		return err
	})
	if closeErr := conn.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	pageSize := int(binary.BigEndian.Uint16(plain[16:18]))
	if pageSize != 4096 || plain[20] != 80 || len(plain)%pageSize != 0 {
		t.Fatal("unexpected synthetic fixture page layout")
	}
	salt := append([]byte(nil), key[32:]...)
	for i := range salt {
		salt[i] ^= 0x3a
	}
	hmacKey := pbkdf2.Key(key[:32], salt, 2, 32, sha512.New)
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < len(plain); offset += pageSize {
		page := plain[offset : offset+pageSize]
		start := 0
		if offset == 0 {
			start = 32
		}
		payloadEnd := pageSize - 80
		iv := sha256.Sum256([]byte(fmt.Sprintf("synthetic fixture page %d", offset/pageSize+1)))
		copy(page[payloadEnd:], iv[:16])
		cipher.NewCBCEncrypter(block, iv[:16]).CryptBlocks(page[start:payloadEnd], page[start:payloadEnd])
		mac := hmac.New(sha512.New, hmacKey)
		_, _ = mac.Write(page[start : payloadEnd+16])
		_, _ = mac.Write(binary.LittleEndian.AppendUint32(nil, uint32(offset/pageSize+1)))
		copy(page[payloadEnd+16:], mac.Sum(nil))
	}
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		t.Fatal(err)
	}
}
