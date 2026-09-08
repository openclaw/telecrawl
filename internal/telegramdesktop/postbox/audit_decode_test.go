package postbox

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func TestAuditDecodeCountsBoundedByPayload(t *testing.T) {
	for _, kind := range []byte{6, 7, 8, 9, 12, 13} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			payload := []byte{1, 'x', kind, 0, 0, 16, 0}
			if _, err := DecodeEntries(payload); err == nil || !strings.Contains(err.Error(), "count exceeds") {
				t.Fatalf("unbounded count error = %v", err)
			}
			binary.LittleEndian.PutUint32(payload[3:], 0)
			if _, err := DecodeEntries(payload); err != nil {
				t.Fatalf("valid empty array: %v", err)
			}
		})
	}
}

func TestAuditDecodeNestedResourceLimit(t *testing.T) {
	payload := fixtureKVInt32("value", 1)
	for range maxDecodeDepth {
		payload = fixtureKVObject("nested", payload, 1)
	}
	if _, err := DecodeEntries(payload); err != nil {
		t.Fatalf("within nesting limit: %v", err)
	}
	payload = fixtureKVObject("nested", payload, 1)
	if _, err := DecodeEntries(payload); err == nil || !strings.Contains(err.Error(), "nesting limit") {
		t.Fatalf("excess nesting: %v", err)
	}
	items := 1
	d := decoder{reader: newByteReader(fixtureKVObject("child", fixtureKVInt32("value", 1), 1)), items: &items}
	d.size = d.reader.remaining()
	if _, err := d.entries(); err == nil || !strings.Contains(err.Error(), "item limit") {
		t.Fatalf("nested item budget was reset: %v", err)
	}
}

func TestAuditMessageCountsBoundedByPayload(t *testing.T) {
	base := fixtureMessage("synthetic", nil, nil)
	for _, offset := range []int{len(base) - 12, len(base) - 8, len(base) - 4} {
		payload := append([]byte(nil), base...)
		binary.LittleEndian.PutUint32(payload[offset:], 1<<20)
		if _, err := ReadMessage(payload); err == nil || !strings.Contains(err.Error(), "count exceeds") {
			t.Fatalf("message count at %d: %v", offset, err)
		}
	}
}

func TestAuditMessageDecodeFailureAbortsExtraction(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "selected"}[limited], func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if _, err := db.Exec(`CREATE TABLE t7(key BLOB PRIMARY KEY, value BLOB NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			key := make([]byte, 20)
			binary.BigEndian.PutUint64(key, 100)
			binary.BigEndian.PutUint32(key[16:], 1)
			if _, err := db.Exec(`INSERT INTO t7 VALUES(?,?)`, key, []byte{0}); err != nil {
				t.Fatal(err)
			}
			opts := ReadOptions{}
			if limited {
				opts.MessagesLimit = 1
			}
			source := Source{AccountID: "synthetic-account"}
			if rows, err := LoadMessageRecords(context.Background(), db, source, nil, nil, t.TempDir(), false, opts); err == nil || len(rows) != 0 {
				t.Fatalf("truncated message returned %d rows, error %v", len(rows), err)
			}
			if _, err := db.Exec(`UPDATE t7 SET value=?`, []byte{1}); err != nil {
				t.Fatal(err)
			}
			if rows, err := LoadMessageRecords(context.Background(), db, source, nil, nil, t.TempDir(), false, opts); err != nil || len(rows) != 0 {
				t.Fatalf("nonmessage returned %d rows, error %v", len(rows), err)
			}
		})
	}
}

func TestAuditMessageRejectsBrokenEmbeddedMedia(t *testing.T) {
	if _, err := ReadMessage(fixtureMessage("synthetic", nil, nil, []byte{1})); err == nil {
		t.Fatal("malformed embedded object was silently omitted")
	}
}
