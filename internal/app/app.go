// Package app wires capture and upload into a scheduled loop.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/capture"
	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/config"
	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/health"
	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/uploader"
)

// Runner performs capture-and-upload cycles on a schedule.
type Runner struct {
	Grabber  capture.Grabber
	Uploader uploader.Uploader
	Health   *health.State
	Logger   *slog.Logger
	Redactor *config.Redactor

	// LocalPath is where the captured JPEG is written before upload.
	LocalPath string
	// Interval is the delay between cycles. Zero or less means "run once".
	Interval time.Duration

	// seams for tests
	Clock     func() time.Time
	NewTicker func(d time.Duration) (<-chan time.Time, func())
}

// defaults fills in the optional fields so a zero-value Runner is usable.
func (r *Runner) defaults() {
	if r.Logger == nil {
		r.Logger = slog.New(slog.DiscardHandler)
	}
	if r.Health == nil {
		r.Health = health.NewState()
	}
	if r.Clock == nil {
		r.Clock = time.Now
	}
	if r.NewTicker == nil {
		r.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
}

// RunOnce performs exactly one capture-and-upload cycle and records the outcome.
func (r *Runner) RunOnce(ctx context.Context) error {
	r.defaults()
	start := r.Clock()

	frame, err := r.Grabber.Grab(ctx, r.LocalPath)
	if err != nil {
		return r.fail(start, "capture", err)
	}

	if err := r.Uploader.Upload(ctx, frame.Path); err != nil {
		return r.fail(start, "upload", err)
	}

	finished := r.Clock()
	elapsed := finished.Sub(start)
	r.Health.RecordSuccess(finished)
	r.Logger.Info("cycle complete", "bytes", frame.Size, "duration", elapsed)

	if r.Interval > 0 && elapsed > r.Interval {
		r.Logger.Warn("a cycle took longer than the configured interval; captures will fall behind",
			"duration", elapsed, "interval", r.Interval)
	}
	return nil
}

// fail records a failed cycle and returns the redacted error.
func (r *Runner) fail(start time.Time, stage string, err error) error {
	wrapped := r.Redactor.Error(fmt.Errorf("%s: %w", stage, err))
	at := r.Clock()
	r.Health.RecordFailure(at, wrapped)
	r.Logger.Error("cycle failed", "stage", stage, "duration", at.Sub(start), "err", wrapped)
	return wrapped
}

// Run performs one cycle immediately, then repeats every Interval until ctx is
// done. In loop mode a failed cycle is logged and recorded but never stops the
// loop: a rebooting camera must not crash-loop the pod. Run returns nil on
// graceful shutdown.
//
// When Interval is zero or less, Run performs a single cycle and returns its error.
func (r *Runner) Run(ctx context.Context) error {
	r.defaults()

	if r.Interval <= 0 {
		r.Logger.Info("running a single capture", "local_path", r.LocalPath)
		return r.RunOnce(ctx)
	}

	r.Logger.Info("starting the capture loop", "interval", r.Interval, "local_path", r.LocalPath)

	// Capture immediately so the first image is available within seconds of
	// startup rather than after a full interval.
	if err := r.RunOnce(ctx); err != nil && ctx.Err() != nil {
		r.Logger.Info("shutting down")
		return nil
	}

	tick, stop := r.NewTicker(r.Interval)
	defer stop()

	for {
		select {
		case <-ctx.Done():
			r.Logger.Info("shutting down")
			return nil
		case <-tick:
			if err := r.RunOnce(ctx); err != nil && ctx.Err() != nil {
				r.Logger.Info("shutting down")
				return nil
			}
		}
	}
}
