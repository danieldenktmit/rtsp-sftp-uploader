package config

import (
	"net/url"
	"sort"
	"strings"
)

// redactPlaceholder replaces secret values in logs and error messages.
const redactPlaceholder = "***"

// minRedactableSecretLen is the shortest secret worth substituting. Very short
// secrets would match unrelated substrings and mangle otherwise useful output.
const minRedactableSecretLen = 4

// Redactor removes known secret values from strings and errors.
//
// A nil *Redactor is usable and behaves as a no-op.
type Redactor struct {
	secrets []string
}

// NewRedactor collects the non-empty, sufficiently long secrets to substitute.
// Longer secrets are applied first so that overlapping values are fully removed.
func NewRedactor(secrets ...string) *Redactor {
	seen := make(map[string]struct{}, len(secrets))
	kept := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if len(s) < minRedactableSecretLen {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		kept = append(kept, s)
	}
	sort.SliceStable(kept, func(i, j int) bool { return len(kept[i]) > len(kept[j]) })
	return &Redactor{secrets: kept}
}

// FromConfig builds a Redactor covering every secret in c, including the
// percent-encoded forms that appear inside URLs (ffmpeg echoes those on failure).
func FromConfig(c *Config) *Redactor {
	if c == nil {
		return NewRedactor()
	}
	raw := []string{
		c.RTSP.Password,
		c.SFTP.Password,
		c.SFTP.PrivateKeyPassphrase,
	}
	secrets := make([]string, 0, len(raw)*2)
	for _, s := range raw {
		if s == "" {
			continue
		}
		secrets = append(secrets, s)
		if enc := urlEncodeUserinfo(s); enc != s {
			secrets = append(secrets, enc)
		}
	}
	return NewRedactor(secrets...)
}

// urlEncodeUserinfo returns s escaped the way net/url escapes userinfo.
func urlEncodeUserinfo(s string) string {
	return url.User(s).String()
}

// String replaces every known secret in s with the placeholder.
func (r *Redactor) String(s string) string {
	if r == nil || len(r.secrets) == 0 || s == "" {
		return s
	}
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, redactPlaceholder)
	}
	return s
}

// Error returns err with its message redacted. The original error remains
// reachable through errors.Is and errors.As. A nil error returns nil.
func (r *Redactor) Error(err error) error {
	if err == nil {
		return nil
	}
	if r == nil || len(r.secrets) == 0 {
		return err
	}
	msg := r.String(err.Error())
	if msg == err.Error() {
		return err
	}
	return &redactedError{msg: msg, cause: err}
}

// redactedError presents a redacted message while preserving the error chain.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }
