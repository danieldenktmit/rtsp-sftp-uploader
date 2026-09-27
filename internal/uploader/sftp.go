package uploader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/danieldenktmit/rtsp-sftp-uploader/internal/config"
)

// partSuffix marks an upload that has not yet been renamed into place.
const partSuffix = ".part"

// fingerprintPrefix is the prefix OpenSSH prints for SHA256 fingerprints.
const fingerprintPrefix = "SHA256:"

// SFTPUploader uploads over SSH/SFTP.
//
// A fresh connection is opened per upload. For a once-a-minute schedule the cost
// is negligible and it keeps a long-running process immune to server restarts,
// idle timeouts and silently dropped TCP sessions.
type SFTPUploader struct {
	cfg      config.SFTPConfig
	remote   string
	auth     []ssh.AuthMethod
	hostKey  ssh.HostKeyCallback
	hostAlgs []string // restricts negotiation to key types known_hosts records
	logger   *slog.Logger
	redactor *config.Redactor

	// seams for tests
	dialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	randSuffix  func() string
}

// New builds an uploader for remotePath. Credentials and the host-key strategy
// are resolved eagerly so that misconfiguration is reported at startup.
func New(cfg config.SFTPConfig, remotePath string, r *config.Redactor, logger *slog.Logger) (*SFTPUploader, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	hostKey, err := hostKeyCallback(cfg, logger)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(remotePath) == "" {
		return nil, errors.New("sftp: the remote path must not be empty")
	}

	return &SFTPUploader{
		cfg:         cfg,
		remote:      remotePath,
		auth:        auth,
		hostKey:     hostKey,
		hostAlgs:    knownHostAlgorithms(hostKey, cfg.Addr()),
		logger:      logger,
		redactor:    r,
		dialContext: (&net.Dialer{Timeout: cfg.Timeout}).DialContext,
		randSuffix:  randomSuffix,
	}, nil
}

// RemotePath reports the destination this uploader writes to.
func (u *SFTPUploader) RemotePath() string { return u.remote }

func authMethods(cfg config.SFTPConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if cfg.PrivateKeyPath != "" {
		pem, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", config.EnvSFTPPrivateKeyPath, err)
		}
		var signer ssh.Signer
		if cfg.PrivateKeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(cfg.PrivateKeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", config.EnvSFTPPrivateKeyPath, cfg.PrivateKeyPath, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("sftp: no credential configured: set %s or %s",
			config.EnvSFTPPassword, config.EnvSFTPPrivateKeyPath)
	}
	return methods, nil
}

func hostKeyCallback(cfg config.SFTPConfig, logger *slog.Logger) (ssh.HostKeyCallback, error) {
	strategies := 0
	if cfg.KnownHostsPath != "" {
		strategies++
	}
	if cfg.HostKeyFingerprint != "" {
		strategies++
	}
	if cfg.InsecureIgnoreHostKey {
		strategies++
	}

	switch {
	case strategies == 0:
		return nil, fmt.Errorf("sftp: configure host key verification: set %s or %s, or explicitly set %s=true",
			config.EnvSFTPKnownHostsPath, config.EnvSFTPHostKeyFingerprint, config.EnvSFTPInsecureIgnoreHost)
	case strategies > 1:
		return nil, fmt.Errorf("sftp: %s, %s and %s are mutually exclusive; choose one",
			config.EnvSFTPKnownHostsPath, config.EnvSFTPHostKeyFingerprint, config.EnvSFTPInsecureIgnoreHost)
	case cfg.KnownHostsPath != "":
		cb, err := knownhosts.New(cfg.KnownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", config.EnvSFTPKnownHostsPath, cfg.KnownHostsPath, err)
		}
		return cb, nil
	case cfg.HostKeyFingerprint != "":
		return fingerprintCallback(cfg.HostKeyFingerprint), nil
	default:
		logger.Warn("SFTP host key verification is disabled; the connection is vulnerable to interception",
			"setting", config.EnvSFTPInsecureIgnoreHost)
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // explicitly opted into by configuration
	}
}

// fingerprintCallback pins a single SHA256 host key fingerprint. The configured
// value is accepted with or without the "SHA256:" prefix.
func fingerprintCallback(want string) ssh.HostKeyCallback {
	want = strings.TrimSpace(want)
	normalizedWant := strings.TrimPrefix(want, fingerprintPrefix)
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		if strings.TrimPrefix(got, fingerprintPrefix) == normalizedWant {
			return nil
		}
		return fmt.Errorf("sftp: host key mismatch for %s: server presented %s, expected %s%s",
			hostname, got, fingerprintPrefix, normalizedWant)
	}
}

func randomSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail in practice; fall back to a time-based value.
		return fmt.Sprintf("%d", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b)
}

// Upload publishes localPath to the configured remote path. With Atomic enabled
// the bytes land on a temporary name first and are renamed into place, so a
// consumer polling the remote file never observes a partial image.
func (u *SFTPUploader) Upload(ctx context.Context, localPath string) error {
	ctx, cancel := context.WithTimeout(ctx, u.cfg.Timeout)
	defer cancel()

	// Fail before touching the network if there is nothing to send.
	local, err := os.Open(localPath) //nolint:gosec // path comes from this process's own configuration
	if err != nil {
		return fmt.Errorf("sftp: opening %s: %w", localPath, err)
	}
	defer func() { _ = local.Close() }()

	info, err := local.Stat()
	if err != nil {
		return fmt.Errorf("sftp: stat %s: %w", localPath, err)
	}

	started := time.Now()
	client, closeClient, err := u.connect(ctx)
	if err != nil {
		return err
	}
	defer closeClient()

	if u.cfg.Mkdir {
		if dir := path.Dir(u.remote); dir != "" && dir != "." {
			if err := client.MkdirAll(dir); err != nil {
				return u.redactor.Error(fmt.Errorf("sftp: creating remote directory %s: %w", dir, err))
			}
		}
	}

	target := u.remote
	if u.cfg.Atomic {
		target = fmt.Sprintf("%s.%s%s", u.remote, u.randSuffix(), partSuffix)
	}

	written, err := u.writeRemote(client, target, local, info.Size())
	if err != nil {
		u.cleanup(client, target)
		return u.redactor.Error(err)
	}

	if u.cfg.Atomic {
		if err := u.publish(client, target); err != nil {
			u.cleanup(client, target)
			return u.redactor.Error(err)
		}
	}

	u.logger.Info("image uploaded",
		"host", u.cfg.Addr(),
		"remote", u.remote,
		"bytes", written,
		"duration", time.Since(started),
	)
	return nil
}

// connect dials the server and returns an SFTP client plus a cleanup function.
func (u *SFTPUploader) connect(ctx context.Context) (*sftp.Client, func(), error) {
	addr := u.cfg.Addr()

	conn, err := u.dialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("sftp: dialing %s: %w", addr, err)
	}

	// ssh.ClientConfig.Timeout only applies to ssh.Dial, which we do not use, and
	// ssh.NewClientConn ignores the context entirely. Without a socket deadline a
	// server that accepts TCP but never speaks SSH would hang the handshake
	// forever. The deadline also bounds the transfer that follows.
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return nil, nil, fmt.Errorf("sftp: setting the connection deadline for %s: %w", addr, err)
		}
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              u.cfg.Username,
		Auth:              u.auth,
		HostKeyCallback:   u.hostKey,
		HostKeyAlgorithms: u.hostAlgs,
		Timeout:           u.cfg.Timeout,
	})
	if err != nil {
		_ = conn.Close()
		return nil, nil, u.redactor.Error(fmt.Errorf("sftp: ssh handshake with %s as %q failed: %w", addr, u.cfg.Username, err))
	}
	sshClient := ssh.NewClient(sshConn, chans, reqs)

	// Cancellation must interrupt an in-flight transfer, not just the dial.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = sshClient.Close()
		case <-done:
		}
	}()

	client, err := sftp.NewClient(sshClient)
	if err != nil {
		close(done)
		_ = sshClient.Close()
		return nil, nil, u.redactor.Error(fmt.Errorf("sftp: starting the sftp subsystem on %s: %w", addr, err))
	}

	return client, func() {
		_ = client.Close()
		close(done)
		_ = sshClient.Close()
	}, nil
}

// writeRemote streams src to target and applies the configured file mode.
func (u *SFTPUploader) writeRemote(client *sftp.Client, target string, src io.Reader, expected int64) (int64, error) {
	remote, err := client.Create(target)
	if err != nil {
		return 0, fmt.Errorf("sftp: creating %s: %w", target, err)
	}

	written, copyErr := io.Copy(remote, src)
	// The SFTP protocol reports write failures on close, so its error matters.
	closeErr := remote.Close()
	switch {
	case copyErr != nil:
		return written, fmt.Errorf("sftp: writing %s: %w", target, copyErr)
	case closeErr != nil:
		return written, fmt.Errorf("sftp: closing %s: %w", target, closeErr)
	case written != expected:
		return written, fmt.Errorf("sftp: wrote %d of %d bytes to %s", written, expected, target)
	}

	if err := client.Chmod(target, u.cfg.FileMode); err != nil {
		return written, fmt.Errorf("sftp: chmod %s to %o: %w", target, u.cfg.FileMode.Perm(), err)
	}
	return written, nil
}

// publish moves target onto the final remote path.
func (u *SFTPUploader) publish(client *sftp.Client, target string) error {
	if err := client.PosixRename(target, u.remote); err == nil {
		return nil
	} else if !isUnsupportedExtension(err) {
		return fmt.Errorf("sftp: renaming %s to %s: %w", target, u.remote, err)
	}

	// Servers without the posix-rename extension need the destination removed first.
	if err := client.Remove(u.remote); err != nil && !errors.Is(err, os.ErrNotExist) {
		u.logger.Debug("could not remove the previous remote file before rename", "remote", u.remote, "err", err)
	}
	if err := client.Rename(target, u.remote); err != nil {
		return fmt.Errorf("sftp: renaming %s to %s: %w", target, u.remote, err)
	}
	return nil
}

// isUnsupportedExtension reports whether err means the server lacks posix-rename.
func isUnsupportedExtension(err error) bool {
	var status *sftp.StatusError
	if errors.As(err, &status) {
		return status.Code == uint32(sftp.ErrSSHFxOpUnsupported)
	}
	return strings.Contains(strings.ToLower(err.Error()), "unsupported")
}

// cleanup removes a partial upload, best effort.
func (u *SFTPUploader) cleanup(client *sftp.Client, target string) {
	if target == u.remote {
		// Non-atomic mode writes in place; removing it would destroy the previous
		// good image as well.
		return
	}
	if err := client.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		u.logger.Debug("could not remove the partial upload", "remote", target, "err", err)
	}
}

// knownHostAlgorithms reports the host key algorithms recorded for addr.
//
// Without this, a server offering several host key types may present one the
// known_hosts file has no entry for, and verification fails with a confusing
// "key mismatch" even though the configuration is correct. That is the common
// case: "ssh-keyscan -t ed25519" is the recommended way to pin a host, yet most
// servers also offer an RSA key.
//
// x/crypto/ssh/knownhosts exposes no accessor for this, so the callback is
// probed with a key that cannot match: the resulting KeyError lists every key
// known for the host. An empty result means "no restriction", which is the right
// fallback for the fingerprint and insecure strategies.
func knownHostAlgorithms(cb ssh.HostKeyCallback, addr string) []string {
	if cb == nil {
		return nil
	}
	err := cb(addr, &net.TCPAddr{IP: net.IPv4zero}, probeKey{})

	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	var algs []string
	add := func(a string) {
		if _, dup := seen[a]; dup {
			return
		}
		seen[a] = struct{}{}
		algs = append(algs, a)
	}
	for _, known := range keyErr.Want {
		switch t := known.Key.Type(); t {
		case ssh.KeyAlgoRSA:
			// The same RSA key is negotiated under modern signature algorithms.
			add(ssh.KeyAlgoRSASHA512)
			add(ssh.KeyAlgoRSASHA256)
			add(ssh.KeyAlgoRSA)
		default:
			add(t)
		}
	}
	return algs
}

// probeKey is an ssh.PublicKey that can never match a real host key. It exists
// only to make a knownhosts callback report what it knows.
type probeKey struct{}

func (probeKey) Type() string                        { return "rtsp-sftp-uploader-probe" }
func (probeKey) Marshal() []byte                     { return []byte{0} }
func (probeKey) Verify([]byte, *ssh.Signature) error { return errors.New("probe key cannot verify") }
