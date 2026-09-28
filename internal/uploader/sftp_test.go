package uploader

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/config"
)

const (
	testUser     = "uploader"
	testPassword = "sftp-secret-password"
	imageBody    = "\xff\xd8\xff\xe0 pretend this is a JPEG \xff\xd9"
)

// baseConfig returns a valid configuration pointing at ts.
func baseConfig(ts *testServer) config.SFTPConfig {
	host, port, _ := net.SplitHostPort(ts.Addr)
	p := 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	return config.SFTPConfig{
		Host:                  host,
		Port:                  p,
		Username:              testUser,
		Password:              testPassword,
		Timeout:               10 * time.Second,
		Mkdir:                 true,
		Atomic:                true,
		FileMode:              0o644,
		InsecureIgnoreHostKey: true,
		RetryAttempts:         1,
	}
}

// localFile writes body to a temporary file and returns its path.
func localFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "image.jpg")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the local file: %v", err)
	}
	return p
}

func newUploader(t *testing.T, cfg config.SFTPConfig, remote string) *SFTPUploader {
	t.Helper()
	u, err := New(cfg, remote, config.NewRedactor(testPassword), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return u
}

func TestUploadSuccess(t *testing.T) {
	t.Run("password_auth_uploads_the_file", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		remote := path.Join(ts.Root, "image.jpg")
		u := newUploader(t, baseConfig(ts), remote)

		if err := u.Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)
		assertOnlyFile(t, ts.Root, "image.jpg")
	})

	t.Run("publickey_auth_uploads_the_file", func(t *testing.T) {
		keyPath, pub := writeKeyPair(t)
		ts := startTestServer(t, testServerOpts{User: testUser, AuthorizedKey: pub})

		cfg := baseConfig(ts)
		cfg.Password = ""
		cfg.PrivateKeyPath = keyPath
		remote := path.Join(ts.Root, "image.jpg")

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)
	})

	t.Run("key_is_preferred_when_the_password_is_wrong", func(t *testing.T) {
		keyPath, pub := writeKeyPair(t)
		ts := startTestServer(t, testServerOpts{User: testUser, AuthorizedKey: pub})

		cfg := baseConfig(ts)
		cfg.Password = "definitely-not-the-password"
		cfg.PrivateKeyPath = keyPath
		remote := path.Join(ts.Root, "image.jpg")

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)

		// Prove the key was actually used rather than the password silently working.
		var sawPublicKey bool
		for _, attempt := range ts.authAttempts() {
			if strings.HasPrefix(attempt, "publickey:") {
				sawPublicKey = true
			}
		}
		if !sawPublicKey {
			t.Errorf("the server saw no publickey attempt: %v", ts.authAttempts())
		}
	})

	t.Run("mkdir_creates_nested_remote_directories", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		remote := path.Join(ts.Root, "a", "b", "c", "image.jpg")

		if err := newUploader(t, baseConfig(ts), remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)
	})

	t.Run("non_atomic_upload_writes_directly", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		cfg := baseConfig(ts)
		cfg.Atomic = false
		remote := path.Join(ts.Root, "image.jpg")

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)
		assertOnlyFile(t, ts.Root, "image.jpg")
	})

	t.Run("fully_replaces_a_longer_existing_file", func(t *testing.T) {
		// The classic non-truncating-upload bug: old trailing bytes survive.
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		remote := path.Join(ts.Root, "image.jpg")
		stale := strings.Repeat("STALE", 200)
		if err := os.WriteFile(remote, []byte(stale), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := newUploader(t, baseConfig(ts), remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)
	})

	t.Run("non_atomic_upload_replaces_a_longer_existing_file", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		cfg := baseConfig(ts)
		cfg.Atomic = false
		remote := path.Join(ts.Root, "image.jpg")
		if err := os.WriteFile(remote, []byte(strings.Repeat("STALE", 200)), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)
	})

	t.Run("file_mode_is_applied", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		cfg := baseConfig(ts)
		cfg.FileMode = 0o600
		remote := path.Join(ts.Root, "image.jpg")

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		info, err := os.Stat(remote)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("mode = %o, want 600", got)
		}
	})

	t.Run("custom_remote_filename", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		remote := path.Join(ts.Root, "snapshot.jpg")

		if err := newUploader(t, baseConfig(ts), remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertOnlyFile(t, ts.Root, "snapshot.jpg")
	})

	t.Run("repeated_uploads_succeed", func(t *testing.T) {
		// A long-running process reconnects on every cycle; prove that works.
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		remote := path.Join(ts.Root, "image.jpg")
		u := newUploader(t, baseConfig(ts), remote)
		local := localFile(t, imageBody)

		for i := range 3 {
			if err := u.Upload(t.Context(), local); err != nil {
				t.Fatalf("Upload %d: %v", i+1, err)
			}
		}
		assertRemote(t, remote, imageBody)
		assertOnlyFile(t, ts.Root, "image.jpg")
	})

	t.Run("empty_file_uploads_cleanly", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		remote := path.Join(ts.Root, "image.jpg")
		if err := newUploader(t, baseConfig(ts), remote).Upload(t.Context(), localFile(t, "")); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, "")
	})

	t.Run("RemotePath_reports_the_destination", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		remote := path.Join(ts.Root, "image.jpg")
		if got := newUploader(t, baseConfig(ts), remote).RemotePath(); got != remote {
			t.Errorf("RemotePath() = %q, want %q", got, remote)
		}
	})
}

func TestUploadHostKeyVerification(t *testing.T) {
	t.Run("known_hosts_accepts_a_matching_key", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		cfg := baseConfig(ts)
		cfg.InsecureIgnoreHostKey = false
		cfg.KnownHostsPath = writeFile(t, "known_hosts", ts.HostKeyLine+"\n")
		remote := path.Join(ts.Root, "image.jpg")

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)
	})

	t.Run("known_hosts_rejects_a_different_key", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		other := generateSigner(t)

		cfg := baseConfig(ts)
		cfg.InsecureIgnoreHostKey = false
		cfg.KnownHostsPath = writeFile(t, "known_hosts", knownHostsLine(t, ts.Addr, other.PublicKey())+"\n")
		remote := path.Join(ts.Root, "image.jpg")

		err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody))
		if err == nil {
			t.Fatal("expected the connection to be rejected")
		}
		if _, statErr := os.Stat(remote); statErr == nil {
			t.Error("a rejected connection must not upload anything")
		}
	})

	t.Run("known_hosts_with_only_one_key_type_still_connects", func(t *testing.T) {
		// Regression test: "ssh-keyscan -t ed25519" is the recommended way to pin
		// a host, but most servers also offer an RSA key. Unless the client
		// restricts negotiation to the algorithms known_hosts records, the server
		// may present the RSA key and verification fails with a misleading
		// "key mismatch".
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword, AlsoOfferRSA: true})
		if ts.RSAHostKey == nil {
			t.Fatal("test precondition: the server should also offer an RSA key")
		}

		cfg := baseConfig(ts)
		cfg.InsecureIgnoreHostKey = false
		cfg.KnownHostsPath = writeFile(t, "known_hosts", ts.HostKeyLine+"\n")
		remote := path.Join(ts.Root, "image.jpg")

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)
	})

	t.Run("known_hosts_with_only_the_rsa_key_still_connects", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword, AlsoOfferRSA: true})

		cfg := baseConfig(ts)
		cfg.InsecureIgnoreHostKey = false
		cfg.KnownHostsPath = writeFile(t, "known_hosts", knownHostsLine(t, ts.Addr, ts.RSAHostKey)+"\n")
		remote := path.Join(ts.Root, "image.jpg")

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)
	})

	t.Run("known_hosts_with_both_key_types_connects", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword, AlsoOfferRSA: true})

		cfg := baseConfig(ts)
		cfg.InsecureIgnoreHostKey = false
		cfg.KnownHostsPath = writeFile(t, "known_hosts",
			ts.HostKeyLine+"\n"+knownHostsLine(t, ts.Addr, ts.RSAHostKey)+"\n")
		remote := path.Join(ts.Root, "image.jpg")

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)
	})

	t.Run("fingerprint_pin_accepts", func(t *testing.T) {
		for _, withPrefix := range []bool{true, false} {
			name := "without_prefix"
			if withPrefix {
				name = "with_prefix"
			}
			t.Run(name, func(t *testing.T) {
				ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
				fp := gossh.FingerprintSHA256(ts.HostKey)
				if !withPrefix {
					fp = strings.TrimPrefix(fp, fingerprintPrefix)
				}

				cfg := baseConfig(ts)
				cfg.InsecureIgnoreHostKey = false
				cfg.HostKeyFingerprint = fp
				remote := path.Join(ts.Root, "image.jpg")

				if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
					t.Fatalf("Upload: %v", err)
				}
				assertRemote(t, remote, imageBody)
			})
		}
	})

	t.Run("pinning_only_the_unnegotiated_key_type_fails", func(t *testing.T) {
		// A server publishing both RSA and ed25519 negotiates only one of them.
		// Pinning the other is a valid fingerprint for the host but still cannot
		// match -- the trap this multi-value support exists to solve.
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword, AlsoOfferRSA: true})
		cfg := baseConfig(ts)
		cfg.InsecureIgnoreHostKey = false
		remote := path.Join(ts.Root, "image.jpg")

		// Determine which key the client actually negotiates.
		var presented gossh.PublicKey
		probe := baseConfig(ts)
		probe.InsecureIgnoreHostKey = true
		u := newUploader(t, probe, remote)
		u.hostKey = func(_ string, _ net.Addr, key gossh.PublicKey) error {
			presented = key
			return nil
		}
		if err := u.Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("probe upload: %v", err)
		}
		if presented == nil {
			t.Fatal("no host key was presented")
		}

		other := ts.HostKey
		if gossh.FingerprintSHA256(presented) == gossh.FingerprintSHA256(ts.HostKey) {
			other = ts.RSAHostKey
		}
		cfg.HostKeyFingerprint = gossh.FingerprintSHA256(other)

		err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody))
		if err == nil {
			t.Fatal("expected a mismatch when pinning the key type that is not negotiated")
		}
		if !strings.Contains(err.Error(), "ssh-keyscan") {
			t.Errorf("err = %v, want it to explain how to pin every key type", err)
		}
	})

	t.Run("pinning_every_published_key_type_succeeds", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword, AlsoOfferRSA: true})
		cfg := baseConfig(ts)
		cfg.InsecureIgnoreHostKey = false
		cfg.HostKeyFingerprint = gossh.FingerprintSHA256(ts.HostKey) + " " + gossh.FingerprintSHA256(ts.RSAHostKey)
		remote := path.Join(ts.Root, "image.jpg")

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		assertRemote(t, remote, imageBody)
	})

	t.Run("a_comma_separated_list_works_too", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword, AlsoOfferRSA: true})
		cfg := baseConfig(ts)
		cfg.InsecureIgnoreHostKey = false
		cfg.HostKeyFingerprint = gossh.FingerprintSHA256(ts.HostKey) + "," + gossh.FingerprintSHA256(ts.RSAHostKey)
		remote := path.Join(ts.Root, "image.jpg")

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err != nil {
			t.Fatalf("Upload: %v", err)
		}
	})

	t.Run("a_list_of_wrong_fingerprints_still_fails", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword, AlsoOfferRSA: true})
		cfg := baseConfig(ts)
		cfg.InsecureIgnoreHostKey = false
		cfg.HostKeyFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA,SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
		remote := path.Join(ts.Root, "image.jpg")

		if err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody)); err == nil {
			t.Fatal("a list of wrong fingerprints must still be rejected")
		}
		if _, statErr := os.Stat(remote); statErr == nil {
			t.Error("nothing may be uploaded when verification fails")
		}
	})

	t.Run("fingerprint_pin_rejects_a_mismatch", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		cfg := baseConfig(ts)
		cfg.InsecureIgnoreHostKey = false
		cfg.HostKeyFingerprint = "SHA256:ZZZZnotTheRightFingerprintZZZZ"
		remote := path.Join(ts.Root, "image.jpg")

		err := newUploader(t, cfg, remote).Upload(t.Context(), localFile(t, imageBody))
		if err == nil {
			t.Fatal("expected a host key mismatch")
		}
		if !strings.Contains(err.Error(), "host key mismatch") {
			t.Errorf("err = %v, want a host key mismatch", err)
		}
		if !strings.Contains(err.Error(), gossh.FingerprintSHA256(ts.HostKey)) {
			t.Errorf("err = %v, want it to name the presented fingerprint", err)
		}
		if !strings.Contains(err.Error(), "ZZZZnotTheRightFingerprintZZZZ") {
			t.Errorf("err = %v, want it to name the expected fingerprint", err)
		}
	})

	t.Run("insecure_mode_warns_at_construction", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		cfg := config.SFTPConfig{
			Host: "h", Port: 22, Username: "u", Password: "pw",
			Timeout: time.Second, FileMode: 0o644, InsecureIgnoreHostKey: true,
		}
		if _, err := New(cfg, "/upload/image.jpg", nil, logger); err != nil {
			t.Fatalf("New: %v", err)
		}
		if !strings.Contains(buf.String(), "host key verification is disabled") {
			t.Errorf("expected a warning, got: %s", buf.String())
		}
	})
}

func TestUploadFailures(t *testing.T) {
	t.Run("auth_failure_does_not_leak_the_password", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		cfg := baseConfig(ts)
		cfg.Password = "wrong-" + testPassword
		u, err := New(cfg, path.Join(ts.Root, "image.jpg"), config.FromConfig(&config.Config{SFTP: cfg}), slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		uploadErr := u.Upload(t.Context(), localFile(t, imageBody))
		if uploadErr == nil {
			t.Fatal("expected authentication to fail")
		}
		if strings.Contains(uploadErr.Error(), cfg.Password) {
			t.Fatalf("the password leaked into the error: %v", uploadErr)
		}
		if !strings.Contains(uploadErr.Error(), ts.Addr) {
			t.Errorf("err = %v, want it to name the server", uploadErr)
		}
	})

	t.Run("server_rejects_all_auth", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword, RejectAuth: true})
		err := newUploader(t, baseConfig(ts), path.Join(ts.Root, "image.jpg")).Upload(t.Context(), localFile(t, imageBody))
		if err == nil || !strings.Contains(err.Error(), "handshake") {
			t.Errorf("err = %v, want a handshake failure", err)
		}
	})

	t.Run("unreachable_host", func(t *testing.T) {
		// Bind and immediately close to obtain a port nothing listens on.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().(*net.TCPAddr)
		_ = ln.Close()

		cfg := config.SFTPConfig{
			Host: "127.0.0.1", Port: addr.Port, Username: testUser, Password: testPassword,
			Timeout: 2 * time.Second, FileMode: 0o644, InsecureIgnoreHostKey: true,
		}
		start := time.Now()
		err = newUploader(t, cfg, "/tmp/image.jpg").Upload(t.Context(), localFile(t, imageBody))
		if err == nil || !strings.Contains(err.Error(), "dialing") {
			t.Errorf("err = %v, want a dial failure", err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("took %v; the dial timeout was not honoured", elapsed)
		}
	})

	t.Run("local_file_missing_skips_the_network", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		u := newUploader(t, baseConfig(ts), path.Join(ts.Root, "image.jpg"))

		var dials atomic.Int32
		inner := u.dialContext
		u.dialContext = func(ctx context.Context, n, a string) (net.Conn, error) {
			dials.Add(1)
			return inner(ctx, n, a)
		}

		err := u.Upload(t.Context(), filepath.Join(t.TempDir(), "does-not-exist.jpg"))
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v, want it to wrap fs.ErrNotExist", err)
		}
		if n := dials.Load(); n != 0 {
			t.Errorf("dialled %d times; a missing local file must fail before connecting", n)
		}
	})

	t.Run("already_cancelled_context", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := newUploader(t, baseConfig(ts), path.Join(ts.Root, "image.jpg")).Upload(ctx, localFile(t, imageBody))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("cancellation_mid_transfer", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		remote := path.Join(ts.Root, "image.jpg")
		u := newUploader(t, baseConfig(ts), remote)

		ctx, cancel := context.WithCancel(t.Context())
		// Slow the wire down so the transfer is still running when we cancel.
		inner := u.dialContext
		u.dialContext = func(c context.Context, n, a string) (net.Conn, error) {
			conn, err := inner(c, n, a)
			if err != nil {
				return nil, err
			}
			return &slowConn{Conn: conn, delay: 2 * time.Millisecond}, nil
		}

		big := filepath.Join(t.TempDir(), "big.jpg")
		if err := os.WriteFile(big, bytes.Repeat([]byte("x"), 4<<20), 0o600); err != nil {
			t.Fatal(err)
		}
		go func() {
			time.Sleep(80 * time.Millisecond)
			cancel()
		}()

		if err := u.Upload(ctx, big); err == nil {
			t.Fatal("expected the cancelled transfer to fail")
		}
		if _, err := os.Stat(remote); err == nil {
			t.Error("a cancelled transfer must not publish the remote file")
		}
	})

	t.Run("handshake_timeout", func(t *testing.T) {
		// A listener that accepts connections but never speaks SSH.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				t.Cleanup(func() { _ = c.Close() })
			}
		}()

		addr := ln.Addr().(*net.TCPAddr)
		cfg := config.SFTPConfig{
			Host: "127.0.0.1", Port: addr.Port, Username: testUser, Password: testPassword,
			Timeout: 200 * time.Millisecond, FileMode: 0o644, InsecureIgnoreHostKey: true,
		}

		start := time.Now()
		err = newUploader(t, cfg, "/tmp/image.jpg").Upload(t.Context(), localFile(t, imageBody))
		if err == nil {
			t.Fatal("expected a handshake timeout")
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("took %v; the timeout was not honoured", elapsed)
		}
	})

	t.Run("remote_directory_not_writable", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		locked := filepath.Join(ts.Root, "locked")
		if err := os.Mkdir(locked, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

		cfg := baseConfig(ts)
		cfg.Mkdir = false
		u, err := New(cfg, path.Join(locked, "image.jpg"), config.FromConfig(&config.Config{SFTP: cfg}), slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		uploadErr := u.Upload(t.Context(), localFile(t, imageBody))
		if uploadErr == nil {
			t.Fatal("expected the upload to fail")
		}
		if strings.Contains(uploadErr.Error(), testPassword) {
			t.Errorf("the password leaked into the error: %v", uploadErr)
		}
	})

	t.Run("part_file_is_removed_when_publishing_fails", func(t *testing.T) {
		ts := startTestServer(t, testServerOpts{User: testUser, Password: testPassword})
		// A directory at the destination makes the final rename fail.
		remote := path.Join(ts.Root, "image.jpg")
		if err := os.Mkdir(remote, 0o755); err != nil {
			t.Fatal(err)
		}

		u := newUploader(t, baseConfig(ts), remote)
		u.randSuffix = func() string { return "fixed" }

		if err := u.Upload(t.Context(), localFile(t, imageBody)); err == nil {
			t.Fatal("expected publishing to fail")
		}
		if _, err := os.Stat(remote + ".fixed" + partSuffix); err == nil {
			t.Error("the partial upload was not cleaned up")
		}
	})
}

func TestNew(t *testing.T) {
	valid := config.SFTPConfig{
		Host: "h", Port: 22, Username: "u", Password: "pw",
		Timeout: time.Second, FileMode: 0o644, InsecureIgnoreHostKey: true,
	}

	t.Run("rejects_no_credentials", func(t *testing.T) {
		cfg := valid
		cfg.Password = ""
		_, err := New(cfg, "/upload/image.jpg", nil, nil)
		if err == nil || !strings.Contains(err.Error(), config.EnvSFTPPassword) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("rejects_no_host_key_strategy", func(t *testing.T) {
		cfg := valid
		cfg.InsecureIgnoreHostKey = false
		_, err := New(cfg, "/upload/image.jpg", nil, nil)
		if err == nil || !strings.Contains(err.Error(), config.EnvSFTPKnownHostsPath) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("rejects_two_host_key_strategies", func(t *testing.T) {
		cfg := valid
		cfg.HostKeyFingerprint = "SHA256:x"
		_, err := New(cfg, "/upload/image.jpg", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("rejects_a_missing_known_hosts_file", func(t *testing.T) {
		cfg := valid
		cfg.InsecureIgnoreHostKey = false
		cfg.KnownHostsPath = filepath.Join(t.TempDir(), "nope")
		_, err := New(cfg, "/upload/image.jpg", nil, nil)
		if err == nil || !strings.Contains(err.Error(), cfg.KnownHostsPath) {
			t.Errorf("err = %v, want it to name the file", err)
		}
	})

	t.Run("rejects_a_missing_private_key", func(t *testing.T) {
		cfg := valid
		cfg.PrivateKeyPath = filepath.Join(t.TempDir(), "nope")
		_, err := New(cfg, "/upload/image.jpg", nil, nil)
		if err == nil || !strings.Contains(err.Error(), config.EnvSFTPPrivateKeyPath) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("rejects_an_unparsable_private_key", func(t *testing.T) {
		cfg := valid
		cfg.PrivateKeyPath = writeFile(t, "id", "not a key at all")
		_, err := New(cfg, "/upload/image.jpg", nil, nil)
		if err == nil || !strings.Contains(err.Error(), cfg.PrivateKeyPath) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("accepts_a_passphrase_protected_key", func(t *testing.T) {
		keyPath := writePassphraseKey(t, "correct horse")
		cfg := valid
		cfg.PrivateKeyPath = keyPath
		cfg.PrivateKeyPassphrase = "correct horse"
		if _, err := New(cfg, "/upload/image.jpg", nil, nil); err != nil {
			t.Errorf("New: %v", err)
		}
	})

	t.Run("rejects_a_wrong_key_passphrase", func(t *testing.T) {
		keyPath := writePassphraseKey(t, "correct horse")
		cfg := valid
		cfg.PrivateKeyPath = keyPath
		cfg.PrivateKeyPassphrase = "wrong horse"
		if _, err := New(cfg, "/upload/image.jpg", nil, nil); err == nil {
			t.Error("expected the passphrase to be rejected")
		}
	})

	t.Run("rejects_an_empty_remote_path", func(t *testing.T) {
		if _, err := New(valid, "   ", nil, nil); err == nil {
			t.Error("expected an error")
		}
	})
}

func TestIsUnsupportedExtension(t *testing.T) {
	if isUnsupportedExtension(errors.New("permission denied")) {
		t.Error("an unrelated error must not be treated as unsupported")
	}
	if !isUnsupportedExtension(errors.New("SSH_FX_OP_UNSUPPORTED: unsupported request")) {
		t.Error("an unsupported-request error was not recognised")
	}
}

// --- helpers ---

func assertRemote(t *testing.T, remote, want string) {
	t.Helper()
	got, err := os.ReadFile(remote) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("reading the uploaded file: %v", err)
	}
	if string(got) != want {
		t.Errorf("remote content = %q (%d bytes), want %q (%d bytes)", got, len(got), want, len(want))
	}
}

// assertOnlyFile checks that dir contains exactly the named entries, proving no
// .part files were left behind.
func assertOnlyFile(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the remote directory: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if len(got) != len(want) {
		t.Fatalf("remote directory contains %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return p
}

// writeKeyPair generates an ed25519 key, writes the private half in OpenSSH
// format, and returns the path plus the public key.
func writeKeyPair(t *testing.T) (string, gossh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pem, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, "id_ed25519", string(encodePEM(t, pem))), sshPub
}

func writePassphraseKey(t *testing.T, passphrase string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pem, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, "id_ed25519", string(encodePEM(t, pem)))
}

// slowConn delays every write so that a transfer can be cancelled mid-flight.
type slowConn struct {
	net.Conn
	delay time.Duration
}

func (c *slowConn) Write(p []byte) (int, error) {
	time.Sleep(c.delay)
	return c.Conn.Write(p)
}

var _ io.ReadWriteCloser = (*slowConn)(nil)

// encodePEM renders a *pem.Block as bytes.
func encodePEM(t *testing.T, block any) []byte {
	t.Helper()
	b, ok := block.(*pem.Block)
	if !ok {
		t.Fatalf("unexpected type %T", block)
	}
	return pem.EncodeToMemory(b)
}

func TestKnownHostAlgorithms(t *testing.T) {
	ed := generateSigner(t)
	rsaSigner := generateRSASigner(t)
	const addr = "127.0.0.1:2222"

	callbackFor := func(t *testing.T, lines ...string) gossh.HostKeyCallback {
		t.Helper()
		cb, err := knownhosts.New(writeFile(t, "known_hosts", strings.Join(lines, "\n")+"\n"))
		if err != nil {
			t.Fatalf("knownhosts.New: %v", err)
		}
		return cb
	}

	t.Run("ed25519_only", func(t *testing.T) {
		got := knownHostAlgorithms(callbackFor(t, knownHostsLine(t, addr, ed.PublicKey())), addr)
		want := []string{gossh.KeyAlgoED25519}
		if !slices.Equal(got, want) {
			t.Errorf("algorithms = %v, want %v", got, want)
		}
	})

	t.Run("rsa_expands_to_the_sha2_variants", func(t *testing.T) {
		got := knownHostAlgorithms(callbackFor(t, knownHostsLine(t, addr, rsaSigner.PublicKey())), addr)
		want := []string{gossh.KeyAlgoRSASHA512, gossh.KeyAlgoRSASHA256, gossh.KeyAlgoRSA}
		if !slices.Equal(got, want) {
			t.Errorf("algorithms = %v, want %v", got, want)
		}
	})

	t.Run("both_key_types", func(t *testing.T) {
		cb := callbackFor(t,
			knownHostsLine(t, addr, rsaSigner.PublicKey()),
			knownHostsLine(t, addr, ed.PublicKey()),
		)
		got := knownHostAlgorithms(cb, addr)
		if len(got) != 4 {
			t.Fatalf("algorithms = %v, want 4 entries", got)
		}
		if !slices.Contains(got, gossh.KeyAlgoED25519) || !slices.Contains(got, gossh.KeyAlgoRSASHA512) {
			t.Errorf("algorithms = %v, want both key families", got)
		}
	})

	t.Run("unknown_host_imposes_no_restriction", func(t *testing.T) {
		cb := callbackFor(t, knownHostsLine(t, "other.example:22", ed.PublicKey()))
		if got := knownHostAlgorithms(cb, addr); got != nil {
			t.Errorf("algorithms = %v, want nil so negotiation stays unrestricted", got)
		}
	})

	t.Run("non_knownhosts_callbacks_impose_no_restriction", func(t *testing.T) {
		if got := knownHostAlgorithms(gossh.InsecureIgnoreHostKey(), addr); got != nil {
			t.Errorf("insecure callback: algorithms = %v, want nil", got)
		}
		if got := knownHostAlgorithms(fingerprintCallback("SHA256:whatever"), addr); got != nil {
			t.Errorf("fingerprint callback: algorithms = %v, want nil", got)
		}
		if got := knownHostAlgorithms(nil, addr); got != nil {
			t.Errorf("nil callback: algorithms = %v, want nil", got)
		}
	})
}

func TestProbeKey(t *testing.T) {
	var k probeKey
	if k.Type() == gossh.KeyAlgoED25519 || k.Type() == gossh.KeyAlgoRSA {
		t.Errorf("the probe key must not claim a real algorithm, got %q", k.Type())
	}
	if len(k.Marshal()) == 0 {
		t.Error("Marshal must return bytes so knownhosts can compare it")
	}
	if err := k.Verify(nil, nil); err == nil {
		t.Error("Verify must fail")
	}
}
