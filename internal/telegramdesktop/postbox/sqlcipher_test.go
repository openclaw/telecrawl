package postbox

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDecryptSQLCipherV4Fixture(t *testing.T) {
	keyAndSalt := make([]byte, 48)
	for i := range keyAndSalt {
		keyAndSalt[i] = byte(i)
	}
	encrypted, err := os.ReadFile(filepath.Join("testdata", "sqlcipher_v4.db"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := DecryptSQLCipherV4(encrypted, keyAndSalt)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain[:len(sqliteHeader)]) != sqliteHeader {
		t.Fatalf("plaintext header = %q", plain[:len(sqliteHeader)])
	}
	path := filepath.Join(t.TempDir(), "plain.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var peerCount int
	if err := db.QueryRow("SELECT count(*) FROM t2 WHERE key = 100").Scan(&peerCount); err != nil {
		t.Fatal(err)
	}
	if peerCount != 1 {
		t.Fatalf("peer count = %d, want 1", peerCount)
	}
	var messageCount int
	if err := db.QueryRow("SELECT count(*) FROM t7 WHERE key = X'00000000000000640000000054b8ea8000000001'").Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != 1 {
		t.Fatalf("message count = %d, want 1", messageCount)
	}
}

func TestDecryptSQLCipherV4RejectsWrongKey(t *testing.T) {
	keyAndSalt := make([]byte, 48)
	for i := range keyAndSalt {
		keyAndSalt[i] = byte(i)
	}
	keyAndSalt[0] ^= 0xff
	encrypted, err := os.ReadFile(filepath.Join("testdata", "sqlcipher_v4.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptSQLCipherV4(encrypted, keyAndSalt); err == nil {
		t.Fatal("wrong key decrypted fixture")
	}
}
