package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/telecrawl/internal/store"
	"github.com/openclaw/telecrawl/internal/telegramdesktop"
)

func TestStoreImportResultUpsertsReturnedAccountScopedChats(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "telecrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	sourcePath := t.TempDir()

	full := accountScopedImportResult("old")
	full.Stats.SourcePath = sourcePath
	if err := storeImportResult(ctx, st, &full, "", false); err != nil {
		t.Fatal(err)
	}
	partial := accountScopedImportResult("new")
	partial.Stats.SourcePath = sourcePath
	if err := storeImportResult(ctx, st, &partial, "100", false); err != nil {
		t.Fatal(err)
	}

	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Chats != 2 || status.Messages != 2 {
		t.Fatalf("status = chats %d messages %d, want 2/2", status.Chats, status.Messages)
	}
	messages, err := st.Messages(ctx, store.MessageFilter{Limit: 10, Asc: true})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{messages[0].Text, messages[1].Text}
	want := []string{"new a", "new b"}
	if !slices.Equal(got, want) {
		t.Fatalf("messages = %v, want %v", got, want)
	}
}

func TestStoreImportResultMergesBoundedWindowUnlessReplace(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "telecrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	older := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	sourcePath := t.TempDir()
	full := telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: sourcePath, SourceIdentity: "test:fixture", StartedAt: older, FinishedAt: older},
		Chats: []store.Chat{{JID: "100", Kind: "chat", Name: "fixture", LastMessageAt: newer, MessageCount: 2}},
		Messages: []store.Message{
			{SourcePK: 1, ChatJID: "100", ChatName: "fixture", MessageID: "1", Timestamp: older, Text: "older"},
			{SourcePK: 2, ChatJID: "100", ChatName: "fixture", MessageID: "2", Timestamp: newer, Text: "newer"},
		},
	}
	if err := storeImportResult(ctx, st, &full, "", false); err != nil {
		t.Fatal(err)
	}
	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(full.Stats.SourcePath) || status.LastSource != full.Stats.SourcePath {
		t.Fatalf("source paths = result %q stored %q, want matching absolute paths", full.Stats.SourcePath, status.LastSource)
	}

	windowed := telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: sourcePath, SourceIdentity: "test:fixture", StartedAt: newer, FinishedAt: newer},
		Chats: []store.Chat{{JID: "100", Kind: "chat", Name: "fixture", LastMessageAt: newer, MessageCount: 2}},
		Messages: []store.Message{
			{SourcePK: 2, ChatJID: "100", ChatName: "fixture", MessageID: "2", Timestamp: newer, Text: "newer updated"},
		},
	}
	if err := storeImportResult(ctx, st, &windowed, "", false); err != nil {
		t.Fatal(err)
	}
	messages, err := st.Messages(ctx, store.MessageFilter{ChatJID: "100", Limit: 10, Asc: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("merged messages = %d, want 2", len(messages))
	}
	if got, want := []string{messages[0].Text, messages[1].Text}, []string{"older", "newer updated"}; !slices.Equal(got, want) {
		t.Fatalf("merged messages = %v, want %v", got, want)
	}

	if err := storeImportResult(ctx, st, &windowed, "", true); err != nil {
		t.Fatal(err)
	}
	messages, err = st.Messages(ctx, store.MessageFilter{ChatJID: "100", Limit: 10, Asc: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Text != "newer updated" {
		t.Fatalf("replaced messages = %#v, want only windowed message", messages)
	}
}

func TestStoreImportResultRejectsRetargetedSourceSymlink(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "telecrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	targetA := t.TempDir()
	targetB := t.TempDir()
	link := filepath.Join(t.TempDir(), "current")
	if err := os.Symlink(targetA, link); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	result := telegramdesktop.ImportResult{
		Stats:    store.ImportStats{SourcePath: link, SourceIdentity: "test:a", FinishedAt: now},
		Chats:    []store.Chat{{JID: "100", Kind: "chat", Name: "fixture"}},
		Messages: []store.Message{{SourcePK: 1, ChatJID: "100", MessageID: "1", Timestamp: now, Text: "original"}},
	}
	if err := storeImportResult(ctx, st, &result, "", false); err != nil {
		t.Fatal(err)
	}
	canonicalTargetA, err := filepath.EvalSymlinks(targetA)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.SourcePath != canonicalTargetA {
		t.Fatalf("canonical source = %q, want %q", result.Stats.SourcePath, canonicalTargetA)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetB, link); err != nil {
		t.Fatal(err)
	}
	result.Stats.SourcePath = link
	result.Stats.SourceIdentity = "test:b"
	result.Messages[0].Text = "wrong source"
	err = storeImportResult(ctx, st, &result, "", false)
	if err == nil || !strings.Contains(err.Error(), "use --restore") {
		t.Fatalf("error = %v, want source mismatch requiring --restore", err)
	}
	messages, err := st.Messages(ctx, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Text != "original" {
		t.Fatalf("messages = %#v, want original preserved", messages)
	}
}

func TestPrepareImportResultSourceKeepsPinnedCanonicalPath(t *testing.T) {
	t.Parallel()
	pinned := filepath.Join(t.TempDir(), "source-that-may-move")
	result := telegramdesktop.ImportResult{Stats: store.ImportStats{SourcePath: pinned, SourcePathCanonical: true}}
	if err := prepareImportResultSource(&result); err != nil {
		t.Fatal(err)
	}
	if result.Stats.SourcePath != pinned {
		t.Fatalf("source_path = %q, want pinned %q", result.Stats.SourcePath, pinned)
	}
}

func TestPromoteImportMediaMovesStagedFiles(t *testing.T) {
	t.Parallel()
	stage := t.TempDir()
	archive := filepath.Join(t.TempDir(), "media")
	stagedPath := writeContentAddressedMedia(t, stage, []byte("fixture media"))
	result := telegramdesktop.ImportResult{
		Messages: []store.Message{{MediaPath: stagedPath}},
		Contacts: []store.Contact{{AvatarPath: stagedPath}},
	}
	if err := promoteImportMedia(&result, stage, archive); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(archive, filepath.Base(filepath.Dir(stagedPath)), filepath.Base(stagedPath))
	want, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	if result.Messages[0].MediaPath != want || result.Contacts[0].AvatarPath != want {
		t.Fatalf("promoted paths = message %q avatar %q, want %q", result.Messages[0].MediaPath, result.Contacts[0].AvatarPath, want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fixture media" {
		t.Fatalf("promoted media = %q", data)
	}
}

func TestPromoteImportMediaKeepsExistingArchivePath(t *testing.T) {
	t.Parallel()
	archive := filepath.Join(t.TempDir(), "media")
	existing := writeContentAddressedMedia(t, archive, []byte("existing"))
	existing, err := filepath.EvalSymlinks(existing)
	if err != nil {
		t.Fatal(err)
	}
	result := telegramdesktop.ImportResult{Messages: []store.Message{{MediaPath: existing}}}
	if err := promoteImportMedia(&result, t.TempDir(), archive); err != nil {
		t.Fatal(err)
	}
	if result.Messages[0].MediaPath != existing {
		t.Fatalf("media path = %q, want existing %q", result.Messages[0].MediaPath, existing)
	}
	if _, err := os.Stat(existing); err != nil {
		t.Fatalf("existing archive media removed: %v", err)
	}
}

func TestPromoteImportMediaConcurrentReuseKeepsSharedFile(t *testing.T) {
	t.Parallel()
	archive := filepath.Join(t.TempDir(), "media")
	firstStage := t.TempDir()
	secondStage := t.TempDir()
	firstPath := writeContentAddressedMedia(t, firstStage, []byte("shared"))
	secondPath := writeContentAddressedMedia(t, secondStage, []byte("shared"))
	first := telegramdesktop.ImportResult{Messages: []store.Message{{MediaPath: firstPath}}}
	second := telegramdesktop.ImportResult{Messages: []store.Message{{MediaPath: secondPath}}}

	if err := promoteImportMedia(&first, firstStage, archive); err != nil {
		t.Fatal(err)
	}
	if err := promoteImportMedia(&second, secondStage, archive); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Messages[0].MediaPath); err != nil {
		t.Fatalf("shared media missing after reuse: %v", err)
	}
}

func TestPromoteImportMediaKeepsContentOnPartialFailure(t *testing.T) {
	t.Parallel()
	stage := t.TempDir()
	archive := filepath.Join(t.TempDir(), "media")
	staged := writeContentAddressedMedia(t, stage, []byte("first"))
	result := telegramdesktop.ImportResult{Messages: []store.Message{{MediaPath: staged}, {MediaPath: filepath.Join(t.TempDir(), "outside")}}}
	if err := promoteImportMedia(&result, stage, archive); err == nil {
		t.Fatal("promotion succeeded with outside path")
	}
	promoted := filepath.Join(archive, filepath.Base(filepath.Dir(staged)), filepath.Base(staged))
	if _, err := os.Stat(promoted); err != nil {
		t.Fatalf("immutable promoted media removed after partial failure: %v", err)
	}
}

func TestPromoteImportMediaRejectsCorruptExistingDestination(t *testing.T) {
	t.Parallel()
	stage := t.TempDir()
	archive := filepath.Join(t.TempDir(), "media")
	staged := writeContentAddressedMedia(t, stage, []byte("correct"))
	destination := filepath.Join(archive, filepath.Base(filepath.Dir(staged)), filepath.Base(staged))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := telegramdesktop.ImportResult{Messages: []store.Message{{MediaPath: staged}}}
	if err := promoteImportMedia(&result, stage, archive); err == nil {
		t.Fatal("promotion accepted corrupt existing destination")
	}
	data, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "correct" {
		t.Fatalf("staged media = %q, want preserved correct copy", data)
	}
}

func TestPromoteImportMediaRejectsSymlinkedArchivePrefix(t *testing.T) {
	t.Parallel()
	stage := t.TempDir()
	archive := filepath.Join(t.TempDir(), "media")
	staged := writeContentAddressedMedia(t, stage, []byte("redirected"))
	digest := filepath.Base(staged)
	outside := t.TempDir()
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(archive, digest[:2])); err != nil {
		t.Fatal(err)
	}
	result := telegramdesktop.ImportResult{Messages: []store.Message{{MediaPath: staged}}}
	if err := promoteImportMedia(&result, stage, archive); err == nil {
		t.Fatal("promotion accepted symlinked archive prefix")
	}
	if _, err := os.Stat(filepath.Join(outside, digest)); !os.IsNotExist(err) {
		t.Fatalf("escaped media stat err = %v, want not exists", err)
	}
}

func writeContentAddressedMedia(t *testing.T, root string, data []byte) string {
	t.Helper()
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	path := filepath.Join(root, digest[:2], digest)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStoreImportResultValidatesAllFilteredChatsBeforeLegacyAdoption(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "telecrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	legacyChats := []store.Chat{{JID: "111", Kind: "chat"}, {JID: "222", Kind: "chat"}}
	legacyMessages := []store.Message{
		{SourcePK: 1, ChatJID: "111", MessageID: "1", Timestamp: now, Text: "old a"},
		{SourcePK: 2, ChatJID: "222", MessageID: "2", Timestamp: now, Text: "old b"},
	}
	if err := st.ReplaceAll(ctx, store.ImportStats{SourcePath: "relative-source", FinishedAt: now}, nil, legacyChats, nil, nil, nil, legacyMessages); err != nil {
		t.Fatal(err)
	}
	result := telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: t.TempDir(), SourceIdentity: "test:current", FinishedAt: now.Add(time.Minute)},
		Chats: legacyChats,
		Messages: []store.Message{
			{SourcePK: 1, ChatJID: "111", MessageID: "1", Timestamp: now, Text: "new a"},
			{SourcePK: 2, ChatJID: "222", MessageID: "different", Timestamp: now, Text: "wrong source"},
		},
	}
	err = storeImportResult(ctx, st, &result, "100", false)
	if err == nil || !strings.Contains(err.Error(), "use --adopt-source") {
		t.Fatalf("error = %v, want legacy identity mismatch", err)
	}
	messages, err := st.Messages(ctx, store.MessageFilter{Limit: 10, Asc: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := []string{messages[0].Text, messages[1].Text}, []string{"old a", "old b"}; !slices.Equal(got, want) {
		t.Fatalf("messages = %v, want atomic preservation %v", got, want)
	}
	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastSource != "relative-source" {
		t.Fatalf("source = %q, want legacy source unchanged", status.LastSource)
	}
}

func TestImportResultForChatFiltersContacts(t *testing.T) {
	result := accountScopedImportResult("filtered")
	partial := importResultForChat(result, "111")

	got := make([]string, 0, len(partial.Contacts))
	for _, contact := range partial.Contacts {
		got = append(got, contact.JID)
	}
	want := []string{"111", "10"}
	if !slices.Equal(got, want) {
		t.Fatalf("contacts = %v, want %v", got, want)
	}
}

func TestContactsExportUsesContractShapeAndSkipsUnsafeNames(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "telecrawl.db")
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	contacts := make([]store.Contact, 0, 104)
	messages := make([]store.Message, 0, 104)
	addContact := func(contact store.Contact, withEvidence bool) {
		contacts = append(contacts, contact)
		if !withEvidence {
			return
		}
		messages = append(messages, store.Message{
			SourcePK:  int64(len(messages) + 1),
			ChatJID:   contact.JID,
			MessageID: fmt.Sprintf("msg-%d", len(messages)+1),
			Timestamp: time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC),
			Text:      "contact evidence",
		})
	}
	for i := 0; i < 101; i++ {
		addContact(store.Contact{
			JID:      "safe-" + string(rune('a'+(i%26))) + "-" + string(rune('a'+((i/26)%26))),
			Phone:    fmt.Sprintf("+155501%05d", i),
			FullName: "Safe Person",
		}, true)
	}
	addContact(store.Contact{JID: "first-last", Phone: "+15559990001", FirstName: "First", LastName: "Last"}, true)
	addContact(store.Contact{JID: "first-last-duplicate", Phone: "+15559990001", FirstName: "First", LastName: "Last"}, true)
	addContact(store.Contact{JID: "recent-short", Phone: "+15559990008", FullName: "Recent", UpdatedAt: time.Unix(200, 0).UTC()}, true)
	addContact(store.Contact{JID: "older-richer", Phone: "+15559990008", FullName: "Older Richer Name", UpdatedAt: time.Unix(100, 0).UTC()}, true)
	addContact(store.Contact{JID: "equal-short", Phone: "+15559990009", FullName: "Pim"}, true)
	addContact(store.Contact{JID: "equal-richer", Phone: "+15559990009", FullName: "Pim van den Berg"}, true)
	addContact(store.Contact{JID: "username-only", Phone: "+15559990002", Username: "handle", FullName: "@handle"}, true)
	addContact(store.Contact{JID: "bare-username-only", Phone: "+15559990006", Username: "handle", FullName: "Handle"}, true)
	addContact(store.Contact{JID: "phone-only", Phone: "+15559990003", FullName: "+15559990003"}, true)
	addContact(store.Contact{JID: "jid-only", Phone: "+15559990004", FullName: "jid-only"}, true)
	addContact(store.Contact{JID: "blank-name", Phone: "+15559990005"}, true)
	addContact(store.Contact{JID: "no-phone", FullName: "No Phone"}, true)
	addContact(store.Contact{JID: "short-phone-person", Phone: "12345", FullName: "Short Phone Person"}, true)
	addContact(store.Contact{JID: "telegram-service", Phone: "42777", FullName: "Telegram", FirstName: "Telegram"}, true)
	addContact(store.Contact{JID: "stale-peer", Phone: "+15559990007", FullName: "Stale Peer"}, false)
	if err := st.ReplaceAll(ctx, store.ImportStats{}, contacts, nil, nil, nil, nil, messages); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	err = Run(ctx, []string{"--json", "--db", db, "contacts", "export"}, &out, &errOut)
	if err != nil {
		t.Fatalf("contacts export: %v stderr=%s", err, errOut.String())
	}
	var payload struct {
		Contacts []struct {
			DisplayName  string   `json:"display_name"`
			PhoneNumbers []string `json:"phone_numbers"`
			JID          string   `json:"jid"`
			Username     string   `json:"username"`
		} `json:"contacts"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("json = %s err=%v", out.String(), err)
	}
	assertContactExportKeys(t, out.Bytes())
	if len(payload.Contacts) != 105 {
		t.Fatalf("contacts = %d, want 105", len(payload.Contacts))
	}
	var sawFirstLast, sawShortPhonePerson, sawRecent, sawRicherEqual bool
	firstLastCount := 0
	for _, contact := range payload.Contacts {
		if contact.DisplayName == "First Last" {
			sawFirstLast = true
			if contact.PhoneNumbers[0] == "+15559990001" {
				firstLastCount++
			}
		}
		if contact.DisplayName == "Recent" && contact.PhoneNumbers[0] == "+15559990008" {
			sawRecent = true
		}
		if contact.DisplayName == "Pim van den Berg" && contact.PhoneNumbers[0] == "+15559990009" {
			sawRicherEqual = true
		}
		if contact.DisplayName == "Short Phone Person" && contact.PhoneNumbers[0] == "12345" {
			sawShortPhonePerson = true
		}
		if contact.DisplayName == "" || len(contact.PhoneNumbers) != 1 {
			t.Fatalf("bad contact = %#v", contact)
		}
		if contact.JID != "" || contact.Username != "" {
			t.Fatalf("leaked source fields = %#v", contact)
		}
		if strings.HasPrefix(contact.DisplayName, "@") || strings.HasPrefix(contact.DisplayName, "+") || contact.DisplayName == "jid-only" {
			t.Fatalf("unsafe display name exported: %#v", contact)
		}
		if contact.DisplayName == "Handle" || contact.PhoneNumbers[0] == "42777" {
			t.Fatalf("unsafe contact exported: %#v", contact)
		}
		if contact.DisplayName == "Stale Peer" {
			t.Fatalf("stale contact without conversation evidence exported: %#v", contact)
		}
		if contact.DisplayName == "Older Richer Name" || contact.DisplayName == "Pim" {
			t.Fatalf("wrong duplicate contact name exported: %#v", contact)
		}
	}
	if !sawFirstLast {
		t.Fatalf("missing composed first/last name: %#v", payload.Contacts)
	}
	if firstLastCount != 1 {
		t.Fatalf("first/last duplicate count = %d, want 1", firstLastCount)
	}
	if !sawShortPhonePerson {
		t.Fatalf("missing short phone person: %#v", payload.Contacts)
	}
	if !sawRecent {
		t.Fatalf("missing newer duplicate contact name: %#v", payload.Contacts)
	}
	if !sawRicherEqual {
		t.Fatalf("missing richer equal-time contact name: %#v", payload.Contacts)
	}
}

func assertContactExportKeys(t *testing.T, data []byte) {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	contactsJSON, ok := root["contacts"]
	if !ok || len(root) != 1 {
		t.Fatalf("root keys = %#v, want only contacts", root)
	}
	var contacts []map[string]json.RawMessage
	if err := json.Unmarshal(contactsJSON, &contacts); err != nil {
		t.Fatal(err)
	}
	for _, contact := range contacts {
		if _, ok := contact["display_name"]; !ok {
			t.Fatalf("contact keys = %#v, missing display_name", contact)
		}
		if _, ok := contact["phone_numbers"]; !ok {
			t.Fatalf("contact keys = %#v, missing phone_numbers", contact)
		}
		if len(contact) != 2 {
			t.Fatalf("contact keys = %#v, want only display_name and phone_numbers", contact)
		}
	}
}

func TestMetadataAdvertisesContactExport(t *testing.T) {
	manifest := controlManifest()
	command, ok := manifest.Commands["contact-export"]
	if !ok {
		t.Fatalf("commands = %#v", manifest.Commands)
	}
	if command.Mutates || !command.JSON {
		t.Fatalf("contact-export command = %#v", command)
	}
	want := []string{"telecrawl", "--json", "contacts", "export"}
	if !slices.Equal(command.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", command.Argv, want)
	}
}

func TestStoreImportResultPreservesArchivedMediaOnReimport(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "telecrawl.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Unix(1_800_000_000, 0).UTC()
	sourcePath := t.TempDir()
	archivedData := []byte("archived")
	archivedSize := int64(len(archivedData))
	archivedPath := writeContentAddressedMedia(t, filepath.Join(filepath.Dir(dbPath), "media"), archivedData)
	first := telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: sourcePath, SourceIdentity: "test:media", StartedAt: now, FinishedAt: now},
		Chats: []store.Chat{{JID: "100", Kind: "chat", Name: "saved media", LastMessageAt: now, MessageCount: 1}},
		Messages: []store.Message{{
			SourcePK:  9,
			ChatJID:   "100",
			ChatName:  "saved media",
			MessageID: "0:9",
			Timestamp: now,
			MediaType: "photo",
			MediaPath: archivedPath,
			MediaSize: archivedSize,
		}},
	}
	if err := storeImportResult(ctx, st, &first, "", false); err != nil {
		t.Fatal(err)
	}
	archivedPath = first.Messages[0].MediaPath

	second := telegramdesktop.ImportResult{
		Stats: first.Stats,
		Chats: first.Chats,
		Messages: []store.Message{{
			SourcePK:  9,
			ChatJID:   "100",
			ChatName:  "saved media",
			MessageID: "0:9",
			Timestamp: now,
		}},
	}
	if err := storeImportResult(ctx, st, &second, "", false); err != nil {
		t.Fatal(err)
	}
	if second.Stats.MediaMessages != 1 || second.Stats.MediaFiles != 1 || second.Stats.MediaBytes != archivedSize {
		t.Fatalf("refreshed stats = %+v, want preserved media stats", second.Stats)
	}

	messages, err := st.Messages(ctx, store.MessageFilter{HasMedia: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if messages[0].MediaPath != archivedPath || messages[0].MediaSize != archivedSize {
		t.Fatalf("media ref = path %q size %d, want %q/%d", messages[0].MediaPath, messages[0].MediaSize, archivedPath, archivedSize)
	}
	if messages[0].MediaType != "photo" {
		t.Fatalf("media type = %q, want preserved photo", messages[0].MediaType)
	}
	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.MediaMessages != 1 {
		t.Fatalf("media_messages = %d, want 1", status.MediaMessages)
	}

	otherSource := telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: sourcePath, SourceIdentity: "test:other", StartedAt: now, FinishedAt: now},
		Chats: first.Chats,
		Messages: []store.Message{{
			SourcePK:  9,
			ChatJID:   "100",
			ChatName:  "saved media",
			MessageID: "0:9",
			Timestamp: now,
			MediaType: "photo",
		}},
	}
	if err := storeImportResult(ctx, st, &otherSource, "", true); err != nil {
		t.Fatal(err)
	}
	messages, err = st.Messages(ctx, store.MessageFilter{HasMedia: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages after source switch = %d, want 1", len(messages))
	}
	if messages[0].MediaPath != "" || messages[0].MediaSize != 0 {
		t.Fatalf("media ref crossed source boundary: path %q size %d", messages[0].MediaPath, messages[0].MediaSize)
	}
}

func TestPrintImportStatsIncludesMediaArchiveStats(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	now := time.Unix(1_800_000_000, 0).UTC()
	r := &runtime{stdout: &out}

	if err := r.print(store.ImportStats{
		SourcePath:    "postbox",
		DBPath:        "/tmp/telecrawl.db",
		Chats:         2,
		Messages:      3,
		MediaMessages: 2,
		MediaFiles:    1,
		MediaBytes:    1234,
		StartedAt:     now,
		FinishedAt:    now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"media_files: 1\n", "media_bytes: 1234\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "remote_media_downloads:") || strings.Contains(out.String(), "remote_media_missing:") {
		t.Fatalf("zero remote media stats should be omitted:\n%s", out.String())
	}
}

func TestPrintImportStatsIncludesRemoteMediaWhenUsed(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	now := time.Unix(1_800_000_000, 0).UTC()
	r := &runtime{stdout: &out}

	if err := r.print(store.ImportStats{
		SourcePath:             "postbox",
		DBPath:                 "/tmp/telecrawl.db",
		RemoteMediaCandidates:  4,
		RemoteMediaAttempted:   3,
		RemoteMediaDownloads:   2,
		RemoteMediaMissing:     1,
		RemoteMediaUnavailable: 1,
		RemoteMediaTimeouts:    0,
		RemoteMediaErrors:      0,
		StartedAt:              now,
		FinishedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"remote_media_candidates: 4\n",
		"remote_media_attempted: 3\n",
		"remote_media_downloads: 2\n",
		"remote_media_missing: 1\n",
		"remote_media_unavailable: 1\n",
		"remote_media_timeouts: 0\n",
		"remote_media_errors: 0\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestPrintImportStatsIncludesRemoteMediaDiagnosticsWithoutDownloads(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	now := time.Unix(1_800_000_000, 0).UTC()
	r := &runtime{stdout: &out}

	if err := r.print(store.ImportStats{
		SourcePath:             "postbox",
		DBPath:                 "/tmp/telecrawl.db",
		RemoteMediaCandidates:  4,
		RemoteMediaAttempted:   4,
		RemoteMediaUnavailable: 4,
		StartedAt:              now,
		FinishedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"remote_media_candidates: 4\n",
		"remote_media_attempted: 4\n",
		"remote_media_downloads: 0\n",
		"remote_media_missing: 0\n",
		"remote_media_unavailable: 4\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestUsageDocumentsMediaFetchOptIn(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	printUsage(&out)
	if !strings.Contains(out.String(), "--fetch-media") {
		t.Fatalf("usage should document media fetch opt-in:\n%s", out.String())
	}
}

func TestUsageDocumentsExplicitRestore(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	printUsage(&out)
	if !strings.Contains(out.String(), "--restore replaces the entire existing archive") {
		t.Fatalf("usage should document explicit restore:\n%s", out.String())
	}
}

func TestImportRejectsReplaceWithChat(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	err := Run(context.Background(), []string{"import", "--replace", "--chat", "100"}, &out, &errOut)
	if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "--restore cannot be combined with --chat") {
		t.Fatalf("error = %v (exit %d), want usage error", err, ExitCode(err))
	}
}

func TestImportRejectsReplaceWithAdoptSource(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	err := Run(context.Background(), []string{"import", "--replace", "--adopt-source"}, &out, &errOut)
	if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "--restore cannot be combined with --adopt-source") {
		t.Fatalf("error = %v (exit %d), want usage error", err, ExitCode(err))
	}
}

func TestImportRejectsRestoreWithChatOrAdoptSource(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"import", "--restore", "--chat", "100"},
		{"import", "--restore", "--adopt-source"},
		{"import", "--restore", "--replace"},
	} {
		var out, errOut bytes.Buffer
		err := Run(context.Background(), args, &out, &errOut)
		if err == nil || ExitCode(err) != 2 {
			t.Fatalf("args=%v error=%v, want usage error", args, err)
		}
	}
}

func TestBackupHistoricalPullRequiresRestoreBeforeIO(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	err := Run(context.Background(), []string{"backup", "pull", "--ref", "snapshot/old"}, &out, &errOut)
	if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "requires --restore") {
		t.Fatalf("error = %v, want historical restore usage error", err)
	}
}

func accountScopedImportResult(label string) telegramdesktop.ImportResult {
	now := time.Unix(1_800_000_000, 0).UTC()
	return telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: "postbox", SourceIdentity: "test:account", StartedAt: now, FinishedAt: now},
		Contacts: []store.Contact{
			{JID: "111", FullName: "Account A"},
			{JID: "10", FullName: "Sender A"},
			{JID: "222", FullName: "Account B"},
			{JID: "20", FullName: "Sender B"},
			{JID: "999", FullName: "Unrelated"},
		},
		Chats: []store.Chat{
			{JID: "111", Kind: "chat", Name: "account a", LastMessageAt: now, MessageCount: 1},
			{JID: "222", Kind: "chat", Name: "account b", LastMessageAt: now, MessageCount: 1},
		},
		Messages: []store.Message{
			{SourcePK: 1, ChatJID: "111", ChatName: "account a", MessageID: "0:1", SenderJID: "10", Timestamp: now, Text: label + " a"},
			{SourcePK: 2, ChatJID: "222", ChatName: "account b", MessageID: "0:1", SenderJID: "20", Timestamp: now, Text: label + " b"},
		},
	}
}
