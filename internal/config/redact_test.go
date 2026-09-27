package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRedactorString(t *testing.T) {
	t.Run("redacts_all_secrets", func(t *testing.T) {
		r := NewRedactor("cam-password", "sftp-password")
		got := r.String("cam-password failed then sftp-password failed")
		if strings.Contains(got, "password") && strings.Contains(got, "cam-") {
			t.Fatalf("not redacted: %q", got)
		}
		if got != "*** failed then *** failed" {
			t.Errorf("String() = %q", got)
		}
	})

	t.Run("redacts_repeated_occurrences", func(t *testing.T) {
		r := NewRedactor("hunter22")
		if got := r.String("hunter22/hunter22"); got != "***/***" {
			t.Errorf("String() = %q", got)
		}
	})

	t.Run("longer_secrets_are_applied_first", func(t *testing.T) {
		// "pass" is a prefix of "password1"; the longer value must win so that
		// the full secret cannot survive as "***word1".
		r := NewRedactor("pass", "password1")
		if got := r.String("login=password1"); got != "login=***" {
			t.Errorf("String() = %q, want login=***", got)
		}
	})

	t.Run("ignores_short_secrets", func(t *testing.T) {
		r := NewRedactor("abc")
		const in = "abc is too short to redact safely"
		if got := r.String(in); got != in {
			t.Errorf("String() = %q, want it unchanged", got)
		}
	})

	t.Run("deduplicates_secrets", func(t *testing.T) {
		r := NewRedactor("same-secret", "same-secret")
		if n := len(r.secrets); n != 1 {
			t.Errorf("secrets = %d, want 1", n)
		}
	})

	t.Run("no_secrets_is_identity", func(t *testing.T) {
		r := NewRedactor()
		const in = "nothing to hide"
		if got := r.String(in); got != in {
			t.Errorf("String() = %q", got)
		}
	})

	t.Run("empty_string", func(t *testing.T) {
		if got := NewRedactor("secret-value").String(""); got != "" {
			t.Errorf("String() = %q", got)
		}
	})

	t.Run("nil_redactor_is_a_noop", func(t *testing.T) {
		var r *Redactor
		const in = "secret-value"
		if got := r.String(in); got != in {
			t.Errorf("String() = %q", got)
		}
		if err := r.Error(errors.New(in)); err == nil || err.Error() != in {
			t.Errorf("Error() = %v", err)
		}
	})
}

func TestRedactorError(t *testing.T) {
	sentinel := errors.New("wrong credentials")

	t.Run("nil_error_returns_nil", func(t *testing.T) {
		if err := NewRedactor("secret-value").Error(nil); err != nil {
			t.Errorf("Error(nil) = %v", err)
		}
	})

	t.Run("redacts_message", func(t *testing.T) {
		in := fmt.Errorf("auth failed for %s: %w", "secret-value", sentinel)
		err := NewRedactor("secret-value").Error(in)
		if strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("leaked: %q", err.Error())
		}
		if !strings.Contains(err.Error(), redactPlaceholder) {
			t.Errorf("Error() = %q, want the placeholder", err.Error())
		}
	})

	t.Run("preserves_errors_is", func(t *testing.T) {
		in := fmt.Errorf("auth failed for %s: %w", "secret-value", sentinel)
		err := NewRedactor("secret-value").Error(in)
		if !errors.Is(err, sentinel) {
			t.Error("errors.Is no longer finds the wrapped sentinel")
		}
	})

	t.Run("returns_the_original_when_nothing_changed", func(t *testing.T) {
		err := NewRedactor("secret-value").Error(sentinel)
		if err != sentinel { //nolint:errorlint // identity is exactly what is asserted
			t.Errorf("expected the original error instance back, got %#v", err)
		}
	})

	t.Run("no_secrets_returns_the_original", func(t *testing.T) {
		if err := NewRedactor().Error(sentinel); err != sentinel { //nolint:errorlint
			t.Errorf("expected the original error instance back, got %#v", err)
		}
	})
}

func TestFromConfig(t *testing.T) {
	t.Run("covers_every_secret", func(t *testing.T) {
		cfg := &Config{
			RTSP: RTSPConfig{Password: "cam-password"},
			SFTP: SFTPConfig{Password: "sftp-password", PrivateKeyPassphrase: "key-passphrase"},
		}
		r := FromConfig(cfg)
		got := r.String("cam-password sftp-password key-passphrase")
		if got != "*** *** ***" {
			t.Errorf("String() = %q", got)
		}
	})

	t.Run("covers_url_encoded_forms", func(t *testing.T) {
		// ffmpeg echoes the percent-encoded input URL on failure, so the encoded
		// form of the password must be redacted too.
		cfg := &Config{RTSP: RTSPConfig{Password: `p@ss/w:rd`}}
		r := FromConfig(cfg)
		encoded := urlEncodeUserinfo(`p@ss/w:rd`)
		if encoded == `p@ss/w:rd` {
			t.Fatalf("test precondition failed: %q was not re-encoded", encoded)
		}
		msg := fmt.Sprintf("rtsp://admin:%s@cam.lan:554/live: 401 Unauthorized", encoded)
		got := r.String(msg)
		if strings.Contains(got, encoded) {
			t.Errorf("encoded password leaked: %q", got)
		}
		if !strings.Contains(got, redactPlaceholder) {
			t.Errorf("String() = %q, want the placeholder", got)
		}
	})

	t.Run("ignores_empty_secrets", func(t *testing.T) {
		r := FromConfig(&Config{})
		if len(r.secrets) != 0 {
			t.Errorf("secrets = %v, want none", r.secrets)
		}
	})

	t.Run("nil_config", func(t *testing.T) {
		if r := FromConfig(nil); r == nil || len(r.secrets) != 0 {
			t.Errorf("FromConfig(nil) = %#v", r)
		}
	})
}
