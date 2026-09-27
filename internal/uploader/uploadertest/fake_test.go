package uploadertest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "image.jpg")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFake(t *testing.T) {
	t.Run("records_calls_and_bodies", func(t *testing.T) {
		f := &Fake{}
		p := write(t, "jpeg-bytes")

		if err := f.Upload(t.Context(), p); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		if f.CallCount() != 1 || f.Calls[0] != p {
			t.Errorf("calls = %v", f.Calls)
		}
		if string(f.LastBody()) != "jpeg-bytes" {
			t.Errorf("LastBody() = %q", f.LastBody())
		}
	})

	t.Run("scripted_errors_are_consumed_in_order", func(t *testing.T) {
		first := errors.New("first")
		f := &Fake{Errs: []error{first, nil}, Err: errors.New("fallback")}
		p := write(t, "x")

		if err := f.Upload(t.Context(), p); !errors.Is(err, first) {
			t.Errorf("call 1 = %v", err)
		}
		if err := f.Upload(t.Context(), p); err != nil {
			t.Errorf("call 2 = %v", err)
		}
		if err := f.Upload(t.Context(), p); err == nil || err.Error() != "fallback" {
			t.Errorf("call 3 = %v", err)
		}
	})

	t.Run("reports_a_missing_local_file", func(t *testing.T) {
		f := &Fake{}
		if err := f.Upload(t.Context(), filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Error("expected an error for a missing file")
		}
	})

	t.Run("honours_context_cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := (&Fake{}).Upload(ctx, write(t, "x")); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("OnCall_hook", func(t *testing.T) {
		var seen []int
		f := &Fake{OnCall: func(n int) { seen = append(seen, n) }}
		p := write(t, "x")
		for range 2 {
			_ = f.Upload(t.Context(), p)
		}
		if len(seen) != 2 || seen[1] != 2 {
			t.Errorf("hook saw %v", seen)
		}
	})

	t.Run("fresh_fake_has_no_body", func(t *testing.T) {
		if (&Fake{}).LastBody() != nil {
			t.Error("expected nil")
		}
	})
}
