package uploader

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/uploader/uploadertest"
)

// recordingSleeper captures the requested delays without ever sleeping.
type recordingSleeper struct {
	delays []time.Duration
	err    error
}

func (s *recordingSleeper) sleep(ctx context.Context, d time.Duration) error {
	s.delays = append(s.delays, d)
	if s.err != nil {
		return s.err
	}
	return ctx.Err()
}

func newRetrier(t *testing.T, inner Uploader, attempts int, backoff time.Duration) (*Retrier, *recordingSleeper, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	sleeper := &recordingSleeper{}
	r := NewRetrier(inner, attempts, backoff, slog.New(slog.NewTextHandler(&buf, nil)))
	r.Sleep = sleeper.sleep
	return r, sleeper, &buf
}

func TestRetrier(t *testing.T) {
	boom := errors.New("upload exploded")

	t.Run("succeeds_on_the_first_attempt", func(t *testing.T) {
		fake := &uploadertest.Fake{}
		r, sleeper, _ := newRetrier(t, fake, 3, 2*time.Second)

		if err := r.Upload(t.Context(), writeTemp(t)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		if fake.CallCount() != 1 {
			t.Errorf("inner called %d times, want 1", fake.CallCount())
		}
		if len(sleeper.delays) != 0 {
			t.Errorf("slept %v, want no sleeping", sleeper.delays)
		}
	})

	t.Run("succeeds_on_the_third_attempt", func(t *testing.T) {
		fake := &uploadertest.Fake{Errs: []error{boom, boom, nil}}
		r, sleeper, _ := newRetrier(t, fake, 3, 2*time.Second)

		if err := r.Upload(t.Context(), writeTemp(t)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		if fake.CallCount() != 3 {
			t.Errorf("inner called %d times, want 3", fake.CallCount())
		}
		want := []time.Duration{2 * time.Second, 4 * time.Second}
		if !equalDurations(sleeper.delays, want) {
			t.Errorf("delays = %v, want %v", sleeper.delays, want)
		}
	})

	t.Run("exhausts_its_attempts", func(t *testing.T) {
		fake := &uploadertest.Fake{Err: boom}
		r, sleeper, _ := newRetrier(t, fake, 3, time.Second)

		err := r.Upload(t.Context(), writeTemp(t))
		if err == nil {
			t.Fatal("expected an error")
		}
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to wrap the inner error", err)
		}
		if !strings.Contains(err.Error(), "after 3 attempt") {
			t.Errorf("err = %v, want it to state the attempt count", err)
		}
		if fake.CallCount() != 3 {
			t.Errorf("inner called %d times, want 3", fake.CallCount())
		}
		if len(sleeper.delays) != 2 {
			t.Errorf("slept %d times, want 2 (no sleep after the final attempt)", len(sleeper.delays))
		}
	})

	t.Run("single_attempt_never_retries", func(t *testing.T) {
		fake := &uploadertest.Fake{Err: boom}
		r, sleeper, _ := newRetrier(t, fake, 1, time.Second)

		if err := r.Upload(t.Context(), writeTemp(t)); err == nil {
			t.Fatal("expected an error")
		}
		if fake.CallCount() != 1 {
			t.Errorf("inner called %d times, want 1", fake.CallCount())
		}
		if len(sleeper.delays) != 0 {
			t.Errorf("slept %v, want none", sleeper.delays)
		}
	})

	t.Run("attempts_below_one_is_treated_as_one", func(t *testing.T) {
		fake := &uploadertest.Fake{Err: boom}
		r := NewRetrier(fake, 0, time.Second, nil)
		r.Sleep = (&recordingSleeper{}).sleep
		if err := r.Upload(t.Context(), writeTemp(t)); err == nil {
			t.Fatal("expected an error")
		}
		if fake.CallCount() != 1 {
			t.Errorf("inner called %d times, want 1", fake.CallCount())
		}
	})

	t.Run("backoff_is_capped", func(t *testing.T) {
		fake := &uploadertest.Fake{Err: boom}
		r, sleeper, _ := newRetrier(t, fake, 4, 20*time.Second)

		if err := r.Upload(t.Context(), writeTemp(t)); err == nil {
			t.Fatal("expected an error")
		}
		want := []time.Duration{20 * time.Second, maxRetryBackoff, maxRetryBackoff}
		if !equalDurations(sleeper.delays, want) {
			t.Errorf("delays = %v, want %v", sleeper.delays, want)
		}
	})

	t.Run("zero_backoff_retries_immediately", func(t *testing.T) {
		fake := &uploadertest.Fake{Errs: []error{boom, nil}}
		r, sleeper, _ := newRetrier(t, fake, 2, 0)

		if err := r.Upload(t.Context(), writeTemp(t)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		if !equalDurations(sleeper.delays, []time.Duration{0}) {
			t.Errorf("delays = %v, want [0s]", sleeper.delays)
		}
	})

	t.Run("aborts_on_an_already_cancelled_context", func(t *testing.T) {
		fake := &uploadertest.Fake{}
		r, sleeper, _ := newRetrier(t, fake, 3, time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := r.Upload(ctx, writeTemp(t))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if fake.CallCount() != 0 {
			t.Errorf("inner called %d times, want 0", fake.CallCount())
		}
		if len(sleeper.delays) != 0 {
			t.Errorf("slept %v, want none", sleeper.delays)
		}
	})

	t.Run("stops_when_the_sleep_is_interrupted", func(t *testing.T) {
		fake := &uploadertest.Fake{Err: boom}
		r, sleeper, _ := newRetrier(t, fake, 5, time.Second)
		sleeper.err = context.Canceled

		err := r.Upload(t.Context(), writeTemp(t))
		if err == nil {
			t.Fatal("expected an error")
		}
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to report the upload failure", err)
		}
		if fake.CallCount() != 1 {
			t.Errorf("inner called %d times, want 1", fake.CallCount())
		}
	})

	t.Run("cancellation_between_attempts_reports_the_last_error", func(t *testing.T) {
		fake := &uploadertest.Fake{Err: boom}
		ctx, cancel := context.WithCancel(t.Context())
		fake.OnCall = func(int) { cancel() }

		r, _, _ := newRetrier(t, fake, 3, time.Second)
		err := r.Upload(ctx, writeTemp(t))
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "aborted") {
			t.Errorf("err = %v, want it to say the upload was aborted", err)
		}
	})

	t.Run("logs_a_warning_per_retry", func(t *testing.T) {
		fake := &uploadertest.Fake{Errs: []error{boom, boom, nil}}
		r, _, logs := newRetrier(t, fake, 3, time.Second)

		if err := r.Upload(t.Context(), writeTemp(t)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		if n := strings.Count(logs.String(), "level=WARN"); n != 2 {
			t.Errorf("logged %d warnings, want 2:\n%s", n, logs.String())
		}
	})

	t.Run("default_sleep_is_installed", func(t *testing.T) {
		// A Retrier built as a literal must not panic on a nil Sleep.
		fake := &uploadertest.Fake{Errs: []error{errors.New("x"), nil}}
		r := &Retrier{Inner: fake, Attempts: 2, Backoff: time.Millisecond, Logger: slog.New(slog.DiscardHandler)}
		if err := r.Upload(t.Context(), writeTemp(t)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
	})
}

func TestBackoffFor(t *testing.T) {
	cases := []struct {
		base    time.Duration
		attempt int
		want    time.Duration
	}{
		{2 * time.Second, 1, 2 * time.Second},
		{2 * time.Second, 2, 4 * time.Second},
		{2 * time.Second, 3, 8 * time.Second},
		{2 * time.Second, 10, maxRetryBackoff},
		{0, 3, 0},
		{time.Hour, 1, time.Hour},
		{time.Hour, 2, maxRetryBackoff},
	}
	for _, tc := range cases {
		if got := backoffFor(tc.base, tc.attempt); got != tc.want {
			t.Errorf("backoffFor(%v, %d) = %v, want %v", tc.base, tc.attempt, got, tc.want)
		}
	}
}

func TestSleepContext(t *testing.T) {
	t.Run("returns_after_the_delay", func(t *testing.T) {
		if err := sleepContext(t.Context(), time.Millisecond); err != nil {
			t.Errorf("sleepContext: %v", err)
		}
	})

	t.Run("returns_immediately_for_a_zero_delay", func(t *testing.T) {
		if err := sleepContext(t.Context(), 0); err != nil {
			t.Errorf("sleepContext: %v", err)
		}
	})

	t.Run("honours_cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
			t.Errorf("sleepContext = %v, want context.Canceled", err)
		}
	})
}

func equalDurations(got, want []time.Duration) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func writeTemp(t *testing.T) string {
	t.Helper()
	return localFile(t, imageBody)
}
