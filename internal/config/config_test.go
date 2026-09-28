package config

import (
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// minimal returns the smallest environment that passes validation.
func minimal() map[string]string {
	return map[string]string{
		EnvRTSPHost:               "cam.lan",
		EnvSFTPHost:               "sftp.lan",
		EnvSFTPUsername:           "uploader",
		EnvSFTPPassword:           "s3cr3t-password",
		EnvSFTPInsecureIgnoreHost: "true",
	}
}

func withEnv(extra map[string]string) map[string]string {
	m := minimal()
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func mustLoad(t *testing.T, env map[string]string, args ...string) *Config {
	t.Helper()
	cfg, err := load(MapLookup(env), args, io.Discard)
	if err != nil {
		t.Fatalf("load: unexpected error: %v", err)
	}
	return cfg
}

func loadErr(t *testing.T, env map[string]string, args ...string) error {
	t.Helper()
	cfg, err := load(MapLookup(env), args, io.Discard)
	if err == nil {
		t.Fatalf("load: expected an error, got config %+v", cfg)
	}
	return err
}

func TestLoadDefaults(t *testing.T) {
	cfg := mustLoad(t, minimal())

	checks := []struct {
		name string
		got  any
		want any
	}{
		{EnvRTSPPort, cfg.RTSP.Port, 554},
		{EnvRTSPPath, cfg.RTSP.Path, "/"},
		{EnvRTSPTransport, cfg.RTSP.Transport, TransportTCP},
		{EnvRTSPTimeout, cfg.RTSP.Timeout, 15 * time.Second},
		{EnvCaptureInterval, cfg.Capture.Interval, 60 * time.Second},
		{EnvCaptureOutputDir, cfg.Capture.OutputDir, "/tmp"},
		{EnvCaptureFilename, cfg.Capture.Filename, "image.jpg"},
		{EnvCaptureJPEGQuality, cfg.Capture.JPEGQuality, 2},
		{EnvCaptureTimeout, cfg.Capture.Timeout, 30 * time.Second},
		{EnvFFmpegPath, cfg.Capture.FFmpegPath, "ffmpeg"},
		{EnvSFTPPort, cfg.SFTP.Port, 22},
		{EnvSFTPRemoteDir, cfg.SFTP.RemoteDir, "/upload"},
		{EnvSFTPRemoteFilename, cfg.SFTP.RemoteFilename, "image.jpg"},
		{EnvSFTPTimeout, cfg.SFTP.Timeout, 30 * time.Second},
		{EnvSFTPMkdir, cfg.SFTP.Mkdir, true},
		{EnvSFTPAtomic, cfg.SFTP.Atomic, true},
		{EnvSFTPFileMode, cfg.SFTP.FileMode, os.FileMode(0o644)},
		{EnvSFTPRetryAttempts, cfg.SFTP.RetryAttempts, 3},
		{EnvSFTPRetryBackoff, cfg.SFTP.RetryBackoff, 2 * time.Second},
		{EnvHTTPAddr, cfg.HTTP.Addr, ":8080"},
		{EnvLogLevel, cfg.Log.Level, "info"},
		{EnvLogFormat, cfg.Log.Format, "json"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoadFullEnvironment(t *testing.T) {
	env := map[string]string{
		EnvRTSPHost:      "10.0.0.5",
		EnvRTSPPort:      "8554",
		EnvRTSPPath:      "/Streaming/Channels/101",
		EnvRTSPUsername:  "admin",
		EnvRTSPPassword:  "cam-password",
		EnvRTSPTransport: "udp",
		EnvRTSPTimeout:   "20s",

		EnvCaptureInterval:    "5m",
		EnvCaptureOutputDir:   "/var/cache",
		EnvCaptureFilename:    "snap.jpg",
		EnvCaptureJPEGQuality: "7",
		EnvCaptureTimeout:     "45s",
		EnvFFmpegPath:         "/usr/bin/ffmpeg",

		EnvSFTPHost:                 "files.example.com",
		EnvSFTPPort:                 "2222",
		EnvSFTPUsername:             "bob",
		EnvSFTPPassword:             "sftp-password",
		EnvSFTPRemoteDir:            "/photos/cam1",
		EnvSFTPRemoteFilename:       "latest.jpg",
		EnvSFTPTimeout:              "40s",
		EnvSFTPHostKeyFingerprint:   "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		EnvSFTPMkdir:                "false",
		EnvSFTPAtomic:               "false",
		EnvSFTPFileMode:             "0600",
		EnvSFTPRetryAttempts:        "5",
		EnvSFTPRetryBackoff:         "1s",
		EnvSFTPPrivateKeyPassphrase: "key-passphrase",

		EnvHTTPAddr:              ":9090",
		EnvHTTPReadyMaxStaleness: "10m",
		EnvLogLevel:              "debug",
		EnvLogFormat:             "text",
	}
	cfg := mustLoad(t, env)

	if cfg.RTSP.Host != "10.0.0.5" || cfg.RTSP.Port != 8554 || cfg.RTSP.Transport != "udp" {
		t.Errorf("rtsp not applied: %+v", cfg.RTSP)
	}
	if cfg.RTSP.Timeout != 20*time.Second || cfg.Capture.Interval != 5*time.Minute {
		t.Errorf("durations not applied: %+v %+v", cfg.RTSP.Timeout, cfg.Capture.Interval)
	}
	if cfg.Capture.Filename != "snap.jpg" || cfg.Capture.JPEGQuality != 7 {
		t.Errorf("capture not applied: %+v", cfg.Capture)
	}
	if cfg.SFTP.Port != 2222 || cfg.SFTP.Username != "bob" || cfg.SFTP.RemoteFilename != "latest.jpg" {
		t.Errorf("sftp not applied: %+v", cfg.SFTP)
	}
	if cfg.SFTP.Mkdir || cfg.SFTP.Atomic {
		t.Error("SFTP_MKDIR/SFTP_ATOMIC=false not applied")
	}
	if cfg.SFTP.FileMode != 0o600 || cfg.SFTP.RetryAttempts != 5 {
		t.Errorf("sftp file mode/retries not applied: %v %d", cfg.SFTP.FileMode, cfg.SFTP.RetryAttempts)
	}
	if cfg.HTTP.Addr != ":9090" || cfg.HTTP.ReadyMaxStaleness != 10*time.Minute {
		t.Errorf("http not applied: %+v", cfg.HTTP)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "text" {
		t.Errorf("log not applied: %+v", cfg.Log)
	}
}

func TestLoadPrecedence(t *testing.T) {
	t.Run("flag_overrides_env", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{EnvRTSPHost: "from-env"}), "--rtsp-host=from-flag")
		if cfg.RTSP.Host != "from-flag" {
			t.Errorf("host = %q, want from-flag", cfg.RTSP.Host)
		}
	})

	t.Run("flag_only", func(t *testing.T) {
		cfg, err := load(MapLookup(nil), []string{
			"--rtsp-host=cam", "--sftp-host=s", "--sftp-username=u",
			"--sftp-password=pass1234", "--sftp-insecure-ignore-host-key",
		}, io.Discard)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.RTSP.Host != "cam" || !cfg.SFTP.InsecureIgnoreHostKey {
			t.Errorf("flags not applied: %+v", cfg)
		}
	})

	t.Run("bool_flag_explicit_false", func(t *testing.T) {
		cfg := mustLoad(t, minimal(), "--sftp-atomic=false")
		if cfg.SFTP.Atomic {
			t.Error("--sftp-atomic=false was not applied")
		}
	})

	t.Run("env_present_but_empty_is_explicit", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{EnvHTTPAddr: ""}))
		if cfg.HTTP.Addr != "" {
			t.Errorf("HTTP_ADDR = %q, want empty (disables the listener)", cfg.HTTP.Addr)
		}
	})

	t.Run("interval_bare_seconds", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{EnvCaptureInterval: "60"}))
		if cfg.Capture.Interval != 60*time.Second {
			t.Errorf("interval = %v, want 60s", cfg.Capture.Interval)
		}
	})

	t.Run("interval_duration_string", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{EnvCaptureInterval: "90s"}))
		if cfg.Capture.Interval != 90*time.Second {
			t.Errorf("interval = %v, want 90s", cfg.Capture.Interval)
		}
	})

	t.Run("help_flag", func(t *testing.T) {
		_, err := load(MapLookup(minimal()), []string{"--help"}, io.Discard)
		if !errors.Is(err, flag.ErrHelp) {
			t.Errorf("err = %v, want flag.ErrHelp", err)
		}
	})

	t.Run("unknown_flag", func(t *testing.T) {
		err := loadErr(t, minimal(), "--nope=1")
		if !strings.Contains(err.Error(), "nope") {
			t.Errorf("err = %v, want it to mention the flag", err)
		}
	})

	t.Run("positional_argument_rejected", func(t *testing.T) {
		err := loadErr(t, minimal(), "extra")
		if !strings.Contains(err.Error(), "extra") {
			t.Errorf("err = %v, want it to mention the argument", err)
		}
	})

	t.Run("version_skips_validation", func(t *testing.T) {
		cfg, err := load(MapLookup(nil), []string{"--version"}, io.Discard)
		if err != nil {
			t.Fatalf("--version must not require configuration: %v", err)
		}
		if !cfg.ShowVersion {
			t.Error("ShowVersion not set")
		}
	})

	t.Run("nil_lookup_is_safe", func(t *testing.T) {
		if _, err := load(nil, []string{"--version"}, io.Discard); err != nil {
			t.Fatalf("nil lookup: %v", err)
		}
	})
}

func TestLoadParseErrors(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		mentions string
	}{
		{"port_not_a_number", map[string]string{EnvRTSPPort: "abc"}, EnvRTSPPort},
		{"port_zero", map[string]string{EnvRTSPPort: "0"}, EnvRTSPPort},
		{"port_too_large", map[string]string{EnvRTSPPort: "70000"}, EnvRTSPPort},
		{"sftp_port_invalid", map[string]string{EnvSFTPPort: "-1"}, EnvSFTPPort},
		{"negative_interval", map[string]string{EnvCaptureInterval: "-5s"}, EnvCaptureInterval},
		{"bad_duration", map[string]string{EnvRTSPTimeout: "soon"}, EnvRTSPTimeout},
		{"quality_too_low", map[string]string{EnvCaptureJPEGQuality: "1"}, EnvCaptureJPEGQuality},
		{"quality_too_high", map[string]string{EnvCaptureJPEGQuality: "99"}, EnvCaptureJPEGQuality},
		{"file_mode_not_octal", map[string]string{EnvSFTPFileMode: "0999"}, EnvSFTPFileMode},
		{"file_mode_too_large", map[string]string{EnvSFTPFileMode: "7777"}, EnvSFTPFileMode},
		{"file_mode_zero", map[string]string{EnvSFTPFileMode: "0"}, EnvSFTPFileMode},
		{"retry_attempts_zero", map[string]string{EnvSFTPRetryAttempts: "0"}, EnvSFTPRetryAttempts},
		{"bad_transport", map[string]string{EnvRTSPTransport: "sctp"}, EnvRTSPTransport},
		{"bad_log_level", map[string]string{EnvLogLevel: "verbose"}, EnvLogLevel},
		{"bad_log_format", map[string]string{EnvLogFormat: "xml"}, EnvLogFormat},
		{"bad_bool", map[string]string{EnvSFTPMkdir: "maybe"}, EnvSFTPMkdir},
		{"zero_rtsp_timeout", map[string]string{EnvRTSPTimeout: "0s"}, EnvRTSPTimeout},
		{"empty_output_dir", map[string]string{EnvCaptureOutputDir: ""}, EnvCaptureOutputDir},
		{"empty_ffmpeg_path", map[string]string{EnvFFmpegPath: ""}, EnvFFmpegPath},
		{"empty_remote_dir", map[string]string{EnvSFTPRemoteDir: ""}, EnvSFTPRemoteDir},
		{"negative_staleness", map[string]string{EnvHTTPReadyMaxStaleness: "-1s"}, EnvHTTPReadyMaxStaleness},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := loadErr(t, withEnv(tc.env))
			if !strings.Contains(err.Error(), tc.mentions) {
				t.Errorf("err = %v, want it to mention %s", err, tc.mentions)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		remove   []string
		mentions []string
	}{
		{
			name:     "missing_rtsp_source",
			remove:   []string{EnvRTSPHost},
			mentions: []string{EnvRTSPURL, EnvRTSPHost},
		},
		{
			name:     "missing_sftp_host",
			remove:   []string{EnvSFTPHost},
			mentions: []string{EnvSFTPURL, EnvSFTPHost},
		},
		{
			name:     "missing_sftp_username",
			remove:   []string{EnvSFTPUsername},
			mentions: []string{EnvSFTPUsername},
		},
		{
			name:     "no_sftp_credential",
			remove:   []string{EnvSFTPPassword},
			mentions: []string{EnvSFTPPassword, EnvSFTPPrivateKeyPath},
		},
		{
			name:     "no_host_key_strategy",
			remove:   []string{EnvSFTPInsecureIgnoreHost},
			mentions: []string{EnvSFTPKnownHostsPath, EnvSFTPHostKeyFingerprint, EnvSFTPInsecureIgnoreHost},
		},
		{
			name:     "two_host_key_strategies",
			env:      map[string]string{EnvSFTPHostKeyFingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
			mentions: []string{"mutually exclusive"},
		},
		{
			name: "three_host_key_strategies",
			env: map[string]string{
				EnvSFTPHostKeyFingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				EnvSFTPKnownHostsPath:     "/etc/known_hosts",
			},
			mentions: []string{"mutually exclusive"},
		},
		{
			name:     "capture_timeout_not_greater_than_rtsp_timeout",
			env:      map[string]string{EnvCaptureTimeout: "15s", EnvRTSPTimeout: "15s"},
			mentions: []string{EnvCaptureTimeout, EnvRTSPTimeout},
		},
		{
			name:     "capture_filename_with_separator",
			env:      map[string]string{EnvCaptureFilename: "sub/dir.jpg"},
			mentions: []string{EnvCaptureFilename},
		},
		{
			name:     "remote_filename_with_separator",
			env:      map[string]string{EnvSFTPRemoteFilename: `a\b.jpg`},
			mentions: []string{EnvSFTPRemoteFilename},
		},
		{
			name:     "capture_filename_dotdot",
			env:      map[string]string{EnvCaptureFilename: ".."},
			mentions: []string{EnvCaptureFilename},
		},
		{
			name:     "capture_filename_empty",
			env:      map[string]string{EnvCaptureFilename: ""},
			mentions: []string{EnvCaptureFilename},
		},
		{
			name:     "bad_rtsp_url_scheme",
			env:      map[string]string{EnvRTSPURL: "http://cam/stream"},
			mentions: []string{EnvRTSPURL, "scheme"},
		},
		{
			name:     "rtsp_url_without_host",
			env:      map[string]string{EnvRTSPURL: "rtsp:///stream"},
			mentions: []string{EnvRTSPURL},
		},
		{
			name:     "bad_sftp_url_scheme",
			env:      map[string]string{EnvSFTPURL: "ftp://host/dir"},
			mentions: []string{EnvSFTPURL, "scheme"},
		},
		{
			name:     "zero_sftp_timeout",
			env:      map[string]string{EnvSFTPTimeout: "0"},
			mentions: []string{EnvSFTPTimeout},
		},
		{
			name:     "negative_retry_backoff",
			env:      map[string]string{EnvSFTPRetryBackoff: "-1s"},
			mentions: []string{EnvSFTPRetryBackoff},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := withEnv(tc.env)
			for _, k := range tc.remove {
				delete(env, k)
			}
			err := loadErr(t, env)
			for _, m := range tc.mentions {
				if !strings.Contains(err.Error(), m) {
					t.Errorf("err = %v\nwant it to mention %q", err, m)
				}
			}
		})
	}

	t.Run("host_must_be_bare", func(t *testing.T) {
		// Pasting a URL, a host:port pair or a path into a bare host setting used
		// to build a nonsensical URL that only failed at connect time.
		cases := []struct {
			name     string
			env      map[string]string
			mentions []string
		}{
			{"rtsp_scheme_in_host", map[string]string{EnvRTSPHost: "rtsp://10.0.0.5"}, []string{EnvRTSPHost, EnvRTSPURL, "no scheme"}},
			{"rtsp_url_with_path_in_host", map[string]string{EnvRTSPHost: "rtsp://cam/live"}, []string{EnvRTSPHost, "no scheme"}},
			{"rtsp_port_in_host", map[string]string{EnvRTSPHost: "10.0.0.5:554"}, []string{EnvRTSPHost, EnvRTSPPort, "port"}},
			{"rtsp_path_in_host", map[string]string{EnvRTSPHost: "10.0.0.5/Streaming"}, []string{EnvRTSPHost, EnvRTSPPath, "path"}},
			{"rtsp_query_in_host", map[string]string{EnvRTSPHost: "10.0.0.5?channel=1"}, []string{EnvRTSPHost, "path"}},
			{"sftp_scheme_in_host", map[string]string{EnvSFTPHost: "sftp://files.example.com"}, []string{EnvSFTPHost, EnvSFTPURL}},
			{"sftp_port_in_host", map[string]string{EnvSFTPHost: "files.example.com:2222"}, []string{EnvSFTPHost, EnvSFTPPort}},
			{"sftp_path_in_host", map[string]string{EnvSFTPHost: "files.example.com/upload"}, []string{EnvSFTPHost, EnvSFTPRemoteDir}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				err := loadErr(t, withEnv(tc.env))
				for _, m := range tc.mentions {
					if !strings.Contains(err.Error(), m) {
						t.Errorf("err = %v\nwant it to mention %q", err, m)
					}
				}
			})
		}
	})

	t.Run("legitimate_hosts_are_accepted", func(t *testing.T) {
		for _, host := range []string{"10.0.0.5", "cam.example.com", "fd00::1", "::1", "localhost"} {
			t.Run(host, func(t *testing.T) {
				cfg := mustLoad(t, withEnv(map[string]string{EnvRTSPHost: host}))
				if cfg.RTSP.Host != host {
					t.Errorf("host = %q, want %q", cfg.RTSP.Host, host)
				}
			})
		}
	})

	t.Run("a_full_url_still_belongs_in_the_url_setting", func(t *testing.T) {
		// The suggested remedy must actually work.
		env := withEnv(map[string]string{EnvRTSPURL: "rtsp://10.0.0.5:554/live"})
		delete(env, EnvRTSPHost)
		cfg := mustLoad(t, env)
		got, err := cfg.RTSP.ResolvedURL()
		if err != nil {
			t.Fatalf("ResolvedURL: %v", err)
		}
		if got != "rtsp://10.0.0.5:554/live" {
			t.Errorf("ResolvedURL() = %q", got)
		}
	})

	t.Run("host_key_fingerprint_format", func(t *testing.T) {
		// Hosting control panels (IONOS among them) still display MD5. Pasting
		// that in used to fail later as an opaque "host key mismatch".
		rejected := []struct{ name, fp, mentions string }{
			{"md5_hex", "e5f04b35d161e4c14d6c764130fb53ff", "MD5"},
			{"md5_colons", "e5:f0:4b:35:d1:61:e4:c1:4d:6c:76:41:30:fb:53:ff", "MD5"},
			{"md5_prefixed", "MD5:e5:f0:4b:35:d1:61:e4:c1:4d:6c:76:41:30:fb:53:ff", "MD5"},
			{"md5_uppercase", "E5F04B35D161E4C14D6C764130FB53FF", "MD5"},
			{"garbage", "not-a-fingerprint", "not a SHA256 fingerprint"},
			{"too_short", "SHA256:abc", "not a SHA256 fingerprint"},
			{"sha1_hex", "da39a3ee5e6b4b0d3255bfef95601890afd80709", "not a SHA256 fingerprint"},
		}
		for _, tc := range rejected {
			t.Run(tc.name, func(t *testing.T) {
				env := withEnv(map[string]string{EnvSFTPHostKeyFingerprint: tc.fp})
				delete(env, EnvSFTPInsecureIgnoreHost) // fingerprint is then the only strategy
				err := loadErr(t, env)
				if !strings.Contains(err.Error(), tc.mentions) {
					t.Errorf("err = %v\nwant it to mention %q", err, tc.mentions)
				}
				if !strings.Contains(err.Error(), "ssh-keyscan") {
					t.Errorf("err = %v\nwant it to say how to get the right value", err)
				}
			})
		}

		accepted := []struct{ name, fp string }{
			{"with_prefix", "SHA256:09YVpKZ5e28UhFW+IJVe6poOVt1ZAVZvdOIGRvlFZS8"},
			{"without_prefix", "09YVpKZ5e28UhFW+IJVe6poOVt1ZAVZvdOIGRvlFZS8"},
			{"lowercase_prefix", "sha256:09YVpKZ5e28UhFW+IJVe6poOVt1ZAVZvdOIGRvlFZS8"},
			{"padded", "SHA256:09YVpKZ5e28UhFW+IJVe6poOVt1ZAVZvdOIGRvlFZS8="},
			{"with_surrounding_space", "  SHA256:09YVpKZ5e28UhFW+IJVe6poOVt1ZAVZvdOIGRvlFZS8  "},
		}
		for _, tc := range accepted {
			t.Run(tc.name, func(t *testing.T) {
				env := withEnv(map[string]string{EnvSFTPHostKeyFingerprint: tc.fp})
				delete(env, EnvSFTPInsecureIgnoreHost)
				if cfg := mustLoad(t, env); cfg.SFTP.HostKeyFingerprint == "" {
					t.Error("fingerprint was not applied")
				}
			})
		}
	})

	t.Run("multiple_errors_aggregated", func(t *testing.T) {
		env := withEnv(map[string]string{
			EnvRTSPTransport:      "sctp",
			EnvCaptureJPEGQuality: "99",
			EnvLogFormat:          "xml",
		})
		delete(env, EnvSFTPUsername)
		err := loadErr(t, env)
		for _, m := range []string{EnvRTSPTransport, EnvCaptureJPEGQuality, EnvLogFormat, EnvSFTPUsername} {
			if !strings.Contains(err.Error(), m) {
				t.Errorf("aggregated error is missing %s:\n%v", m, err)
			}
		}
		if n := strings.Count(err.Error(), "\n") + 1; n < 4 {
			t.Errorf("expected at least 4 joined errors, got %d:\n%v", n, err)
		}
	})

	t.Run("one_shot_interval_zero_is_valid", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{EnvCaptureInterval: "0"}))
		if cfg.Capture.Interval != 0 {
			t.Errorf("interval = %v, want 0", cfg.Capture.Interval)
		}
	})

	t.Run("private_key_satisfies_credential_requirement", func(t *testing.T) {
		env := withEnv(map[string]string{EnvSFTPPrivateKeyPath: "/etc/keys/id_ed25519"})
		delete(env, EnvSFTPPassword)
		cfg := mustLoad(t, env)
		if cfg.SFTP.PrivateKeyPath == "" {
			t.Error("private key path not applied")
		}
	})

	t.Run("known_hosts_satisfies_host_key_requirement", func(t *testing.T) {
		env := withEnv(map[string]string{EnvSFTPKnownHostsPath: "/etc/ssh/known_hosts"})
		delete(env, EnvSFTPInsecureIgnoreHost)
		if cfg := mustLoad(t, env); cfg.SFTP.KnownHostsPath == "" {
			t.Error("known hosts path not applied")
		}
	})
}

func TestResolvedURL(t *testing.T) {
	cases := []struct {
		name string
		rtsp RTSPConfig
		want string
	}{
		{
			name: "from_parts_with_credentials",
			rtsp: RTSPConfig{Host: "cam.lan", Port: 554, Path: "/Streaming/Channels/101", Username: "admin", Password: "pw"},
			want: "rtsp://admin:pw@cam.lan:554/Streaming/Channels/101",
		},
		{
			name: "from_parts_without_credentials",
			rtsp: RTSPConfig{Host: "cam.lan", Port: 554, Path: "/live"},
			want: "rtsp://cam.lan:554/live",
		},
		{
			name: "username_only",
			rtsp: RTSPConfig{Host: "cam.lan", Port: 554, Path: "/live", Username: "admin"},
			want: "rtsp://admin@cam.lan:554/live",
		},
		{
			name: "path_without_leading_slash",
			rtsp: RTSPConfig{Host: "cam.lan", Port: 554, Path: "Streaming/1"},
			want: "rtsp://cam.lan:554/Streaming/1",
		},
		{
			name: "empty_path_becomes_root",
			rtsp: RTSPConfig{Host: "cam.lan", Port: 554, Path: ""},
			want: "rtsp://cam.lan:554/",
		},
		{
			name: "query_string_is_preserved",
			rtsp: RTSPConfig{Host: "cam.lan", Port: 554, Path: "/cam/realmonitor?channel=1&subtype=0"},
			want: "rtsp://cam.lan:554/cam/realmonitor?channel=1&subtype=0",
		},
		{
			name: "ipv6_host",
			rtsp: RTSPConfig{Host: "fd00::1", Port: 554, Path: "/live"},
			want: "rtsp://[fd00::1]:554/live",
		},
		{
			name: "explicit_url_wins_over_parts",
			rtsp: RTSPConfig{URL: "rtsp://other.lan:1234/x", Host: "cam.lan", Port: 554, Path: "/ignored"},
			want: "rtsp://other.lan:1234/x",
		},
		{
			name: "explicit_url_gets_credentials_injected",
			rtsp: RTSPConfig{URL: "rtsp://cam.lan:554/x", Username: "admin", Password: "pw"},
			want: "rtsp://admin:pw@cam.lan:554/x",
		},
		{
			name: "explicit_url_credentials_overridden",
			rtsp: RTSPConfig{URL: "rtsp://old:old@cam.lan:554/x", Username: "admin", Password: "pw"},
			want: "rtsp://admin:pw@cam.lan:554/x",
		},
		{
			name: "explicit_url_without_port_gets_configured_port",
			rtsp: RTSPConfig{URL: "rtsp://cam.lan/x", Port: 8554},
			want: "rtsp://cam.lan:8554/x",
		},
		{
			name: "rtsps_scheme_allowed",
			rtsp: RTSPConfig{URL: "rtsps://cam.lan:322/x"},
			want: "rtsps://cam.lan:322/x",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.rtsp.ResolvedURL()
			if err != nil {
				t.Fatalf("ResolvedURL: %v", err)
			}
			if got != tc.want {
				t.Errorf("ResolvedURL() = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("password_with_special_characters_round_trips", func(t *testing.T) {
		const pw = `p@ss/w:rd#1?&=+ x`
		raw, err := RTSPConfig{Host: "cam.lan", Port: 554, Path: "/live", Username: "ad min", Password: pw}.ResolvedURL()
		if err != nil {
			t.Fatalf("ResolvedURL: %v", err)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("the produced URL does not parse: %q: %v", raw, err)
		}
		gotPW, ok := u.User.Password()
		if !ok || gotPW != pw {
			t.Errorf("password round-trip failed: got %q (ok=%v), want %q\nurl: %s", gotPW, ok, pw, raw)
		}
		if u.User.Username() != "ad min" {
			t.Errorf("username round-trip failed: got %q", u.User.Username())
		}
	})

	t.Run("errors", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			rtsp RTSPConfig
		}{
			{"no_host_no_url", RTSPConfig{Port: 554}},
			{"bad_scheme", RTSPConfig{URL: "http://cam/x"}},
			{"unparsable_url", RTSPConfig{URL: "rtsp://cam\x7f/x"}},
			{"url_without_host", RTSPConfig{URL: "rtsp:///x"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := tc.rtsp.ResolvedURL(); err == nil {
					t.Error("expected an error")
				}
			})
		}
	})
}

func TestRedactedURL(t *testing.T) {
	const pw = "super-secret-pw"

	t.Run("hides_password_and_keeps_context", func(t *testing.T) {
		r := RTSPConfig{Host: "cam.lan", Port: 554, Path: "/live", Username: "admin", Password: pw}
		got := r.RedactedURL()
		if strings.Contains(got, pw) {
			t.Fatalf("RedactedURL leaked the password: %s", got)
		}
		for _, want := range []string{redactPlaceholder, "admin", "cam.lan", "554", "/live"} {
			if !strings.Contains(got, want) {
				t.Errorf("RedactedURL() = %q, want it to contain %q", got, want)
			}
		}
	})

	t.Run("special_characters_are_not_leaked", func(t *testing.T) {
		r := RTSPConfig{Host: "cam.lan", Port: 554, Username: "admin", Password: `p@ss/w:rd`}
		got := r.RedactedURL()
		if strings.Contains(got, "p%40ss") || strings.Contains(got, "p@ss") {
			t.Errorf("RedactedURL leaked an encoded password: %s", got)
		}
	})

	t.Run("no_credentials_is_unchanged", func(t *testing.T) {
		r := RTSPConfig{Host: "cam.lan", Port: 554, Path: "/live"}
		if got := r.RedactedURL(); got != "rtsp://cam.lan:554/live" {
			t.Errorf("RedactedURL() = %q", got)
		}
	})

	t.Run("username_only_is_unchanged", func(t *testing.T) {
		r := RTSPConfig{Host: "cam.lan", Port: 554, Path: "/live", Username: "admin"}
		if got := r.RedactedURL(); got != "rtsp://admin@cam.lan:554/live" {
			t.Errorf("RedactedURL() = %q", got)
		}
	})

	t.Run("invalid_config_falls_back_to_host", func(t *testing.T) {
		if got := (RTSPConfig{URL: "http://x/y", Host: "cam.lan", Port: 554}).RedactedURL(); got != "rtsp://cam.lan:554" {
			t.Errorf("RedactedURL() = %q", got)
		}
	})

	t.Run("invalid_config_without_host", func(t *testing.T) {
		if got := (RTSPConfig{URL: "http://x/y"}).RedactedURL(); !strings.Contains(got, "invalid") {
			t.Errorf("RedactedURL() = %q", got)
		}
	})
}

func TestSFTPURLForm(t *testing.T) {
	t.Run("full_url", func(t *testing.T) {
		env := map[string]string{
			EnvRTSPHost:               "cam.lan",
			EnvSFTPURL:                "sftp://bob:url-password@files.example.com:2222/photos/cam1",
			EnvSFTPInsecureIgnoreHost: "true",
		}
		cfg := mustLoad(t, env)
		if cfg.SFTP.Host != "files.example.com" {
			t.Errorf("host = %q", cfg.SFTP.Host)
		}
		if cfg.SFTP.Port != 2222 {
			t.Errorf("port = %d", cfg.SFTP.Port)
		}
		if cfg.SFTP.Username != "bob" {
			t.Errorf("username = %q", cfg.SFTP.Username)
		}
		if cfg.SFTP.Password != "url-password" {
			t.Errorf("password = %q", cfg.SFTP.Password)
		}
		if cfg.SFTP.RemoteDir != "/photos/cam1" {
			t.Errorf("remote dir = %q", cfg.SFTP.RemoteDir)
		}
	})

	t.Run("url_without_port_uses_default", func(t *testing.T) {
		cfg := mustLoad(t, map[string]string{
			EnvRTSPHost:               "cam.lan",
			EnvSFTPURL:                "sftp://bob:url-password@files.example.com/dir",
			EnvSFTPInsecureIgnoreHost: "true",
		})
		if cfg.SFTP.Port != 22 {
			t.Errorf("port = %d, want 22", cfg.SFTP.Port)
		}
	})

	t.Run("explicit_settings_win_over_url", func(t *testing.T) {
		cfg := mustLoad(t, map[string]string{
			EnvRTSPHost:               "cam.lan",
			EnvSFTPURL:                "sftp://bob:url-password@files.example.com:2222/photos",
			EnvSFTPUsername:           "alice",
			EnvSFTPPort:               "2200",
			EnvSFTPRemoteDir:          "/elsewhere",
			EnvSFTPInsecureIgnoreHost: "true",
		})
		if cfg.SFTP.Username != "alice" {
			t.Errorf("username = %q, want alice", cfg.SFTP.Username)
		}
		if cfg.SFTP.Port != 2200 {
			t.Errorf("port = %d, want 2200", cfg.SFTP.Port)
		}
		if cfg.SFTP.RemoteDir != "/elsewhere" {
			t.Errorf("remote dir = %q", cfg.SFTP.RemoteDir)
		}
	})

	t.Run("flag_wins_over_url", func(t *testing.T) {
		cfg := mustLoad(t, map[string]string{
			EnvRTSPHost:               "cam.lan",
			EnvSFTPURL:                "sftp://bob:url-password@files.example.com/dir",
			EnvSFTPInsecureIgnoreHost: "true",
		}, "--sftp-username=carol")
		if cfg.SFTP.Username != "carol" {
			t.Errorf("username = %q, want carol", cfg.SFTP.Username)
		}
	})

	t.Run("unparsable_url", func(t *testing.T) {
		err := loadErr(t, withEnv(map[string]string{EnvSFTPURL: "sftp://ho st\x7f/dir"}))
		if !strings.Contains(err.Error(), EnvSFTPURL) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRTSPURLCredentialsAreHoisted(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		EnvRTSPURL:                "rtsp://admin:url-cam-password@cam.lan:554/live",
		EnvSFTPHost:               "sftp.lan",
		EnvSFTPUsername:           "up",
		EnvSFTPPassword:           "sftp-password",
		EnvSFTPInsecureIgnoreHost: "true",
	})
	if cfg.RTSP.Username != "admin" || cfg.RTSP.Password != "url-cam-password" {
		t.Fatalf("credentials were not hoisted out of RTSP_URL: %+v", cfg.RTSP)
	}
	// The whole point of hoisting: the redactor must know about the password.
	if got := FromConfig(cfg).String("connect failed for url-cam-password"); strings.Contains(got, "url-cam-password") {
		t.Errorf("redactor does not know the URL password: %q", got)
	}
}

func TestDerivedPaths(t *testing.T) {
	t.Run("local_path", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{EnvCaptureOutputDir: "/var/cache", EnvCaptureFilename: "a.jpg"}))
		if got, want := cfg.LocalPath(), filepath.Join("/var/cache", "a.jpg"); got != want {
			t.Errorf("LocalPath() = %q, want %q", got, want)
		}
	})

	t.Run("remote_path_absolute_dir", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{EnvSFTPRemoteDir: "/photos/"}))
		if got := cfg.RemotePath(); got != "/photos/image.jpg" {
			t.Errorf("RemotePath() = %q", got)
		}
	})

	t.Run("remote_path_relative_dir", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{EnvSFTPRemoteDir: "photos"}))
		if got := cfg.RemotePath(); got != "photos/image.jpg" {
			t.Errorf("RemotePath() = %q, want no leading slash added", got)
		}
	})

	t.Run("remote_path_root_dir", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{EnvSFTPRemoteDir: "/"}))
		if got := cfg.RemotePath(); got != "/image.jpg" {
			t.Errorf("RemotePath() = %q", got)
		}
	})

	t.Run("remote_filename_defaults_to_capture_filename", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{EnvCaptureFilename: "snap.jpg"}))
		if cfg.SFTP.RemoteFilename != "snap.jpg" {
			t.Errorf("remote filename = %q", cfg.SFTP.RemoteFilename)
		}
	})

	t.Run("sftp_addr", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{EnvSFTPHost: "h", EnvSFTPPort: "2222"}))
		if got := cfg.SFTP.Addr(); got != "h:2222" {
			t.Errorf("Addr() = %q", got)
		}
	})
}

func TestEffectiveReadyMaxStaleness(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want time.Duration
	}{
		{"three_intervals", map[string]string{EnvCaptureInterval: "60s"}, 180 * time.Second},
		{"floor_applies_for_short_intervals", map[string]string{EnvCaptureInterval: "5s"}, minReadyStaleness},
		{"floor_applies_for_one_shot", map[string]string{EnvCaptureInterval: "0"}, minReadyStaleness},
		{"explicit_value_wins", map[string]string{EnvHTTPReadyMaxStaleness: "10m"}, 10 * time.Minute},
		{"long_interval", map[string]string{EnvCaptureInterval: "10m"}, 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mustLoad(t, withEnv(tc.env))
			if got := cfg.EffectiveReadyMaxStaleness(); got != tc.want {
				t.Errorf("EffectiveReadyMaxStaleness() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHostKeyFingerprints(t *testing.T) {
	const a = "SHA256:09YVpKZ5e28UhFW+IJVe6poOVt1ZAVZvdOIGRvlFZS8"
	const b = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"single", a, 1},
		{"comma_separated", a + "," + b, 2},
		{"space_separated", a + " " + b, 2},
		{"semicolon_separated", a + ";" + b, 2},
		{"newline_separated", a + "\n" + b, 2},
		{"mixed_with_padding", "  " + a + " ,  " + b + "  ", 2},
		{"trailing_separator", a + ",", 1},
		{"only_separators", " , ; ", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SFTPConfig{HostKeyFingerprint: tc.in}.HostKeyFingerprints()
			if len(got) != tc.want {
				t.Fatalf("HostKeyFingerprints() = %q, want %d entries", got, tc.want)
			}
			for _, fp := range got {
				if strings.ContainsAny(fp, " ,;\n\t") {
					t.Errorf("entry %q still contains a separator", fp)
				}
			}
		})
	}

	t.Run("every_entry_in_a_list_is_validated", func(t *testing.T) {
		env := withEnv(map[string]string{
			// Second entry is MD5: the whole list must be rejected.
			EnvSFTPHostKeyFingerprint: a + ",e5f04b35d161e4c14d6c764130fb53ff",
		})
		delete(env, EnvSFTPInsecureIgnoreHost)
		if err := loadErr(t, env); !strings.Contains(err.Error(), "MD5") {
			t.Errorf("err = %v, want it to flag the MD5 entry", err)
		}
	})

	t.Run("a_valid_list_is_accepted", func(t *testing.T) {
		env := withEnv(map[string]string{EnvSFTPHostKeyFingerprint: a + " " + b})
		delete(env, EnvSFTPInsecureIgnoreHost)
		if cfg := mustLoad(t, env); len(cfg.SFTP.HostKeyFingerprints()) != 2 {
			t.Error("both fingerprints should survive loading")
		}
	})
}

func TestCropConfig(t *testing.T) {
	t.Run("disabled_by_default", func(t *testing.T) {
		cfg := mustLoad(t, minimal())
		if cfg.Capture.Crop.Enabled() {
			t.Error("crop should be off by default")
		}
		if got := cfg.Capture.Crop.FFmpegFilter(); got != "" {
			t.Errorf("FFmpegFilter() = %q, want empty", got)
		}
	})

	t.Run("filter_expression", func(t *testing.T) {
		cases := []struct {
			name string
			crop CropConfig
			want string
		}{
			{"none", CropConfig{}, ""},
			{"left", CropConfig{Left: 100}, "crop=in_w-100:in_h-0:100:0"},
			{"right", CropConfig{Right: 200}, "crop=in_w-200:in_h-0:0:0"},
			{"top", CropConfig{Top: 50}, "crop=in_w-0:in_h-50:0:50"},
			{"bottom", CropConfig{Bottom: 80}, "crop=in_w-0:in_h-80:0:0"},
			{"left_and_right", CropConfig{Left: 100, Right: 200}, "crop=in_w-300:in_h-0:100:0"},
			{"top_and_bottom", CropConfig{Top: 50, Bottom: 80}, "crop=in_w-0:in_h-130:0:50"},
			{"all_four", CropConfig{Left: 100, Right: 200, Top: 50, Bottom: 80}, "crop=in_w-300:in_h-130:100:50"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := tc.crop.FFmpegFilter(); got != tc.want {
					t.Errorf("FFmpegFilter() = %q, want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("enabled_reports_any_edge", func(t *testing.T) {
		for _, c := range []CropConfig{{Left: 1}, {Right: 1}, {Top: 1}, {Bottom: 1}} {
			if !c.Enabled() {
				t.Errorf("%+v should be enabled", c)
			}
		}
		if (CropConfig{}).Enabled() {
			t.Error("the zero value should be disabled")
		}
	})

	t.Run("loaded_from_the_environment", func(t *testing.T) {
		cfg := mustLoad(t, withEnv(map[string]string{
			EnvCaptureCropLeft:   "100",
			EnvCaptureCropRight:  "200",
			EnvCaptureCropTop:    "50",
			EnvCaptureCropBottom: "80",
		}))
		want := CropConfig{Left: 100, Right: 200, Top: 50, Bottom: 80}
		if cfg.Capture.Crop != want {
			t.Errorf("crop = %+v, want %+v", cfg.Capture.Crop, want)
		}
	})

	t.Run("settable_by_flag", func(t *testing.T) {
		cfg := mustLoad(t, minimal(), "--capture-crop-left=25", "--capture-crop-bottom=75")
		if cfg.Capture.Crop.Left != 25 || cfg.Capture.Crop.Bottom != 75 {
			t.Errorf("crop = %+v", cfg.Capture.Crop)
		}
	})

	t.Run("negative_values_are_rejected", func(t *testing.T) {
		for _, env := range []string{EnvCaptureCropLeft, EnvCaptureCropRight, EnvCaptureCropTop, EnvCaptureCropBottom} {
			t.Run(env, func(t *testing.T) {
				err := loadErr(t, withEnv(map[string]string{env: "-10"}))
				if !strings.Contains(err.Error(), env) {
					t.Errorf("err = %v, want it to mention %s", err, env)
				}
			})
		}
	})

	t.Run("non_numeric_values_are_rejected", func(t *testing.T) {
		err := loadErr(t, withEnv(map[string]string{EnvCaptureCropLeft: "a bit"}))
		if !strings.Contains(err.Error(), EnvCaptureCropLeft) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestFlagNameMapping(t *testing.T) {
	cases := map[string]string{
		EnvRTSPHost:               "rtsp-host",
		EnvCaptureJPEGQuality:     "capture-jpeg-quality",
		EnvSFTPInsecureIgnoreHost: "sftp-insecure-ignore-host-key",
		EnvHTTPReadyMaxStaleness:  "http-ready-max-staleness",
	}
	for env, want := range cases {
		if got := flagName(env); got != want {
			t.Errorf("flagName(%s) = %q, want %q", env, got, want)
		}
	}
}

func TestEverySpecHasAUniqueFlag(t *testing.T) {
	seen := map[string]string{}
	for _, s := range specs() {
		name := flagName(s.env)
		if prev, dup := seen[name]; dup {
			t.Errorf("flag %q is registered for both %s and %s", name, prev, s.env)
		}
		seen[name] = s.env
		if s.usage == "" {
			t.Errorf("%s has no usage text", s.env)
		}
	}
	if got := len(specs()); got != 40 {
		t.Errorf("specs() has %d entries; update the test if a setting was added intentionally", got)
	}
}

func TestLoadUsesRealEnvironment(t *testing.T) {
	// Load() is the exported entry point; verify it reads the process environment.
	t.Setenv(EnvRTSPHost, "cam-from-os-env")
	t.Setenv(EnvSFTPHost, "sftp.lan")
	t.Setenv(EnvSFTPUsername, "up")
	t.Setenv(EnvSFTPPassword, "sftp-password")
	t.Setenv(EnvSFTPInsecureIgnoreHost, "true")

	cfg, err := Load(os.LookupEnv, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RTSP.Host != "cam-from-os-env" {
		t.Errorf("host = %q", cfg.RTSP.Host)
	}
}

func TestLoadToWritesUsageToTheGivenWriter(t *testing.T) {
	var buf strings.Builder
	if _, err := LoadTo(MapLookup(minimal()), []string{"--help"}, &buf); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
	for _, want := range []string{"-rtsp-host", "env RTSP_HOST", "-capture-interval"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("usage output is missing %q", want)
		}
	}
}
