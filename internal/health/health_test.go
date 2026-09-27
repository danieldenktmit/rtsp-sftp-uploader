package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

const staleness = 3 * time.Minute

// get performs a request against h and returns the status code and body.
func get(t *testing.T, h http.Handler, method, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code, rec.Body.String()
}

func handlerAt(s *State, now time.Time) http.Handler {
	return Handler(s, staleness, func() time.Time { return now })
}

func TestLiveness(t *testing.T) {
	t.Run("always_ok_even_with_only_failures", func(t *testing.T) {
		s := NewState()
		for range 5 {
			s.RecordFailure(t0, errors.New("camera unreachable"))
		}
		code, body := get(t, handlerAt(s, t0), http.MethodGet, "/healthz")
		if code != http.StatusOK {
			t.Errorf("status = %d, want 200", code)
		}
		if !strings.Contains(body, "ok") {
			t.Errorf("body = %q", body)
		}
	})
}

func TestReadiness(t *testing.T) {
	t.Run("not_ready_before_the_first_success", func(t *testing.T) {
		code, body := get(t, handlerAt(NewState(), t0), http.MethodGet, "/readyz")
		if code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", code)
		}
		if !strings.Contains(body, "no successful capture yet") {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("ready_after_a_success", func(t *testing.T) {
		s := NewState()
		s.RecordSuccess(t0)
		if code, _ := get(t, handlerAt(s, t0), http.MethodGet, "/readyz"); code != http.StatusOK {
			t.Errorf("status = %d, want 200", code)
		}
	})

	t.Run("not_ready_once_the_last_success_is_stale", func(t *testing.T) {
		s := NewState()
		s.RecordSuccess(t0)
		code, body := get(t, handlerAt(s, t0.Add(2*staleness)), http.MethodGet, "/readyz")
		if code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", code)
		}
		if !strings.Contains(body, "6m0s ago") {
			t.Errorf("body = %q, want it to report the age", body)
		}
		if !strings.Contains(body, staleness.String()) {
			t.Errorf("body = %q, want it to report the limit", body)
		}
	})

	t.Run("boundary_is_inclusive", func(t *testing.T) {
		s := NewState()
		s.RecordSuccess(t0)

		if code, _ := get(t, handlerAt(s, t0.Add(staleness)), http.MethodGet, "/readyz"); code != http.StatusOK {
			t.Errorf("exactly at the limit: status = %d, want 200", code)
		}
		if code, _ := get(t, handlerAt(s, t0.Add(staleness+1)), http.MethodGet, "/readyz"); code != http.StatusServiceUnavailable {
			t.Errorf("one nanosecond past the limit: status = %d, want 503", code)
		}
	})

	t.Run("transient_failures_do_not_flap_readiness", func(t *testing.T) {
		// A camera that misses one cycle must not restart the pod.
		s := NewState()
		s.RecordSuccess(t0)
		s.RecordFailure(t0.Add(time.Minute), errors.New("timeout"))
		if code, _ := get(t, handlerAt(s, t0.Add(time.Minute)), http.MethodGet, "/readyz"); code != http.StatusOK {
			t.Errorf("status = %d, want 200", code)
		}
	})

	t.Run("nil_clock_defaults_to_time_Now", func(t *testing.T) {
		s := NewState()
		s.RecordSuccess(time.Now())
		if code, _ := get(t, Handler(s, staleness, nil), http.MethodGet, "/readyz"); code != http.StatusOK {
			t.Errorf("status = %d, want 200", code)
		}
	})
}

func TestStatus(t *testing.T) {
	t.Run("json_shape", func(t *testing.T) {
		s := NewState()
		s.RecordSuccess(t0)
		s.RecordFailure(t0.Add(time.Minute), errors.New("upload failed"))

		rec := httptest.NewRecorder()
		handlerAt(s, t0).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var snap Snapshot
		if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
			t.Fatalf("body is not valid JSON (%v): %s", err, rec.Body.String())
		}
		if snap.Successes != 1 || snap.Failures != 1 || snap.ConsecutiveFailures != 1 {
			t.Errorf("counters = %+v", snap)
		}
		if snap.LastSuccess == nil || !snap.LastSuccess.Equal(t0) {
			t.Errorf("LastSuccess = %v, want %v", snap.LastSuccess, t0)
		}
		if snap.LastError != "upload failed" {
			t.Errorf("LastError = %q", snap.LastError)
		}
	})

	t.Run("omits_empty_fields", func(t *testing.T) {
		_, body := get(t, handlerAt(NewState(), t0), http.MethodGet, "/status")
		for _, absent := range []string{"last_success", "last_failure", "last_error"} {
			if strings.Contains(body, absent) {
				t.Errorf("body should omit %s: %s", absent, body)
			}
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if m["successes"] != float64(0) {
			t.Errorf("successes = %#v", m["successes"])
		}
	})

	t.Run("a_success_clears_the_consecutive_failure_streak", func(t *testing.T) {
		s := NewState()
		for i := range 3 {
			s.RecordFailure(t0.Add(time.Duration(i)*time.Second), fmt.Errorf("attempt %d", i))
		}
		if got := s.Snapshot().ConsecutiveFailures; got != 3 {
			t.Errorf("ConsecutiveFailures = %d, want 3", got)
		}
		s.RecordSuccess(t0.Add(time.Minute))
		snap := s.Snapshot()
		if snap.ConsecutiveFailures != 0 {
			t.Errorf("ConsecutiveFailures = %d, want 0", snap.ConsecutiveFailures)
		}
		if snap.LastError != "" {
			t.Errorf("LastError = %q, want it cleared after a success", snap.LastError)
		}
		if snap.Failures != 3 {
			t.Errorf("Failures = %d, want the total to be retained", snap.Failures)
		}
	})

	t.Run("a_nil_error_does_not_overwrite_the_last_message", func(t *testing.T) {
		s := NewState()
		s.RecordFailure(t0, errors.New("real problem"))
		s.RecordFailure(t0, nil)
		if got := s.Snapshot().LastError; got != "real problem" {
			t.Errorf("LastError = %q", got)
		}
	})
}

func TestRouting(t *testing.T) {
	s := NewState()
	h := handlerAt(s, t0)

	t.Run("unknown_path_is_404", func(t *testing.T) {
		if code, _ := get(t, h, http.MethodGet, "/nope"); code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", code)
		}
	})

	t.Run("wrong_method_is_rejected", func(t *testing.T) {
		for _, path := range []string{"/healthz", "/readyz", "/status"} {
			code, _ := get(t, h, http.MethodPost, path)
			if code != http.StatusMethodNotAllowed {
				t.Errorf("POST %s: status = %d, want 405", path, code)
			}
		}
	})
}

func TestStateIsRaceFree(t *testing.T) {
	s := NewState()
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				s.RecordSuccess(t0)
			} else {
				s.RecordFailure(t0, errors.New("boom"))
			}
			_ = s.Snapshot()
		}(i)
	}
	wg.Wait()

	snap := s.Snapshot()
	if snap.Successes+snap.Failures != 100 {
		t.Errorf("recorded %d outcomes, want 100", snap.Successes+snap.Failures)
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	s := NewState()
	s.RecordSuccess(t0)
	snap := s.Snapshot()

	// Mutating the returned timestamp must not affect the state.
	*snap.LastSuccess = t0.Add(time.Hour)
	if got := s.Snapshot().LastSuccess; !got.Equal(t0) {
		t.Errorf("state was mutated through the snapshot: %v", got)
	}
}

func TestServer(t *testing.T) {
	t.Run("start_serve_shutdown", func(t *testing.T) {
		s := NewState()
		s.RecordSuccess(time.Now())
		srv := NewServer("127.0.0.1:0", Handler(s, staleness, time.Now), nil)

		addr, err := srv.Start(t.Context())
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if addr == "" || strings.HasSuffix(addr, ":0") {
			t.Fatalf("Start returned %q, want a resolved address", addr)
		}

		for _, probe := range []struct {
			path string
			want int
		}{{"/healthz", 200}, {"/readyz", 200}, {"/status", 200}} {
			resp, err := http.Get("http://" + addr + probe.path) //nolint:noctx // short-lived test request
			if err != nil {
				t.Fatalf("GET %s: %v", probe.path, err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != probe.want {
				t.Errorf("GET %s = %d, want %d (%s)", probe.path, resp.StatusCode, probe.want, body)
			}
		}

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}

		if _, err := http.Get("http://" + addr + "/healthz"); err == nil { //nolint:noctx,bodyclose
			t.Error("the server still accepts connections after Shutdown")
		}
	})

	t.Run("empty_address_is_rejected", func(t *testing.T) {
		if _, err := NewServer("", nil, nil).Start(t.Context()); err == nil {
			t.Error("expected an error for an empty listen address")
		}
	})

	t.Run("unusable_address_is_reported", func(t *testing.T) {
		_, err := NewServer("256.0.0.1:99999", nil, nil).Start(t.Context())
		if err == nil || !strings.Contains(err.Error(), "listening on") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("shutdown_before_start_is_a_noop", func(t *testing.T) {
		if err := NewServer("127.0.0.1:0", nil, nil).Shutdown(t.Context()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
}
