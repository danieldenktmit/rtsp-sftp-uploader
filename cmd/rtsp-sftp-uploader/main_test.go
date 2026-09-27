package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/config"
)

// setMinimalEnv configures just enough for the wiring to succeed.
func setMinimalEnv(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvRTSPHost, "cam.lan")
	t.Setenv(config.EnvSFTPHost, "sftp.lan")
	t.Setenv(config.EnvSFTPUsername, "uploader")
	t.Setenv(config.EnvSFTPPassword, "sftp-secret-password")
	t.Setenv(config.EnvSFTPInsecureIgnoreHost, "true")
}

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"--version"}, &out, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	for _, want := range []string{"rtsp-sftp-uploader", version, commit} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q is missing %q", got, want)
		}
	}
}

func TestRunVersionNeedsNoConfiguration(t *testing.T) {
	// --version must work in a container with no environment at all; the smoke
	// test in CI depends on it.
	var out bytes.Buffer
	if err := run([]string{"--version"}, &out, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRunHelp(t *testing.T) {
	var stderr bytes.Buffer
	err := run([]string{"--help"}, io.Discard, &stderr)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
	usage := stderr.String()
	for _, want := range []string{"-rtsp-host", "-capture-interval", "-sftp-remote-dir", "env RTSP_HOST"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage output is missing %q", want)
		}
	}
}

func TestRunInvalidConfiguration(t *testing.T) {
	t.Run("missing_everything", func(t *testing.T) {
		err := run(nil, io.Discard, io.Discard)
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, want := range []string{config.EnvRTSPHost, config.EnvSFTPUsername} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to mention %s", err, want)
			}
		}
	})

	t.Run("bad_log_level", func(t *testing.T) {
		setMinimalEnv(t)
		t.Setenv(config.EnvLogLevel, "verbose")
		err := run(nil, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), config.EnvLogLevel) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("unknown_flag", func(t *testing.T) {
		setMinimalEnv(t)
		if err := run([]string{"--nope"}, io.Discard, io.Discard); err == nil {
			t.Error("expected an error")
		}
	})
}

func TestRunRejectsAnUnusableFFmpegPath(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv(config.EnvFFmpegPath, "/nonexistent/ffmpeg")
	t.Setenv(config.EnvHTTPAddr, "")

	err := run(nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), config.EnvFFmpegPath) {
		t.Errorf("err = %v, want it to mention %s", err, config.EnvFFmpegPath)
	}
}

func TestRunRejectsAnUnusablePrivateKey(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv(config.EnvFFmpegPath, "sh") // any real binary, so ffmpeg lookup succeeds
	t.Setenv(config.EnvSFTPPrivateKeyPath, "/nonexistent/id_ed25519")
	t.Setenv(config.EnvHTTPAddr, "")

	err := run(nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), config.EnvSFTPPrivateKeyPath) {
		t.Errorf("err = %v, want it to mention %s", err, config.EnvSFTPPrivateKeyPath)
	}
}

func TestRunReportsAnUnusableHealthAddress(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv(config.EnvFFmpegPath, "sh")
	t.Setenv(config.EnvHTTPAddr, "256.0.0.1:99999")

	err := run(nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "listening on") {
		t.Errorf("err = %v, want a listen failure", err)
	}
}

func TestRunLogsStartupDetailsWithoutLeakingSecrets(t *testing.T) {
	const camPassword = "cam-secret-password"
	setMinimalEnv(t)
	t.Setenv(config.EnvRTSPPassword, camPassword)
	t.Setenv(config.EnvFFmpegPath, "/nonexistent/ffmpeg") // fail right after the startup log
	t.Setenv(config.EnvHTTPAddr, "")

	var stderr bytes.Buffer
	if err := run(nil, io.Discard, &stderr); err == nil {
		t.Fatal("expected the run to fail")
	}

	logs := stderr.String()
	if !strings.Contains(logs, `"msg":"starting"`) {
		t.Errorf("no startup log line:\n%s", logs)
	}
	if strings.Contains(logs, camPassword) {
		t.Errorf("the startup log leaked the camera password:\n%s", logs)
	}
	if !strings.Contains(logs, "cam.lan") {
		t.Errorf("the startup log should name the camera host:\n%s", logs)
	}
}
