//go:build !windows

package telegramdesktop

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/openclaw/telecrawl/internal/store"
)

func TestAuditProbeSkipsFIFOAndEscapingSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("outside sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	report := Probe(ctx, Options{Path: root})
	if time.Since(start) > time.Second || report.FilesScanned != 0 {
		t.Fatalf("special files were read: %+v", report)
	}
	if _, ok := sniffFile(filepath.Join(root, "fifo")); ok {
		t.Fatal("sniff accepted FIFO")
	}
	cancel()
	if report := Probe(ctx, Options{Path: root}); report.Error != context.Canceled.Error() {
		t.Fatalf("canceled probe: %+v", report)
	}
}

func TestAuditMediaReadsRequireSelectedRegularSource(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("outside sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "cache-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{outside, link, fifo} {
		destination := filepath.Join(t.TempDir(), "media")
		messages := []store.Message{{MediaPath: source}}
		if err := copyImportedMedia(messages, destination, nil, root); err == nil || isMediaSourceUnavailable(err) {
			t.Fatalf("invalid source treated as success/cache miss: %q %v", source, err)
		}
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatal("invalid source created archive media")
		}
	}
}

func TestAuditImportRejectsSymlinkedAccountMediaParent(t *testing.T) {
	root, _, account := makePostboxFixture(t)
	postbox := filepath.Join(account, "postbox")
	outside := filepath.Join(t.TempDir(), "postbox")
	if err := os.Rename(postbox, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, postbox); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "not-created", "archive.db")
	if _, err := Import(context.Background(), ImportOptions{Path: root}, archive); err == nil {
		t.Fatal("accepted a cache root through a symlinked account parent")
	}
	if _, err := os.Stat(filepath.Dir(archive)); !os.IsNotExist(err) {
		t.Fatal("invalid source created archive media")
	}
}
