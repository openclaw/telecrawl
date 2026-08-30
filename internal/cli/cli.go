package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/openclaw/crawlkit/control"
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
	versionFlag := global.Bool("version", false, "")
	if err := global.Parse(args); err != nil {
		return usageErr(err)
	}
	if *versionFlag {
		_, _ = io.WriteString(stdout, currentVersion()+"\n")
		return nil
	}
	rest := global.Args()
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "--help" || rest[0] == "-h" {
		printUsage(stdout)
		return nil
	}
	if rest[0] == "version" {
		_, _ = io.WriteString(stdout, currentVersion()+"\n")
		return nil
	}
	r := &runtime{ctx: ctx, stdout: stdout, stderr: stderr, json: *jsonOut, dbPath: *dbPath, source: *source}
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
	case "wiretap":
		return r.runImport(args[1:])
	default:
		return usageErr(fmt.Errorf("unknown command %q", args[0]))
	}
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
	dialogsLimit := fs.Int("dialogs-limit", 200, "")
	messagesLimit := fs.Int("messages-limit", 500, "")
	chat := fs.String("chat", "", "")
	fetchMedia := fs.Bool("fetch-media", false, "")
	restore := fs.Bool("restore", false, "")
	replace := fs.Bool("replace", false, "") // Compatibility alias shipped in v0.3.4.
	adoptSource := fs.Bool("adopt-source", false, "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() != 0 {
		return usageErr(errors.New("import takes flags only"))
	}
	if *restore && *replace {
		return usageErr(errors.New("--restore and --replace are aliases; use only --restore"))
	}
	restoreMode := *restore || *replace
	if restoreMode && strings.TrimSpace(*chat) != "" {
		return usageErr(errors.New("--restore cannot be combined with --chat"))
	}
	if restoreMode && *adoptSource {
		return usageErr(errors.New("--restore cannot be combined with --adopt-source"))
	}
	return r.withStore(func(st *store.Store) error {
		mediaStage, err := os.MkdirTemp(filepath.Dir(st.Path()), ".telecrawl-import-media-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(mediaStage) }()
		var existingMediaSourcePath string
		var existingMediaRefs []telegramdesktop.ExistingMediaRef
		mediaCache := &mediaRefCache{}
		if *fetchMedia && !restoreMode {
			existingMediaSourcePath, existingMediaRefs, err = existingMediaRefsForImport(r.ctx, st, mediaCache)
			if err != nil {
				return err
			}
		}
		result, err := telegramdesktop.Import(r.ctx, telegramdesktop.ImportOptions{
			Path:                    *path,
			DialogsLimit:            *dialogsLimit,
			MessagesLimit:           *messagesLimit,
			ChatID:                  *chat,
			FetchMedia:              *fetchMedia,
			Progress:                r.stderr,
			ExistingMediaSourcePath: existingMediaSourcePath,
			ExistingMediaRefs:       existingMediaRefs,
			MediaArchiveDir:         mediaStage,
		}, st.Path())
		if err != nil {
			return err
		}
		result.Stats.AdoptSource = *adoptSource
		if err := prepareImportResultSource(&result); err != nil {
			return err
		}
		if !restoreMode {
			if err := st.ValidateMergeSource(r.ctx, result.Stats, result.Messages); err != nil {
				return err
			}
		}
		if !restoreMode {
			if err := preserveExistingMediaRefs(r.ctx, st, result.Stats.SourcePath, result.Messages, true, mediaCache); err != nil {
				return err
			}
		}
		if err := promoteImportMedia(&result, mediaStage, filepath.Join(filepath.Dir(st.Path()), "media")); err != nil {
			return err
		}
		if err := storeImportResultCached(r.ctx, st, &result, *chat, restoreMode, mediaCache); err != nil {
			return err
		}
		return r.print(result.Stats)
	})
}

func storeImportResult(ctx context.Context, st *store.Store, result *telegramdesktop.ImportResult, chatFilter string, restore bool) error {
	return storeImportResultCached(ctx, st, result, chatFilter, restore, nil)
}

func storeImportResultCached(ctx context.Context, st *store.Store, result *telegramdesktop.ImportResult, chatFilter string, restore bool, cache *mediaRefCache) error {
	if err := prepareImportResultSource(result); err != nil {
		return err
	}
	if !restore {
		if err := preserveExistingMediaRefs(ctx, st, result.Stats.SourcePath, result.Messages, true, cache); err != nil {
			return err
		}
	}
	if err := validateImportMediaRefs(result, filepath.Join(filepath.Dir(st.Path()), "media")); err != nil {
		return err
	}
	refreshImportMediaStats(result)
	if strings.TrimSpace(chatFilter) == "" {
		if restore {
			return st.ReplaceAll(ctx, result.Stats, result.Contacts, result.Chats, result.Folders, result.FolderChats, result.Topics, result.Messages)
		}
		return st.MergeAll(ctx, result.Stats, result.Contacts, result.Chats, result.Folders, result.FolderChats, result.Topics, result.Messages)
	}
	if restore {
		return errors.New("--restore cannot be combined with --chat")
	}
	if len(result.Chats) == 0 {
		return fmt.Errorf("telegram import returned no chats for --chat %s", chatFilter)
	}
	combined := telegramdesktop.ImportResult{Stats: result.Stats, Folders: result.Folders}
	seenContacts := make(map[string]struct{})
	for _, chat := range result.Chats {
		partial := importResultForChat(*result, chat.JID)
		combined.Chats = append(combined.Chats, partial.Chats...)
		combined.FolderChats = append(combined.FolderChats, partial.FolderChats...)
		combined.Topics = append(combined.Topics, partial.Topics...)
		combined.Messages = append(combined.Messages, partial.Messages...)
		for _, contact := range partial.Contacts {
			if _, ok := seenContacts[contact.JID]; ok {
				continue
			}
			seenContacts[contact.JID] = struct{}{}
			combined.Contacts = append(combined.Contacts, contact)
		}
	}
	return st.MergeAll(ctx, combined.Stats, combined.Contacts, combined.Chats, combined.Folders, combined.FolderChats, combined.Topics, combined.Messages)
}

func prepareImportResultSource(result *telegramdesktop.ImportResult) error {
	sourcePath := strings.TrimSpace(result.Stats.SourcePath)
	if sourcePath == "" {
		return errors.New("import source path is required")
	}
	if result.Stats.SourcePathCanonical {
		if !filepath.IsAbs(sourcePath) {
			return errors.New("canonical import source path is not absolute")
		}
		result.Stats.SourcePath = filepath.Clean(sourcePath)
		return nil
	}
	sourcePath, err := filepath.Abs(filepath.Clean(sourcePath))
	if err != nil {
		return fmt.Errorf("resolve import source path: %w", err)
	}
	sourcePath, err = filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return fmt.Errorf("resolve import source target: %w", err)
	}
	result.Stats.SourcePath = sourcePath
	result.Stats.SourcePathCanonical = true
	return nil
}

func promoteImportMedia(result *telegramdesktop.ImportResult, stageDir, archiveDir string) error {
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		return err
	}
	archiveInfo, err := os.Lstat(archiveDir)
	if err != nil {
		return err
	}
	if !archiveInfo.IsDir() || archiveInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("media archive %q is not a regular directory", archiveDir)
	}
	resolvedArchiveDir, err := filepath.EvalSymlinks(archiveDir)
	if err != nil {
		return err
	}
	promoted := make(map[string]string)
	promote := func(path string) (string, error) {
		path = strings.TrimSpace(path)
		if path == "" {
			return "", nil
		}
		if destination, ok := promoted[path]; ok {
			return destination, nil
		}
		if resolvedPathWithin(resolvedArchiveDir, path) {
			validated, err := validateArchivedMedia(path, resolvedArchiveDir)
			if err != nil {
				return "", err
			}
			promoted[path] = validated
			return validated, nil
		}
		stagedPath := path
		relative, err := filepath.Rel(stageDir, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("imported media path %q is outside staging directory", path)
		}
		validatedStage, err := validateArchivedMedia(path, stageDir)
		if err != nil {
			return "", err
		}
		path = validatedStage
		digest := filepath.Base(path)
		expectedRelative := filepath.Join(digest[:2], digest)
		if filepath.Clean(relative) != expectedRelative {
			return "", fmt.Errorf("imported media path %q has unexpected archive layout", stagedPath)
		}
		destinationDir := filepath.Join(resolvedArchiveDir, digest[:2])
		if err := os.Mkdir(destinationDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		destinationInfo, err := os.Lstat(destinationDir)
		if err != nil {
			return "", err
		}
		if !destinationInfo.IsDir() || destinationInfo.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("media archive directory %q is not a regular directory", destinationDir)
		}
		destination := filepath.Join(destinationDir, digest)
		if err := os.Link(path, destination); err == nil {
			if err := os.Remove(path); err != nil {
				return "", err
			}
			validated, err := validateArchivedMedia(destination, resolvedArchiveDir)
			if err != nil {
				return "", err
			}
			destination = validated
		} else if errors.Is(err, os.ErrExist) {
			validated, err := validateArchivedMedia(destination, resolvedArchiveDir)
			if err != nil {
				return "", err
			}
			stagedDigest, err := fileSHA256(path)
			if err != nil {
				return "", err
			}
			if stagedDigest != filepath.Base(validated) {
				return "", fmt.Errorf("staged media %q does not match existing archive file %q", path, validated)
			}
			if err := os.Remove(path); err != nil {
				return "", err
			}
			destination = validated
		} else {
			return "", err
		}
		promoted[stagedPath] = destination
		return destination, nil
	}
	for i := range result.Messages {
		path, err := promote(result.Messages[i].MediaPath)
		if err != nil {
			return err
		}
		result.Messages[i].MediaPath = path
	}
	for i := range result.Contacts {
		path, err := promote(result.Contacts[i].AvatarPath)
		if err != nil {
			return err
		}
		result.Contacts[i].AvatarPath = path
	}
	return nil
}

func pathWithin(root, path string) bool {
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return false
	}
	path, err = filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func resolvedPathWithin(root, path string) bool {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	return pathWithin(resolvedRoot, resolvedPath)
}

func validateArchivedMedia(path, archiveDir string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("archived media %q is not a regular file", path)
	}
	resolvedArchive, err := filepath.EvalSymlinks(archiveDir)
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !pathWithin(resolvedArchive, resolvedPath) {
		return "", fmt.Errorf("archived media %q resolves outside archive directory", path)
	}
	digest, err := fileSHA256(resolvedPath)
	if err != nil {
		return "", err
	}
	if filepath.Base(resolvedPath) != digest || filepath.Base(filepath.Dir(resolvedPath)) != digest[:2] {
		return "", fmt.Errorf("archived media %q does not match its content hash", path)
	}
	return resolvedPath, nil
}

func validateImportMediaRefs(result *telegramdesktop.ImportResult, archiveDir string) error {
	for i := range result.Messages {
		path := strings.TrimSpace(result.Messages[i].MediaPath)
		if path == "" {
			continue
		}
		validated, err := validateArchivedMedia(path, archiveDir)
		if err != nil {
			return err
		}
		result.Messages[i].MediaPath = validated
	}
	for i := range result.Contacts {
		path := strings.TrimSpace(result.Contacts[i].AvatarPath)
		if path == "" {
			continue
		}
		validated, err := validateArchivedMedia(path, archiveDir)
		if err != nil {
			return err
		}
		result.Contacts[i].AvatarPath = validated
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
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

type mediaRefCache struct {
	loaded     bool
	sourcePath string
	refs       map[int64]telegramdesktop.ExistingMediaRef
	loads      int
}

func (c *mediaRefCache) get(ctx context.Context, st *store.Store) (string, map[int64]telegramdesktop.ExistingMediaRef, error) {
	if c != nil && c.loaded {
		return c.sourcePath, c.refs, nil
	}
	sourcePath, refs, err := existingMediaRefs(ctx, st)
	if err != nil {
		return "", nil, err
	}
	if c != nil {
		c.sourcePath = sourcePath
		c.refs = refs
		c.loaded = true
		c.loads++
	}
	return sourcePath, refs, nil
}

func existingMediaRefsForImport(ctx context.Context, st *store.Store, cache *mediaRefCache) (string, []telegramdesktop.ExistingMediaRef, error) {
	sourcePath, refsByPK, err := cache.get(ctx, st)
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

func preserveExistingMediaRefs(ctx context.Context, st *store.Store, sourcePath string, messages []store.Message, allowLegacySource bool, cache *mediaRefCache) error {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return nil
	}
	existingSourcePath, refs, err := cache.get(ctx, st)
	if err != nil || (!allowLegacySource && existingSourcePath != sourcePath) {
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
	existing, err := st.MediaRefs(ctx)
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
		export := control.ContactExport{Contacts: exportContacts(contacts)}
		if err := control.ValidateContactExport(export); err != nil {
			return err
		}
		return r.print(export)
	})
}

func exportContacts(contacts []store.Contact) []control.Contact {
	out := make([]control.Contact, 0, len(contacts))
	byPhone := map[string]store.Contact{}
	phoneOrder := make([]string, 0, len(contacts))
	for _, contact := range contacts {
		if isTelegramServiceContact(contact) {
			continue
		}
		name := contactDisplayName(contact)
		phone := strings.TrimSpace(contact.Phone)
		if name == "" || phone == "" {
			continue
		}
		if current, ok := byPhone[phone]; ok {
			if preferContactExportName(contact, current) {
				byPhone[phone] = contact
			}
		} else {
			byPhone[phone] = contact
			phoneOrder = append(phoneOrder, phone)
		}
	}
	for _, phone := range phoneOrder {
		contact := byPhone[phone]
		name := contactDisplayName(contact)
		out = append(out, control.Contact{DisplayName: name, PhoneNumbers: []string{phone}})
	}
	return out
}

func preferContactExportName(candidate, current store.Contact) bool {
	if candidate.UpdatedAt.After(current.UpdatedAt) {
		return true
	}
	if current.UpdatedAt.After(candidate.UpdatedAt) {
		return false
	}
	return len([]rune(contactDisplayName(candidate))) > len([]rune(contactDisplayName(current)))
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

func isTelegramServiceContact(contact store.Contact) bool {
	return strings.TrimSpace(contact.Phone) == "42777" &&
		sameContactText(contact.FullName, "Telegram") &&
		sameContactText(contact.FirstName, "Telegram") &&
		strings.TrimSpace(contact.LastName) == "" &&
		strings.TrimSpace(contact.Username) == ""
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
		return usageErr(errors.New("backup needs subcommand: init, push, pull, status, snapshots"))
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
	case "snapshots":
		return r.backupSnapshots(args[1:])
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
	fs.StringVar(&opts.Ref, "ref", "", "")
	fs.StringVar(&opts.Tag, "tag", "", "")
	fs.IntVar(&opts.Limit, "limit", 20, "")
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
	fs.BoolVar(&opts.Restore, "restore", false, "")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() != 0 {
		return usageErr(errors.New("backup pull takes flags only"))
	}
	if strings.TrimSpace(opts.Ref) != "" && !opts.Restore {
		return usageErr(errors.New("backup pull --ref requires --restore because historical snapshots replace local rows"))
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

func (r *runtime) backupSnapshots(args []string) error {
	fs, opts, _ := backupFlags("telecrawl backup snapshots")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() != 0 {
		return usageErr(errors.New("backup snapshots takes flags only"))
	}
	if opts.Limit < 1 {
		return usageErr(errors.New("backup snapshots --limit must be greater than zero"))
	}
	snapshots, repo, err := backup.Snapshots(r.ctx, *opts)
	if err != nil {
		return err
	}
	if r.json {
		return r.print(map[string]any{"repo": repo, "snapshots": snapshots})
	}
	return r.print(snapshots)
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
	case []backup.Snapshot:
		for _, snapshot := range value {
			ref := snapshot.Ref
			if len(snapshot.Tags) > 0 {
				ref = snapshot.Tags[0]
			}
			if _, err := fmt.Fprintf(r.stdout, "%s\t%s\t%d\t%d\t%s\n", ref, snapshot.Exported.Format(time.RFC3339), snapshot.Counts.Messages, snapshot.Shards, strings.Join(snapshot.Tags, ",")); err != nil {
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
  telecrawl [--json] import [--path PATH] [--chat ID] [--dialogs-limit N] [--messages-limit N] [--fetch-media] [--adopt-source] [--restore]
  telecrawl [--json] status
  telecrawl [--json] folders
  telecrawl [--json] contacts [--limit N]
  telecrawl [--json] contacts export
  telecrawl [--json] chats [--limit N] [--unread] [--folder ID]
  telecrawl [--json] topics --chat ID [--limit N]
  telecrawl [--json] messages [--chat ID] [--topic ID] [--limit N] [--after DATE]
  telecrawl [--json] search "query" [--chat ID] [--topic ID]
  telecrawl [--json] backup init|push|pull [--restore]|status|snapshots
  telecrawl version

notes:
  import auto-detects Telegram Desktop tdata or native macOS Postbox data
  imports and backup pulls merge by default; --restore replaces the entire existing archive
  --adopt-source non-destructively records a verified legacy archive's current source
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
