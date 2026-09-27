package uploader

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"sync"
	"testing"

	glssh "github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// testServer is a real SSH server with an SFTP subsystem, listening on
// 127.0.0.1:0 and serving the local filesystem. pkg/sftp's server is not
// chrooted, so tests address files by their absolute host path under Root.
type testServer struct {
	Addr        string
	HostKey     gossh.PublicKey // the ed25519 key
	HostKeyLine string          // known_hosts line for the ed25519 key
	RSAHostKey  gossh.PublicKey // present only when opts.AlsoOfferRSA was set
	Root        string

	mu      sync.Mutex
	authLog []string
}

type testServerOpts struct {
	User          string
	Password      string
	AuthorizedKey gossh.PublicKey
	RejectAuth    bool
	// AlsoOfferRSA adds a second host key, reproducing the common real-world
	// setup where a server offers both RSA and ed25519.
	AlsoOfferRSA bool
}

func startTestServer(t *testing.T, opts testServerOpts) *testServer {
	t.Helper()

	signer := generateSigner(t)
	root := t.TempDir()
	var rsaSigner gossh.Signer
	if opts.AlsoOfferRSA {
		rsaSigner = generateRSASigner(t)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ts := &testServer{
		Addr:    ln.Addr().String(),
		HostKey: signer.PublicKey(),
		Root:    root,
	}
	ts.HostKeyLine = knownHostsLine(t, ts.Addr, signer.PublicKey())
	if rsaSigner != nil {
		ts.RSAHostKey = rsaSigner.PublicKey()
	}

	srv := &glssh.Server{
		Handler: func(s glssh.Session) { _ = s.Exit(0) },
		PasswordHandler: func(ctx glssh.Context, password string) bool {
			ts.recordAuth("password:" + ctx.User())
			if opts.RejectAuth || opts.Password == "" {
				return false
			}
			return ctx.User() == opts.User && password == opts.Password
		},
		PublicKeyHandler: func(ctx glssh.Context, key glssh.PublicKey) bool {
			ts.recordAuth("publickey:" + ctx.User())
			if opts.RejectAuth || opts.AuthorizedKey == nil {
				return false
			}
			return ctx.User() == opts.User && glssh.KeysEqual(key, opts.AuthorizedKey)
		},
		SubsystemHandlers: map[string]glssh.SubsystemHandler{
			"sftp": func(s glssh.Session) {
				server, err := sftp.NewServer(s)
				if err != nil {
					return
				}
				defer func() { _ = server.Close() }()
				if err := server.Serve(); err != nil && !errors.Is(err, net.ErrClosed) {
					return
				}
			},
		},
	}
	// Order matters: gliderlabs picks the first key matching the client's
	// preference, so offering RSA first mirrors a typical OpenSSH server.
	if rsaSigner != nil {
		srv.AddHostKey(rsaSigner)
	}
	srv.AddHostKey(signer)

	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return ts
}

func (ts *testServer) recordAuth(entry string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.authLog = append(ts.authLog, entry)
}

func (ts *testServer) authAttempts() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]string(nil), ts.authLog...)
}

// generateSigner produces a throwaway ed25519 SSH signer.
func generateSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating an ed25519 key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("building a signer: %v", err)
	}
	return signer
}

// generateRSASigner produces a throwaway 2048-bit RSA SSH signer.
func generateRSASigner(t *testing.T) gossh.Signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating an RSA key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("building an RSA signer: %v", err)
	}
	return signer
}

// knownHostsLine renders an OpenSSH known_hosts entry for addr, normalising
// host:port into the "[host]:port" form OpenSSH expects.
func knownHostsLine(t *testing.T, addr string, key gossh.PublicKey) string {
	t.Helper()
	return knownhosts.Line([]string{knownhosts.Normalize(addr)}, key)
}
