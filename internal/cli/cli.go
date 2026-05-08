package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/telecrawl/internal/backup"
	"github.com/openclaw/telecrawl/internal/store"
	"github.com/openclaw/telecrawl/internal/telegramdesktop"
)

type cliError struct {
	code int
	err  error
}

func (e *cliError) Error() string {
	return e.err.Error()
}

func (e *cliError) Unwrap() error {
	return e.err
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 1
	}
	var codeErr *cliError
	if errors.As(err, &codeErr) {
		return codeErr.code
	}
	return 1
}

type runtime struct {
	ctx    context.Context
	stdout io.Writer
	stderr io.Writer
	json   bool
	dbPath string
	source string
	python string
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printUsage(stdout)
		return nil
	}
	global := flag.NewFlagSet("telecrawl", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	jsonOut := global.Bool("json", false, "")
	dbPath := global.String("db", defaultDBPath(), "")
	source := global.String("source", "", "")
	python := global.String("python", "", "")
	versionFlag := global.Bool("version", false, "")
	if err := global.Parse(args); err != nil {
		return usageErr(err)
	}
	if *versionFlag {
		_, _ = io.WriteString(stdout, version+"\n")
		return nil
	}
	rest := global.Args()
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "--help" || rest[0] == "-h" {
		printUsage(stdout)
		return nil
	}
	if rest[0] == "version" {
		_, _ = io.WriteString(stdout, version+"\n")
		return nil
	}
	r := &runtime{ctx: ctx, stdout: stdout, stderr: stderr, json: *jsonOut, dbPath: *dbPath, source: *source, python: *python}
	return r.dispatch(rest)
}

func (r *runtime) dispatch(args []string) error {
	switch args[0] {
	case "import", "sync":
		return r.runImport(args[1:])
	case "doctor":
		return r.runDoctor(args[1:])
	case "status":
		return r.runStatus(args[1:])
	case "chats":
		return r.runChats(args[1:])
	case "messages":
		return r.runMessages(args[1:])
	case "search":
		return r.runSearch(args[1:])
	case "backup":
		return r.runBackup(args[1:])
	case "deps":
		return r.runDeps(args[1:])
	case "wiretap":
		return r.runImport(args[1:])
	default:
		return usageErr(fmt.Errorf("unknown command %q", args[0]))
	}
}

func (r *runtime) runDeps(args []string) error {
	if len(args) != 1 || args[0] != "install" {
		return usageErr(errors.New("usage: telecrawl deps install"))
	}
	venv := filepath.Join(defaultBaseDir(), "venv")
	python, err := exec.LookPath("python3.11")
	if err != nil {
		python, err = exec.LookPath("python3")
		if err != nil {
			return errors.New("python3.11 or python3 required")
		}
	}
	if err := os.MkdirAll(defaultBaseDir(), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(venv, "bin", "python")); os.IsNotExist(err) {
		if err := runLogged(r.ctx, r.stderr, python, "-m", "venv", venv); err != nil {
			return err
		}
	}
	pipPython := filepath.Join(venv, "bin", "python")
	if err := runLogged(r.ctx, r.stderr, pipPython, "-m", "pip", "install", "--upgrade", "pip"); err != nil {
		return err
	}
	if err := runLogged(r.ctx, r.stderr, pipPython, "-m", "pip", "install", "opentele2", "telethon"); err != nil {
		return err
	}
	return r.print(map[string]any{"python": pipPython, "installed": true})
}

func (r *runtime) withStore(fn func(*store.Store) error) error {
	st, err := store.Open(r.ctx, r.dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	return fn(st)
}

func (r *runtime) runDoctor(args []string) error {
	fs := flag.NewFlagSet("telecrawl doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("path", r.source, "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	return r.printProbe(telegramdesktop.Probe(r.ctx, telegramdesktop.Options{Path: *path}))
}

func (r *runtime) runStatus(args []string) error {
	fs := flag.NewFlagSet("telecrawl status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	return r.withStore(func(st *store.Store) error {
		status, err := st.Status(r.ctx)
		if err != nil {
			return err
		}
		return r.print(status)
	})
}

func (r *runtime) runImport(args []string) error {
	fs := flag.NewFlagSet("telecrawl import", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("path", r.source, "")
	python := fs.String("python", r.python, "")
	dialogsLimit := fs.Int("dialogs-limit", 200, "")
	messagesLimit := fs.Int("messages-limit", 500, "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() != 0 {
		return usageErr(errors.New("import takes flags only"))
	}
	return r.withStore(func(st *store.Store) error {
		result, err := telegramdesktop.Import(r.ctx, telegramdesktop.ImportOptions{
			Path:          *path,
			Python:        *python,
			DialogsLimit:  *dialogsLimit,
			MessagesLimit: *messagesLimit,
		}, st.Path())
		if err != nil {
			return err
		}
		if err := st.ReplaceAll(r.ctx, result.Stats, result.Chats, result.Messages); err != nil {
			return err
		}
		return r.print(result.Stats)
	})
}

func (r *runtime) runChats(args []string) error {
	fs := flag.NewFlagSet("telecrawl chats", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	limit := fs.Int("limit", 50, "")
	unread := fs.Bool("unread", false, "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	return r.withStore(func(st *store.Store) error {
		chats, err := st.ListChats(r.ctx, *limit, *unread)
		if err != nil {
			return err
		}
		return r.print(chats)
	})
}

func (r *runtime) runMessages(args []string) error {
	filter, err := r.messageFilter("telecrawl messages", args, false)
	if err != nil {
		return err
	}
	return r.withStore(func(st *store.Store) error {
		messages, err := st.Messages(r.ctx, filter)
		if err != nil {
			return err
		}
		return r.print(messages)
	})
}

func (r *runtime) runSearch(args []string) error {
	filter, err := r.messageFilter("telecrawl search", args, true)
	if err != nil {
		return err
	}
	return r.withStore(func(st *store.Store) error {
		messages, err := st.Search(r.ctx, filter)
		if err != nil {
			return err
		}
		return r.print(messages)
	})
}

func (r *runtime) messageFilter(name string, args []string, requireQuery bool) (store.MessageFilter, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var filter store.MessageFilter
	fs.StringVar(&filter.ChatJID, "chat", "", "")
	fs.StringVar(&filter.Sender, "sender", "", "")
	fs.IntVar(&filter.Limit, "limit", 50, "")
	after := fs.String("after", "", "")
	before := fs.String("before", "", "")
	fromMe := fs.Bool("from-me", false, "")
	fromThem := fs.Bool("from-them", false, "")
	fs.BoolVar(&filter.HasMedia, "media", false, "")
	fs.BoolVar(&filter.Asc, "asc", false, "")
	if err := fs.Parse(args); err != nil {
		return filter, usageErr(err)
	}
	if requireQuery {
		if fs.NArg() != 1 {
			return filter, usageErr(errors.New("search takes exactly one query"))
		}
		filter.Query = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return filter, usageErr(errors.New("messages takes flags only"))
	}
	if *after != "" {
		t, err := parseDate(*after)
		if err != nil {
			return filter, usageErr(err)
		}
		filter.After = &t
	}
	if *before != "" {
		t, err := parseDate(*before)
		if err != nil {
			return filter, usageErr(err)
		}
		filter.Before = &t
	}
	if *fromMe && *fromThem {
		return filter, usageErr(errors.New("--from-me and --from-them conflict"))
	}
	if *fromMe || *fromThem {
		v := *fromMe
		filter.FromMe = &v
	}
	return filter, nil
}

func (r *runtime) runBackup(args []string) error {
	if len(args) == 0 {
		return usageErr(errors.New("backup needs subcommand: init, push, pull, status"))
	}
	switch args[0] {
	case "init":
		return r.backupInit(args[1:])
	case "push":
		return r.backupPush(args[1:])
	case "pull":
		return r.backupPull(args[1:])
	case "status":
		return r.backupStatus(args[1:])
	default:
		return usageErr(fmt.Errorf("unknown backup command %q", args[0]))
	}
}

func backupFlags(name string) (*flag.FlagSet, *backup.Options, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := &backup.Options{}
	fs.StringVar(&opts.ConfigPath, "config", backup.DefaultConfigPath(), "")
	fs.StringVar(&opts.Repo, "repo", "", "")
	fs.StringVar(&opts.Remote, "remote", "", "")
	fs.StringVar(&opts.Identity, "identity", "", "")
	fs.Func("recipient", "", func(value string) error {
		opts.Recipients = append(opts.Recipients, value)
		return nil
	})
	noPush := fs.Bool("no-push", false, "")
	return fs, opts, noPush
}

func (r *runtime) backupInit(args []string) error {
	fs, opts, noPush := backupFlags("telecrawl backup init")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	opts.Push = !*noPush
	cfg, recipient, err := backup.Init(r.ctx, *opts)
	if err != nil {
		return err
	}
	return r.print(map[string]any{"repo": cfg.Repo, "remote": cfg.Remote, "identity": cfg.Identity, "recipient": recipient})
}

func (r *runtime) backupPush(args []string) error {
	fs, opts, noPush := backupFlags("telecrawl backup push")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	opts.Push = !*noPush
	return r.withStore(func(st *store.Store) error {
		result, err := backup.Push(r.ctx, st, *opts)
		if err != nil {
			return err
		}
		return r.print(result)
	})
}

func (r *runtime) backupPull(args []string) error {
	fs, opts, _ := backupFlags("telecrawl backup pull")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	return r.withStore(func(st *store.Store) error {
		result, err := backup.Pull(r.ctx, st, *opts)
		if err != nil {
			return err
		}
		return r.print(result)
	})
}

func (r *runtime) backupStatus(args []string) error {
	fs, opts, _ := backupFlags("telecrawl backup status")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	manifest, repo, err := backup.Status(r.ctx, *opts)
	if err != nil {
		return err
	}
	return r.print(map[string]any{"repo": repo, "manifest": manifest})
}

func (r *runtime) printProbe(report telegramdesktop.Report) error {
	if r.json {
		enc := json.NewEncoder(r.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Fprintf(r.stdout, "path: %s\n", report.Path)
	fmt.Fprintf(r.stdout, "exists: %t\n", report.Exists)
	fmt.Fprintf(r.stdout, "accessible: %t\n", report.Accessible)
	fmt.Fprintf(r.stdout, "store: %s\n", report.Store)
	fmt.Fprintf(r.stdout, "sqlite_files: %d\n", report.SQLiteFiles)
	fmt.Fprintf(r.stdout, "tdesktop_files: %d\n", report.TDesktopFiles)
	fmt.Fprintf(r.stdout, "files_scanned: %d\n", report.FilesScanned)
	fmt.Fprintf(r.stdout, "bytes_scanned: %d\n", report.BytesScanned)
	if report.AccountDirs > 0 {
		fmt.Fprintf(r.stdout, "account_dirs: %d\n", report.AccountDirs)
	}
	if report.Error != "" {
		fmt.Fprintf(r.stdout, "error: %s\n", report.Error)
	}
	if report.Note != "" {
		fmt.Fprintf(r.stdout, "note: %s\n", report.Note)
	}
	return nil
}

func (r *runtime) print(v any) error {
	enc := json.NewEncoder(r.stdout)
	if r.json {
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	switch value := v.(type) {
	case store.Status:
		fmt.Fprintf(r.stdout, "db_path: %s\nchats: %d\nmessages: %d\nunread_chats: %d\nunread_messages: %d\nmedia_messages: %d\n",
			value.DBPath, value.Chats, value.Messages, value.UnreadChats, value.UnreadMessages, value.MediaMessages)
		if !value.OldestMessage.IsZero() {
			fmt.Fprintf(r.stdout, "oldest_message: %s\n", value.OldestMessage.Format(time.RFC3339))
		}
		if !value.NewestMessage.IsZero() {
			fmt.Fprintf(r.stdout, "newest_message: %s\n", value.NewestMessage.Format(time.RFC3339))
		}
		if !value.LastImportAt.IsZero() {
			fmt.Fprintf(r.stdout, "last_import_at: %s\n", value.LastImportAt.Format(time.RFC3339))
		}
		return nil
	case store.ImportStats:
		fmt.Fprintf(r.stdout, "source_path: %s\ndb_path: %s\nchats: %d\nmessages: %d\nmedia_messages: %d\nstarted_at: %s\nfinished_at: %s\n",
			value.SourcePath, value.DBPath, value.Chats, value.Messages, value.MediaMessages, value.StartedAt.Format(time.RFC3339), value.FinishedAt.Format(time.RFC3339))
		return nil
	default:
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
}

func usageErr(err error) error {
	return &cliError{code: 2, err: err}
}

func printUsage(w io.Writer) {
	_, _ = io.WriteString(w, `telecrawl: Telegram archive probe/import CLI

usage:
  telecrawl [--json] doctor [--path PATH]
  telecrawl [--json] import [--path PATH] [--dialogs-limit N] [--messages-limit N]
  telecrawl [--json] status
  telecrawl [--json] chats [--limit N] [--unread]
  telecrawl [--json] messages [--chat ID] [--limit N] [--after DATE]
  telecrawl [--json] search "query" [--chat ID]
  telecrawl [--json] backup init|push|pull|status
  telecrawl deps install
  telecrawl version

notes:
  import uses Telegram Desktop tdata via opentele2/Telethon
  backup writes encrypted age shards to a git repo
`)
}

func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "telecrawl.db"
	}
	return filepath.Join(home, ".telecrawl", "telecrawl.db")
}

func parseDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q", value)
}

func defaultBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".telecrawl"
	}
	return filepath.Join(home, ".telecrawl")
}

func runLogged(ctx context.Context, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- dependency install uses fixed commands.
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
