// Package uploadertest provides an uploader.Uploader test double for use by
// other packages' tests.
package uploadertest

import (
	"context"
	"os"
	"sync"
)

// Fake is a scriptable Uploader. It is safe for concurrent use.
type Fake struct {
	mu sync.Mutex

	// Calls records the localPath of every Upload call.
	Calls []string
	// Bodies records the file contents observed at call time.
	Bodies [][]byte
	// Err is returned by every call once Errs is exhausted.
	Err error
	// Errs is consumed one entry per call, before Err applies.
	Errs []error
	// OnCall runs at the start of each call with the 1-based call number.
	OnCall func(n int)
}

// Upload records the call and returns the next scripted error.
func (f *Fake) Upload(ctx context.Context, localPath string) error {
	f.mu.Lock()
	f.Calls = append(f.Calls, localPath)
	n := len(f.Calls)
	err := f.Err
	if len(f.Errs) > 0 {
		err, f.Errs = f.Errs[0], f.Errs[1:]
	}
	hook := f.OnCall
	f.mu.Unlock()

	if hook != nil {
		hook(n)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}

	body, readErr := os.ReadFile(localPath) //nolint:gosec // test double reading a test-controlled path
	f.mu.Lock()
	f.Bodies = append(f.Bodies, body)
	f.mu.Unlock()

	if err != nil {
		return err
	}
	return readErr
}

// CallCount reports how many times Upload was called.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}

// LastBody returns the contents seen by the most recent call.
func (f *Fake) LastBody() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Bodies) == 0 {
		return nil
	}
	return f.Bodies[len(f.Bodies)-1]
}
