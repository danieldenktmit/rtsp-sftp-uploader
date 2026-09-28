// Package config loads and validates runtime configuration from environment
// variables and command-line flags. Flags take precedence over the environment,
// which takes precedence over the built-in defaults.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Environment variable names. These are the canonical setting identifiers used
// throughout the loader; the corresponding flag name is the lowercased form with
// underscores replaced by dashes.
const (
	EnvRTSPURL       = "RTSP_URL"
	EnvRTSPHost      = "RTSP_HOST"
	EnvRTSPPort      = "RTSP_PORT"
	EnvRTSPPath      = "RTSP_PATH"
	EnvRTSPUsername  = "RTSP_USERNAME"
	EnvRTSPPassword  = "RTSP_PASSWORD"
	EnvRTSPTransport = "RTSP_TRANSPORT"
	EnvRTSPTimeout   = "RTSP_TIMEOUT"

	EnvCaptureInterval    = "CAPTURE_INTERVAL"
	EnvCaptureOutputDir   = "CAPTURE_OUTPUT_DIR"
	EnvCaptureFilename    = "CAPTURE_FILENAME"
	EnvCaptureJPEGQuality = "CAPTURE_JPEG_QUALITY"
	EnvCaptureTimeout     = "CAPTURE_TIMEOUT"
	EnvFFmpegPath         = "FFMPEG_PATH"

	EnvSFTPURL            = "SFTP_URL"
	EnvSFTPHost           = "SFTP_HOST"
	EnvSFTPPort           = "SFTP_PORT"
	EnvSFTPUsername       = "SFTP_USERNAME"
	EnvSFTPPassword       = "SFTP_PASSWORD"
	EnvSFTPPrivateKeyPath = "SFTP_PRIVATE_KEY_PATH"
	// EnvSFTPPrivateKeyPassphrase names the variable; it holds no secret itself.
	EnvSFTPPrivateKeyPassphrase = "SFTP_PRIVATE_KEY_PASSPHRASE" //nolint:gosec // env var name, not a credential
	EnvSFTPRemoteDir            = "SFTP_REMOTE_DIR"
	EnvSFTPRemoteFilename       = "SFTP_REMOTE_FILENAME"
	EnvSFTPTimeout              = "SFTP_TIMEOUT"
	EnvSFTPKnownHostsPath       = "SFTP_KNOWN_HOSTS_PATH"
	EnvSFTPHostKeyFingerprint   = "SFTP_HOST_KEY_FINGERPRINT"
	EnvSFTPInsecureIgnoreHost   = "SFTP_INSECURE_IGNORE_HOST_KEY"
	EnvSFTPMkdir                = "SFTP_MKDIR"
	EnvSFTPAtomic               = "SFTP_ATOMIC"
	EnvSFTPFileMode             = "SFTP_FILE_MODE"
	EnvSFTPRetryAttempts        = "SFTP_RETRY_ATTEMPTS"
	EnvSFTPRetryBackoff         = "SFTP_RETRY_BACKOFF"

	EnvHTTPAddr              = "HTTP_ADDR"
	EnvHTTPReadyMaxStaleness = "HTTP_READY_MAX_STALENESS"

	EnvLogLevel  = "LOG_LEVEL"
	EnvLogFormat = "LOG_FORMAT"
)

// Transport values accepted by RTSP_TRANSPORT.
const (
	TransportTCP = "tcp"
	TransportUDP = "udp"
)

// minReadyStaleness is the floor applied when HTTP_READY_MAX_STALENESS is unset
// and the capture interval is very short.
const minReadyStaleness = 90 * time.Second

// RTSPConfig describes the camera to read from.
type RTSPConfig struct {
	URL       string
	Host      string
	Port      int
	Path      string
	Username  string
	Password  string
	Transport string
	Timeout   time.Duration
}

// CaptureConfig describes how and how often a still image is produced.
type CaptureConfig struct {
	Interval    time.Duration
	OutputDir   string
	Filename    string
	JPEGQuality int
	Timeout     time.Duration
	FFmpegPath  string
}

// SFTPConfig describes the upload destination.
type SFTPConfig struct {
	URL                   string
	Host                  string
	Port                  int
	Username              string
	Password              string
	PrivateKeyPath        string
	PrivateKeyPassphrase  string
	RemoteDir             string
	RemoteFilename        string
	Timeout               time.Duration
	KnownHostsPath        string
	HostKeyFingerprint    string
	InsecureIgnoreHostKey bool
	Mkdir                 bool
	Atomic                bool
	FileMode              os.FileMode
	RetryAttempts         int
	RetryBackoff          time.Duration
}

// HTTPConfig describes the probe/status endpoint listener.
type HTTPConfig struct {
	Addr              string
	ReadyMaxStaleness time.Duration
}

// LogConfig describes log output.
type LogConfig struct {
	Level  string
	Format string
}

// Config is the fully resolved runtime configuration.
type Config struct {
	RTSP        RTSPConfig
	Capture     CaptureConfig
	SFTP        SFTPConfig
	HTTP        HTTPConfig
	Log         LogConfig
	ShowVersion bool
}

// Lookup mirrors os.LookupEnv so tests can supply a map instead of the real environment.
type Lookup func(key string) (string, bool)

// MapLookup adapts a map to the Lookup signature.
func MapLookup(m map[string]string) Lookup {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

type kind int

const (
	kindString kind = iota
	kindInt
	kindBool
	kindDuration
	kindFileMode
)

type spec struct {
	env   string
	def   string
	kind  kind
	usage string
}

// flagName derives the command-line flag name from an environment variable name.
func flagName(env string) string {
	return strings.ToLower(strings.ReplaceAll(env, "_", "-"))
}

func specs() []spec {
	return []spec{
		{EnvRTSPURL, "", kindString, "full RTSP URL; takes precedence over host/port/path"},
		{EnvRTSPHost, "", kindString, "camera hostname or IP (required unless RTSP_URL is set)"},
		{EnvRTSPPort, "554", kindInt, "camera RTSP port"},
		{EnvRTSPPath, "/", kindString, "stream path, e.g. /Streaming/Channels/101"},
		{EnvRTSPUsername, "", kindString, "camera username"},
		{EnvRTSPPassword, "", kindString, "camera password"},
		{EnvRTSPTransport, TransportTCP, kindString, "RTSP transport: tcp or udp"},
		{EnvRTSPTimeout, "15s", kindDuration, "RTSP socket timeout"},

		{EnvCaptureInterval, "60s", kindDuration, "how often to capture and upload; 0 captures once and exits"},
		{EnvCaptureOutputDir, "/tmp", kindString, "directory the JPEG is written to"},
		{EnvCaptureFilename, "image.jpg", kindString, "local JPEG filename"},
		{EnvCaptureJPEGQuality, "2", kindInt, "ffmpeg JPEG quality, 2 (best) to 31 (worst)"},
		{EnvCaptureTimeout, "30s", kindDuration, "hard time limit for one capture"},
		{EnvFFmpegPath, "ffmpeg", kindString, "path to the ffmpeg binary"},

		{EnvSFTPURL, "", kindString, "sftp://user:pass@host:port/folder; supplies defaults for the settings below"},
		{EnvSFTPHost, "", kindString, "SFTP hostname or IP (required unless SFTP_URL is set)"},
		{EnvSFTPPort, "22", kindInt, "SFTP port"},
		{EnvSFTPUsername, "", kindString, "SFTP username"},
		{EnvSFTPPassword, "", kindString, "SFTP password"},
		{EnvSFTPPrivateKeyPath, "", kindString, "path to a PEM private key for publickey auth"},
		{EnvSFTPPrivateKeyPassphrase, "", kindString, "passphrase for the private key"},
		{EnvSFTPRemoteDir, "/upload", kindString, "remote directory to upload into"},
		{EnvSFTPRemoteFilename, "", kindString, "remote filename (defaults to CAPTURE_FILENAME)"},
		{EnvSFTPTimeout, "30s", kindDuration, "SFTP dial, handshake and transfer timeout"},
		{EnvSFTPKnownHostsPath, "", kindString, "path to an OpenSSH known_hosts file used to verify the server"},
		{EnvSFTPHostKeyFingerprint, "", kindString, "expected server host key fingerprint (SHA256:...)"},
		{EnvSFTPInsecureIgnoreHost, "false", kindBool, "skip host key verification (insecure)"},
		{EnvSFTPMkdir, "true", kindBool, "create the remote directory if it does not exist"},
		{EnvSFTPAtomic, "true", kindBool, "upload to a temporary name and rename into place"},
		{EnvSFTPFileMode, "0644", kindFileMode, "octal permissions applied to the uploaded file"},
		{EnvSFTPRetryAttempts, "3", kindInt, "total upload attempts before giving up"},
		{EnvSFTPRetryBackoff, "2s", kindDuration, "base delay for exponential upload backoff"},

		{EnvHTTPAddr, ":8080", kindString, "listen address for health endpoints; empty disables them"},
		{EnvHTTPReadyMaxStaleness, "0", kindDuration, "max age of the last success before /readyz fails; 0 means 3x the capture interval"},

		{EnvLogLevel, "info", kindString, "log level: debug, info, warn or error"},
		{EnvLogFormat, "json", kindString, "log format: json or text"},
	}
}

// stringValue is a flag.Value backed by a string pointer.
type stringValue struct{ p *string }

func (v stringValue) String() string {
	if v.p == nil {
		return ""
	}
	return *v.p
}
func (v stringValue) Set(s string) error { *v.p = s; return nil }

// boolValue behaves like a real boolean flag (usable as --flag as well as
// --flag=false) while still storing its value as a string.
type boolValue struct{ p *string }

func (v boolValue) String() string {
	if v.p == nil {
		return ""
	}
	return *v.p
}
func (v boolValue) Set(s string) error { *v.p = s; return nil }
func (v boolValue) IsBoolFlag() bool   { return true }

// Load resolves configuration from lookup and args (args excludes the program
// name). The returned error is flag.ErrHelp when help was requested. Flag usage
// and parse diagnostics go to os.Stderr; use LoadTo to redirect them.
func Load(lookup Lookup, args []string) (*Config, error) {
	return load(lookup, args, os.Stderr)
}

// LoadTo behaves like Load but writes flag usage and parse diagnostics to out.
func LoadTo(lookup Lookup, args []string, out io.Writer) (*Config, error) {
	return load(lookup, args, out)
}

func load(lookup Lookup, args []string, out io.Writer) (*Config, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}

	all := specs()
	flagVals := make(map[string]*string, len(all))

	fs := flag.NewFlagSet("rtsp-sftp-uploader", flag.ContinueOnError)
	fs.SetOutput(out)
	showVersion := fs.Bool("version", false, "print version information and exit")

	for _, s := range all {
		v := new(string)
		flagVals[s.env] = v
		name := flagName(s.env)
		usage := fmt.Sprintf("%s (env %s)", s.usage, s.env)
		if s.kind == kindBool {
			fs.Var(boolValue{v}, name, usage)
		} else {
			fs.Var(stringValue{v}, name, usage)
		}
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, err
		}
		return nil, fmt.Errorf("parsing flags: %w", err)
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected positional argument %q", fs.Arg(0))
	}

	setFlags := make(map[string]bool, len(all))
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	r := &resolver{
		raw:      make(map[string]string, len(all)),
		explicit: make(map[string]bool, len(all)),
	}
	for _, s := range all {
		switch {
		case setFlags[flagName(s.env)]:
			r.raw[s.env] = *flagVals[s.env]
			r.explicit[s.env] = true
		default:
			if v, ok := lookup(s.env); ok {
				r.raw[s.env] = v
				r.explicit[s.env] = true
			} else {
				r.raw[s.env] = s.def
			}
		}
	}

	// A supplied SFTP_URL provides defaults for every setting the user did not
	// state explicitly.
	if err := applySFTPURL(r); err != nil {
		r.errs = append(r.errs, err)
	}
	// Credentials embedded in RTSP_URL are hoisted into the struct so that the
	// redactor knows about them even when the individual vars are unset.
	if err := applyRTSPURLCredentials(r); err != nil {
		r.errs = append(r.errs, err)
	}

	cfg := &Config{
		ShowVersion: *showVersion,
		RTSP: RTSPConfig{
			URL:       r.str(EnvRTSPURL),
			Host:      r.str(EnvRTSPHost),
			Port:      r.integer(EnvRTSPPort),
			Path:      r.str(EnvRTSPPath),
			Username:  r.str(EnvRTSPUsername),
			Password:  r.str(EnvRTSPPassword),
			Transport: strings.ToLower(r.str(EnvRTSPTransport)),
			Timeout:   r.duration(EnvRTSPTimeout),
		},
		Capture: CaptureConfig{
			Interval:    r.duration(EnvCaptureInterval),
			OutputDir:   r.str(EnvCaptureOutputDir),
			Filename:    r.str(EnvCaptureFilename),
			JPEGQuality: r.integer(EnvCaptureJPEGQuality),
			Timeout:     r.duration(EnvCaptureTimeout),
			FFmpegPath:  r.str(EnvFFmpegPath),
		},
		SFTP: SFTPConfig{
			URL:                   r.str(EnvSFTPURL),
			Host:                  r.str(EnvSFTPHost),
			Port:                  r.integer(EnvSFTPPort),
			Username:              r.str(EnvSFTPUsername),
			Password:              r.str(EnvSFTPPassword),
			PrivateKeyPath:        r.str(EnvSFTPPrivateKeyPath),
			PrivateKeyPassphrase:  r.str(EnvSFTPPrivateKeyPassphrase),
			RemoteDir:             r.str(EnvSFTPRemoteDir),
			RemoteFilename:        r.str(EnvSFTPRemoteFilename),
			Timeout:               r.duration(EnvSFTPTimeout),
			KnownHostsPath:        r.str(EnvSFTPKnownHostsPath),
			HostKeyFingerprint:    r.str(EnvSFTPHostKeyFingerprint),
			InsecureIgnoreHostKey: r.boolean(EnvSFTPInsecureIgnoreHost),
			Mkdir:                 r.boolean(EnvSFTPMkdir),
			Atomic:                r.boolean(EnvSFTPAtomic),
			FileMode:              r.fileMode(EnvSFTPFileMode),
			RetryAttempts:         r.integer(EnvSFTPRetryAttempts),
			RetryBackoff:          r.duration(EnvSFTPRetryBackoff),
		},
		HTTP: HTTPConfig{
			Addr:              r.str(EnvHTTPAddr),
			ReadyMaxStaleness: r.duration(EnvHTTPReadyMaxStaleness),
		},
		Log: LogConfig{
			Level:  strings.ToLower(r.str(EnvLogLevel)),
			Format: strings.ToLower(r.str(EnvLogFormat)),
		},
	}

	if cfg.SFTP.RemoteFilename == "" {
		cfg.SFTP.RemoteFilename = cfg.Capture.Filename
	}
	cfg.SFTP.RemoteDir = normalizeRemoteDir(cfg.SFTP.RemoteDir)

	if cfg.ShowVersion {
		// Nothing else matters; skip validation so --version always works.
		return cfg, nil
	}

	if len(r.errs) > 0 {
		return nil, errors.Join(r.errs...)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// resolver converts raw string settings into typed values, collecting every
// parse failure instead of stopping at the first.
type resolver struct {
	raw      map[string]string
	explicit map[string]bool
	errs     []error
}

func (r *resolver) isSet(env string) bool { return r.explicit[env] }

func (r *resolver) str(env string) string { return r.raw[env] }

func (r *resolver) integer(env string) int {
	s := strings.TrimSpace(r.raw[env])
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not a valid integer", env, s))
		return 0
	}
	return n
}

func (r *resolver) boolean(env string) bool {
	s := strings.TrimSpace(r.raw[env])
	if s == "" {
		return false
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not a valid boolean (use true or false)", env, s))
		return false
	}
	return b
}

// duration accepts a Go duration string ("90s", "2m") and, for convenience in
// Helm values and shell environments, a bare integer meaning seconds.
func (r *resolver) duration(env string) time.Duration {
	s := strings.TrimSpace(r.raw[env])
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not a valid duration (e.g. 60s, 2m)", env, s))
		return 0
	}
	return d
}

func (r *resolver) fileMode(env string) os.FileMode {
	s := strings.TrimSpace(r.raw[env])
	if s == "" {
		return 0
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not a valid octal file mode (e.g. 0644)", env, s))
		return 0
	}
	if n > 0o777 {
		r.errs = append(r.errs, fmt.Errorf("%s: %q exceeds 0777", env, s))
		return 0
	}
	return os.FileMode(n)
}

// applySFTPURL fills in unset SFTP settings from a supplied SFTP_URL.
func applySFTPURL(r *resolver) error {
	raw := strings.TrimSpace(r.raw[EnvSFTPURL])
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %q is not a valid URL: %w", EnvSFTPURL, raw, err)
	}
	if u.Scheme != "" && u.Scheme != "sftp" && u.Scheme != "ssh" {
		return fmt.Errorf("%s: scheme %q is not supported (use sftp://)", EnvSFTPURL, u.Scheme)
	}
	if h := u.Hostname(); h != "" && !r.isSet(EnvSFTPHost) {
		r.raw[EnvSFTPHost] = h
	}
	if p := u.Port(); p != "" && !r.isSet(EnvSFTPPort) {
		r.raw[EnvSFTPPort] = p
	}
	if u.User != nil {
		if n := u.User.Username(); n != "" && !r.isSet(EnvSFTPUsername) {
			r.raw[EnvSFTPUsername] = n
		}
		if pw, ok := u.User.Password(); ok && !r.isSet(EnvSFTPPassword) {
			r.raw[EnvSFTPPassword] = pw
		}
	}
	if u.Path != "" && u.Path != "/" && !r.isSet(EnvSFTPRemoteDir) {
		r.raw[EnvSFTPRemoteDir] = u.Path
	}
	return nil
}

// applyRTSPURLCredentials hoists credentials out of RTSP_URL so they can be
// redacted from logs even when the dedicated variables are unset.
func applyRTSPURLCredentials(r *resolver) error {
	raw := strings.TrimSpace(r.raw[EnvRTSPURL])
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %q is not a valid URL: %w", EnvRTSPURL, raw, err)
	}
	if u.User == nil {
		return nil
	}
	if n := u.User.Username(); n != "" && !r.isSet(EnvRTSPUsername) {
		r.raw[EnvRTSPUsername] = n
	}
	if pw, ok := u.User.Password(); ok && !r.isSet(EnvRTSPPassword) {
		r.raw[EnvRTSPPassword] = pw
	}
	return nil
}

func normalizeRemoteDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return dir
	}
	if len(dir) > 1 {
		dir = strings.TrimRight(dir, "/")
	}
	if dir == "" {
		dir = "/"
	}
	return dir
}

// Validate reports every configuration problem at once.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	// --- RTSP ---
	if err := validateHost(EnvRTSPHost, c.RTSP.Host, EnvRTSPURL, EnvRTSPPort, EnvRTSPPath); err != nil {
		errs = append(errs, err)
	}
	if c.RTSP.URL == "" && c.RTSP.Host == "" {
		add("rtsp: either %s or %s must be set", EnvRTSPURL, EnvRTSPHost)
	} else if _, err := c.RTSP.ResolvedURL(); err != nil {
		// Only meaningful once a source is present; otherwise it duplicates the
		// message above.
		errs = append(errs, err)
	}
	if c.RTSP.Port < 1 || c.RTSP.Port > 65535 {
		add("%s: %d is out of range (1-65535)", EnvRTSPPort, c.RTSP.Port)
	}
	if c.RTSP.Transport != TransportTCP && c.RTSP.Transport != TransportUDP {
		add("%s: %q must be %q or %q", EnvRTSPTransport, c.RTSP.Transport, TransportTCP, TransportUDP)
	}
	if c.RTSP.Timeout <= 0 {
		add("%s: must be greater than zero", EnvRTSPTimeout)
	}
	// --- Capture ---
	if c.Capture.Interval < 0 {
		add("%s: must not be negative", EnvCaptureInterval)
	}
	if c.Capture.OutputDir == "" {
		add("%s: must not be empty", EnvCaptureOutputDir)
	}
	if err := validateFilename(EnvCaptureFilename, c.Capture.Filename); err != nil {
		errs = append(errs, err)
	}
	if c.Capture.JPEGQuality < 2 || c.Capture.JPEGQuality > 31 {
		add("%s: %d is out of range (2-31)", EnvCaptureJPEGQuality, c.Capture.JPEGQuality)
	}
	if c.Capture.Timeout <= 0 {
		add("%s: must be greater than zero", EnvCaptureTimeout)
	}
	if c.Capture.Timeout > 0 && c.RTSP.Timeout > 0 && c.Capture.Timeout <= c.RTSP.Timeout {
		add("%s (%s) must be greater than %s (%s)",
			EnvCaptureTimeout, c.Capture.Timeout, EnvRTSPTimeout, c.RTSP.Timeout)
	}
	if c.Capture.FFmpegPath == "" {
		add("%s: must not be empty", EnvFFmpegPath)
	}

	// --- SFTP ---
	if err := validateHost(EnvSFTPHost, c.SFTP.Host, EnvSFTPURL, EnvSFTPPort, EnvSFTPRemoteDir); err != nil {
		errs = append(errs, err)
	}
	if c.SFTP.URL == "" && c.SFTP.Host == "" {
		add("sftp: either %s or %s must be set", EnvSFTPURL, EnvSFTPHost)
	}
	if c.SFTP.Port < 1 || c.SFTP.Port > 65535 {
		add("%s: %d is out of range (1-65535)", EnvSFTPPort, c.SFTP.Port)
	}
	if c.SFTP.Username == "" {
		add("%s: must be set", EnvSFTPUsername)
	}
	if c.SFTP.Password == "" && c.SFTP.PrivateKeyPath == "" {
		add("sftp: provide a credential: set %s or %s", EnvSFTPPassword, EnvSFTPPrivateKeyPath)
	}
	if c.SFTP.RemoteDir == "" {
		add("%s: must not be empty", EnvSFTPRemoteDir)
	}
	if err := validateFilename(EnvSFTPRemoteFilename, c.SFTP.RemoteFilename); err != nil {
		errs = append(errs, err)
	}
	if c.SFTP.Timeout <= 0 {
		add("%s: must be greater than zero", EnvSFTPTimeout)
	}
	for _, fp := range c.SFTP.HostKeyFingerprints() {
		if err := validateFingerprint(EnvSFTPHostKeyFingerprint, fp); err != nil {
			errs = append(errs, err)
		}
	}
	switch strategies := c.hostKeyStrategies(); {
	case strategies == 0:
		add("sftp: configure host key verification: set %s or %s, or explicitly set %s=true",
			EnvSFTPKnownHostsPath, EnvSFTPHostKeyFingerprint, EnvSFTPInsecureIgnoreHost)
	case strategies > 1:
		add("sftp: %s, %s and %s are mutually exclusive; choose one",
			EnvSFTPKnownHostsPath, EnvSFTPHostKeyFingerprint, EnvSFTPInsecureIgnoreHost)
	}
	if c.SFTP.RetryAttempts < 1 {
		add("%s: %d must be at least 1", EnvSFTPRetryAttempts, c.SFTP.RetryAttempts)
	}
	if c.SFTP.RetryBackoff < 0 {
		add("%s: must not be negative", EnvSFTPRetryBackoff)
	}
	if c.SFTP.FileMode == 0 {
		add("%s: must not be 0000", EnvSFTPFileMode)
	}

	// --- Runtime ---
	if c.HTTP.ReadyMaxStaleness < 0 {
		add("%s: must not be negative", EnvHTTPReadyMaxStaleness)
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("%s: %q must be one of debug, info, warn, error", EnvLogLevel, c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		add("%s: %q must be json or text", EnvLogFormat, c.Log.Format)
	}

	return errors.Join(errs...)
}

func (c *Config) hostKeyStrategies() int {
	n := 0
	if c.SFTP.KnownHostsPath != "" {
		n++
	}
	if c.SFTP.HostKeyFingerprint != "" {
		n++
	}
	if c.SFTP.InsecureIgnoreHostKey {
		n++
	}
	return n
}

// validateHost rejects the common mistakes of pasting a whole URL, a host:port
// pair, or a path into a bare host setting. Left unchecked these build a
// syntactically valid but nonsensical URL that only fails much later, at connect
// time, with an error that points nowhere near the actual cause.
func validateHost(env, host, urlEnv, portEnv, pathEnv string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil // emptiness is reported by the caller's own check
	}
	if strings.Contains(host, "://") {
		return fmt.Errorf("%s: %q must be a bare hostname or IP with no scheme; set %s to a full URL instead, or use %s=%s",
			env, host, urlEnv, env, strings.TrimPrefix(host[strings.Index(host, "://")+3:], "/"))
	}
	if i := strings.IndexAny(host, "/?"); i >= 0 {
		return fmt.Errorf("%s: %q must not contain a path; set the host alone and put the rest in %s",
			env, host, pathEnv)
	}
	// A colon is only legitimate here as part of an IPv6 literal.
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return fmt.Errorf("%s: %q must not include a port; set the port with %s", env, host, portEnv)
	}
	return nil
}

// md5Hex matches an MD5 fingerprint, with or without the colons that ssh-keygen
// prints: 32 hex digits.
var md5Hex = regexp.MustCompile(`^(?i)(?:[0-9a-f]{2}:){15}[0-9a-f]{2}$|^(?i)[0-9a-f]{32}$`)

// sha256Body matches the base64 body of a SHA256 fingerprint: 32 bytes,
// unpadded, so exactly 43 characters.
var sha256Body = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=?$`)

// validateFingerprint rejects fingerprints in a format this setting cannot use.
// Many hosting control panels (IONOS among them) still display the legacy MD5
// form, and pasting that in would otherwise fail much later as an opaque
// "host key mismatch" at connect time.
func validateFingerprint(env, fp string) error {
	fp = strings.TrimSpace(fp)
	if fp == "" {
		return nil
	}
	body := fp
	if i := strings.IndexByte(fp, ':'); i >= 0 && strings.EqualFold(fp[:i], "sha256") {
		body = fp[i+1:]
	}

	if strings.HasPrefix(strings.ToLower(fp), "md5:") || md5Hex.MatchString(fp) {
		return fmt.Errorf("%s: %q is an MD5 fingerprint, but this setting takes SHA256. "+
			"Get the SHA256 form with: ssh-keyscan -p <port> <host> | ssh-keygen -lf -", env, fp)
	}
	if !sha256Body.MatchString(body) {
		return fmt.Errorf("%s: %q is not a SHA256 fingerprint (expected %q followed by 43 base64 characters). "+
			"Get it with: ssh-keyscan -p <port> <host> | ssh-keygen -lf -", env, fp, "SHA256:")
	}
	return nil
}

func validateFilename(env, name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%s: must not be empty", env)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%s: %q must be a filename without a path separator", env, name)
	case name == "." || name == "..":
		return fmt.Errorf("%s: %q is not a valid filename", env, name)
	}
	return nil
}

// LocalPath is the absolute path of the JPEG written by the capture step.
func (c *Config) LocalPath() string {
	return filepath.Join(c.Capture.OutputDir, c.Capture.Filename)
}

// RemotePath is the destination path on the SFTP server, always slash-separated.
func (c *Config) RemotePath() string {
	return path.Join(c.SFTP.RemoteDir, c.SFTP.RemoteFilename)
}

// EffectiveReadyMaxStaleness resolves the "0 means three intervals" rule.
func (c *Config) EffectiveReadyMaxStaleness() time.Duration {
	if c.HTTP.ReadyMaxStaleness > 0 {
		return c.HTTP.ReadyMaxStaleness
	}
	if d := 3 * c.Capture.Interval; d > minReadyStaleness {
		return d
	}
	return minReadyStaleness
}

// ResolvedURL builds the RTSP URL handed to ffmpeg, percent-encoding any
// credentials so that special characters survive intact.
func (r RTSPConfig) ResolvedURL() (string, error) {
	u, err := r.resolvedURL()
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (r RTSPConfig) resolvedURL() (*url.URL, error) {
	var u *url.URL

	if raw := strings.TrimSpace(r.URL); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a valid URL: %w", EnvRTSPURL, raw, err)
		}
		switch parsed.Scheme {
		case "rtsp", "rtsps":
		default:
			return nil, fmt.Errorf("%s: scheme %q is not supported (use rtsp:// or rtsps://)", EnvRTSPURL, parsed.Scheme)
		}
		if parsed.Hostname() == "" {
			return nil, fmt.Errorf("%s: %q has no host", EnvRTSPURL, raw)
		}
		u = parsed
		if u.Port() == "" && r.Port > 0 {
			u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(r.Port))
		}
	} else {
		if r.Host == "" {
			return nil, fmt.Errorf("rtsp: either %s or %s must be set", EnvRTSPURL, EnvRTSPHost)
		}
		p, q := splitPathQuery(r.Path)
		u = &url.URL{
			Scheme:   "rtsp",
			Host:     net.JoinHostPort(r.Host, strconv.Itoa(r.Port)),
			Path:     p,
			RawQuery: q,
		}
	}

	// Explicit credentials always win over anything embedded in the URL.
	switch {
	case r.Username != "" && r.Password != "":
		u.User = url.UserPassword(r.Username, r.Password)
	case r.Username != "":
		u.User = url.User(r.Username)
	}
	return u, nil
}

// RedactedURL is the resolved URL with any password replaced, safe for logging.
func (r RTSPConfig) RedactedURL() string {
	u, err := r.resolvedURL()
	if err != nil {
		// Fall back to something harmless but still useful for diagnosis.
		if r.Host != "" {
			return "rtsp://" + net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
		}
		return "<invalid rtsp url>"
	}
	if u.User == nil {
		return u.String()
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return u.String()
	}
	// Serialise without credentials, then splice in a redacted userinfo section.
	username := url.User(u.User.Username()).String()
	u.User = nil
	return strings.Replace(u.String(), "://", "://"+username+":"+redactPlaceholder+"@", 1)
}

// splitPathQuery separates "/cam/realmonitor?channel=1" into path and query so
// that the query is not percent-escaped into the path.
func splitPathQuery(p string) (string, string) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "/"
	}
	var q string
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p, q = p[:i], p[i+1:]
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p, q
}

// HostKeyFingerprints splits the configured value into individual fingerprints.
//
// Several may be given, separated by commas or whitespace. A server usually
// offers more than one host key type and the client negotiates exactly one of
// them, so pinning a single fingerprint fails whenever the negotiated type is
// not the one that was pinned. Listing every fingerprint the server publishes
// keeps the pin strict while making it work regardless of which key is chosen.
func (s SFTPConfig) HostKeyFingerprints() []string {
	fields := strings.FieldsFunc(s.HostKeyFingerprint, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Addr is the host:port pair the SFTP client dials.
func (s SFTPConfig) Addr() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}
