// Package capturetest provides a capture.Grabber test double for use by other
// packages' tests.
package capturetest

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/capture"
)

// MinimalJPEG is the smallest byte sequence that satisfies a JPEG magic check.
var MinimalJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xFF, 0xD9}

// Fake is a scriptable Grabber. It is safe for concurrent use.
type Fake struct {
	mu sync.Mutex

	// Calls records the dst of every Grab call.
	Calls []string
	// Err is returned by every call once Errs is exhausted.
	Err error
	// Errs is consumed one entry per call, before Err applies.
	Errs []error
	// Contents is written to dst on success; MinimalJPEG is used when nil.
	Contents []byte
	// Now, when set, is used as the frame timestamp.
	Now time.Time
	// OnCall runs at the start of each call with the 1-based call number.
	OnCall func(n int)
}

// Grab records the call, optionally writes Contents to dst, and returns the next
// scripted error.
func (f *Fake) Grab(ctx context.Context, dst string) (capture.Frame, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, dst)
	n := len(f.Calls)
	err := f.Err
	if len(f.Errs) > 0 {
		err, f.Errs = f.Errs[0], f.Errs[1:]
	}
	body := f.Contents
	if body == nil {
		body = MinimalJPEG
	}
	stamp := f.Now
	hook := f.OnCall
	f.mu.Unlock()

	if hook != nil {
		hook(n)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return capture.Frame{}, ctxErr
	}
	if err != nil {
		return capture.Frame{}, err
	}
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		return capture.Frame{}, err
	}
	if stamp.IsZero() {
		stamp = time.Now()
	}
	return capture.Frame{Path: dst, Size: int64(len(body)), CapturedAt: stamp}, nil
}

// CallCount reports how many times Grab was called.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}

// LastCall returns the dst of the most recent call, or "" if there was none.
func (f *Fake) LastCall() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Calls) == 0 {
		return ""
	}
	return f.Calls[len(f.Calls)-1]
}
