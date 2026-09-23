package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlagsOnlyCommandsRejectPositionalArgumentsBeforeEffects(t *testing.T) {
	for _, command := range []string{
		"doctor", "status", "chats", "folders", "contacts", "contacts export",
		"topics", "messages", "import", "backup init", "backup push",
		"backup pull", "backup status", "backup snapshots",
	} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			args := []string{"--db", filepath.Join(root, "archive.db"), "--source", filepath.Join(root, "source")}
			args = append(args, strings.Fields(command)...)
			if strings.HasPrefix(command, "backup ") {
				args = append(args, "--config", filepath.Join(root, "config.json"),
					"--repo", filepath.Join(root, "repo"), "--identity", filepath.Join(root, "age.key"),
					"--remote", filepath.Join(root, "remote"), "--no-push")
			}
			args = append(args, "unexpected", "--unknown")
			ctx, cancel := context.WithCancel(t.Context())
			cancel() // Prevent command execution on an implementation that accepts the arguments.
			var stdout, stderr bytes.Buffer
			err := Run(ctx, args, &stdout, &stderr)
			if err == nil || ExitCode(err) != 2 {
				t.Errorf("want usage error, got %v", err)
			}
			if stdout.Len() != 0 {
				t.Errorf("unexpected output: %q", stdout.String())
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("invalid command created %d filesystem entries", len(entries))
			}
		})
	}
}
