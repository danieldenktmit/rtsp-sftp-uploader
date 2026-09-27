package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/capture/capturetest"
	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/config"
	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/health"
	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/uploader/uploadertest"
)

const camPassword = "cam-secret-password"

var base = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

// fakeClock advances by step on every read, so durations are deterministic.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	step  time.Duration
	reads int
}

func newClock(step time.Duration) *fakeClock {
	return &fakeClock{now: base, step: step}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.now
	c.now = c.now.Add(c.step)
	c.reads++
	return t
}

// manualTicker is a ticker whose ticks the test controls.
type manualTicker struct {
	ch      chan time.Time
	mu      sync.Mutex
	stopped int
}

func newManualTicker() *manualTicker {
	return &manualTicker{ch: make(chan time.Time, 16)}
}

func (m *manualTicker) new(time.Duration) (<-chan time.Time, func()) {
	return m.ch, func() {
		m.mu.Lock()
		m.stopped++
		m.mu.Unlock()
	}
}

func (m *manualTicker) tick() { m.ch <- base }

func (m *manualTicker) stopCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopped
}

type harness struct {
	runner  *Runner
	grabber *capturetest.Fake
	upload  *uploadertest.Fake
	state   *health.State
	logs    *bytes.Buffer
	ticker  *manualTicker
	local   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	logs := &bytes.Buffer{}
	h := &harness{
		grabber: &capturetest.Fake{},
		upload:  &uploadertest.Fake{},
		state:   health.NewState(),
		logs:    logs,
		ticker:  newManualTicker(),
		local:   filepath.Join(t.TempDir(), "image.jpg"),
	}
	h.runner = &Runner{
		Grabber:   h.grabber,
		Uploader:  h.upload,
		Health:    h.state,
		Logger:    slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Redactor:  config.NewRedactor(camPassword),
		LocalPath: h.local,
		Clock:     newClock(time.Second).Now,
		NewTicker: h.ticker.new,
	}
	return h
}

// logRecords decodes the captured JSON log lines.
func (h *harness) logRecords(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(h.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON (%v): %s", err, line)
		}
		out = append(out, m)
	}
	return out
}

func (h *harness) findLog(t *testing.T, msg string) map[string]any {
	t.Helper()
	for _, rec := range h.logRecords(t) {
		if rec["msg"] == msg {
			return rec
		}
	}
	return nil
}

func TestRunOnce(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h := newHarness(t)

		if err := h.runner.RunOnce(t.Context()); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		if h.grabber.CallCount() != 1 || h.grabber.LastCall() != h.local {
			t.Errorf("grabber calls = %v, want one call for %s", h.grabber.Calls, h.local)
		}
		if h.upload.CallCount() != 1 || h.upload.Calls[0] != h.local {
			t.Errorf("uploader calls = %v", h.upload.Calls)
		}
		if !bytes.Equal(h.upload.LastBody(), capturetest.MinimalJPEG) {
			t.Errorf("the uploader saw %q, want the bytes the grabber wrote", h.upload.LastBody())
		}
		snap := h.state.Snapshot()
		if snap.Successes != 1 || snap.Failures != 0 {
			t.Errorf("health = %+v", snap)
		}
	})

	t.Run("capture_failure_skips_the_upload", func(t *testing.T) {
		h := newHarness(t)
		boom := errors.New("camera unreachable")
		h.grabber.Err = boom

		err := h.runner.RunOnce(t.Context())
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want it to wrap the grabber error", err)
		}
		if !strings.Contains(err.Error(), "capture") {
			t.Errorf("err = %v, want it to name the stage", err)
		}
		if h.upload.CallCount() != 0 {
			t.Errorf("the uploader was called %d times; a failed capture must not upload", h.upload.CallCount())
		}
		snap := h.state.Snapshot()
		if snap.Failures != 1 || snap.Successes != 0 {
			t.Errorf("health = %+v", snap)
		}
		if !strings.Contains(snap.LastError, "camera unreachable") {
			t.Errorf("LastError = %q", snap.LastError)
		}
	})

	t.Run("upload_failure_is_recorded", func(t *testing.T) {
		h := newHarness(t)
		boom := errors.New("sftp refused")
		h.upload.Err = boom

		err := h.runner.RunOnce(t.Context())
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), "upload") {
			t.Errorf("err = %v, want it to name the stage", err)
		}
		if h.grabber.CallCount() != 1 {
			t.Errorf("grabber calls = %d, want 1", h.grabber.CallCount())
		}
		if h.state.Snapshot().Failures != 1 {
			t.Errorf("health = %+v", h.state.Snapshot())
		}
	})

	t.Run("errors_are_redacted_before_being_logged_or_served", func(t *testing.T) {
		h := newHarness(t)
		h.grabber.Err = errors.New("rtsp://admin:" + camPassword + "@cam.lan/live failed")

		err := h.runner.RunOnce(t.Context())
		if strings.Contains(err.Error(), camPassword) {
			t.Fatalf("the returned error leaked the password: %v", err)
		}
		if got := h.state.Snapshot().LastError; strings.Contains(got, camPassword) {
			t.Fatalf("/status would leak the password: %q", got)
		}
		if strings.Contains(h.logs.String(), camPassword) {
			t.Fatalf("the logs leaked the password: %s", h.logs.String())
		}
		if !strings.Contains(err.Error(), "***") {
			t.Errorf("err = %v, want a redaction placeholder", err)
		}
	})

	t.Run("logs_the_failing_stage", func(t *testing.T) {
		for stage, setup := range map[string]func(*harness){
			"capture": func(h *harness) { h.grabber.Err = errors.New("x") },
			"upload":  func(h *harness) { h.upload.Err = errors.New("x") },
		} {
			t.Run(stage, func(t *testing.T) {
				h := newHarness(t)
				setup(h)
				_ = h.runner.RunOnce(t.Context())

				rec := h.findLog(t, "cycle failed")
				if rec == nil {
					t.Fatalf("no failure log line:\n%s", h.logs.String())
				}
				if rec["stage"] != stage {
					t.Errorf("stage = %v, want %q", rec["stage"], stage)
				}
			})
		}
	})

	t.Run("logs_the_byte_count_on_success", func(t *testing.T) {
		h := newHarness(t)
		if err := h.runner.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		rec := h.findLog(t, "cycle complete")
		if rec == nil {
			t.Fatalf("no success log line:\n%s", h.logs.String())
		}
		if rec["bytes"] != float64(len(capturetest.MinimalJPEG)) {
			t.Errorf("bytes = %v, want %d", rec["bytes"], len(capturetest.MinimalJPEG))
		}
	})

	t.Run("warns_when_a_cycle_overruns_the_interval", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = time.Second
		h.runner.Clock = newClock(10 * time.Second).Now // each cycle appears to take 10s

		if err := h.runner.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		if rec := h.findLog(t, "a cycle took longer than the configured interval; captures will fall behind"); rec == nil {
			t.Errorf("expected an overrun warning:\n%s", h.logs.String())
		}
	})

	t.Run("no_overrun_warning_when_within_the_interval", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = time.Hour
		if err := h.runner.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h.logs.String(), "fall behind") {
			t.Errorf("unexpected overrun warning:\n%s", h.logs.String())
		}
	})

	t.Run("zero_value_optional_fields_do_not_panic", func(t *testing.T) {
		local := filepath.Join(t.TempDir(), "image.jpg")
		r := &Runner{
			Grabber:   &capturetest.Fake{},
			Uploader:  &uploadertest.Fake{},
			LocalPath: local,
		}
		if err := r.RunOnce(t.Context()); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		if r.Health.Snapshot().Successes != 1 {
			t.Error("a default health state was not installed")
		}
	})
}

func TestRunOnceMode(t *testing.T) {
	t.Run("interval_zero_runs_exactly_one_cycle", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = 0

		if err := h.runner.Run(t.Context()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if h.grabber.CallCount() != 1 {
			t.Errorf("grabber calls = %d, want 1", h.grabber.CallCount())
		}
		if h.ticker.stopCount() != 0 {
			t.Error("one-shot mode must not create a ticker")
		}
	})

	t.Run("interval_zero_propagates_the_error", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = 0
		boom := errors.New("boom")
		h.grabber.Err = boom

		if err := h.runner.Run(t.Context()); !errors.Is(err, boom) {
			t.Errorf("Run = %v, want it to wrap %v", err, boom)
		}
	})

	t.Run("negative_interval_behaves_as_one_shot", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = -time.Second
		if err := h.runner.Run(t.Context()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if h.grabber.CallCount() != 1 {
			t.Errorf("grabber calls = %d, want 1", h.grabber.CallCount())
		}
	})
}

func TestRunLoop(t *testing.T) {
	// runLoop starts Run in the background and returns a func that waits for it.
	runLoop := func(t *testing.T, h *harness, ctx context.Context) func() error {
		t.Helper()
		errCh := make(chan error, 1)
		go func() { errCh <- h.runner.Run(ctx) }()
		return func() error {
			select {
			case err := <-errCh:
				return err
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not return within 5s")
				return nil
			}
		}
	}

	// waitForCycles polls until n cycles have *finished*.
	//
	// Waiting on the grabber call count instead would be a race: a cycle is a
	// capture followed by an upload, so the nth capture can land while the nth
	// upload is still in flight, and a cancel at that moment aborts it.
	waitForCycles := func(t *testing.T, h *harness, n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			snap := h.state.Snapshot()
			if snap.Successes+snap.Failures >= uint64(n) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		snap := h.state.Snapshot()
		t.Fatalf("%d cycles completed (%d ok, %d failed), want %d",
			snap.Successes+snap.Failures, snap.Successes, snap.Failures, n)
	}

	t.Run("first_cycle_runs_immediately", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = time.Hour
		ctx, cancel := context.WithCancel(t.Context())
		wait := runLoop(t, h, ctx)

		waitForCycles(t, h, 1) // no tick was sent, so this can only be the eager cycle
		cancel()
		if err := wait(); err != nil {
			t.Errorf("Run = %v, want nil on graceful shutdown", err)
		}
	})

	t.Run("one_cycle_per_tick", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = time.Hour
		ctx, cancel := context.WithCancel(t.Context())
		wait := runLoop(t, h, ctx)

		waitForCycles(t, h, 1)
		for range 3 {
			h.ticker.tick()
		}
		waitForCycles(t, h, 4) // 1 eager + 3 ticks

		cancel()
		if err := wait(); err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
		if h.upload.CallCount() != 4 {
			t.Errorf("uploader calls = %d, want 4", h.upload.CallCount())
		}
		if h.state.Snapshot().Successes != 4 {
			t.Errorf("health = %+v", h.state.Snapshot())
		}
	})

	t.Run("survives_cycle_errors", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = time.Hour
		h.grabber.Errs = []error{errors.New("first"), errors.New("second"), nil}
		ctx, cancel := context.WithCancel(t.Context())
		wait := runLoop(t, h, ctx)

		waitForCycles(t, h, 1)
		h.ticker.tick()
		h.ticker.tick()
		waitForCycles(t, h, 3)

		cancel()
		if err := wait(); err != nil {
			t.Errorf("Run = %v, want nil: cycle errors must not stop the loop", err)
		}
		snap := h.state.Snapshot()
		if snap.Failures != 2 || snap.Successes != 1 {
			t.Errorf("health = %+v, want 2 failures and 1 success", snap)
		}
	})

	t.Run("stops_the_ticker_on_shutdown", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = time.Hour
		ctx, cancel := context.WithCancel(t.Context())
		wait := runLoop(t, h, ctx)

		waitForCycles(t, h, 1)
		cancel()
		if err := wait(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := h.ticker.stopCount(); got != 1 {
			t.Errorf("ticker stopped %d times, want exactly 1", got)
		}
	})

	t.Run("cancellation_during_the_first_cycle_returns_nil", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = time.Hour
		ctx, cancel := context.WithCancel(t.Context())
		h.grabber.OnCall = func(int) { cancel() }

		if err := h.runner.Run(ctx); err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
		if h.grabber.CallCount() != 1 {
			t.Errorf("grabber calls = %d, want 1", h.grabber.CallCount())
		}
	})

	t.Run("cancellation_during_a_later_cycle_returns_nil", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = time.Hour
		ctx, cancel := context.WithCancel(t.Context())
		h.grabber.OnCall = func(n int) {
			if n == 2 {
				cancel()
			}
		}
		wait := runLoop(t, h, ctx)

		waitForCycles(t, h, 1)
		h.ticker.tick()
		if err := wait(); err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
	})

	t.Run("already_cancelled_context_stops_immediately", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = time.Hour
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		if err := h.runner.Run(ctx); err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
		if h.upload.CallCount() != 0 {
			t.Errorf("uploader calls = %d, want 0", h.upload.CallCount())
		}
	})

	t.Run("logs_the_shutdown", func(t *testing.T) {
		h := newHarness(t)
		h.runner.Interval = time.Hour
		ctx, cancel := context.WithCancel(t.Context())
		wait := runLoop(t, h, ctx)
		waitForCycles(t, h, 1)
		cancel()
		_ = wait()

		if h.findLog(t, "shutting down") == nil {
			t.Errorf("expected a shutdown log line:\n%s", h.logs.String())
		}
		if h.findLog(t, "starting the capture loop") == nil {
			t.Errorf("expected a startup log line:\n%s", h.logs.String())
		}
	})

	t.Run("a_cycle_cut_short_by_shutdown_is_not_recorded_as_a_failure", func(t *testing.T) {
		// A graceful termination that lands mid-cycle must not leave an ERROR in
		// the logs or a phantom failure in /status.
		h := newHarness(t)
		h.runner.Interval = time.Hour
		ctx, cancel := context.WithCancel(t.Context())
		h.upload.OnCall = func(int) { cancel() } // cancel between capture and upload

		if err := h.runner.Run(ctx); err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}

		snap := h.state.Snapshot()
		if snap.Failures != 0 {
			t.Errorf("Failures = %d, want 0: shutdown is not a failure (%+v)", snap.Failures, snap)
		}
		if snap.LastError != "" {
			t.Errorf("LastError = %q, want empty", snap.LastError)
		}
		if h.findLog(t, "cycle failed") != nil {
			t.Errorf("a shutdown must not log an error:\n%s", h.logs.String())
		}
		if h.findLog(t, "cycle interrupted by shutdown") == nil {
			t.Errorf("expected an informational interruption log:\n%s", h.logs.String())
		}
	})

	t.Run("a_genuine_failure_is_still_recorded", func(t *testing.T) {
		// Guard against the shutdown suppression swallowing real failures.
		h := newHarness(t)
		h.runner.Interval = time.Hour
		h.grabber.Err = errors.New("camera unreachable")
		ctx, cancel := context.WithCancel(t.Context())
		wait := runLoop(t, h, ctx)

		waitForCycles(t, h, 1)
		cancel()
		if err := wait(); err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		snap := h.state.Snapshot()
		if snap.Failures != 1 {
			t.Errorf("Failures = %d, want 1 (%+v)", snap.Failures, snap)
		}
		if !strings.Contains(snap.LastError, "camera unreachable") {
			t.Errorf("LastError = %q", snap.LastError)
		}
	})

	t.Run("default_ticker_is_installed", func(t *testing.T) {
		// A Runner built without seams must loop on a real ticker. The loop is
		// stopped by the third cycle rather than by a wall-clock window, so a
		// loaded machine cannot turn this into a flake; the surrounding timeout is
		// only a backstop for the case the ticker never fires at all.
		local := filepath.Join(t.TempDir(), "image.jpg")
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		grabber := &capturetest.Fake{OnCall: func(n int) {
			if n >= 3 {
				cancel()
			}
		}}
		r := &Runner{
			Grabber:   grabber,
			Uploader:  &uploadertest.Fake{},
			LocalPath: local,
			Interval:  5 * time.Millisecond,
		}

		if err := r.Run(ctx); err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
		if got := grabber.CallCount(); got < 3 {
			t.Errorf("grabber called %d times, want 3; the real ticker did not fire", got)
		}
	})
}
