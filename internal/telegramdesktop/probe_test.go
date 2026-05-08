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
