package postbox

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Postbox stores threadId as a single Int64 when MessageDataFlags.hasThreadId
// (1 << 5) is set; see Telegram-iOS submodules/Postbox/Sources/MessageHistoryTable.swift.
func TestReadMessageWithThreadID(t *testing.T) {
	const authorID int64 = 0x0000_0002_1234_5678
	var buf bytes.Buffer
	w := func(v any) {
		if err := binary.Write(&buf, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	w(int8(0))              // type
	w(uint32(7))            // stableId
	w(uint32(0))            // stableVersion
	w(uint8(1 << 5))        // dataFlags: hasThreadId
	w(int64(42))            // threadId
	w(uint32(incomingFlag)) // flags
	w(uint32(0))            // tags
	w(int8(0))              // no forward info
	w(int8(1))              // has author
	w(authorID)
	text := "client question"
	w(int32(len(text)))
	buf.WriteString(text)
	w(int32(0)) // attributes
	w(int32(0)) // embedded media
	w(int32(0)) // referenced media

	msg, err := ReadMessage(buf.Bytes())
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if msg == nil {
		t.Fatal("ReadMessage returned nil message")
	}
	if !msg.HasAuthorID || msg.AuthorID != authorID {
		t.Fatalf("author = %d (has=%v), want %d", msg.AuthorID, msg.HasAuthorID, authorID)
	}
	if msg.Flags != incomingFlag {
		t.Fatalf("flags = %d, want %d", msg.Flags, incomingFlag)
	}
	if msg.Tags != 0 {
		t.Fatalf("tags = %d, want 0", msg.Tags)
	}
	if msg.Text != text {
		t.Fatalf("text = %q, want %q", msg.Text, text)
	}
}
