package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/config"
)

// stderrTailBytes caps how much of ffmpeg's stderr is kept for error messages.
const stderrTailBytes = 4 << 10

// partSuffix is appended while the JPEG is still being written.
const partSuffix = ".part"

// jpegMagic is the SOI marker every JPEG file starts with.
var jpegMagic = []byte{0xFF, 0xD8, 0xFF}

// FFmpegGrabber captures a frame by running ffmpeg as a subprocess. ffmpeg is
// used purely as a decoder: there is no practical pure-Go H.264/H.265 decoder.
type FFmpegGrabber struct {
	binary      string
	sourceURL   string // contains credentials; never log this directly
	redactedURL string // safe for logs
	transport   string
	rtspTimeout time.Duration
	timeout     time.Duration
	quality     int
	redactor    *config.Redactor
	logger      *slog.Logger

	// seams for tests
	clock          func() time.Time
	commandContext func(ctx context.Context, name string, arg ...string) *exec.Cmd
}

// NewFFmpegGrabber resolves the ffmpeg binary and pre-builds the stream URL so
// that misconfiguration is reported at startup rather than on the first capture.
func NewFFmpegGrabber(rtsp config.RTSPConfig, cap config.CaptureConfig, r *config.Redactor, logger *slog.Logger) (*FFmpegGrabber, error) {
	sourceURL, err := rtsp.ResolvedURL()
	if err != nil {
		return nil, err
	}
	binary, err := exec.LookPath(cap.FFmpegPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %q is not executable: %w", config.EnvFFmpegPath, cap.FFmpegPath, err)
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &FFmpegGrabber{
		binary:         binary,
		sourceURL:      sourceURL,
		redactedURL:    rtsp.RedactedURL(),
		transport:      rtsp.Transport,
		rtspTimeout:    rtsp.Timeout,
		timeout:        cap.Timeout,
		quality:        cap.JPEGQuality,
		redactor:       r,
		logger:         logger,
		clock:          time.Now,
		commandContext: exec.CommandContext,
	}, nil
}

// SourceURL returns the redacted stream URL, for logging and diagnostics.
func (g *FFmpegGrabber) SourceURL() string { return g.redactedURL }

// Args returns the exact ffmpeg argument list used to produce dst.
func (g *FFmpegGrabber) Args(dst string) []string {
	return []string{
		"-hide_banner",
		"-nostdin",
		"-loglevel", "error",
		"-y",
		"-rtsp_transport", g.transport,
		"-timeout", strconv.FormatInt(g.rtspTimeout.Microseconds(), 10),
		"-i", g.sourceURL,
		"-frames:v", "1",
		"-q:v", strconv.Itoa(g.quality),
		"-f", "image2",
		"-update", "1",
		dst + partSuffix,
	}
}

// Grab captures one frame into dst. The file at dst is replaced atomically, so a
// consumer polling it never observes a partially written image.
func (g *FFmpegGrabber) Grab(ctx context.Context, dst string) (Frame, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	part := dst + partSuffix
	// Clean up on every failure path; harmless after a successful rename.
	defer func() { _ = os.Remove(part) }()

	if dir := filepath.Dir(dst); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return Frame{}, fmt.Errorf("creating output directory %s: %w", dir, err)
		}
	}

	started := g.clock()
	stderr := newTailBuffer(stderrTailBytes)

	cmd := g.commandContext(ctx, g.binary, g.Args(dst)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Frame{}, g.redactor.Error(fmt.Errorf("capture did not finish within %s: %w", g.timeout, ctxErr))
		}
		return Frame{}, g.redactor.Error(fmt.Errorf("ffmpeg failed: %w: %s", err, stderr))
	}

	info, err := os.Stat(part)
	if err != nil {
		return Frame{}, g.redactor.Error(fmt.Errorf("ffmpeg produced no output file: %w: %s", err, stderr))
	}
	if info.Size() == 0 {
		return Frame{}, g.redactor.Error(fmt.Errorf("ffmpeg produced an empty image: %s", stderr))
	}
	if err := verifyJPEG(part); err != nil {
		return Frame{}, g.redactor.Error(fmt.Errorf("%w: %s", err, stderr))
	}

	if err := os.Rename(part, dst); err != nil {
		return Frame{}, fmt.Errorf("publishing %s: %w", dst, err)
	}

	captured := g.clock()
	g.logger.Debug("frame captured",
		"url", g.redactedURL,
		"path", dst,
		"bytes", info.Size(),
		"duration", captured.Sub(started),
	)
	return Frame{Path: dst, Size: info.Size(), CapturedAt: captured}, nil
}

// errNotJPEG reports output that is not a JPEG, which usually means ffmpeg wrote
// a diagnostic to the output path instead of an image.
var errNotJPEG = errors.New("ffmpeg output is not a JPEG")

func verifyJPEG(path string) error {
	f, err := os.Open(path) //nolint:gosec // path is the .part file this process just produced
	if err != nil {
		return fmt.Errorf("reading captured image: %w", err)
	}
	defer func() { _ = f.Close() }()

	head := make([]byte, len(jpegMagic))
	if _, err := io.ReadFull(f, head); err != nil {
		return fmt.Errorf("%w: file is too short", errNotJPEG)
	}
	if !bytes.Equal(head, jpegMagic) {
		return fmt.Errorf("%w: unexpected header %#x", errNotJPEG, head)
	}
	return nil
}
