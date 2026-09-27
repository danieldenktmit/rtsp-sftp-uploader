// Package capture turns a live RTSP stream into a still JPEG on local disk.
package capture

import (
	"context"
	"time"
)

// Frame describes a successfully captured still image.
type Frame struct {
	// Path is the location of the JPEG on local disk.
	Path string
	// Size is the file size in bytes.
	Size int64
	// CapturedAt is when the capture completed.
	CapturedAt time.Time
}

// Grabber captures a single frame and writes it to dst, replacing any existing
// file atomically. Implementations must honour ctx cancellation.
type Grabber interface {
	Grab(ctx context.Context, dst string) (Frame, error)
}
