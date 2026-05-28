package cli

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/openclaw/telecrawl/internal/store"
	"github.com/openclaw/telecrawl/internal/telegramdesktop"
)

func TestDepsInstallPackagesKeepTDataPathIndependent(t *testing.T) {
	got := depsInstallPackages()
	want := []string{"opentele2", "telethon"}
	if !slices.Equal(got, want) {
		t.Fatalf("deps = %v, want %v", got, want)
	}
	if slices.Contains(got, "pycryptodomex") || slices.Contains(got, "sqlcipher3") {
		t.Fatalf("tdata deps should not require Postbox packages: %v", got)
	}
	if want := []string{"pycryptodomex", "sqlcipher3"}; !slices.Equal(postboxDepsInstallPackages(), want) {
		t.Fatalf("postbox deps = %v, want %v", postboxDepsInstallPackages(), want)
	}
}

func TestStoreImportResultUpsertsReturnedAccountScopedChats(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "telecrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	full := accountScopedImportResult("old")
	if err := storeImportResult(ctx, st, full, ""); err != nil {
		t.Fatal(err)
	}
	partial := accountScopedImportResult("new")
	if err := storeImportResult(ctx, st, partial, "100"); err != nil {
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

func accountScopedImportResult(label string) telegramdesktop.ImportResult {
	now := time.Unix(1_800_000_000, 0).UTC()
	return telegramdesktop.ImportResult{
		Stats: store.ImportStats{SourcePath: "postbox", StartedAt: now, FinishedAt: now},
		Chats: []store.Chat{
			{JID: "111", Kind: "chat", Name: "account a", LastMessageAt: now, MessageCount: 1},
			{JID: "222", Kind: "chat", Name: "account b", LastMessageAt: now, MessageCount: 1},
		},
		Messages: []store.Message{
			{SourcePK: 1, ChatJID: "111", ChatName: "account a", MessageID: "0:1", Timestamp: now, Text: label + " a"},
			{SourcePK: 2, ChatJID: "222", ChatName: "account b", MessageID: "0:1", Timestamp: now, Text: label + " b"},
		},
	}
}
