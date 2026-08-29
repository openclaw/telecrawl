package telegramdesktop

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"
)

func TestCollectFoldersReturnsRPCError(t *testing.T) {
	want := errors.New("FLOOD_WAIT_30")
	_, _, err := collectFolders(context.Background(), 1, 0, func(context.Context) (*tg.MessagesDialogFilters, error) {
		return nil, want
	}, unusedFolderWalker(t))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestCollectFoldersReturnsFolderWalkError(t *testing.T) {
	want := errors.New("FLOOD_WAIT_30")
	_, _, err := collectFolders(context.Background(), 1, 0, func(context.Context) (*tg.MessagesDialogFilters, error) {
		return folderFiltersResult(2, "Work"), nil
	}, func(context.Context, int, func(context.Context, dialogs.Elem) error) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestCollectFoldersHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := collectFolders(ctx, 1, 0, func(ctx context.Context) (*tg.MessagesDialogFilters, error) {
		return nil, ctx.Err()
	}, unusedFolderWalker(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCollectFoldersCapsFolderDialogs(t *testing.T) {
	const dialogLimit = 3
	visits := 0
	folders, folderChats, err := collectFolders(context.Background(), 1, dialogLimit, func(context.Context) (*tg.MessagesDialogFilters, error) {
		return folderFiltersResult(2, "Work"), nil
	}, func(_ context.Context, _ int, visit func(context.Context, dialogs.Elem) error) error {
		for i := 1; i <= 10; i++ {
			visits++
			if err := visit(context.Background(), folderDialogElem(int64(i))); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("collectFolders: %v", err)
	}
	if visits != dialogLimit {
		t.Fatalf("folder dialog visits = %d, want cap %d", visits, dialogLimit)
	}
	if len(folders) != 1 || folders[0].ID != "2" || folders[0].Title != "Work" {
		t.Fatalf("folders = %+v", folders)
	}
	if len(folderChats) != dialogLimit {
		t.Fatalf("folder chats = %d, want cap %d", len(folderChats), dialogLimit)
	}
}

func unusedFolderWalker(t *testing.T) folderDialogsWalker {
	t.Helper()
	return func(context.Context, int, func(context.Context, dialogs.Elem) error) error {
		t.Fatal("folder dialog walk should not run")
		return nil
	}
}

func folderFiltersResult(id int, title string) *tg.MessagesDialogFilters {
	return &tg.MessagesDialogFilters{
		Filters: []tg.DialogFilterClass{
			&tg.DialogFilter{
				ID:    id,
				Title: tg.TextWithEntities{Text: title},
			},
		},
	}
}

func folderDialogElem(userID int64) dialogs.Elem {
	return dialogs.Elem{
		Dialog: &tg.Dialog{Peer: &tg.PeerUser{UserID: userID}},
	}
}
