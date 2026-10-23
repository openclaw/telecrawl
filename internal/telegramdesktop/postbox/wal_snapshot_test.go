package postbox

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func walFixtureKey() []byte {
	key := make([]byte, sqlcipherKeySize+sqlcipherSaltSize)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sqlcipher_v4_wal", name+".b64"))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// bumpModTime makes a rewrite visible even on file systems with coarse
// modification-time resolution.
func bumpModTime(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

// rechecksumWAL recomputes the header checksum and the cumulative frame
// checksums of a little-endian-checksum WAL after a test edits it.
func rechecksumWAL(wal []byte, pageSize int) {
	s1, s2 := sqliteWALChecksum(false, wal[:24], 0, 0)
	binary.BigEndian.PutUint32(wal[24:28], s1)
	binary.BigEndian.PutUint32(wal[28:32], s2)
	frameSize := sqliteWALFrameHeaderSize + pageSize
	for off := sqliteWALHeaderSize; off+frameSize <= len(wal); off += frameSize {
		header := wal[off : off+sqliteWALFrameHeaderSize]
		s1, s2 = sqliteWALChecksum(false, header[:8], s1, s2)
		s1, s2 = sqliteWALChecksum(false, wal[off+sqliteWALFrameHeaderSize:off+frameSize], s1, s2)
		binary.BigEndian.PutUint32(header[16:20], s1)
		binary.BigEndian.PutUint32(header[20:24], s2)
	}
}

// resetWAL mimics SQLite restarting the log after a full checkpoint: the
// checkpoint sequence and salt-1 are incremented, salt-2 changes, and the old
// frames stay in the file but no longer match the header salts.
func resetWAL(wal []byte) []byte {
	out := append([]byte(nil), wal...)
	binary.BigEndian.PutUint32(out[12:16], binary.BigEndian.Uint32(out[12:16])+1)
	binary.BigEndian.PutUint32(out[16:20], binary.BigEndian.Uint32(out[16:20])+1)
	binary.BigEndian.PutUint32(out[20:24], binary.BigEndian.Uint32(out[20:24])^0x5a5a5a5a)
	s1, s2 := sqliteWALChecksum(false, out[:24], 0, 0)
	binary.BigEndian.PutUint32(out[24:28], s1)
	binary.BigEndian.PutUint32(out[28:32], s2)
	return out
}

// encryptSQLCipherV4 is the inverse of DecryptSQLCipherV4 for test images.
func encryptSQLCipherV4(t *testing.T, plain, keyAndSalt []byte) []byte {
	t.Helper()
	pageSize, err := sqlcipherPageSize(plain)
	if err != nil {
		t.Fatal(err)
	}
	encKey := keyAndSalt[:sqlcipherKeySize]
	hmacKey := sqlcipherHMACKey(encKey, keyAndSalt[sqlcipherKeySize:])
	block, err := aes.NewCipher(encKey)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(plain))
	for i := 0; i*pageSize < len(plain); i++ {
		pageNo := uint32(i + 1)
		src := plain[i*pageSize : (i+1)*pageSize]
		dst := out[i*pageSize : (i+1)*pageSize]
		offset := 0
		if pageNo == 1 {
			offset = sqlcipherPlainHeader
			copy(dst[:offset], src[:offset])
		}
		src, dst = src[offset:], dst[offset:]
		payload := len(src) - sqlcipherReserveSize
		iv := dst[payload : payload+sqlcipherIVSize]
		for j := range iv {
			iv[j] = byte(int(pageNo)*31 + j)
		}
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(dst[:payload], src[:payload])
		copy(dst[payload+sqlcipherIVSize:], sqlcipherPageHMAC(hmacKey, pageNo, dst[:payload+sqlcipherIVSize]))
	}
	return out
}

// newerCheckpointedMain builds the main file Telegram would leave after it
// committed T2 on top of the fixture WAL transaction T1 (inserting row x'03'
// with a value large enough to need overflow pages, so T2 touches more pages
// than T1) and then checkpointed both transactions.
func newerCheckpointedMain(t *testing.T, mainData, wal, keyAndSalt []byte) []byte {
	t.Helper()
	decrypted, err := DecryptSQLCipherV4(mainData, keyAndSalt)
	if err != nil {
		t.Fatal(err)
	}
	image, err := ApplySQLCipherV4WAL(decrypted, wal, keyAndSalt)
	if err != nil {
		t.Fatal(err)
	}
	image = append([]byte(nil), image...)
	disableSQLiteWALHeader(image)
	db := deserializeTestDB(t, image)
	if _, err := db.Exec("INSERT INTO t7 VALUES (x'03', zeroblob(6000))"); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var newer []byte
	if err := conn.Raw(func(driverConn any) error {
		serializer, ok := driverConn.(interface{ Serialize() ([]byte, error) })
		if !ok {
			return errors.New("sqlite driver does not support serialize")
		}
		newer, err = serializer.Serialize()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	newer[18], newer[19] = 2, 2 // the database is still in WAL mode
	return encryptSQLCipherV4(t, newer, keyAndSalt)
}

func deserializeTestDB(t *testing.T, image []byte) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:wal-snapshot-test-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Raw(func(driverConn any) error {
		return driverConn.(interface{ Deserialize([]byte) error }).Deserialize(append([]byte(nil), image...))
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

func queryKeysAndIntegrity(db *sql.DB) ([]string, string, error) {
	var keys []string
	if err := queryStrings(db, "SELECT hex(key) FROM t7 ORDER BY key", &keys); err != nil {
		return keys, "", err
	}
	var messages []string
	if err := queryStrings(db, "PRAGMA integrity_check", &messages); err != nil {
		return keys, "", err
	}
	return keys, strings.Join(messages, "; "), nil
}

func queryStrings(db *sql.DB, query string, out *[]string) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return err
		}
		*out = append(*out, value)
	}
	return rows.Err()
}

func openWithHook(t *testing.T, dbPath string, hook func(attempt int, stage string)) ([]string, string, error) {
	t.Helper()
	db, cleanup, err := openDecryptedDB(context.Background(), dbPath, walFixtureKey(), hook)
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	defer func() { _ = db.Close() }()
	keys, integrity, err := queryKeysAndIntegrity(db)
	if err != nil {
		t.Fatal(err)
	}
	return keys, integrity, nil
}

// The reviewer's race: the WAL is captured with T1, then Telegram commits T2,
// checkpoints T1+T2 into the main file and resets the WAL before the main file
// is read. Replaying the stale T1 frames over that main file would yield a
// state that never committed; the capture must be retried instead.
func TestOpenDecryptedDBRetriesWhenCheckpointRacesSnapshot(t *testing.T) {
	key := walFixtureKey()
	mainData, wal := readFixture(t, "db_sqlite"), readFixture(t, "db_sqlite-wal")
	newerMain := newerCheckpointedMain(t, mainData, wal, key)

	// Without snapshot checks the stale WAL overlays the newer main image.
	decryptedNewer, err := DecryptSQLCipherV4(newerMain, key)
	if err != nil {
		t.Fatal(err)
	}
	mixed, err := ApplySQLCipherV4WAL(decryptedNewer, wal, key)
	if err != nil {
		t.Fatal(err)
	}
	mixed = append([]byte(nil), mixed...)
	disableSQLiteWALHeader(mixed)
	keys, integrity, err := queryKeysAndIntegrity(deserializeTestDB(t, mixed))
	if err == nil && slices.Equal(keys, []string{"01", "02", "03"}) && integrity == "ok" {
		t.Fatalf("stale WAL over newer main unexpectedly consistent: keys=%v", keys)
	}
	t.Logf("stale WAL over newer main: keys=%v integrity=%q err=%v", keys, integrity, err)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db_sqlite")
	writeFile(t, dbPath, mainData)
	writeFile(t, dbPath+"-wal", wal)
	var attempts []int
	keys, integrity, err = openWithHook(t, dbPath, func(attempt int, stage string) {
		if stage != "wal" {
			return
		}
		attempts = append(attempts, attempt)
		if attempt == 1 {
			writeFile(t, dbPath, newerMain)
			writeFile(t, dbPath+"-wal", resetWAL(wal))
			bumpModTime(t, dbPath)
			bumpModTime(t, dbPath+"-wal")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(attempts, []int{1, 2}) {
		t.Fatalf("attempts = %v, want [1 2]", attempts)
	}
	if want := []string{"01", "02", "03"}; !slices.Equal(keys, want) || integrity != "ok" {
		t.Fatalf("keys = %v integrity = %q, want %v ok", keys, integrity, want)
	}
}

func TestOpenDecryptedDBRetriesWhenWALGrowsDuringSnapshot(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db_sqlite")
	writeFile(t, dbPath, readFixture(t, "db_sqlite"))
	writeFile(t, dbPath+"-wal", readFixture(t, "db_sqlite-wal"))
	var attempts int
	keys, _, err := openWithHook(t, dbPath, func(attempt int, stage string) {
		if stage != "main" {
			return
		}
		attempts = attempt
		if attempt == 1 {
			// A writer starts appending a frame after the WAL was read.
			f, err := os.OpenFile(dbPath+"-wal", os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.Write(make([]byte, sqliteWALFrameHeaderSize))
			_ = f.Close()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if want := []string{"01", "02"}; !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

func TestOpenDecryptedDBFailsWhenSnapshotNeverStabilizes(t *testing.T) {
	old := sqliteSnapshotRetryDelay
	sqliteSnapshotRetryDelay = time.Millisecond
	t.Cleanup(func() { sqliteSnapshotRetryDelay = old })
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db_sqlite")
	wal := readFixture(t, "db_sqlite-wal")
	writeFile(t, dbPath, readFixture(t, "db_sqlite"))
	writeFile(t, dbPath+"-wal", wal)
	var attempts int
	_, _, err := openWithHook(t, dbPath, func(attempt int, stage string) {
		if stage != "wal" {
			return
		}
		attempts = attempt
		wal = resetWAL(wal)
		writeFile(t, dbPath+"-wal", wal)
	})
	if !errors.Is(err, errSQLiteSnapshotUnstable) {
		t.Fatalf("err = %v, want errSQLiteSnapshotUnstable", err)
	}
	if attempts != sqliteSnapshotAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, sqliteSnapshotAttempts)
	}
}

// After a WAL reset the old frames remain in the file with the previous salts;
// they must not be applied.
func TestOpenDecryptedDBIgnoresFramesFromBeforeWALReset(t *testing.T) {
	got := walFixtureKeys(t, copyWALFixture(t, resetWAL))
	if want := []string{"01"}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func TestApplySQLCipherV4WALRejectsImpossibleCommitPageCount(t *testing.T) {
	key := walFixtureKey()
	decrypted, err := DecryptSQLCipherV4(readFixture(t, "db_sqlite"), key)
	if err != nil {
		t.Fatal(err)
	}
	for _, dbPages := range []uint32{0xffffffff, 7} {
		wal := readFixture(t, "db_sqlite-wal")
		commit := sqliteWALHeaderSize + sqliteWALFrameHeaderSize + 4096
		binary.BigEndian.PutUint32(wal[commit+4:commit+8], dbPages)
		rechecksumWAL(wal, 4096)
		out, err := ApplySQLCipherV4WAL(decrypted, wal, key)
		if err == nil || !strings.Contains(err.Error(), "claims") {
			t.Fatalf("dbPages=%#x: len(out)=%d err=%v, want impossible page count error", dbPages, len(out), err)
		}
	}
	// Recomputing checksums leaves an unmodified WAL byte-identical, so the
	// rejected inputs above differ only in the commit-page count.
	wal := readFixture(t, "db_sqlite-wal")
	rechecksumWAL(wal, 4096)
	if !bytes.Equal(wal, readFixture(t, "db_sqlite-wal")) {
		t.Fatal("rechecksumWAL changed an unmodified WAL")
	}
}
