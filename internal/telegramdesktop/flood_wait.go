package telegramdesktop

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

const (
	telegramFloodWaitMaxRetries  = 3
	telegramFloodWaitMaxDelay    = 5 * time.Minute
	telegramFloodWaitRetryBudget = telegramFloodWaitMaxRetries * telegramFloodWaitMaxDelay
	telegramMediaDownloadTimeout = 3*time.Minute + telegramFloodWaitRetryBudget
	postboxRemoteMessageTimeout  = 45*time.Second + telegramFloodWaitRetryBudget
)

type telegramFloodWaitPolicy struct {
	progress io.Writer
	sleep    func(context.Context, time.Duration) error
	mu       sync.Mutex
}

func newTelegramFloodWaitPolicy(progress io.Writer) *telegramFloodWaitPolicy {
	return &telegramFloodWaitPolicy{
		progress: progress,
		sleep:    telegramFloodWaitSleep,
	}
}

func (p *telegramFloodWaitPolicy) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		for retries := 0; ; retries++ {
			err := next.Invoke(ctx, input, output)
			if err == nil {
				return nil
			}
			delay, ok := tgerr.AsFloodWait(err)
			if !ok {
				return err
			}
			if retries >= telegramFloodWaitMaxRetries {
				return fmt.Errorf("telegram flood-wait retry limit exceeded after %d retries: %v", telegramFloodWaitMaxRetries, err)
			}
			wait := delay + time.Second
			if wait > telegramFloodWaitMaxDelay {
				return fmt.Errorf("telegram flood wait of %s exceeds %s limit: %v", wait, telegramFloodWaitMaxDelay, err)
			}
			p.reportWait(wait, retries+1)
			if err := p.sleep(ctx, wait); err != nil {
				return fmt.Errorf("wait for telegram rate limit: %w", err)
			}
		}
	}
}

func (p *telegramFloodWaitPolicy) reportWait(wait time.Duration, retry int) {
	if p.progress == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.progress, "telecrawl: Telegram rate limit; waiting %s before retry %d/%d\n", wait, retry, telegramFloodWaitMaxRetries)
}

func telegramFloodWaitSleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
