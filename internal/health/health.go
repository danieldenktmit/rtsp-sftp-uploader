// Package health tracks capture and upload outcomes and serves the probe
// endpoints Kubernetes uses to tell a working pod from a wedged one.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// State is a concurrency-safe record of cycle outcomes.
type State struct {
	mu                  sync.RWMutex
	successes           uint64
	failures            uint64
	consecutiveFailures uint64
	lastSuccess         time.Time
	lastFailure         time.Time
	lastError           string
}

// NewState returns an empty State, which reports "not ready" until the first
// successful cycle.
func NewState() *State { return &State{} }

// RecordSuccess notes a completed capture-and-upload cycle.
func (s *State) RecordSuccess(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.successes++
	s.consecutiveFailures = 0
	s.lastSuccess = at
	s.lastError = ""
}

// RecordFailure notes a failed cycle. err must already be redacted: the message
// is served over HTTP.
func (s *State) RecordFailure(at time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	s.consecutiveFailures++
	s.lastFailure = at
	if err != nil {
		s.lastError = err.Error()
	}
}

// Snapshot is an immutable view of a State, suitable for JSON encoding.
type Snapshot struct {
	Successes           uint64     `json:"successes"`
	Failures            uint64     `json:"failures"`
	ConsecutiveFailures uint64     `json:"consecutive_failures"`
	LastSuccess         *time.Time `json:"last_success,omitempty"`
	LastFailure         *time.Time `json:"last_failure,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
}

// Snapshot copies the current counters.
func (s *State) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap := Snapshot{
		Successes:           s.successes,
		Failures:            s.failures,
		ConsecutiveFailures: s.consecutiveFailures,
		LastError:           s.lastError,
	}
	if !s.lastSuccess.IsZero() {
		t := s.lastSuccess
		snap.LastSuccess = &t
	}
	if !s.lastFailure.IsZero() {
		t := s.lastFailure
		snap.LastFailure = &t
	}
	return snap
}

// Handler serves the probe endpoints:
//
//	GET /healthz  liveness: 200 while the process is running
//	GET /readyz   readiness: 200 when a success happened within maxStaleness
//	GET /status   a JSON Snapshot, for humans and debugging
func Handler(s *State, maxStaleness time.Duration, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeText(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		snap := s.Snapshot()
		if snap.LastSuccess == nil {
			writeText(w, http.StatusServiceUnavailable, "no successful capture yet")
			return
		}
		if age := now().Sub(*snap.LastSuccess); age > maxStaleness {
			writeText(w, http.StatusServiceUnavailable,
				fmt.Sprintf("last successful capture was %s ago, which exceeds %s", age.Round(time.Second), maxStaleness))
			return
		}
		writeText(w, http.StatusOK, "ready")
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(s.Snapshot())
	})
	return mux
}

func writeText(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = fmt.Fprintln(w, body)
}

// Server runs the probe endpoints with graceful shutdown.
type Server struct {
	addr   string
	srv    *http.Server
	logger *slog.Logger

	mu       sync.Mutex
	listener net.Listener
}

// NewServer prepares a Server bound to addr.
func NewServer(addr string, h http.Handler, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Server{
		addr: addr,
		srv: &http.Server{
			Handler:           h,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		logger: logger,
	}
}

// Start binds the listener and serves in the background. It returns the resolved
// address, which is useful when addr requests port 0. ctx governs only the bind;
// use Shutdown to stop the server.
func (s *Server) Start(ctx context.Context) (string, error) {
	if s.addr == "" {
		return "", errors.New("health: no listen address configured")
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return "", fmt.Errorf("health: listening on %s: %w", s.addr, err)
	}

	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("health server stopped unexpectedly", "err", err)
		}
	}()
	return ln.Addr().String(), nil
}

// Shutdown stops accepting connections and waits for in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	started := s.listener != nil
	s.mu.Unlock()
	if !started {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
