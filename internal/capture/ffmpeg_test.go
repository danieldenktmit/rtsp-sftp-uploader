package capture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/config"
)

const (
	camPassword = "cam-secret-password"
	helperEnv   = "GO_WANT_HELPER_PROCESS"
)

var fixedNow = time.Date(2026, 9, 27, 8, 30, 0, 0, time.UTC)

// testRTSP is a camera configuration whose URL contains a password, so that
// redaction can be asserted.
func testRTSP() config.RTSPConfig {
	return config.RTSPConfig{
		Host:      "cam.lan",
		Port:      554,
		Path:      "/Streaming/Channels/101",
		Username:  "admin",
		Password:  camPassword,
		Transport: config.TransportTCP,
		Timeout:   15 * time.Second,
	}
}

func testCapture() config.CaptureConfig {
	return config.CaptureConfig{
		Interval:    time.Minute,
		OutputDir:   "/tmp",
		Filename:    "image.jpg",
		JPEGQuality: 2,
		Timeout:     30 * time.Second,
		// os.Args[0] is the test binary: a real, executable path for LookPath.
		FFmpegPath: os.Args[0],
	}
}

// newGrabber builds a grabber whose subprocess is this test binary in helper mode.
func newGrabber(t *testing.T, mode string, env ...string) *FFmpegGrabber {
	t.Helper()
	rtsp := testRTSP()
	cfg := &config.Config{RTSP: rtsp, SFTP: config.SFTPConfig{}}
	g, err := NewFFmpegGrabber(rtsp, testCapture(), config.FromConfig(cfg), nil)
	if err != nil {
		t.Fatalf("NewFFmpegGrabber: %v", err)
	}
	g.clock = func() time.Time { return fixedNow }
	g.commandContext = fakeExec(append([]string{"HELPER_MODE=" + mode}, env...)...)
	return g
}

// fakeExec re-executes this test binary in helper mode instead of running ffmpeg.
func fakeExec(env ...string) func(context.Context, string, ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(), helperEnv+"=1")
		cmd.Env = append(cmd.Env, env...)
		return cmd
	}
}

// TestHelperProcess stands in for ffmpeg. It only runs when the parent sets
// GO_WANT_HELPER_PROCESS=1 and behaves according to HELPER_MODE.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("not a test: this is the ffmpeg stand-in subprocess")
	}

	args := os.Args
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[i+1:]
	}
	out := ""
	if len(args) > 0 {
		out = args[len(args)-1]
	}

	if s := os.Getenv("HELPER_STDERR"); s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	if n := os.Getenv("HELPER_STDERR_REPEAT"); n != "" {
		for range 2000 {
			fmt.Fprintln(os.Stderr, "ffmpeg noise line that is long enough to matter:", n)
		}
	}

	switch os.Getenv("HELPER_MODE") {
	case "success":
		write(out, []byte{0xFF, 0xD8, 0xFF, 0xE0, 'p', 'a', 'y', 'l', 'o', 'a', 'd', 0xFF, 0xD9})
	case "empty":
		write(out, nil)
	case "notjpeg":
		write(out, []byte("hello, not an image"))
	case "tooshort":
		write(out, []byte{0xFF})
	case "nooutput":
		// exit cleanly without producing anything
	case "fail":
		os.Exit(1)
	case "hang":
		// Sleep rather than block on a channel: the runtime's deadlock detector
		// would abort a permanently blocked process before it can be killed.
		time.Sleep(10 * time.Minute)
	default:
		fmt.Fprintln(os.Stderr, "helper: unknown HELPER_MODE")
		os.Exit(2)
	}
	os.Exit(0)
}

func write(path string, body []byte) {
	if path == "" {
		return
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		os.Exit(3)
	}
}

func TestArgs(t *testing.T) {
	t.Run("exact_order", func(t *testing.T) {
		g := newGrabber(t, "success")
		got := g.Args("/tmp/image.jpg")
		want := []string{
			"-hide_banner",
			"-nostdin",
			"-loglevel", "error",
			"-y",
			"-rtsp_transport", "tcp",
			"-timeout", "15000000",
			"-i", "rtsp://admin:" + camPassword + "@cam.lan:554/Streaming/Channels/101",
			"-frames:v", "1",
			"-q:v", "2",
			"-f", "image2",
			"-update", "1",
			"/tmp/image.jpg.part",
		}
		if !slices.Equal(got, want) {
			t.Errorf("Args() mismatch\ngot:  %q\nwant: %q", got, want)
		}
	})

	t.Run("udp_transport", func(t *testing.T) {
		rtsp := testRTSP()
		rtsp.Transport = config.TransportUDP
		g, err := NewFFmpegGrabber(rtsp, testCapture(), nil, nil)
		if err != nil {
			t.Fatalf("NewFFmpegGrabber: %v", err)
		}
		args := g.Args("/tmp/x.jpg")
		if i := slices.Index(args, "-rtsp_transport"); args[i+1] != "udp" {
			t.Errorf("transport = %q, want udp", args[i+1])
		}
	})

	t.Run("quality_and_timeout_are_configurable", func(t *testing.T) {
		rtsp := testRTSP()
		rtsp.Timeout = 3 * time.Second
		cap := testCapture()
		cap.JPEGQuality = 7
		g, err := NewFFmpegGrabber(rtsp, cap, nil, nil)
		if err != nil {
			t.Fatalf("NewFFmpegGrabber: %v", err)
		}
		args := g.Args("/tmp/x.jpg")
		if i := slices.Index(args, "-q:v"); args[i+1] != "7" {
			t.Errorf("-q:v = %q, want 7", args[i+1])
		}
		if i := slices.Index(args, "-timeout"); args[i+1] != "3000000" {
			t.Errorf("-timeout = %q, want 3000000 microseconds", args[i+1])
		}
	})

	t.Run("output_is_the_part_file", func(t *testing.T) {
		g := newGrabber(t, "success")
		args := g.Args("/tmp/image.jpg")
		if got := args[len(args)-1]; got != "/tmp/image.jpg"+partSuffix {
			t.Errorf("last arg = %q", got)
		}
	})
}

func TestNewFFmpegGrabber(t *testing.T) {
	t.Run("rejects_missing_binary", func(t *testing.T) {
		cap := testCapture()
		cap.FFmpegPath = "/nonexistent/path/to/ffmpeg"
		_, err := NewFFmpegGrabber(testRTSP(), cap, nil, nil)
		if err == nil || !strings.Contains(err.Error(), config.EnvFFmpegPath) {
			t.Errorf("err = %v, want it to mention %s", err, config.EnvFFmpegPath)
		}
	})

	t.Run("rejects_invalid_rtsp_config", func(t *testing.T) {
		_, err := NewFFmpegGrabber(config.RTSPConfig{URL: "http://cam/x"}, testCapture(), nil, nil)
		if err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("exposes_a_redacted_source_url", func(t *testing.T) {
		g := newGrabber(t, "success")
		if strings.Contains(g.SourceURL(), camPassword) {
			t.Errorf("SourceURL leaked the password: %s", g.SourceURL())
		}
		if !strings.Contains(g.SourceURL(), "cam.lan") {
			t.Errorf("SourceURL = %q", g.SourceURL())
		}
	})
}

func TestGrabSuccess(t *testing.T) {
	t.Run("writes_the_file_and_returns_a_frame", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "image.jpg")
		g := newGrabber(t, "success")

		frame, err := g.Grab(t.Context(), dst)
		if err != nil {
			t.Fatalf("Grab: %v", err)
		}
		if frame.Path != dst {
			t.Errorf("Path = %q, want %q", frame.Path, dst)
		}
		if frame.CapturedAt != fixedNow {
			t.Errorf("CapturedAt = %v, want the injected clock value %v", frame.CapturedAt, fixedNow)
		}
		body, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("reading the result: %v", err)
		}
		if int64(len(body)) != frame.Size {
			t.Errorf("Size = %d, want %d", frame.Size, len(body))
		}
		assertNoPartFile(t, dst)
	})

	t.Run("overwrites_an_existing_file", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "image.jpg")
		if err := os.WriteFile(dst, []byte("stale content that is much longer than the new one"), 0o600); err != nil {
			t.Fatal(err)
		}
		g := newGrabber(t, "success")
		if _, err := g.Grab(t.Context(), dst); err != nil {
			t.Fatalf("Grab: %v", err)
		}
		body, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "stale") {
			t.Errorf("stale content survived: %q", body)
		}
	})

	t.Run("creates_the_output_directory", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "nested", "deeper", "image.jpg")
		g := newGrabber(t, "success")
		if _, err := g.Grab(t.Context(), dst); err != nil {
			t.Fatalf("Grab: %v", err)
		}
		if _, err := os.Stat(dst); err != nil {
			t.Errorf("output file missing: %v", err)
		}
	})
}

func TestGrabFailures(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		env      []string
		mentions string
	}{
		{"nonzero_exit", "fail", []string{"HELPER_STDERR=Connection refused"}, "Connection refused"},
		{"empty_output", "empty", nil, "empty image"},
		{"non_jpeg_output", "notjpeg", nil, "not a JPEG"},
		{"output_too_short", "tooshort", nil, "not a JPEG"},
		{"no_output_file", "nooutput", nil, "no output file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "image.jpg")
			g := newGrabber(t, tc.mode, tc.env...)

			_, err := g.Grab(t.Context(), dst)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.mentions) {
				t.Errorf("err = %v, want it to mention %q", err, tc.mentions)
			}
			if _, statErr := os.Stat(dst); statErr == nil {
				t.Error("a failed capture must not publish the destination file")
			}
			assertNoPartFile(t, dst)
		})
	}

	t.Run("stderr_includes_the_ffmpeg_output", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "image.jpg")
		g := newGrabber(t, "fail", "HELPER_STDERR=rtsp: 401 Unauthorized")
		_, err := g.Grab(t.Context(), dst)
		if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("stderr_is_capped", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "image.jpg")
		g := newGrabber(t, "fail", "HELPER_STDERR_REPEAT=x")
		_, err := g.Grab(t.Context(), dst)
		if err == nil {
			t.Fatal("expected an error")
		}
		if n := len(err.Error()); n > stderrTailBytes+512 {
			t.Errorf("error message is %d bytes; the stderr tail is not capped", n)
		}
		if !strings.Contains(err.Error(), "...") {
			t.Error("a truncated tail should be marked with an ellipsis")
		}
	})

	t.Run("password_is_redacted_from_ffmpeg_stderr", func(t *testing.T) {
		// ffmpeg echoes the input URL, credentials included, when it fails.
		dst := filepath.Join(t.TempDir(), "image.jpg")
		leak := "rtsp://admin:" + camPassword + "@cam.lan:554/live: Server returned 401"
		g := newGrabber(t, "fail", "HELPER_STDERR="+leak)

		_, err := g.Grab(t.Context(), dst)
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), camPassword) {
			t.Fatalf("the camera password leaked into the error: %v", err)
		}
		if !strings.Contains(err.Error(), "***") {
			t.Errorf("err = %v, want a redaction placeholder", err)
		}
		if !strings.Contains(err.Error(), "401") {
			t.Errorf("redaction destroyed the diagnostic: %v", err)
		}
	})

	t.Run("output_directory_cannot_be_created", func(t *testing.T) {
		base := t.TempDir()
		blocker := filepath.Join(base, "blocked")
		if err := os.WriteFile(blocker, []byte("i am a file"), 0o600); err != nil {
			t.Fatal(err)
		}
		g := newGrabber(t, "success")
		if _, err := g.Grab(t.Context(), filepath.Join(blocker, "image.jpg")); err == nil {
			t.Error("expected an error when the output directory cannot be created")
		}
	})

	t.Run("rename_failure_is_reported", func(t *testing.T) {
		dir := t.TempDir()
		dst := filepath.Join(dir, "image.jpg")
		// A directory at the destination makes the final rename fail.
		if err := os.Mkdir(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		g := newGrabber(t, "success")
		if _, err := g.Grab(t.Context(), dst); err == nil {
			t.Error("expected the rename to fail")
		}
		assertNoPartFile(t, dst)
	})
}

func TestGrabContext(t *testing.T) {
	t.Run("capture_timeout_kills_the_process", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "image.jpg")
		g := newGrabber(t, "hang")
		g.timeout = 150 * time.Millisecond

		start := time.Now()
		_, err := g.Grab(t.Context(), dst)
		elapsed := time.Since(start)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want it to wrap context.DeadlineExceeded", err)
		}
		if elapsed > 3*time.Second {
			t.Errorf("Grab took %v; the subprocess was not killed promptly", elapsed)
		}
		assertNoPartFile(t, dst)
	})

	t.Run("parent_cancellation_is_propagated", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "image.jpg")
		g := newGrabber(t, "hang")

		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		_, err := g.Grab(ctx, dst)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want it to wrap context.Canceled", err)
		}
	})

	t.Run("already_cancelled_context", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "image.jpg")
		g := newGrabber(t, "success")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := g.Grab(ctx, dst); err == nil {
			t.Error("expected an error for an already-cancelled context")
		}
	})
}

func assertNoPartFile(t *testing.T, dst string) {
	t.Helper()
	if _, err := os.Stat(dst + partSuffix); err == nil {
		t.Errorf("a leftover %s file remains", partSuffix)
	}
}

func TestTailBuffer(t *testing.T) {
	cases := []struct {
		name   string
		max    int
		writes []string
		want   string
	}{
		{"short_write", 10, []string{"abc"}, "abc"},
		{"exact_size", 5, []string{"abcde"}, "...abcde"},
		{"single_overflowing_write", 4, []string{"abcdefgh"}, "...efgh"},
		{"accumulating_writes_overflow", 5, []string{"abc", "def"}, "...bcdef"},
		{"many_small_writes", 4, []string{"a", "b", "c", "d", "e", "f"}, "...cdef"},
		{"no_writes", 8, nil, ""},
		{"whitespace_is_trimmed", 16, []string{"  padded  "}, "padded"},
		{"zero_capacity", 0, []string{"anything"}, "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newTailBuffer(tc.max)
			for _, w := range tc.writes {
				n, err := b.Write([]byte(w))
				if err != nil {
					t.Fatalf("Write: %v", err)
				}
				if n != len(w) {
					t.Errorf("Write returned %d, want %d", n, len(w))
				}
			}
			if got := b.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
