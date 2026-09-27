package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/config"
)

const secret = "super-secret-password"

func newTestLogger(t *testing.T, format string) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger, err := New(&buf, "debug", format, config.NewRedactor(secret))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return logger, &buf
}

// decode reads the single JSON record written to buf.
func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("no log output")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("output is not valid JSON (%v): %s", err, line)
	}
	return m
}

func TestNew(t *testing.T) {
	t.Run("json_format_emits_valid_json", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		logger.Info("hello", "key", "value")
		m := decode(t, buf)
		if m["msg"] != "hello" || m["key"] != "value" || m["level"] != "INFO" {
			t.Errorf("unexpected record: %#v", m)
		}
	})

	t.Run("text_format", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatText)
		logger.Info("hello", "key", "value")
		out := buf.String()
		if !strings.Contains(out, "level=INFO") || !strings.Contains(out, `msg=hello`) {
			t.Errorf("unexpected text output: %s", out)
		}
	})

	t.Run("format_is_case_insensitive", func(t *testing.T) {
		if _, err := New(io.Discard, "info", "JSON", nil); err != nil {
			t.Errorf("New: %v", err)
		}
	})

	t.Run("unknown_format", func(t *testing.T) {
		_, err := New(io.Discard, "info", "xml", nil)
		if err == nil || !strings.Contains(err.Error(), config.EnvLogFormat) {
			t.Errorf("err = %v, want it to mention %s", err, config.EnvLogFormat)
		}
	})

	t.Run("unknown_level", func(t *testing.T) {
		_, err := New(io.Discard, "verbose", FormatJSON, nil)
		if err == nil || !strings.Contains(err.Error(), config.EnvLogLevel) {
			t.Errorf("err = %v, want it to mention %s", err, config.EnvLogLevel)
		}
	})

	t.Run("nil_redactor_is_accepted", func(t *testing.T) {
		var buf bytes.Buffer
		logger, err := New(&buf, "info", FormatJSON, nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		logger.Info("plain", "pw", secret)
		if !strings.Contains(buf.String(), secret) {
			t.Error("a nil redactor must not alter the output")
		}
	})
}

func TestLevels(t *testing.T) {
	cases := []struct {
		level string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			got, err := parseLevel(tc.level)
			if err != nil {
				t.Fatalf("parseLevel: %v", err)
			}
			if got != tc.want {
				t.Errorf("parseLevel(%q) = %v, want %v", tc.level, got, tc.want)
			}
		})
	}

	t.Run("level_filtering", func(t *testing.T) {
		var buf bytes.Buffer
		logger, err := New(&buf, "info", FormatJSON, nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		logger.Debug("dropped")
		if buf.Len() != 0 {
			t.Errorf("debug record was not filtered at info level: %s", buf.String())
		}
		logger.Info("kept")
		if !strings.Contains(buf.String(), "kept") {
			t.Error("info record was filtered at info level")
		}
	})

	t.Run("enabled_delegates_to_the_inner_handler", func(t *testing.T) {
		logger, _ := newTestLogger(t, FormatJSON)
		if !logger.Enabled(t.Context(), slog.LevelDebug) {
			t.Error("debug should be enabled at level debug")
		}
		quiet, err := New(io.Discard, "error", FormatJSON, nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if quiet.Enabled(t.Context(), slog.LevelInfo) {
			t.Error("info should be disabled at level error")
		}
	})
}

func TestRedaction(t *testing.T) {
	t.Run("redacts_message", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		logger.Info("connecting with " + secret)
		m := decode(t, buf)
		if strings.Contains(fmt.Sprint(m["msg"]), secret) {
			t.Fatalf("message leaked the secret: %v", m["msg"])
		}
		if !strings.Contains(fmt.Sprint(m["msg"]), "***") {
			t.Errorf("msg = %v, want a placeholder", m["msg"])
		}
	})

	t.Run("redacts_string_attribute", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		logger.Info("auth", "url", "rtsp://admin:"+secret+"@cam.lan/live")
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("attribute leaked the secret: %s", buf.String())
		}
	})

	t.Run("redacts_error_attribute", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		logger.Error("failed", "err", fmt.Errorf("auth failed for %s", secret))
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("error attribute leaked the secret: %s", buf.String())
		}
		if !strings.Contains(buf.String(), "***") {
			t.Errorf("no placeholder in %s", buf.String())
		}
	})

	t.Run("redacts_stringer_attribute", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		logger.Info("stringer", "val", stringer{secret})
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("Stringer leaked the secret: %s", buf.String())
		}
	})

	t.Run("redacts_grouped_attributes", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		logger.Info("grouped", slog.Group("sftp", slog.String("password", secret), slog.Int("port", 22)))
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("group leaked the secret: %s", buf.String())
		}
		m := decode(t, buf)
		group, ok := m["sftp"].(map[string]any)
		if !ok {
			t.Fatalf("sftp group missing: %#v", m)
		}
		if group["port"] != float64(22) {
			t.Errorf("group lost its non-string attribute: %#v", group)
		}
	})

	t.Run("redacts_attributes_added_with_With", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		logger.With("pw", secret).Info("later")
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("With() attribute leaked the secret: %s", buf.String())
		}
	})

	t.Run("redacts_inside_WithGroup", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		logger.WithGroup("conn").Info("x", "pw", secret)
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("WithGroup leaked the secret: %s", buf.String())
		}
	})

	t.Run("passes_through_non_string_attributes", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		logger.Info("types", "n", 42, "ok", true, "d", 3*time.Second, "f", 1.5)
		m := decode(t, buf)
		if m["n"] != float64(42) {
			t.Errorf("n = %#v, want 42", m["n"])
		}
		if m["ok"] != true {
			t.Errorf("ok = %#v, want true", m["ok"])
		}
		if m["d"] != "3s" {
			t.Errorf("d = %#v, want the human-readable duration \"3s\"", m["d"])
		}
		if m["f"] != 1.5 {
			t.Errorf("f = %#v", m["f"])
		}
	})

	t.Run("durations_render_as_readable_strings", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		logger.Info("timing", "interval", 90*time.Second, "took", 1500*time.Millisecond)
		m := decode(t, buf)
		if m["interval"] != "1m30s" {
			t.Errorf("interval = %#v, want \"1m30s\"", m["interval"])
		}
		if m["took"] != "1.5s" {
			t.Errorf("took = %#v, want \"1.5s\"", m["took"])
		}
	})

	t.Run("preserves_nil_error_attributes", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		var err error
		logger.Info("noerr", "err", err)
		if _, ok := decode(t, buf)["err"]; !ok {
			t.Error("attribute was dropped")
		}
	})

	t.Run("resolves_LogValuer_attributes", func(t *testing.T) {
		logger, buf := newTestLogger(t, FormatJSON)
		logger.Info("valuer", "v", valuer{secret})
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("LogValuer leaked the secret: %s", buf.String())
		}
	})
}

type stringer struct{ s string }

func (s stringer) String() string { return s.s }

type valuer struct{ s string }

func (v valuer) LogValue() slog.Value { return slog.StringValue(v.s) }

func TestHandleReturnsInnerError(t *testing.T) {
	inner := slog.NewJSONHandler(failingWriter{}, nil)
	h := &redactHandler{inner: inner, redactor: config.NewRedactor(secret)}
	err := h.Handle(t.Context(), slog.NewRecord(time.Now(), slog.LevelInfo, "x", 0))
	if !errors.Is(err, errWrite) {
		t.Errorf("Handle() = %v, want the writer error", err)
	}
}

var errWrite = errors.New("write failed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }
