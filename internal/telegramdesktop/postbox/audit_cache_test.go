package postbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuditCacheExcludesEscapingExactAndIndexedLinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("outside sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"resource", "resource.jpg"} {
		if err := os.Symlink(outside, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if got := cachedMediaPaths("resource", root); len(got) != 0 {
		t.Fatalf("cache accepted symlinks: %v", got)
	}
	if got := cachedMediaPaths("../private", root); len(got) != 0 {
		t.Fatalf("cache accepted traversal: %v", got)
	}
}
