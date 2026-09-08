package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAuditOpenUsesLiteralFilename(t *testing.T) {
	for _, name := range []string{"plain.db", "question?.db", "hash#.db", "percent%25.db", "space archive.db", "options?mode=memory&cache=shared"} {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.Contains(name, "?") {
				t.Skip("question mark is not a native Windows filename")
			}
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), name)
			st, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.ExecContext(ctx, "INSERT INTO sync_state(key,value,updated_at) VALUES('audit_uri','synthetic',1)"); err != nil {
				st.Close()
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("exact requested file missing: %v", err)
			}
			st, err = Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			var value string
			if err := st.db.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key='audit_uri'").Scan(&value); err != nil {
				t.Fatal(err)
			}
			if value != "synthetic" {
				t.Fatalf("reopened value = %q", value)
			}
			var foreignKeys int
			var journal string
			if err := st.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
				t.Fatal(err)
			}
			if err := st.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
				t.Fatal(err)
			}
			if foreignKeys != 1 || journal != "wal" {
				t.Fatalf("pragmas foreign_keys=%d journal_mode=%s", foreignKeys, journal)
			}
		})
	}
}

func TestAuditOpenPreservesMemoryDatabase(t *testing.T) {
	t.Chdir(t.TempDir())
	st, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(":memory:"); !os.IsNotExist(err) {
		t.Fatalf("memory database created a file: %v", err)
	}
}
