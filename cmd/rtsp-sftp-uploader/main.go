// Command rtsp-sftp-uploader captures a still frame from an RTSP camera and
// uploads it to an SFTP server on a configurable interval.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/app"
	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/capture"
	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/config"
	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/health"
	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/logging"
	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/uploader"
)

// Populated at build time via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// shutdownGrace bounds how long the health server may take to drain.
const shutdownGrace = 5 * time.Second

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	cfg, err := config.LoadTo(os.LookupEnv, args, stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return err
	case err != nil:
		return fmt.Errorf("configuration:\n%w", err)
	}

	if cfg.ShowVersion {
		_, err := fmt.Fprintf(stdout, "rtsp-sftp-uploader %s (commit %s, built %s, %s/%s)\n",
			version, commit, date, runtime.GOOS, runtime.GOARCH)
		return err
	}

	redactor := config.FromConfig(cfg)
	logger, err := logging.New(stderr, cfg.Log.Level, cfg.Log.Format, redactor)
	if err != nil {
		return err
	}

	logger.Info("starting",
		"version", version,
		"commit", commit,
		"rtsp_url", cfg.RTSP.RedactedURL(),
		"rtsp_transport", cfg.RTSP.Transport,
		"sftp_host", cfg.SFTP.Addr(),
		"sftp_user", cfg.SFTP.Username,
		"remote_path", cfg.RemotePath(),
		"local_path", cfg.LocalPath(),
		"interval", cfg.Capture.Interval.String(),
	)

	grabber, err := capture.NewFFmpegGrabber(cfg.RTSP, cfg.Capture, redactor, logger)
	if err != nil {
		return err
	}

	sftpUploader, err := uploader.New(cfg.SFTP, cfg.RemotePath(), redactor, logger)
	if err != nil {
		return err
	}
	retrying := uploader.NewRetrier(sftpUploader, cfg.SFTP.RetryAttempts, cfg.SFTP.RetryBackoff, logger)

	state := health.NewState()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := startHealthServer(ctx, cfg, state, logger)
	if err != nil {
		return err
	}
	defer shutdownHealthServer(srv, logger)

	runner := &app.Runner{
		Grabber:   grabber,
		Uploader:  retrying,
		Health:    state,
		Logger:    logger,
		Redactor:  redactor,
		LocalPath: cfg.LocalPath(),
		Interval:  cfg.Capture.Interval,
	}

	runErr := runner.Run(ctx)
	logger.Info("stopped")
	return runErr
}

// startHealthServer starts the probe endpoints, or returns nil when they are
// disabled by an empty HTTP_ADDR.
func startHealthServer(ctx context.Context, cfg *config.Config, state *health.State, logger *slog.Logger) (*health.Server, error) {
	if cfg.HTTP.Addr == "" {
		logger.Info("health endpoints are disabled", "setting", config.EnvHTTPAddr)
		return nil, nil
	}

	staleness := cfg.EffectiveReadyMaxStaleness()
	srv := health.NewServer(cfg.HTTP.Addr, health.Handler(state, staleness, time.Now), logger)
	addr, err := srv.Start(ctx)
	if err != nil {
		return nil, err
	}
	logger.Info("health endpoints listening", "addr", addr, "ready_max_staleness", staleness)
	return srv, nil
}

func shutdownHealthServer(srv *health.Server, logger *slog.Logger) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Warn("the health server did not shut down cleanly", "err", err)
	}
}
