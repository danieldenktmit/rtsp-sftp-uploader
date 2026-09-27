package capture

import "strings"

// tailBuffer is an io.Writer that retains only the last max bytes written to it.
// ffmpeg can be verbose on failure; only the tail is useful in an error message
// and an unbounded buffer would be a memory hazard in a long-running process.
type tailBuffer struct {
	max       int
	buf       []byte
	truncated bool
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max, buf: make([]byte, 0, min(max, 1024))}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	switch {
	case b.max <= 0:
		b.truncated = b.truncated || n > 0
		return n, nil
	case n >= b.max:
		b.buf = append(b.buf[:0], p[n-b.max:]...)
		b.truncated = true
	default:
		if over := len(b.buf) + n - b.max; over > 0 {
			copy(b.buf, b.buf[over:])
			b.buf = b.buf[:len(b.buf)-over]
			b.truncated = true
		}
		b.buf = append(b.buf, p...)
	}
	return n, nil
}

// String returns the retained tail, marked with a leading ellipsis when output
// was dropped.
func (b *tailBuffer) String() string {
	s := strings.TrimSpace(string(b.buf))
	if b.truncated {
		return "..." + s
	}
	return s
}
