package telegramdesktop

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tgerr"
)

func TestTelegramFloodWaitPolicyRetriesAndReports(t *testing.T) {
	var progress bytes.Buffer
	policy := newTelegramFloodWaitPolicy(&progress)
	var waits []time.Duration
	policy.sleep = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}
	calls := 0
	next := telegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		if calls == 1 {
			return tgerr.New(420, "FLOOD_WAIT_1")
		}
		return nil
	})

	err := policy.Handle(next).Invoke(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(waits) != 1 || waits[0] != 2*time.Second {
		t.Fatalf("waits = %v, want [2s]", waits)
	}
	if got, want := progress.String(), "telecrawl: Telegram rate limit; waiting 2s before retry 1/3\n"; got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}
}

func TestTelegramFloodWaitPolicyCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	policy := newTelegramFloodWaitPolicy(nil)
	next := telegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		return tgerr.New(420, "FLOOD_WAIT_1")
	})

	err := policy.Handle(next).Invoke(ctx, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestTelegramFloodWaitPolicyRejectsWaitAboveLimit(t *testing.T) {
	policy := newTelegramFloodWaitPolicy(nil)
	policy.sleep = func(context.Context, time.Duration) error {
		t.Fatal("sleep called for out-of-bounds wait")
		return nil
	}
	next := telegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		return tgerr.New(420, "FLOOD_WAIT_300")
	})

	err := policy.Handle(next).Invoke(context.Background(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds 5m0s limit") {
		t.Fatalf("error = %v, want maximum-wait error", err)
	}
	if _, ok := tgerr.AsFloodWait(err); ok {
		t.Fatalf("error still classifies as FLOOD_WAIT: %v", err)
	}
}

func TestTelegramFloodWaitPolicyStopsAtRetryLimit(t *testing.T) {
	policy := newTelegramFloodWaitPolicy(nil)
	waits := 0
	policy.sleep = func(context.Context, time.Duration) error {
		waits++
		return nil
	}
	calls := 0
	next := telegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		return tgerr.New(420, "FLOOD_WAIT_1")
	})

	err := policy.Handle(next).Invoke(context.Background(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "retry limit exceeded after 3 retries") {
		t.Fatalf("error = %v, want retry-limit error", err)
	}
	if _, ok := tgerr.AsFloodWait(err); ok {
		t.Fatalf("error still classifies as FLOOD_WAIT: %v", err)
	}
	if calls != 4 || waits != 3 {
		t.Fatalf("calls = %d, waits = %d; want 4 calls and 3 waits", calls, waits)
	}
}

func TestTelegramChildTimeoutsReserveFloodWaitRetryBudget(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		work    time.Duration
	}{
		{name: "remote message", timeout: postboxRemoteMessageTimeout, work: 45 * time.Second},
		{name: "media download", timeout: telegramMediaDownloadTimeout, work: 3 * time.Minute},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.timeout - test.work; got < telegramFloodWaitRetryBudget {
				t.Fatalf("reserved retry budget = %s, want at least %s", got, telegramFloodWaitRetryBudget)
			}
		})
	}
}

func TestTelegramFloodWaitPolicyRetriesPaginationRequestInPlace(t *testing.T) {
	policy := newTelegramFloodWaitPolicy(nil)
	policy.sleep = func(context.Context, time.Duration) error { return nil }
	var calls []int
	failedPage := false
	next := telegram.InvokeFunc(func(_ context.Context, input bin.Encoder, _ bin.Decoder) error {
		page := input.(*pageRequest).page
		calls = append(calls, page)
		if page == 1 && !failedPage {
			failedPage = true
			return tgerr.New(420, "FLOOD_WAIT_1")
		}
		return nil
	})
	invoke := policy.Handle(next)

	for page := 0; page < 3; page++ {
		if err := invoke.Invoke(context.Background(), &pageRequest{page: page}, nil); err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
	}
	if got, want := calls, []int{0, 1, 1, 2}; !equalInts(got, want) {
		t.Fatalf("page calls = %v, want %v", got, want)
	}
}

type pageRequest struct {
	page int
}

func (*pageRequest) Encode(*bin.Buffer) error { return nil }

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
