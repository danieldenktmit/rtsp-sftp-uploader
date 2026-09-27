package capturetest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFake(t *testing.T) {
	t.Run("records_calls_and_writes_the_file", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "image.jpg")
		f := &Fake{Now: time.Unix(1000, 0)}

		frame, err := f.Grab(t.Context(), dst)
		if err != nil {
			t.Fatalf("Grab: %v", err)
		}
		if f.CallCount() != 1 || f.LastCall() != dst {
			t.Errorf("calls = %v", f.Calls)
		}
		body, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("reading the written file: %v", err)
		}
		if len(body) != len(MinimalJPEG) || frame.Size != int64(len(MinimalJPEG)) {
			t.Errorf("wrote %d bytes, frame reports %d", len(body), frame.Size)
		}
		if !frame.CapturedAt.Equal(time.Unix(1000, 0)) {
			t.Errorf("CapturedAt = %v", frame.CapturedAt)
		}
	})

	t.Run("custom_contents", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "image.jpg")
		f := &Fake{Contents: []byte("custom")}
		if _, err := f.Grab(t.Context(), dst); err != nil {
			t.Fatalf("Grab: %v", err)
		}
		if body, _ := os.ReadFile(dst); string(body) != "custom" {
			t.Errorf("contents = %q", body)
		}
	})

	t.Run("scripted_errors_are_consumed_in_order", func(t *testing.T) {
		first := errors.New("first")
		f := &Fake{Errs: []error{first, nil}, Err: errors.New("fallback")}
		dst := filepath.Join(t.TempDir(), "image.jpg")

		if err := grabErr(t, f, dst); !errors.Is(err, first) {
			t.Errorf("call 1 = %v, want %v", err, first)
		}
		if err := grabErr(t, f, dst); err != nil {
			t.Errorf("call 2 = %v, want nil", err)
		}
		if err := grabErr(t, f, dst); err == nil || err.Error() != "fallback" {
			t.Errorf("call 3 = %v, want the fallback error", err)
		}
	})

	t.Run("honours_context_cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		f := &Fake{}
		if _, err := f.Grab(ctx, filepath.Join(t.TempDir(), "x.jpg")); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("OnCall_hook_receives_the_call_number", func(t *testing.T) {
		var seen []int
		f := &Fake{OnCall: func(n int) { seen = append(seen, n) }}
		dst := filepath.Join(t.TempDir(), "x.jpg")
		for range 3 {
			_, _ = f.Grab(t.Context(), dst)
		}
		if len(seen) != 3 || seen[0] != 1 || seen[2] != 3 {
			t.Errorf("hook saw %v, want [1 2 3]", seen)
		}
	})

	t.Run("no_calls_yet", func(t *testing.T) {
		f := &Fake{}
		if f.CallCount() != 0 || f.LastCall() != "" {
			t.Error("a fresh fake should report no calls")
		}
	})
}

func grabErr(t *testing.T, f *Fake, dst string) error {
	t.Helper()
	_, err := f.Grab(t.Context(), dst)
	return err
}
