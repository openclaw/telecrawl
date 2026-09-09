package telegramdesktop

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/openclaw/telecrawl/internal/store"
)

func TestAuditFolderErrorsAbortImport(t *testing.T) {
	for _, stage := range []string{"filters", "dialogs", "empty"} {
		t.Run(stage, func(t *testing.T) {
			want := errors.New("synthetic folder failure")
			raw := tg.NewClient(telegram.InvokeFunc(func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
				var response bin.Encoder
				switch input.(type) {
				case *tg.MessagesGetDialogsRequest:
					if stage == "dialogs" {
						return want
					}
					response = &tg.MessagesDialogs{}
				case *tg.MessagesGetDialogFiltersRequest:
					if stage == "filters" {
						return want
					}
					response = &tg.MessagesDialogFilters{}
				default:
					return fmt.Errorf("unexpected synthetic request %T", input)
				}
				buffer := &bin.Buffer{}
				if err := response.Encode(buffer); err != nil {
					return err
				}
				return output.Decode(buffer)
			}))
			s := tdataImportSession{raw: raw, selfID: 1}
			result, err := s.importAccount(context.Background())
			if stage == "empty" {
				if err != nil || len(result.Folders) != 0 {
					t.Fatalf("valid empty folders: %+v, %v", result, err)
				}
				return
			}
			if !errors.Is(err, want) || len(result.Folders) != 0 || len(result.FolderChats) != 0 {
				t.Fatalf("incomplete extraction: %+v, %v", result, err)
			}
		})
	}
}

func TestAuditCustomFiltersKeepOnlyExplicitMembership(t *testing.T) {
	first := &tg.DialogFilter{
		ID: 2, Title: tg.TextWithEntities{Text: "explicit"},
		PinnedPeers:  []tg.InputPeerClass{&tg.InputPeerUser{UserID: 12}},
		IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: 12}, &tg.InputPeerUser{UserID: 11}},
	}
	first.SetContacts(true)
	first.SetExcludeRead(true)
	first.SetEmoticon("folder")
	first.SetColor(3)
	second := &tg.DialogFilter{ID: 7, Title: tg.TextWithEntities{Text: "rule only"}}
	second.SetGroups(true)
	customCalls, dialogCalls := 0, 0
	raw := tg.NewClient(telegram.InvokeFunc(func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		var response bin.Encoder
		switch req := input.(type) {
		case *tg.MessagesGetDialogsRequest:
			dialogCalls++
			if req.FolderID == 2 || req.FolderID == 7 {
				customCalls++
				return errors.New("custom filter ID is not a peer folder ID")
			}
			response = &tg.MessagesDialogs{}
		case *tg.MessagesGetDialogFiltersRequest:
			response = &tg.MessagesDialogFilters{Filters: []tg.DialogFilterClass{second, first}}
		default:
			return fmt.Errorf("unexpected synthetic request %T", input)
		}
		buffer := &bin.Buffer{}
		if err := response.Encode(buffer); err != nil {
			return err
		}
		return output.Decode(buffer)
	}))
	s := tdataImportSession{raw: raw, selfID: 1}
	result, err := s.importAccount(context.Background())
	if err != nil || customCalls != 0 || dialogCalls == 0 {
		t.Fatalf("custom=%d ordinary=%d: %v", customCalls, dialogCalls, err)
	}
	wantFolders := []store.Folder{
		{ID: "2", Title: "explicit", Emoticon: "folder", Color: 3, FlagsJSON: `{"bots":false,"broadcasts":false,"contacts":true,"exclude_archived":false,"exclude_muted":false,"exclude_read":true,"groups":false,"non_contacts":false}`},
		{ID: "7", Title: "rule only", FlagsJSON: `{"bots":false,"broadcasts":false,"contacts":false,"exclude_archived":false,"exclude_muted":false,"exclude_read":false,"groups":true,"non_contacts":false}`},
	}
	wantMembers := []store.FolderChat{{FolderID: "2", ChatJID: "11", Position: 0}, {FolderID: "2", ChatJID: "12", Position: 1}}
	if !reflect.DeepEqual(result.Folders, wantFolders) || !reflect.DeepEqual(result.FolderChats, wantMembers) {
		t.Fatalf("folders=%+v memberships=%+v", result.Folders, result.FolderChats)
	}
}
