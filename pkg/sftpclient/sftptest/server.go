// Package sftptest runs a real SSH server with the SFTP subsystem on
// loopback, for tests that must exercise the whole client: host key,
// authentication, and the file operations.
package sftptest

import (
	"crypto/dsa" //nolint:staticcheck // a deliberately weak key, to prove it is refused
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Options configures a Server. The zero value accepts the password
// "secret" for user "partner".
type Options struct {
	User     string
	Password string
	// AuthorizedKey, when set, is accepted for public key authentication.
	AuthorizedKey ssh.PublicKey
	// Root is the directory the SFTP session starts in; t.TempDir() when
	// empty. Paths are real paths on this machine.
	Root string
	// NoSFTP refuses the sftp subsystem, as a shell-only server would.
	NoSFTP bool
	// KeyExchanges limits the key exchanges the server offers.
	KeyExchanges []string
	// DSAHostKey gives the server a 1024-bit DSA host key and nothing else.
	DSAHostKey bool
	// SecondHostKey gives the server an ECDSA P-256 host key as well as
	// its ed25519 one.
	SecondHostKey bool
	// LyingSizes serves files read-only and reports every one as empty,
	// as a server whose declared sizes cannot be trusted would.
	LyingSizes bool
}

// Server is a running test server.
type Server struct {
	Host string
	Port int
	Root string
	User string
	// Password is the one the server accepts.
	Password string
	HostKey  ssh.PublicKey
	// SecondKey is the ECDSA key, when Options.SecondHostKey is set.
	SecondKey ssh.PublicKey

	ln       net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	accepted int
	logs     []string
}

// Start runs a server until the test ends.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()
	if opts.User == "" {
		opts.User = "partner"
	}
	if opts.Password == "" {
		opts.Password = "secret"
	}
	if opts.Root == "" {
		opts.Root = t.TempDir()
	}

	signer := hostSigner(t, opts.DSAHostKey)

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == opts.User && string(pw) == opts.Password {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if opts.AuthorizedKey != nil && c.User() == opts.User &&
				ssh.FingerprintSHA256(key) == ssh.FingerprintSHA256(opts.AuthorizedKey) {
				return nil, nil
			}
			return nil, errors.New("key not authorized")
		},
	}
	if len(opts.KeyExchanges) > 0 {
		cfg.KeyExchanges = opts.KeyExchanges
	}
	cfg.AddHostKey(signer)
	var second ssh.Signer
	if opts.SecondHostKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if second, err = ssh.NewSignerFromKey(k); err != nil {
			t.Fatal(err)
		}
		cfg.AddHostKey(second)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	s := &Server{
		Host: host, Port: port, Root: opts.Root,
		User: opts.User, Password: opts.Password,
		HostKey: signer.PublicKey(), ln: ln,
		conns: map[net.Conn]struct{}{},
	}
	if second != nil {
		s.SecondKey = second.PublicKey()
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns[conn] = struct{}{}
			s.accepted++
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(conn, cfg, opts)
			}()
		}
	}()
	// Every connection is closed at the end, not waited for: a test that
	// fails while holding a client must still end, rather than hang its
	// cleanup until the test binary's deadline.
	t.Cleanup(func() {
		_ = ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s
}

// Accepted is how many TCP connections the server has accepted, so a
// test can prove that something never connected at all.
func (s *Server) Accepted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

func hostSigner(t testing.TB, useDSA bool) ssh.Signer {
	t.Helper()
	var key interface{}
	if useDSA {
		var k dsa.PrivateKey
		if err := dsa.GenerateParameters(&k.Parameters, rand.Reader, dsa.L1024N160); err != nil {
			t.Fatal(err)
		}
		if err := dsa.GenerateKey(&k, rand.Reader); err != nil {
			t.Fatal(err)
		}
		key = &k
	} else {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key = priv
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// Fingerprint is the server's host key as SHA256:...
func (s *Server) Fingerprint() string { return ssh.FingerprintSHA256(s.HostKey) }

// AuthorizedKeyLine is the server's host key as a public key line.
func (s *Server) AuthorizedKeyLine() string {
	return string(ssh.MarshalAuthorizedKey(s.HostKey))
}

func (s *Server) serve(raw net.Conn, cfg *ssh.ServerConfig, opts Options) {
	defer raw.Close()
	conn, chans, reqs, err := ssh.NewServerConn(raw, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)

	var wg sync.WaitGroup
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer ch.Close()
			for req := range chReqs {
				ok := req.Type == "subsystem" && len(req.Payload) > 4 &&
					string(req.Payload[4:]) == "sftp" && !opts.NoSFTP
				if req.WantReply {
					_ = req.Reply(ok, nil)
				}
				if !ok {
					continue
				}
				if opts.LyingSizes {
					h := lyingHandlers{}
					rs := sftp.NewRequestServer(ch, sftp.Handlers{FileGet: h, FilePut: h, FileCmd: h, FileList: h})
					_ = rs.Serve()
					_ = rs.Close()
					return
				}
				srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(opts.Root))
				if err != nil {
					return
				}
				if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
					s.mu.Lock()
					s.logs = append(s.logs, err.Error())
					s.mu.Unlock()
				}
				_ = srv.Close()
				return
			}
		}()
	}
	wg.Wait()
}
