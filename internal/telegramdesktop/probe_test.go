package telegramdesktop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeDetectsTDesktopStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key_datas"), []byte("TDF$hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Probe(context.Background(), Options{Path: dir})
	if !report.Accessible {
		t.Fatalf("expected accessible report: %+v", report)
	}
	if report.Store != "tdesktop-binary" {
		t.Fatalf("store = %q, want tdesktop-binary", report.Store)
	}
	if report.TDesktopFiles != 1 {
		t.Fatalf("tdesktop_files = %d, want 1", report.TDesktopFiles)
	}
}

func TestProbeDetectsSQLiteStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "messages.sqlite"), []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Probe(context.Background(), Options{Path: dir})
	if report.Store != "sqlite" {
		t.Fatalf("store = %q, want sqlite", report.Store)
	}
	if report.SQLiteFiles != 1 {
		t.Fatalf("sqlite_files = %d, want 1", report.SQLiteFiles)
	}
}

func TestProbeDetectsPostboxStore(t *testing.T) {
	dir := t.TempDir()
	lane := filepath.Join(dir, "stable")
	dbDir := filepath.Join(lane, "account-123", "postbox", "db")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane, ".tempkeyEncrypted"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "db_sqlite"), []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}

	report := Probe(context.Background(), Options{Path: dir})
	if report.Store != "telegram-macos-postbox" {
		t.Fatalf("store = %q, want telegram-macos-postbox", report.Store)
	}
	if report.KeyFiles != 1 {
		t.Fatalf("key_files = %d, want 1", report.KeyFiles)
	}
	if report.PostboxDBs != 1 {
		t.Fatalf("postbox_dbs = %d, want 1", report.PostboxDBs)
	}
	if !LooksLikePostbox(dir) {
		t.Fatal("expected root to look like Postbox")
	}
	if !LooksLikePostbox(lane) {
		t.Fatal("expected lane to look like Postbox")
	}
	if !LooksLikePostbox(filepath.Join(lane, "account-123")) {
		t.Fatal("expected account path to look like Postbox")
	}
}
