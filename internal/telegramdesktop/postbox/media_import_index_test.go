package postbox

import (
	"context"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceImportScansMediaDirectoryOnce(t *testing.T) {
	for _, limited := range []bool{false, true} {
		name := "full"
		if limited {
			name = "selected"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			mediaRoot := filepath.Join(root, "media")
			if err := os.Mkdir(mediaRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			mediaPath := filepath.Join(mediaRoot, "telegram-cloud-document-2-987654321.mp4")
			if err := os.WriteFile(mediaPath, []byte("media"), 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if _, err := db.Exec(`CREATE TABLE t2(key INTEGER PRIMARY KEY, value BLOB NOT NULL); CREATE TABLE t7(key BLOB PRIMARY KEY, value BLOB NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			value := fixtureMessage("synthetic", nil, nil, fixtureDocumentMedia(987654321, "fixture.mp4"))
			for n := uint32(1); n <= 20; n++ {
				key := make([]byte, 20)
				binary.BigEndian.PutUint64(key, 100)
				binary.BigEndian.PutUint32(key[16:], n)
				if _, err := db.Exec(`INSERT INTO t7 VALUES(?,?)`, key, value); err != nil {
					t.Fatal(err)
				}
			}
			reads := 0
			previous := readMediaDir
			readMediaDir = func(path string) ([]os.DirEntry, error) { reads++; return previous(path) }
			t.Cleanup(func() { readMediaDir = previous })
			opts := ReadOptions{}
			if limited {
				opts.MessagesLimit = 20
			}
			source := Source{AccountID: "synthetic", DBPath: filepath.Join(root, "postbox", "db_sqlite")}
			records, err := readSourceRecordsDB(context.Background(), source, db, false, opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(records.Messages) != 20 {
				t.Fatalf("messages = %d, want 20", len(records.Messages))
			}
			for _, msg := range records.Messages {
				if msg.MediaPath != mediaPath || msg.MediaSize != 5 {
					t.Fatalf("media = (%q, %d)", msg.MediaPath, msg.MediaSize)
				}
			}
			if reads != 1 {
				t.Fatalf("media directory reads = %d, want 1", reads)
			}
		})
	}
}
