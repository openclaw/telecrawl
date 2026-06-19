package telegramdesktop

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	querymessages "github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"github.com/openclaw/telecrawl/internal/store"
	postboxpkg "github.com/openclaw/telecrawl/internal/telegramdesktop/postbox"
)

func TestResolveImportSourcePrefersTDataDefault(t *testing.T) {
	root, _, _ := makePostboxFixture(t)
	tdata := filepath.Join(t.TempDir(), "tdata")
	if err := os.MkdirAll(tdata, 0o700); err != nil {
		t.Fatal(err)
	}

	source := resolveImportSourcePaths("", tdata, root)
	if source.path != tdata || source.postbox {
		t.Fatalf("source = %+v, want tdata path", source)
	}
}

func TestResolveImportSourceFallsBackToPostboxDefault(t *testing.T) {
	root, _, _ := makePostboxFixture(t)
	missingTData := filepath.Join(t.TempDir(), "missing-tdata")

	source := resolveImportSourcePaths("", missingTData, root)
	if source.path != root || !source.postbox {
		t.Fatalf("source = %+v, want postbox path", source)
	}
}

func TestResolveImportSourceClassifiesExplicitPostboxPath(t *testing.T) {
	_, _, account := makePostboxFixture(t)

	source := resolveImportSourcePaths(account, "unused-tdata", "unused-postbox")
	if source.path != account || !source.postbox {
		t.Fatalf("source = %+v, want explicit postbox path", source)
	}
}

func TestPostboxParserSanitizedFixture(t *testing.T) {
	root, _, _ := makePostboxFixture(t)
	result, err := Import(context.Background(), ImportOptions{
		Path: root,
	}, filepath.Join(t.TempDir(), "telecrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chats) != 1 || result.Chats[0].Name != "Fixture Person" {
		t.Fatalf("chats = %#v", result.Chats)
	}
	if len(result.Contacts) != 1 || result.Contacts[0].FullName != "Fixture Person" {
		t.Fatalf("contacts = %#v", result.Contacts)
	}
	if len(result.Messages) != 1 || result.Messages[0].Text != "fixture hello" || result.Messages[0].MediaType != "photo_or_video" {
		t.Fatalf("messages = %#v", result.Messages)
	}
}

func TestCopyImportedMediaArchivesByContentHash(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "source-media")
	if err := os.WriteFile(source, []byte("fixture media"), 0o600); err != nil {
		t.Fatal(err)
	}
	messages := []store.Message{
		{SourcePK: 1, MediaPath: source},
		{SourcePK: 2, MediaPath: source},
	}
	var stats store.ImportStats
	archiveDir := filepath.Join(t.TempDir(), "media")

	if err := copyImportedMedia(messages, archiveDir, &stats); err != nil {
		t.Fatal(err)
	}
	if messages[0].MediaPath == source {
		t.Fatal("media path still points at source cache")
	}
	if messages[1].MediaPath != messages[0].MediaPath {
		t.Fatalf("duplicate media archived to different paths: %q != %q", messages[1].MediaPath, messages[0].MediaPath)
	}
	if messages[0].MediaSize != int64(len("fixture media")) {
		t.Fatalf("media size = %d, want %d", messages[0].MediaSize, len("fixture media"))
	}
	if stats.MediaFiles != 1 || stats.MediaBytes != int64(len("fixture media")) {
		t.Fatalf("media stats = %+v", stats)
	}
	data, err := os.ReadFile(messages[0].MediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fixture media" {
		t.Fatalf("archived media = %q", data)
	}
	if !strings.HasPrefix(messages[0].MediaPath, archiveDir+string(os.PathSeparator)) {
		t.Fatalf("media path %q is not under archive dir %q", messages[0].MediaPath, archiveDir)
	}
}

func TestCopyImportedContactAvatarsArchivesByContentHash(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "source-avatar")
	if err := os.WriteFile(source, []byte("fixture avatar"), 0o600); err != nil {
		t.Fatal(err)
	}
	contacts := []store.Contact{
		{JID: "1", AvatarPath: source},
		{JID: "2", AvatarPath: source},
		{JID: "3", AvatarPath: filepath.Join(t.TempDir(), "missing-avatar")},
	}
	archiveDir := filepath.Join(t.TempDir(), "media")

	if err := copyImportedContactAvatars(contacts, archiveDir); err != nil {
		t.Fatal(err)
	}
	if contacts[0].AvatarPath == source {
		t.Fatal("avatar path still points at source cache")
	}
	if contacts[1].AvatarPath != contacts[0].AvatarPath {
		t.Fatalf("duplicate avatar archived to different paths: %q != %q", contacts[1].AvatarPath, contacts[0].AvatarPath)
	}
	if contacts[2].AvatarPath != "" {
		t.Fatalf("missing avatar path = %q, want cleared", contacts[2].AvatarPath)
	}
	data, err := os.ReadFile(contacts[0].AvatarPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fixture avatar" {
		t.Fatalf("archived avatar = %q", data)
	}
	if !strings.HasPrefix(contacts[0].AvatarPath, archiveDir+string(os.PathSeparator)) {
		t.Fatalf("avatar path %q is not under archive dir %q", contacts[0].AvatarPath, archiveDir)
	}
}

func TestCopyImportedMediaKeepsExistingArchiveRef(t *testing.T) {
	t.Parallel()
	archiveDir := filepath.Join(t.TempDir(), "media")
	archivedPath := filepath.Join(archiveDir, "ab", "already-archived")
	if err := os.MkdirAll(filepath.Dir(archivedPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivedPath, []byte("already archived"), 0o600); err != nil {
		t.Fatal(err)
	}
	messages := []store.Message{{SourcePK: 1, MediaPath: archivedPath}}
	var stats store.ImportStats

	if err := copyImportedMedia(messages, archiveDir, &stats); err != nil {
		t.Fatal(err)
	}
	if messages[0].MediaPath != archivedPath {
		t.Fatalf("media path = %q, want existing archive path %q", messages[0].MediaPath, archivedPath)
	}
	if messages[0].MediaSize != int64(len("already archived")) {
		t.Fatalf("media size = %d, want %d", messages[0].MediaSize, len("already archived"))
	}
	if stats.MediaFiles != 1 || stats.MediaBytes != int64(len("already archived")) {
		t.Fatalf("media stats = %+v", stats)
	}
}

func TestCopyImportedMediaSkipsMissingSourceCache(t *testing.T) {
	t.Parallel()
	messages := []store.Message{
		{SourcePK: 1, MediaPath: filepath.Join(t.TempDir(), "missing-cache-file"), MediaSize: 99},
	}
	var stats store.ImportStats

	if err := copyImportedMedia(messages, filepath.Join(t.TempDir(), "media"), &stats); err != nil {
		t.Fatal(err)
	}
	if messages[0].MediaPath != "" || messages[0].MediaSize != 0 {
		t.Fatalf("missing media ref = path %q size %d, want cleared", messages[0].MediaPath, messages[0].MediaSize)
	}
	if stats.MediaFiles != 0 || stats.MediaBytes != 0 {
		t.Fatalf("media stats = %+v, want zero", stats)
	}
}

func TestTDataStableSourcePKMatchesLegacyBridge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		chatID    string
		messageID int
		want      int64
	}{
		{"-10042", 7, 7461722351030121860},
		{"123", 456, 6879695626693156840},
		{"-1000000000042", 99, 3163150813737854790},
	}
	for _, tc := range tests {
		if got := stableTDataSourcePK(tc.chatID, tc.messageID); got != tc.want {
			t.Fatalf("stableTDataSourcePK(%q, %d) = %d, want %d", tc.chatID, tc.messageID, got, tc.want)
		}
	}
}

func TestTDataPeerIDsMatchLegacyShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		peer tg.PeerClass
		want string
	}{
		{"user", &tg.PeerUser{UserID: 123}, "123"},
		{"chat", &tg.PeerChat{ChatID: 456}, "-456"},
		{"channel", &tg.PeerChannel{ChannelID: 789}, "-1000000000789"},
	}
	for _, tc := range tests {
		if got := tdataPeerIDString(tc.peer, 0); got != tc.want {
			t.Fatalf("%s peer id = %q, want %q", tc.name, got, tc.want)
		}
	}
	inputs := map[tg.InputPeerClass]string{
		&tg.InputPeerSelf{}:                  "42",
		&tg.InputPeerUser{UserID: 123}:       "123",
		&tg.InputPeerChat{ChatID: 456}:       "-456",
		&tg.InputPeerChannel{ChannelID: 789}: "-1000000000789",
	}
	for input, want := range inputs {
		if got := tdataInputPeerIDString(input, 42); got != want {
			t.Fatalf("input peer id = %q, want %q", got, want)
		}
	}
}

func TestTDataChatFilterMatchesStoredAndRawIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		chatID string
		filter string
		want   bool
	}{
		{"user stored", "123", "123", true},
		{"group stored", "-456", "-456", true},
		{"group raw", "-456", "456", true},
		{"channel stored", "-1000000000789", "-1000000000789", true},
		{"channel no dash", "-1000000000789", "1000000000789", true},
		{"channel raw", "-1000000000789", "789", true},
		{"channel padded raw", "-1000000000789", "0000000789", true},
		{"different", "-1000000000789", "790", false},
	}
	for _, tc := range tests {
		if got := tdataChatFilterMatches(tc.chatID, tc.filter); got != tc.want {
			t.Fatalf("%s: tdataChatFilterMatches(%q, %q) = %v, want %v", tc.name, tc.chatID, tc.filter, got, tc.want)
		}
	}
}

func TestTDataMediaMapping(t *testing.T) {
	t.Parallel()
	documentMessage := &tg.Message{Media: &tg.MessageMediaDocument{Document: &tg.Document{
		Size: 1234,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "fixture.pdf"},
		},
	}}}
	if got := tdataMediaType(documentMessage); got != "document" {
		t.Fatalf("document media type = %q", got)
	}
	if got := tdataMediaTitle(documentMessage); got != "fixture.pdf" {
		t.Fatalf("document media title = %q", got)
	}
	if got := tdataMediaSize(documentMessage); got != 1234 {
		t.Fatalf("document media size = %d", got)
	}
	webPage := &tg.WebPage{URL: "https://example.test/article"}
	webPage.SetTitle("Fixture Article")
	webMessage := &tg.Message{Media: &tg.MessageMediaWebPage{Webpage: webPage}}
	if got := tdataMediaType(webMessage); got != "webpage" {
		t.Fatalf("web media type = %q", got)
	}
	if got := tdataMediaTitle(webMessage); got != "Fixture Article" {
		t.Fatalf("web media title = %q", got)
	}
}

func TestTDataWebpageMediaFileFallback(t *testing.T) {
	t.Parallel()
	docPage := &tg.WebPage{}
	docPage.SetDocument(&tg.Document{
		ID:         1001,
		MimeType:   "application/pdf",
		AccessHash: 22,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "preview.pdf"},
		},
	})
	docFile, ok := telegramMessageFile(querymessages.Elem{Msg: &tg.Message{Media: &tg.MessageMediaWebPage{Webpage: docPage}}})
	if !ok || docFile.Name != "preview.pdf" || docFile.MIMEType != "application/pdf" {
		t.Fatalf("webpage document file = %#v ok=%v", docFile, ok)
	}
	docLocation, ok := docFile.Location.(*tg.InputDocumentFileLocation)
	if !ok {
		t.Fatalf("webpage document location = %T", docFile.Location)
	}
	if docLocation.ThumbSize != "" {
		t.Fatalf("full webpage document thumb size = %q, want empty", docLocation.ThumbSize)
	}

	photoPage := &tg.WebPage{}
	photoPage.SetPhoto(&tg.Photo{
		ID:            2002,
		AccessHash:    33,
		FileReference: []byte{1, 2, 3},
		Date:          1_800_000_000,
		Sizes: []tg.PhotoSizeClass{
			&tg.PhotoSize{Type: "s", W: 90, H: 90},
			&tg.PhotoSize{Type: "x", W: 800, H: 600},
		},
	})
	photoFile, ok := telegramMessageFile(querymessages.Elem{Msg: &tg.Message{Media: &tg.MessageMediaWebPage{Webpage: photoPage}}})
	if !ok || photoFile.MIMEType != "image/jpeg" {
		t.Fatalf("webpage photo file = %#v ok=%v", photoFile, ok)
	}
	location, ok := photoFile.Location.(*tg.InputPhotoFileLocation)
	if !ok {
		t.Fatalf("webpage photo location = %T", photoFile.Location)
	}
	if location.ThumbSize != "x" {
		t.Fatalf("thumb size = %q, want x", location.ThumbSize)
	}
}

func TestTDataExistingMediaRefsRequireFetchAndSameSource(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "tdata")
	ref := ExistingMediaRef{SourcePK: 42, MediaPath: "/tmp/already-archived", MediaSize: 12}
	if refs := tdataExistingMediaRefs(ImportOptions{ExistingMediaRefs: []ExistingMediaRef{ref}}, source); refs != nil {
		t.Fatalf("refs without fetch = %#v, want nil", refs)
	}
	if refs := tdataExistingMediaRefs(ImportOptions{FetchMedia: true, ExistingMediaSourcePath: filepath.Join(t.TempDir(), "other"), ExistingMediaRefs: []ExistingMediaRef{ref}}, source); refs != nil {
		t.Fatalf("refs for different source = %#v, want nil", refs)
	}
	refs := tdataExistingMediaRefs(ImportOptions{FetchMedia: true, ExistingMediaSourcePath: source, ExistingMediaRefs: []ExistingMediaRef{ref}}, source)
	if !reflect.DeepEqual(refs, map[int64]ExistingMediaRef{42: ref}) {
		t.Fatalf("refs = %#v", refs)
	}
}

func TestTDataReplyTopicMapping(t *testing.T) {
	t.Parallel()
	reply := &tg.MessageReplyHeader{}
	reply.SetReplyToMsgID(10)
	reply.SetReplyToTopID(5)
	reply.SetReplyToPeerID(&tg.PeerChannel{ChannelID: 99})
	msg := &tg.Message{}
	msg.SetReplyTo(reply)
	replyTo, threadID, replyChat, topicID := tdataReplyFields(msg, 0)
	if replyTo != "10" || threadID != "5" || topicID != "5" || replyChat != "-1000000000099" {
		t.Fatalf("reply fields = %q %q %q %q", replyTo, threadID, replyChat, topicID)
	}
}

func TestImportPassesExistingMediaRefsToPostboxImporter(t *testing.T) {
	t.Parallel()
	source, _, _ := makePostboxFixture(t)
	media := filepath.Join(t.TempDir(), "already-archived")
	if err := os.WriteFile(media, []byte("already archived"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Import(context.Background(), ImportOptions{
		Path:                    source,
		FetchMedia:              true,
		ExistingMediaSourcePath: source,
		ExistingMediaRefs: []ExistingMediaRef{{
			SourcePK:  postboxpkg.SourcePK("stable/account-123", 100, 0, 1, false),
			MediaType: "photo",
			MediaPath: media,
			MediaSize: int64(len("already archived")),
		}},
	}, filepath.Join(t.TempDir(), "telecrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 1 || result.Messages[0].MediaPath == "" {
		t.Fatalf("existing media ref was not restored: %#v", result.Messages)
	}
	if result.Stats.RemoteMediaMissing != 0 || result.Stats.RemoteMediaCandidates != 0 {
		t.Fatalf("remote stats = %+v", result.Stats)
	}
}

func TestImportDoesNotFetchMediaByDefault(t *testing.T) {
	t.Parallel()
	source, _, _ := makePostboxFixture(t)
	result, err := Import(context.Background(), ImportOptions{Path: source}, filepath.Join(t.TempDir(), "telecrawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.RemoteMediaCandidates != 0 || result.Stats.RemoteMediaMissing != 0 {
		t.Fatalf("postbox default import fetched media: %+v", result.Stats)
	}
}

func makePostboxFixture(t *testing.T) (root string, lane string, account string) {
	t.Helper()
	root = t.TempDir()
	lane = filepath.Join(root, "stable")
	account = filepath.Join(lane, "account-123")
	dbDir := filepath.Join(account, "postbox", "db")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyAndSalt := make([]byte, 48)
	for i := range keyAndSalt {
		keyAndSalt[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(lane, ".tempkeyEncrypted"), encryptedTempKeyFixture(t, []byte("no-matter-key"), keyAndSalt), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureDB := filepath.Join("postbox", "testdata", "sqlcipher_v4.db")
	if err := copyFile(filepath.Join(dbDir, "db_sqlite"), fixtureDB); err != nil {
		t.Fatal(err)
	}
	return root, lane, account
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}

func encryptedTempKeyFixture(t *testing.T, passcode []byte, keyAndSalt []byte) []byte {
	t.Helper()
	plain := make([]byte, 64)
	copy(plain, keyAndSalt)
	binary.LittleEndian.PutUint32(plain[48:52], uint32(tempkeyMurmur3(keyAndSalt)))
	digest := sha512.Sum512(passcode)
	block, err := aes.NewCipher(digest[:32])
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, digest[48:]).CryptBlocks(out, plain)
	return out
}

func tempkeyMurmur3(data []byte) int32 {
	const seed uint32 = 0xf7ca7fd2
	const c1 uint32 = 0xcc9e2d51
	const c2 uint32 = 0x1b873593
	length := len(data)
	h1 := seed
	roundedEnd := length & 0xfffffffc
	for i := 0; i < roundedEnd; i += 4 {
		k1 := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16 | uint32(data[i+3])<<24
		k1 *= c1
		k1 = (k1 << 15) | (k1 >> 17)
		k1 *= c2
		h1 ^= k1
		h1 = (h1 << 13) | (h1 >> 19)
		h1 = h1*5 + 0xe6546b64
	}
	var k1 uint32
	switch length & 3 {
	case 3:
		k1 ^= uint32(data[roundedEnd+2]) << 16
		fallthrough
	case 2:
		k1 ^= uint32(data[roundedEnd+1]) << 8
		fallthrough
	case 1:
		k1 ^= uint32(data[roundedEnd])
		k1 *= c1
		k1 = (k1 << 15) | (k1 >> 17)
		k1 *= c2
		h1 ^= k1
	}
	h1 ^= uint32(length)
	h1 ^= h1 >> 16
	h1 *= 0x85ebca6b
	h1 ^= h1 >> 13
	h1 *= 0xc2b2ae35
	h1 ^= h1 >> 16
	return int32(h1)
}
