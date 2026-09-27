package uploader

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// maxRetryBackoff caps the exponential delay between upload attempts.
const maxRetryBackoff = 30 * time.Second

// Retrier wraps an Uploader with bounded exponential backoff. A camera or file
// server that is briefly unavailable must not turn into a failed cycle.
type Retrier struct {
	Inner    Uploader
	Attempts int
	Backoff  time.Duration
	Logger   *slog.Logger

	// Sleep is injected by tests; it defaults to a context-aware timer.
	Sleep func(ctx context.Context, d time.Duration) error
}

// NewRetrier wraps inner. attempts below 1 is treated as a single attempt.
func NewRetrier(inner Uploader, attempts int, backoff time.Duration, logger *slog.Logger) *Retrier {
	if attempts < 1 {
		attempts = 1
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Retrier{Inner: inner, Attempts: attempts, Backoff: backoff, Logger: logger, Sleep: sleepContext}
}

// Upload calls the wrapped uploader until it succeeds or the attempts are spent.
func (r *Retrier) Upload(ctx context.Context, localPath string) error {
	attempts := r.Attempts
	if attempts < 1 {
		attempts = 1
	}
	sleep := r.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf("upload aborted after %d attempt(s): %w", attempt-1, lastErr)
			}
			return err
		}

		lastErr = r.Inner.Upload(ctx, localPath)
		if lastErr == nil {
			return nil
		}
		if attempt == attempts {
			break
		}

		delay := backoffFor(r.Backoff, attempt)
		r.Logger.Warn("upload attempt failed; retrying",
			"attempt", attempt, "of", attempts, "retry_in", delay, "err", lastErr)
		if err := sleep(ctx, delay); err != nil {
			return fmt.Errorf("upload aborted after %d attempt(s): %w", attempt, lastErr)
		}
	}
	return fmt.Errorf("upload failed after %d attempt(s): %w", attempts, lastErr)
}

// backoffFor returns base * 2^(attempt-1), capped at maxRetryBackoff.
func backoffFor(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	delay := base
	for range attempt - 1 {
		delay *= 2
		if delay >= maxRetryBackoff {
			return maxRetryBackoff
		}
	}
	return delay
}

// sleepContext waits for d unless ctx is cancelled first.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
