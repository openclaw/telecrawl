package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func ensureRepo(ctx context.Context, cfg Config) error {
	if strings.TrimSpace(cfg.Repo) == "" {
		return fmt.Errorf("backup repo path is required")
	}
	if _, err := os.Stat(filepath.Join(cfg.Repo, ".git")); err == nil {
		pullErr := git(ctx, cfg.Repo, "pull", "--rebase")
		if pullErr != nil {
			hasHead := git(ctx, cfg.Repo, "rev-parse", "--verify", "HEAD") == nil
			if !hasHead {
				return nil
			}
			if strings.Contains(pullErr.Error(), "no tracking information") ||
				strings.Contains(pullErr.Error(), "No remote repository specified") ||
				strings.Contains(pullErr.Error(), "no such ref was fetched") {
				return nil
			}
			return pullErr
		}
		return nil
	}
	if strings.TrimSpace(cfg.Remote) != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.Repo), 0o700); err != nil {
			return err
		}
		if err := git(ctx, "", "clone", cfg.Remote, cfg.Repo); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(cfg.Repo, 0o700); err != nil {
		return err
	}
	if err := git(ctx, cfg.Repo, "init"); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Remote) != "" {
		if err := git(ctx, cfg.Repo, "remote", "add", "origin", cfg.Remote); err != nil {
			return err
		}
	}
	return nil
}

func commitAndPush(ctx context.Context, cfg Config, message string, push bool) (bool, error) {
	if err := git(ctx, cfg.Repo, "add", "."); err != nil {
		return false, err
	}
	if err := git(ctx, cfg.Repo, "diff", "--cached", "--quiet"); err == nil {
		return false, nil
	}
	if err := git(ctx, cfg.Repo, "commit", "-m", message); err != nil {
		return false, err
	}
	if push {
		if err := git(ctx, cfg.Repo, "push", "-u", "origin", "HEAD"); err != nil {
			return true, err
		}
	}
	return true, nil
}

func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- telecrawl only passes fixed git subcommands plus configured repo paths.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=telecrawl",
		"GIT_AUTHOR_EMAIL=telecrawl@example.invalid",
		"GIT_COMMITTER_NAME=telecrawl",
		"GIT_COMMITTER_EMAIL=telecrawl@example.invalid",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
