package postbox

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSanitizedPostboxFixture(t *testing.T) {
	root := filepath.Join("..", "testdata", "postbox")
	var expected struct {
		PeerDisplay         string    `json:"peer_display"`
		Text                string    `json:"text"`
		AuthorID            int64     `json:"author_id"`
		MediaType           string    `json:"media_type"`
		ReferencedMediaIDs  [][]int64 `json:"referenced_media_ids"`
		SingleAccountPeerID string    `json:"single_account_peer_id"`
	}
	if err := readFixtureJSON(filepath.Join(root, "postbox_expected.json"), &expected); err != nil {
		t.Fatal(err)
	}

	peer, err := DecodeObject(readHexFixture(t, filepath.Join(root, "postbox_peer.hex")))
	if err != nil {
		t.Fatal(err)
	}
	if got := PeerDisplay(peer); got != expected.PeerDisplay {
		t.Fatalf("peer display = %q, want %q", got, expected.PeerDisplay)
	}

	message, err := ReadMessage(readHexFixture(t, filepath.Join(root, "postbox_message.hex")))
	if err != nil {
		t.Fatal(err)
	}
	if message == nil {
		t.Fatal("message decode returned nil")
	}
	if message.Text != expected.Text {
		t.Fatalf("message text = %q, want %q", message.Text, expected.Text)
	}
	if !message.HasAuthorID || message.AuthorID != expected.AuthorID {
		t.Fatalf("author = (%v, %d), want %d", message.HasAuthorID, message.AuthorID, expected.AuthorID)
	}
	if got := MediaTypeFor(message); got != expected.MediaType {
		t.Fatalf("media type = %q, want %q", got, expected.MediaType)
	}
	if got := mediaRefsAsLists(message.ReferencedMediaIDs); !reflect.DeepEqual(got, expected.ReferencedMediaIDs) {
		t.Fatalf("referenced media ids = %#v, want %#v", got, expected.ReferencedMediaIDs)
	}

	if got := PeerStoreID("stable/account-a", 100, false); got != expected.SingleAccountPeerID {
		t.Fatalf("single account peer id = %q, want %q", got, expected.SingleAccountPeerID)
	}
	accountAChat := PeerStoreID("stable/account-a", 100, true)
	accountBChat := PeerStoreID("stable/account-b", 100, true)
	if accountAChat == accountBChat {
		t.Fatal("multi-account peer ids collided")
	}
	if SourcePK("stable/account-a", 100, 0, 1, true) == SourcePK("stable/account-b", 100, 0, 1, true) {
		t.Fatal("multi-account message source keys collided")
	}
}

func TestMediaResourcesAndCacheLookup(t *testing.T) {
	documentMessage, err := ReadMessage(fixtureMessage("fixture doc", []byte{1}, []byte{2}, fixtureDocumentMedia(987654321, "fixture.mp4")))
	if err != nil {
		t.Fatal(err)
	}
	if got := MediaResourceIDs(documentMessage.EmbeddedMedia); !reflect.DeepEqual(got, []string{"telegram-cloud-document-2-987654321"}) {
		t.Fatalf("document resource ids = %#v", got)
	}
	if got := MediaTitleFor(documentMessage); got != "fixture.mp4" {
		t.Fatalf("document title = %q", got)
	}
	cacheRoot := t.TempDir()
	cached := filepath.Join(cacheRoot, "telegram-cloud-document-2-987654321")
	if err := os.WriteFile(cached, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if path, size := CachedMediaFor(documentMessage, cacheRoot); path != cached || size != 5 {
		t.Fatalf("cached document = (%q, %d), want (%q, 5)", path, size, cached)
	}

	photoMessage, err := ReadMessage(fixtureMessage("fixture photo", nil, nil, fixturePhotoMedia(123456789)))
	if err != nil {
		t.Fatal(err)
	}
	wantPhotoIDs := []string{
		"telegram-cloud-photo-size-4-123456789-s",
		"telegram-cloud-photo-size-4-123456789-x",
	}
	if got := MediaResourceIDs(photoMessage.EmbeddedMedia); !reflect.DeepEqual(got, wantPhotoIDs) {
		t.Fatalf("photo resource ids = %#v, want %#v", got, wantPhotoIDs)
	}
	small := filepath.Join(cacheRoot, "telegram-cloud-photo-size-4-123456789-s")
	large := filepath.Join(cacheRoot, "telegram-cloud-photo-size-4-123456789-x.jpg")
	partial := filepath.Join(cacheRoot, "telegram-cloud-photo-size-4-123456789-x_partial")
	for path, data := range map[string][]byte{small: []byte("1"), large: []byte("larger"), partial: []byte("not complete")} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if path, size := CachedMediaFor(photoMessage, cacheRoot); path != large || size != 6 {
		t.Fatalf("cached photo = (%q, %d), want (%q, 6)", path, size, large)
	}

	peerPhoto := map[string]any{
		"@type": int64(1774815102),
		"r": map[string]any{
			"@type": int64(resourceTypeCloudPeerPhoto),
			"d":     int64(2),
			"s":     int64(1),
			"v":     int64(333),
			"l":     int64(444),
		},
	}
	if got := MediaResourceIDs(peerPhoto); !reflect.DeepEqual(got, []string{"telegram-peer-photo-size-2-1-333-444"}) {
		t.Fatalf("peer photo resource ids = %#v", got)
	}
	avatar := filepath.Join(cacheRoot, "telegram-peer-photo-size-2-1-333-444.jpg")
	if err := os.WriteFile(avatar, []byte("avatar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := CachedPeerAvatarPath(map[string]any{"ph": []any{peerPhoto}}, cacheRoot); got != avatar {
		t.Fatalf("cached peer avatar = %q, want %q", got, avatar)
	}
}

func TestPostboxPeerConversions(t *testing.T) {
	accountID, err := AccountDirRecordID("account-10833815886710207757")
	if err != nil {
		t.Fatal(err)
	}
	if accountID != -7612928186999343859 {
		t.Fatalf("account id = %d", accountID)
	}
	tests := []struct {
		peerID int64
		want   int64
		ok     bool
	}{
		{fixturePostboxPeerID(0, 777000), 777000, true},
		{fixturePostboxPeerID(1, 42), -42, true},
		{fixturePostboxPeerID(2, 42), -1_000_000_000_042, true},
		{fixturePostboxPeerID(3, 42), 0, false},
	}
	for _, tc := range tests {
		got, ok := PostboxPeerToTelegramID(tc.peerID)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("PostboxPeerToTelegramID(%d) = (%d, %v), want (%d, %v)", tc.peerID, got, ok, tc.want, tc.ok)
		}
	}
}

func readFixtureJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func readHexFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mediaRefsAsLists(refs []MediaRef) [][]int64 {
	out := make([][]int64, 0, len(refs))
	for _, ref := range refs {
		out = append(out, []int64{int64(ref.Namespace), ref.ID})
	}
	return out
}

func fixtureShortString(value string) []byte {
	return append([]byte{byte(len(value))}, []byte(value)...)
}

func fixtureBytes(value []byte) []byte {
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, int32(len(value)))
	out.Write(value)
	return out.Bytes()
}

func fixtureString(value string) []byte {
	return fixtureBytes([]byte(value))
}

func fixtureKVString(key, value string) []byte {
	return bytes.Join([][]byte{fixtureShortString(key), {4}, fixtureString(value)}, nil)
}

func fixtureKVInt32(key string, value int32) []byte {
	var out bytes.Buffer
	out.Write(fixtureShortString(key))
	out.WriteByte(0)
	_ = binary.Write(&out, binary.LittleEndian, value)
	return out.Bytes()
}

func fixtureKVInt64(key string, value int64) []byte {
	var out bytes.Buffer
	out.Write(fixtureShortString(key))
	out.WriteByte(1)
	_ = binary.Write(&out, binary.LittleEndian, value)
	return out.Bytes()
}

func fixtureKVObject(key string, payload []byte, typeHash int32) []byte {
	var out bytes.Buffer
	out.Write(fixtureShortString(key))
	out.WriteByte(5)
	out.Write(fixtureObject(payload, typeHash))
	return out.Bytes()
}

func fixtureObject(payload []byte, typeHash int32) []byte {
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, typeHash)
	_ = binary.Write(&out, binary.LittleEndian, int32(len(payload)))
	out.Write(payload)
	return out.Bytes()
}

func fixtureRootTypedObject(payload []byte, typeHash int32) []byte {
	var out bytes.Buffer
	out.Write(fixtureShortString("_"))
	out.WriteByte(5)
	out.Write(fixtureObject(payload, typeHash))
	return out.Bytes()
}

func fixtureMessage(text string, _ []byte, _ []byte, embeddedMedia ...[]byte) []byte {
	var out bytes.Buffer
	out.WriteByte(0)
	_ = binary.Write(&out, binary.LittleEndian, uint32(11))
	_ = binary.Write(&out, binary.LittleEndian, uint32(22))
	out.WriteByte(0)
	_ = binary.Write(&out, binary.LittleEndian, uint32(incomingFlag))
	_ = binary.Write(&out, binary.LittleEndian, uint32(1<<0))
	out.WriteByte(0)
	out.WriteByte(1)
	_ = binary.Write(&out, binary.LittleEndian, int64(4242))
	out.Write(fixtureString(text))
	_ = binary.Write(&out, binary.LittleEndian, int32(0))
	_ = binary.Write(&out, binary.LittleEndian, int32(len(embeddedMedia)))
	for _, media := range embeddedMedia {
		out.Write(fixtureBytes(media))
	}
	_ = binary.Write(&out, binary.LittleEndian, int32(0))
	return out.Bytes()
}

func fixtureDocumentMedia(fileID int64, fileName string) []byte {
	resource := bytes.Join([][]byte{
		fixtureKVInt32("d", 2),
		fixtureKVInt64("f", fileID),
		fixtureKVInt64("a", 123),
		fixtureKVInt64("n64", 5),
		fixtureKVString("fn", fileName),
	}, nil)
	media := bytes.Join([][]byte{
		fixtureKVObject("r", resource, resourceTypeCloudDocument),
		fixtureKVString("mt", "video/mp4"),
	}, nil)
	return fixtureRootTypedObject(media, 665733176)
}

func fixturePhotoMedia(photoID int64) []byte {
	small := bytes.Join([][]byte{
		fixtureKVInt32("d", 4),
		fixtureKVInt64("i", photoID),
		fixtureKVInt64("a", 321),
		fixtureKVString("s", "s"),
	}, nil)
	large := bytes.Join([][]byte{
		fixtureKVInt32("d", 4),
		fixtureKVInt64("i", photoID),
		fixtureKVInt64("a", 321),
		fixtureKVString("s", "x"),
	}, nil)
	media := bytes.Join([][]byte{
		fixtureKVObject("small", small, resourceTypeCloudPhotoSize),
		fixtureKVObject("large", large, resourceTypeCloudPhotoSize),
	}, nil)
	return fixtureRootTypedObject(media, -1951522668)
}

func fixtureWebpageMedia() []byte {
	media := bytes.Join([][]byte{
		fixtureKVString("u", "https://example.com/article"),
		fixtureKVString("ti", "Example Article"),
	}, nil)
	return fixtureRootTypedObject(media, messageMetadataWebpage)
}

func fixtureLocationMedia() []byte {
	media := bytes.Join([][]byte{
		fixtureKVDouble("la", 52.1),
		fixtureKVDouble("lo", 4.3),
		fixtureKVString("adr", "Example Place"),
	}, nil)
	return fixtureRootTypedObject(media, messageMetadataLocation)
}

func fixturePollMedia() []byte {
	return fixtureRootTypedObject(fixtureKVString("t", "Example Poll"), messageMetadataPoll)
}

func fixtureServiceActionMedia(action string) []byte {
	var media []byte
	switch action {
	case "title_change":
		media = fixtureKVString("t", "Example Title")
	case "member_change":
		media = fixtureKVInt32("m", 1001)
	case "pin":
		media = fixtureKVInt32("p", 2002)
	default:
		media = bytes.Join([][]byte{fixtureKVInt32("d", 42), fixtureKVInt32("dr", 10)}, nil)
	}
	return fixtureRootTypedObject(media, messageMetadataServiceAction)
}

func fixtureKVDouble(key string, value float64) []byte {
	var out bytes.Buffer
	out.Write(fixtureShortString(key))
	out.WriteByte(3)
	_ = binary.Write(&out, binary.LittleEndian, value)
	return out.Bytes()
}

func fixturePostboxPeerID(namespace int64, rawID int64) int64 {
	value := ((rawID >> 32) << 35) | ((namespace & 7) << 32) | (rawID & 0xffffffff)
	return value
}
