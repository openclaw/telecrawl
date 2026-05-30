package telegramdesktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/telecrawl/internal/store"
)

func TestResolveImportSourcePrefersTDataDefault(t *testing.T) {
	root, _, _ := makePostboxFixture(t)
	tdata := filepath.Join(t.TempDir(), "tdata")
	if err := os.MkdirAll(tdata, 0o700); err != nil {
		t.Fatal(err)
	}

	source := resolveImportSourcePaths("", tdata, root)
	if source.path != tdata || source.postbox {
		t.Fatalf("source = %+v, want tdata path", source)
	}
}

func TestResolveImportSourceFallsBackToPostboxDefault(t *testing.T) {
	root, _, _ := makePostboxFixture(t)
	missingTData := filepath.Join(t.TempDir(), "missing-tdata")

	source := resolveImportSourcePaths("", missingTData, root)
	if source.path != root || !source.postbox {
		t.Fatalf("source = %+v, want postbox path", source)
	}
}

func TestResolveImportSourceClassifiesExplicitPostboxPath(t *testing.T) {
	_, _, account := makePostboxFixture(t)

	source := resolveImportSourcePaths(account, "unused-tdata", "unused-postbox")
	if source.path != account || !source.postbox {
		t.Fatalf("source = %+v, want explicit postbox path", source)
	}
}

func TestPostboxParserSanitizedFixture(t *testing.T) {
	// Exercises the embedded Postbox decoder against public sanitized format fixtures.
	python, err := resolvePython("")
	if err != nil {
		t.Skip(err)
	}
	script, cleanup, err := writeTempScript("import_postbox.py", importPostboxScript)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, python, script, "--self-test", "--fixture-dir", filepath.Join("testdata", "postbox")).CombinedOutput() // #nosec G204 -- test executes the embedded importer with a resolved Python.
	if err != nil {
		t.Fatalf("postbox parser self-test failed: %v\n%s", err, out)
	}
	var got struct {
		OK      bool   `json:"ok"`
		Fixture string `json:"fixture"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode self-test output: %v\n%s", err, out)
	}
	if !got.OK || got.Fixture != "sanitized-postbox-format" {
		t.Fatalf("unexpected self-test output: %+v", got)
	}
}

func TestCopyImportedMediaArchivesByContentHash(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "source-media")
	if err := os.WriteFile(source, []byte("fixture media"), 0o600); err != nil {
		t.Fatal(err)
	}
	messages := []store.Message{
		{SourcePK: 1, MediaPath: source},
		{SourcePK: 2, MediaPath: source},
	}
	var stats store.ImportStats
	archiveDir := filepath.Join(t.TempDir(), "media")

	if err := copyImportedMedia(messages, archiveDir, &stats); err != nil {
		t.Fatal(err)
	}
	if messages[0].MediaPath == source {
		t.Fatal("media path still points at source cache")
	}
	if messages[1].MediaPath != messages[0].MediaPath {
		t.Fatalf("duplicate media archived to different paths: %q != %q", messages[1].MediaPath, messages[0].MediaPath)
	}
	if messages[0].MediaSize != int64(len("fixture media")) {
		t.Fatalf("media size = %d, want %d", messages[0].MediaSize, len("fixture media"))
	}
	if stats.MediaFiles != 1 || stats.MediaBytes != int64(len("fixture media")) {
		t.Fatalf("media stats = %+v", stats)
	}
	data, err := os.ReadFile(messages[0].MediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fixture media" {
		t.Fatalf("archived media = %q", data)
	}
	if !strings.HasPrefix(messages[0].MediaPath, archiveDir+string(os.PathSeparator)) {
		t.Fatalf("media path %q is not under archive dir %q", messages[0].MediaPath, archiveDir)
	}
}

func TestCopyImportedMediaSkipsMissingSourceCache(t *testing.T) {
	t.Parallel()
	messages := []store.Message{
		{SourcePK: 1, MediaPath: filepath.Join(t.TempDir(), "missing-cache-file"), MediaSize: 99},
	}
	var stats store.ImportStats

	if err := copyImportedMedia(messages, filepath.Join(t.TempDir(), "media"), &stats); err != nil {
		t.Fatal(err)
	}
	if messages[0].MediaPath != "" || messages[0].MediaSize != 0 {
		t.Fatalf("missing media ref = path %q size %d, want cleared", messages[0].MediaPath, messages[0].MediaSize)
	}
	if stats.MediaFiles != 0 || stats.MediaBytes != 0 {
		t.Fatalf("media stats = %+v, want zero", stats)
	}
}

func TestImportPassesFetchMediaToTDataImporter(t *testing.T) {
	t.Parallel()
	python, argvPath := fakePythonImporter(t)
	source := t.TempDir()

	_, err := Import(context.Background(), ImportOptions{
		Path:       source,
		Python:     python,
		FetchMedia: true,
	}, filepath.Join(t.TempDir(), "telecrawl.db"))
	if err != nil {
		t.Fatal(err)
	}

	args := readImporterArgs(t, argvPath)
	if !containsArg(args, "--fetch-media") {
		t.Fatalf("args missing --fetch-media: %v", args)
	}
	idx := indexArg(args, "--media-output-dir")
	if idx < 0 || idx+1 >= len(args) || strings.TrimSpace(args[idx+1]) == "" {
		t.Fatalf("args missing --media-output-dir value: %v", args)
	}
}

func TestImportDoesNotFetchMediaByDefault(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source func(t *testing.T) string
	}{
		{
			name: "tdata",
			source: func(t *testing.T) string {
				t.Helper()
				return t.TempDir()
			},
		},
		{
			name: "postbox",
			source: func(t *testing.T) string {
				t.Helper()
				root, _, _ := makePostboxFixture(t)
				return root
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			python, argvPath := fakePythonImporter(t)
			_, err := Import(context.Background(), ImportOptions{
				Path:   tc.source(t),
				Python: python,
			}, filepath.Join(t.TempDir(), "telecrawl.db"))
			if err != nil {
				t.Fatal(err)
			}
			args := readImporterArgs(t, argvPath)
			if containsArg(args, "--fetch-media") || containsArg(args, "--media-output-dir") {
				t.Fatalf("default import should not fetch media: %v", args)
			}
		})
	}
}

func fakePythonImporter(t *testing.T) (python string, argvPath string) {
	t.Helper()
	dir := t.TempDir()
	argvPath = filepath.Join(dir, "argv")
	python = filepath.Join(dir, "python")
	result := `{"source_path":"fixture","started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:00Z","chats":[],"folders":[],"folder_chats":[],"topics":[],"messages":[]}`
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nprintf '%%s\\n' '%s'\n", argvPath, result)
	if err := os.WriteFile(python, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return python, argvPath
}

func readImporterArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func containsArg(args []string, want string) bool {
	return indexArg(args, want) >= 0
}

func indexArg(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}

func makePostboxFixture(t *testing.T) (root string, lane string, account string) {
	t.Helper()
	root = t.TempDir()
	lane = filepath.Join(root, "stable")
	account = filepath.Join(lane, "account-123")
	dbDir := filepath.Join(account, "postbox", "db")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane, ".tempkeyEncrypted"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "db_sqlite"), []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, lane, account
}
