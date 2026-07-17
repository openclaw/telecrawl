package telegramdesktop

import (
	"testing"

	"github.com/gotd/td/tg"
)

// A MessageMediaDocument can arrive with a nil Document interface (for example
// an expired/TTL document or a service-style media row). Calling AsNotEmpty on
// the nil interface panicked with a nil pointer dereference during tdata import;
// these helpers must degrade to zero values instead of crashing.
func TestTdataMediaDocumentNilDocument(t *testing.T) {
	msg := &tg.Message{Media: &tg.MessageMediaDocument{}} // Document left nil

	if got := tdataMediaTitle(msg); got != "" {
		t.Fatalf("tdataMediaTitle(nil document) = %q, want empty string", got)
	}
	if got := tdataMediaSize(msg); got != 0 {
		t.Fatalf("tdataMediaSize(nil document) = %d, want 0", got)
	}
}
