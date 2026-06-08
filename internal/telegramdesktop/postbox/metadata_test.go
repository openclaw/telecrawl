package postbox

import "testing"

func TestAttachMessageMetadata(t *testing.T) {
	webpage, err := DecodeObject(fixtureWebpageMedia())
	if err != nil {
		t.Fatal(err)
	}
	location, err := DecodeObject(fixtureLocationMedia())
	if err != nil {
		t.Fatal(err)
	}
	poll, err := DecodeObject(fixturePollMedia())
	if err != nil {
		t.Fatal(err)
	}
	service, err := DecodeObject(fixtureServiceActionMedia("call"))
	if err != nil {
		t.Fatal(err)
	}
	messages := []MessageRecord{
		{MediaType: "web_page", EmbeddedMedia: []any{webpage}},
		{MediaType: "media", EmbeddedMedia: []any{location}},
		{MediaType: "media", EmbeddedMedia: []any{poll}},
		{MediaType: "media", EmbeddedMedia: []any{service}},
		{Text: "see https://example.com/from-text.", MediaType: "web_page"},
	}
	if got := AttachMessageMetadata(messages); got != 5 {
		t.Fatalf("metadata count = %d, want 5", got)
	}
	wantTypes := []string{"web_page", "location", "poll", "service_action", "web_page"}
	for i, want := range wantTypes {
		if messages[i].MetadataType != want {
			t.Fatalf("message %d metadata type = %q, want %q", i, messages[i].MetadataType, want)
		}
	}
	if messages[0].MetadataURL != "https://example.com/article" {
		t.Fatalf("web URL = %q", messages[0].MetadataURL)
	}
	if messages[1].MetadataTitle != "Example Place" || messages[2].MetadataTitle != "Example Poll" || messages[3].MetadataTitle != "call" {
		t.Fatalf("metadata titles = %#v", messages)
	}
	if messages[4].MetadataURL != "https://example.com/from-text" {
		t.Fatalf("text URL = %q", messages[4].MetadataURL)
	}
}

func TestServiceActionSubtypes(t *testing.T) {
	var messages []MessageRecord
	for _, action := range []string{"call", "title_change", "member_change", "pin"} {
		media, err := DecodeObject(fixtureServiceActionMedia(action))
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, MessageRecord{MediaType: "media", EmbeddedMedia: []any{media}})
	}
	if got := AttachMessageMetadata(messages); got != 4 {
		t.Fatalf("metadata count = %d, want 4", got)
	}
	want := []string{"call", "title_change", "member_change", "pin"}
	for i := range want {
		if messages[i].MetadataTitle != want[i] {
			t.Fatalf("message %d title = %q, want %q", i, messages[i].MetadataTitle, want[i])
		}
	}
}
