package telegramdesktop

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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
