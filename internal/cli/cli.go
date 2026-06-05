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
	"sort"
	"strings"
	"time"
	"unicode"

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
	case "metadata":
		return r.print(controlManifest())
	case "import", "sync":
		return r.runImport(args[1:])
	case "doctor":
		return r.runDoctor(args[1:])
	case "status":
		return r.runStatus(args[1:])
	case "chats":
		return r.runChats(args[1:])
	case "folders":
		return r.runFolders(args[1:])
	case "contacts":
		return r.runContacts(args[1:])
	case "topics":
		return r.runTopics(args[1:])
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
	if len(args) == 0 || args[0] != "install" {
		return usageErr(errors.New("usage: telecrawl deps install"))
	}
	fs := flag.NewFlagSet("telecrawl deps install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args[1:]); err != nil {
		return usageErr(err)
	}
	if fs.NArg() != 0 {
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
	installArgs := append([]string{"-m", "pip", "install"}, depsInstallPackages()...)
	if err := runLogged(r.ctx, r.stderr, pipPython, installArgs...); err != nil {
		return err
	}
	postboxDepsInstalled := true
	postboxInstallArgs := append([]string{"-m", "pip", "install"}, postboxDepsInstallPackages()...)
	if err := runLogged(r.ctx, r.stderr, pipPython, postboxInstallArgs...); err != nil {
		postboxDepsInstalled = false
		_, _ = fmt.Fprintf(r.stderr, "warning: optional native macOS Postbox dependency install failed: %v\n", err)
	}
	return r.print(map[string]any{"python": pipPython, "installed": true, "postbox_dependencies": postboxDepsInstalled})
}

func depsInstallPackages() []string {
	return []string{"opentele2", "telethon>=1.43.2"}
}

func postboxDepsInstallPackages() []string {
	return []string{"pycryptodomex", "sqlcipher3", "telethon>=1.43.2"}
}

func (r *runtime) withStore(fn func(*store.Store) error) error {
	st, err := store.Open(r.ctx, r.dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
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
	chat := fs.String("chat", "", "")
	fetchMedia := fs.Bool("fetch-media", false, "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() != 0 {
		return usageErr(errors.New("import takes flags only"))
	}
	return r.withStore(func(st *store.Store) error {
		var existingMediaSourcePath string
		var existingMediaRefs []telegramdesktop.ExistingMediaRef
		if *fetchMedia {
			var err error
			existingMediaSourcePath, existingMediaRefs, err = existingMediaRefsForImport(r.ctx, st)
			if err != nil {
				return err
			}
		}
		result, err := telegramdesktop.Import(r.ctx, telegramdesktop.ImportOptions{
			Path:                    *path,
			Python:                  *python,
			DialogsLimit:            *dialogsLimit,
			MessagesLimit:           *messagesLimit,
			ChatID:                  *chat,
			FetchMedia:              *fetchMedia,
			ExistingMediaSourcePath: existingMediaSourcePath,
			ExistingMediaRefs:       existingMediaRefs,
		}, st.Path())
		if err != nil {
			return err
		}
		if err := storeImportResult(r.ctx, st, &result, *chat); err != nil {
			return err
		}
		return r.print(result.Stats)
	})
}

func storeImportResult(ctx context.Context, st *store.Store, result *telegramdesktop.ImportResult, chatFilter string) error {
	if err := preserveExistingMediaRefs(ctx, st, result.Stats.SourcePath, result.Messages); err != nil {
		return err
	}
	refreshImportMediaStats(result)
	if strings.TrimSpace(chatFilter) == "" {
		return st.ReplaceAll(ctx, result.Stats, result.Contacts, result.Chats, result.Folders, result.FolderChats, result.Topics, result.Messages)
	}
	if len(result.Chats) == 0 {
		return fmt.Errorf("telegram import returned no chats for --chat %s", chatFilter)
	}
	for _, chat := range result.Chats {
		partial := importResultForChat(*result, chat.JID)
		if err := st.UpsertChat(ctx, partial.Stats, chat.JID, partial.Contacts, partial.Chats, partial.Folders, partial.FolderChats, partial.Topics, partial.Messages); err != nil {
			return err
		}
	}
	return nil
}

func refreshImportMediaStats(result *telegramdesktop.ImportResult) {
	result.Stats.MediaMessages = 0
	result.Stats.MediaFiles = 0
	result.Stats.MediaBytes = 0
	mediaFiles := map[string]int64{}
	for _, message := range result.Messages {
		if strings.TrimSpace(message.MediaType) != "" {
			result.Stats.MediaMessages++
		}
		path := strings.TrimSpace(message.MediaPath)
		if path == "" {
			continue
		}
		if _, ok := mediaFiles[path]; !ok {
			mediaFiles[path] = message.MediaSize
		}
	}
	for _, size := range mediaFiles {
		result.Stats.MediaFiles++
		result.Stats.MediaBytes += size
	}
}

func existingMediaRefsForImport(ctx context.Context, st *store.Store) (string, []telegramdesktop.ExistingMediaRef, error) {
	sourcePath, refsByPK, err := existingMediaRefs(ctx, st)
	if err != nil || len(refsByPK) == 0 {
		return sourcePath, nil, err
	}
	refs := make([]telegramdesktop.ExistingMediaRef, 0, len(refsByPK))
	for _, ref := range refsByPK {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].SourcePK < refs[j].SourcePK })
	return sourcePath, refs, nil
}

func preserveExistingMediaRefs(ctx context.Context, st *store.Store, sourcePath string, messages []store.Message) error {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return nil
	}
	existingSourcePath, refs, err := existingMediaRefs(ctx, st)
	if err != nil || existingSourcePath != sourcePath {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	for i := range messages {
		if strings.TrimSpace(messages[i].MediaPath) != "" {
			continue
		}
		ref, ok := refs[messages[i].SourcePK]
		if !ok {
			continue
		}
		if messages[i].MediaType == "" {
			messages[i].MediaType = ref.MediaType
		}
		if messages[i].MediaTitle == "" {
			messages[i].MediaTitle = ref.MediaTitle
		}
		messages[i].MediaPath = ref.MediaPath
		messages[i].MediaSize = ref.MediaSize
	}
	return nil
}

func existingMediaRefs(ctx context.Context, st *store.Store) (string, map[int64]telegramdesktop.ExistingMediaRef, error) {
	status, err := st.Status(ctx)
	if err != nil {
		return "", nil, err
	}
	sourcePath := strings.TrimSpace(status.LastSource)
	if sourcePath == "" {
		return "", nil, nil
	}
	existing, err := st.Messages(ctx, store.MessageFilter{HasMedia: true, Limit: int(^uint(0) >> 1)})
	if err != nil {
		return "", nil, err
	}
	refs := make(map[int64]telegramdesktop.ExistingMediaRef)
	for _, msg := range existing {
		path := strings.TrimSpace(msg.MediaPath)
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			continue
		}
		refs[msg.SourcePK] = telegramdesktop.ExistingMediaRef{
			SourcePK:   msg.SourcePK,
			MediaType:  msg.MediaType,
			MediaTitle: msg.MediaTitle,
			MediaPath:  path,
			MediaSize:  msg.MediaSize,
		}
	}
	return sourcePath, refs, nil
}

func importResultForChat(result telegramdesktop.ImportResult, chatJID string) telegramdesktop.ImportResult {
	out := telegramdesktop.ImportResult{Stats: result.Stats, Folders: result.Folders}
	for _, chat := range result.Chats {
		if chat.JID == chatJID {
			out.Chats = append(out.Chats, chat)
		}
	}
	for _, folderChat := range result.FolderChats {
		if folderChat.ChatJID == chatJID {
			out.FolderChats = append(out.FolderChats, folderChat)
		}
	}
	for _, topic := range result.Topics {
		if topic.ChatJID == chatJID {
			out.Topics = append(out.Topics, topic)
		}
	}
	for _, message := range result.Messages {
		if message.ChatJID == chatJID {
			out.Messages = append(out.Messages, message)
		}
	}
	out.Contacts = contactsForMessages(result.Contacts, out.Messages, chatJID)
	return out
}

func contactsForMessages(contacts []store.Contact, messages []store.Message, chatJID string) []store.Contact {
	peerIDs := map[string]struct{}{}
	if strings.TrimSpace(chatJID) != "" {
		peerIDs[chatJID] = struct{}{}
	}
	for _, message := range messages {
		if strings.TrimSpace(message.ChatJID) != "" {
			peerIDs[message.ChatJID] = struct{}{}
		}
		if strings.TrimSpace(message.SenderJID) != "" {
			peerIDs[message.SenderJID] = struct{}{}
		}
	}
	out := make([]store.Contact, 0, len(peerIDs))
	for _, contact := range contacts {
		if _, ok := peerIDs[contact.JID]; ok {
			out = append(out, contact)
		}
	}
	return out
}

func (r *runtime) runChats(args []string) error {
	fs := flag.NewFlagSet("telecrawl chats", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	limit := fs.Int("limit", 50, "")
	unread := fs.Bool("unread", false, "")
	folder := fs.String("folder", "", "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	return r.withStore(func(st *store.Store) error {
		if *folder != "" {
			chats, err := st.ChatsInFolder(r.ctx, *folder, *limit)
			if err != nil {
				return err
			}
			return r.print(chats)
		}
		chats, err := st.ListChats(r.ctx, *limit, *unread)
		if err != nil {
			return err
		}
		return r.print(chats)
	})
}

func (r *runtime) runFolders(args []string) error {
	fs := flag.NewFlagSet("telecrawl folders", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() != 0 {
		return usageErr(errors.New("folders takes flags only"))
	}
	return r.withStore(func(st *store.Store) error {
		folders, err := st.ListFolders(r.ctx)
		if err != nil {
			return err
		}
		return r.print(folders)
	})
}

func (r *runtime) runContacts(args []string) error {
	if len(args) > 0 && args[0] == "export" {
		return r.runContactsExport(args[1:])
	}
	fs := flag.NewFlagSet("telecrawl contacts", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	limit := fs.Int("limit", 100, "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() != 0 {
		return usageErr(errors.New("contacts takes flags only"))
	}
	return r.withStore(func(st *store.Store) error {
		contacts, err := st.ListContacts(r.ctx, *limit)
		if err != nil {
			return err
		}
		return r.print(contacts)
	})
}

type contactExport struct {
	Contacts []exportedContact `json:"contacts"`
}

type exportedContact struct {
	DisplayName  string   `json:"display_name"`
	PhoneNumbers []string `json:"phone_numbers"`
}

func (r *runtime) runContactsExport(args []string) error {
	fs := flag.NewFlagSet("telecrawl contacts export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() != 0 {
		return usageErr(errors.New("contacts export takes no arguments"))
	}
	return r.withStore(func(st *store.Store) error {
		contacts, err := st.ExportContacts(r.ctx)
		if err != nil {
			return err
		}
		return r.print(contactExport{Contacts: exportContacts(contacts)})
	})
}

func exportContacts(contacts []store.Contact) []exportedContact {
	out := make([]exportedContact, 0, len(contacts))
	for _, contact := range contacts {
		name := contactDisplayName(contact)
		phone := contactPhoneNumber(contact.Phone)
		if name == "" || phone == "" {
			continue
		}
		out = append(out, exportedContact{DisplayName: name, PhoneNumbers: []string{phone}})
	}
	return out
}

func contactDisplayName(contact store.Contact) string {
	if name := cleanContactName(contact.FullName, contact); name != "" {
		return name
	}
	return cleanContactName(strings.TrimSpace(contact.FirstName+" "+contact.LastName), contact)
}

func cleanContactName(name string, contact store.Contact) string {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return ""
	case sameContactText(name, contact.Phone):
		return ""
	case sameContactText(name, contact.JID):
		return ""
	case sameContactText(name, contact.Username):
		return ""
	case sameContactText(name, contact.LID):
		return ""
	case strings.HasPrefix(name, "@"):
		return ""
	case looksLikePhone(name):
		return ""
	default:
		return name
	}
}

func sameContactText(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	return a != "" && b != "" && strings.EqualFold(a, b)
}

func contactPhoneNumber(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	digits := 0
	for _, r := range value {
		switch {
		case unicode.IsDigit(r):
			digits++
		case strings.ContainsRune(" +()-.", r):
		default:
			return ""
		}
	}
	if digits < 7 || digits > 15 {
		return ""
	}
	return value
}

func looksLikePhone(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	digits := 0
	other := 0
	for _, r := range value {
		switch {
		case unicode.IsDigit(r):
			digits++
		case strings.ContainsRune(" +()-.", r):
		default:
			other++
		}
	}
	return digits >= 5 && other == 0
}

func (r *runtime) runTopics(args []string) error {
	fs := flag.NewFlagSet("telecrawl topics", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	chat := fs.String("chat", "", "")
	limit := fs.Int("limit", 100, "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() != 0 {
		return usageErr(errors.New("topics takes flags only"))
	}
	return r.withStore(func(st *store.Store) error {
		topics, err := st.ListTopics(r.ctx, *chat, *limit)
		if err != nil {
			return err
		}
		return r.print(topics)
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
	fs.StringVar(&filter.TopicID, "topic", "", "")
	fs.IntVar(&filter.Limit, "limit", 50, "")
	after := fs.String("after", "", "")
	before := fs.String("before", "", "")
	fromMe := fs.Bool("from-me", false, "")
	fromThem := fs.Bool("from-them", false, "")
	fs.BoolVar(&filter.HasMedia, "media", false, "")
	fs.BoolVar(&filter.Pinned, "pinned", false, "")
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
	if _, err := fmt.Fprintf(r.stdout, "path: %s\n", report.Path); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(r.stdout, "exists: %t\n", report.Exists); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(r.stdout, "accessible: %t\n", report.Accessible); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(r.stdout, "store: %s\n", report.Store); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(r.stdout, "sqlite_files: %d\n", report.SQLiteFiles); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(r.stdout, "tdesktop_files: %d\n", report.TDesktopFiles); err != nil {
		return err
	}
	if report.KeyFiles > 0 {
		if _, err := fmt.Fprintf(r.stdout, "key_files: %d\n", report.KeyFiles); err != nil {
			return err
		}
	}
	if report.PostboxDBs > 0 {
		if _, err := fmt.Fprintf(r.stdout, "postbox_dbs: %d\n", report.PostboxDBs); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(r.stdout, "files_scanned: %d\n", report.FilesScanned); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(r.stdout, "bytes_scanned: %d\n", report.BytesScanned); err != nil {
		return err
	}
	if report.AccountDirs > 0 {
		if _, err := fmt.Fprintf(r.stdout, "account_dirs: %d\n", report.AccountDirs); err != nil {
			return err
		}
	}
	if report.Error != "" {
		if _, err := fmt.Fprintf(r.stdout, "error: %s\n", report.Error); err != nil {
			return err
		}
	}
	if report.Note != "" {
		if _, err := fmt.Fprintf(r.stdout, "note: %s\n", report.Note); err != nil {
			return err
		}
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
		if _, err := fmt.Fprintf(r.stdout, "db_path: %s\nchats: %d\nmessages: %d\nunread_chats: %d\nunread_messages: %d\nmedia_messages: %d\n",
			value.DBPath, value.Chats, value.Messages, value.UnreadChats, value.UnreadMessages, value.MediaMessages); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(r.stdout, "folders: %d\ntopics: %d\n", value.Folders, value.Topics); err != nil {
			return err
		}
		if !value.OldestMessage.IsZero() {
			if _, err := fmt.Fprintf(r.stdout, "oldest_message: %s\n", value.OldestMessage.Format(time.RFC3339)); err != nil {
				return err
			}
		}
		if !value.NewestMessage.IsZero() {
			if _, err := fmt.Fprintf(r.stdout, "newest_message: %s\n", value.NewestMessage.Format(time.RFC3339)); err != nil {
				return err
			}
		}
		if !value.LastImportAt.IsZero() {
			if _, err := fmt.Fprintf(r.stdout, "last_import_at: %s\n", value.LastImportAt.Format(time.RFC3339)); err != nil {
				return err
			}
		}
		return nil
	case store.ImportStats:
		if _, err := fmt.Fprintf(r.stdout, "source_path: %s\ndb_path: %s\nchats: %d\nmessages: %d\nmedia_messages: %d\nmedia_files: %d\nmedia_bytes: %d\nstarted_at: %s\nfinished_at: %s\n",
			value.SourcePath, value.DBPath, value.Chats, value.Messages, value.MediaMessages, value.MediaFiles, value.MediaBytes, value.StartedAt.Format(time.RFC3339), value.FinishedAt.Format(time.RFC3339)); err != nil {
			return err
		}
		if hasRemoteMediaStats(value) {
			if _, err := fmt.Fprintf(
				r.stdout,
				"remote_media_candidates: %d\nremote_media_attempted: %d\nremote_media_downloads: %d\nremote_media_missing: %d\nremote_media_unavailable: %d\nremote_media_timeouts: %d\nremote_media_errors: %d\n",
				value.RemoteMediaCandidates,
				value.RemoteMediaAttempted,
				value.RemoteMediaDownloads,
				value.RemoteMediaMissing,
				value.RemoteMediaUnavailable,
				value.RemoteMediaTimeouts,
				value.RemoteMediaErrors,
			); err != nil {
				return err
			}
		}
		return nil
	default:
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
}

func hasRemoteMediaStats(stats store.ImportStats) bool {
	return stats.RemoteMediaCandidates != 0 ||
		stats.RemoteMediaAttempted != 0 ||
		stats.RemoteMediaDownloads != 0 ||
		stats.RemoteMediaMissing != 0 ||
		stats.RemoteMediaUnavailable != 0 ||
		stats.RemoteMediaTimeouts != 0 ||
		stats.RemoteMediaErrors != 0
}

func usageErr(err error) error {
	return &cliError{code: 2, err: err}
}

func printUsage(w io.Writer) {
	_, _ = io.WriteString(w, `telecrawl: Telegram archive probe/import CLI

usage:
  telecrawl [--json] doctor [--path PATH]
  telecrawl [--json] metadata
  telecrawl [--json] import [--path PATH] [--chat ID] [--dialogs-limit N] [--messages-limit N] [--fetch-media]
  telecrawl [--json] status
  telecrawl [--json] folders
  telecrawl [--json] contacts [--limit N]
  telecrawl [--json] contacts export
  telecrawl [--json] chats [--limit N] [--unread] [--folder ID]
  telecrawl [--json] topics --chat ID [--limit N]
  telecrawl [--json] messages [--chat ID] [--topic ID] [--limit N] [--after DATE]
  telecrawl [--json] search "query" [--chat ID] [--topic ID]
  telecrawl [--json] backup init|push|pull|status
  telecrawl deps install
  telecrawl version

notes:
  import auto-detects Telegram Desktop tdata or native macOS Postbox data
  import archives local cached Postbox media by default; --fetch-media also tries Telegram cloud media
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
