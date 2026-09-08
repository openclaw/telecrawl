package telegramdesktop

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

func TestAuditFolderErrorsAbortImport(t *testing.T) {
	for _, stage := range []string{"filters", "members", "empty"} {
		t.Run(stage, func(t *testing.T) {
			want := errors.New("synthetic folder failure")
			memberCalls := 0
			raw := tg.NewClient(telegram.InvokeFunc(func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
				var response bin.Encoder
				switch req := input.(type) {
				case *tg.MessagesGetDialogsRequest:
					if req.FolderID == 2 {
						memberCalls++
						return want
					}
					response = &tg.MessagesDialogs{}
				case *tg.MessagesGetDialogFiltersRequest:
					if stage == "filters" {
						return want
					}
					filters := &tg.MessagesDialogFilters{}
					if stage == "members" {
						filters.Filters = []tg.DialogFilterClass{&tg.DialogFilter{ID: 2, Title: tg.TextWithEntities{Text: "synthetic"}}}
					}
					response = filters
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
			if stage == "members" && memberCalls != 1 {
				t.Fatalf("member calls = %d", memberCalls)
			}
		})
	}
}
