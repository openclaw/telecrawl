package postbox

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testdata/sqlcipher_v4_wal was written by SQLCipher 4.12.0 with the raw
// key+salt bytes 0..47, cipher_plaintext_header_size=32 and journal_mode=WAL
// (the Postbox layout). Row x'01' was checkpointed into db_sqlite; row x'02'
// was committed afterwards and exists only in db_sqlite-wal, as when Telegram
// is running and has not checkpointed yet.
func copyWALFixture(t *testing.T, wal func([]byte) []byte) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"db_sqlite", "db_sqlite-wal"} {
		data := readFixture(t, name)
		if name == "db_sqlite-wal" {
			if wal == nil {
				continue
			}
			data = wal(data)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "db_sqlite")
}

func walFixtureKeys(t *testing.T, dbPath string) []string {
	t.Helper()
	keyAndSalt := make([]byte, 48)
	for i := range keyAndSalt {
		keyAndSalt[i] = byte(i)
	}
	db, cleanup, err := OpenDecryptedDB(context.Background(), dbPath, keyAndSalt)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT hex(key) FROM t7 ORDER BY key")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestOpenDecryptedDBReplaysCommittedWALFrames(t *testing.T) {
	got := walFixtureKeys(t, copyWALFixture(t, func(data []byte) []byte { return data }))
	if want := []string{"01", "02"}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v (row committed in -wal was dropped)", got, want)
	}
}

func TestOpenDecryptedDBWithoutWALReadsMainFile(t *testing.T) {
	got := walFixtureKeys(t, copyWALFixture(t, nil))
	if want := []string{"01"}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func TestOpenDecryptedDBIgnoresEmptyWAL(t *testing.T) {
	got := walFixtureKeys(t, copyWALFixture(t, func([]byte) []byte { return nil }))
	if want := []string{"01"}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func TestOpenDecryptedDBIgnoresTornWALFrames(t *testing.T) {
	// Drop the final commit frame: the remaining frame is uncommitted.
	got := walFixtureKeys(t, copyWALFixture(t, func(data []byte) []byte {
		return data[:sqliteWALHeaderSize+sqliteWALFrameHeaderSize+4096]
	}))
	if want := []string{"01"}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func TestOpenDecryptedDBIgnoresWALFramesWithBadChecksum(t *testing.T) {
	got := walFixtureKeys(t, copyWALFixture(t, func(data []byte) []byte {
		out := append([]byte(nil), data...)
		out[len(out)-1] ^= 0xff
		return out
	}))
	if want := []string{"01"}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func TestOpenDecryptedDBRejectsUnauthenticatedCommittedWAL(t *testing.T) {
	dbPath := copyWALFixture(t, func(data []byte) []byte {
		// Keep SQLite framing valid while breaking SQLCipher page authentication.
		data[sqliteWALHeaderSize+sqliteWALFrameHeaderSize+64] ^= 0xff
		rechecksumWAL(data, 4096)
		return data
	})
	db, cleanup, err := OpenDecryptedDB(context.Background(), dbPath, walFixtureKey())
	if db != nil {
		_ = db.Close()
	}
	cleanup()
	if err == nil || !strings.Contains(err.Error(), "hmac check failed") {
		t.Fatalf("err = %v, want SQLCipher authentication failure", err)
	}
}

func TestOpenDecryptedDBRetainsLastCommitBeforeTornTail(t *testing.T) {
	got := walFixtureKeys(t, copyWALFixture(t, func(data []byte) []byte {
		return append(data, make([]byte, sqliteWALFrameHeaderSize+100)...)
	}))
	if !slices.Equal(got, []string{"01", "02"}) {
		t.Fatalf("keys = %v, want committed rows despite torn tail", got)
	}
}

func TestOpenDecryptedDBReadsBigEndianWALChecksums(t *testing.T) {
	got := walFixtureKeys(t, copyWALFixture(t, func(data []byte) []byte {
		binary.BigEndian.PutUint32(data[:4], sqliteWALMagicBE)
		s1, s2 := sqliteWALChecksum(true, data[:24], 0, 0)
		binary.BigEndian.PutUint32(data[24:28], s1)
		binary.BigEndian.PutUint32(data[28:32], s2)
		for off := sqliteWALHeaderSize; off+24+4096 <= len(data); off += 24 + 4096 {
			header := data[off : off+24]
			s1, s2 = sqliteWALChecksum(true, header[:8], s1, s2)
			s1, s2 = sqliteWALChecksum(true, data[off+24:off+24+4096], s1, s2)
			binary.BigEndian.PutUint32(header[16:20], s1)
			binary.BigEndian.PutUint32(header[20:24], s2)
		}
		return data
	}))
	if !slices.Equal(got, []string{"01", "02"}) {
		t.Fatalf("keys = %v, want committed rows with big-endian checksums", got)
	}
}
