package postbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestImportMediaLookupsShareOneDirectoryIndex(t *testing.T) {
	root := t.TempDir()
	for n := 0; n < 40; n++ {
		path := filepath.Join(root, fmt.Sprintf("res%d.jpg", n))
		if err := os.WriteFile(path, []byte("img"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var reads atomic.Int32
	previous := readMediaDir
	readMediaDir = func(name string) ([]os.DirEntry, error) {
		reads.Add(1)
		return previous(name)
	}
	t.Cleanup(func() { readMediaDir = previous })

	index := mediaCacheIndex(root)
	for n := 0; n < 20; n++ {
		paths := cachedMediaPaths(fmt.Sprintf("res%d", n), root, index)
		if len(paths) != 1 {
			t.Fatalf("res%d paths = %d, want 1", n, len(paths))
		}
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("readMediaDir calls = %d, want 1", got)
	}
}
