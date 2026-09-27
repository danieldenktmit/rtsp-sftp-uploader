// Package logging builds the application logger and guarantees that known
// secrets never reach the log output.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/config"
)

// Format values accepted by New.
const (
	FormatJSON = "json"
	FormatText = "text"
)

// New builds a logger writing to w. level is one of debug, info, warn or error;
// format is json or text. Secrets known to r are scrubbed from every record.
func New(w io.Writer, level, format string, r *config.Redactor) (*slog.Logger, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var h slog.Handler
	switch strings.ToLower(format) {
	case FormatJSON:
		h = slog.NewJSONHandler(w, opts)
	case FormatText:
		h = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("%s: %q must be %q or %q", config.EnvLogFormat, format, FormatJSON, FormatText)
	}

	return slog.New(&redactHandler{inner: h, redactor: r}), nil
}

func parseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%s: %q must be one of debug, info, warn, error", config.EnvLogLevel, level)
	}
}

// redactHandler scrubs secrets from the message and from every string-valued
// attribute before delegating to the wrapped handler.
type redactHandler struct {
	inner    slog.Handler
	redactor *config.Redactor
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.redactor.String(rec.Message), rec.PC)
	attrs := make([]slog.Attr, 0, rec.NumAttrs())
	rec.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, h.redactAttr(a))
		return true
	})
	out.AddAttrs(attrs...)
	return h.inner.Handle(ctx, out)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = h.redactAttr(a)
	}
	return &redactHandler{inner: h.inner.WithAttrs(redacted), redactor: h.redactor}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name), redactor: h.redactor}
}

// redactAttr rewrites string, duration, error and group values; other kinds pass
// through untouched so that numbers and booleans keep their native type.
func (h *redactHandler) redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.redactor.String(v.String()))
	case slog.KindDuration:
		// slog renders durations as raw nanosecond counts, which is unreadable
		// in pod logs ("interval":5000000000). Emit "5s" instead.
		return slog.String(a.Key, v.Duration().String())
	case slog.KindGroup:
		src := v.Group()
		out := make([]any, 0, len(src))
		for _, g := range src {
			out = append(out, h.redactAttr(g))
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, h.redactor.String(err.Error()))
		}
		if s, ok := v.Any().(fmt.Stringer); ok {
			return slog.String(a.Key, h.redactor.String(s.String()))
		}
		return a
	default:
		return a
	}
}
