package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/telecrawl/internal/backup"
)

func TestAuditImportChecksNativeIdentityBeforeCreatingArchive(t *testing.T) {
	source := t.TempDir()
	lane := filepath.Join(source, "stable")
	nativeDB := filepath.Join(lane, "account-123", "postbox", "db", "db_sqlite")
	if err := os.MkdirAll(filepath.Dir(nativeDB), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nativeDB, []byte("synthetic unreadable database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane, ".tempkeyEncrypted"), []byte("invalid synthetic key"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	dbPath := filepath.Join(parent, "not-created", "archive.db")
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"--db", dbPath, "import", "--path", source}, &stdout, &stderr); err == nil {
		t.Fatal("accepted a native source without verified identity")
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
		t.Fatalf("identity preflight wrote archive directories: %v %v", entries, err)
	}
}

func TestAuditMetadataUsesBackupConfigPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got, want := controlManifest().Paths.DefaultConfig, backup.DefaultConfigPath(); got != want {
		t.Fatalf("config path = %q, want %q", got, want)
	}
}
