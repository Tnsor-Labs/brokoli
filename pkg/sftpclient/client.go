// Package sftpclient moves files to and from an SFTP server the way
// ADR-040 requires: the server's host key is verified before anything is
// sent, and a delivery is written under a temporary name and renamed into
// place only once it is complete.
package sftpclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

var (
	// ErrHostKeyUnknown means no host key is configured for the server, so
	// its identity cannot be checked. The error carries the key the
	// server presented, to be verified out of band and then configured.
	ErrHostKeyUnknown = errors.New("sftp: the server's host key is not configured")
	// ErrHostKeyMismatch means the server presented a different key from
	// the configured one: something other than the intended server may
	// have answered.
	ErrHostKeyMismatch = errors.New("sftp: the server's host key does not match the configured one")
	// ErrPathEscapes refuses a path with a ".." segment.
	ErrPathEscapes = errors.New("sftp: a path may not contain a '..' segment")
	// ErrOutsideBaseDir refuses an absolute path outside the connection's
	// base directory. Where a base directory is set, it is a boundary, not
	// only a starting point.
	ErrOutsideBaseDir = errors.New("sftp: the path is outside the connection's base directory")
	// ErrFileTooLarge refuses a download over the size limit.
	ErrFileTooLarge = errors.New("sftp: the file is larger than the download limit")
)

// DefaultMaxDownloadBytes is the largest file Download fetches when the
// configuration sets no limit. The copy lands on a disk other pipelines
// share, so a server must not be able to fill it by sending forever.
const DefaultMaxDownloadBytes int64 = 10 << 30

// HostKeyError is returned when the host key check refuses a server.
type HostKeyError struct {
	// Presented is the fingerprint the server offered, SHA256:...
	Presented string
	// Expected is the configured fingerprint, empty when none was set.
	Expected string
	Err      error
}

func (e *HostKeyError) Error() string {
	if e.Expected == "" {
		return fmt.Sprintf("%v; the server presented %s -- verify it with the server's operator, then set it as the connection's host key", e.Err, e.Presented)
	}
	return fmt.Sprintf("%v: configured %s, server presented %s", e.Err, e.Expected, e.Presented)
}

func (e *HostKeyError) Unwrap() error { return e.Err }

// Config describes one SFTP server and how to authenticate to it.
type Config struct {
	Host string
	// Port defaults to 22.
	Port int
	User string
	// Password and PrivateKey may both be set; each is offered.
	Password   string
	PrivateKey string
	Passphrase string
	// HostKey is the server's key: a SHA256:... fingerprint as ssh-keygen
	// -lf prints it, a public key line, or a known_hosts line.
	HostKey string
	// InsecureSkipHostKeyCheck accepts any host key. It is the only way to
	// skip the check, and every connection made with it calls Warn.
	InsecureSkipHostKeyCheck bool
	// BaseDir is where relative paths resolve, and the directory every
	// path must stay inside. Empty means relative paths start in the login
	// directory and absolute paths are limited only by the account's
	// permissions on the server.
	BaseDir string
	// Dial connects the underlying TCP socket. The engine passes the
	// outbound network policy's dialer here, so SSH is guarded the same
	// way HTTP is. Nil dials directly.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Timeout bounds connecting and the SSH handshake; 15s when zero.
	Timeout time.Duration
	// MaxDownloadBytes is the largest file Download fetches;
	// DefaultMaxDownloadBytes when zero.
	MaxDownloadBytes int64
	// IdleTimeout fails the connection when no data moves in either
	// direction for this long, so a server that stops responding cannot
	// hold a transfer open forever. Keepalives are sent at a third of it,
	// so a connection that is merely quiet on this side stays up. Two
	// minutes when zero.
	IdleTimeout time.Duration
	// Warn receives messages that must be seen but are not errors.
	Warn func(msg string)
}

// Client is an open SFTP session.
type Client struct {
	cfg  Config
	ssh  *ssh.Client
	sftp *sftp.Client
	// HostKey is the fingerprint of the key the server presented.
	HostKey string

	stopWatch func() bool
	done      chan struct{}
	closeOnce sync.Once
	// nonce makes temporary names unguessable; replaced only by tests.
	nonce func() string
}

// Dial connects, verifies the host key, authenticates and opens SFTP.
//
// ctx bounds the whole session, not only connecting: when it is done the
// connection is closed, which ends any transfer in progress. A node's
// timeout or a cancelled run therefore stops a transfer promptly instead
// of leaving it blocked on the network.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("sftp: the connection has no host")
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	if len(auth) == 0 {
		return nil, errors.New("sftp: the connection has neither a password nor a private key")
	}

	// Only algorithms x/crypto does not classify as insecure: a server
	// that offers nothing better (SHA-1 key exchange, DSA host keys) is
	// refused rather than accepted on its terms.
	algs := ssh.SupportedAlgorithms()
	hostKeyAlgs := algs.HostKeys
	// The configured host key is read before anything is sent, so a
	// malformed or revoked one never reaches the network.
	var wantHostKey string
	if !cfg.InsecureSkipHostKeyCheck {
		fp, keyType, err := expectedHostKey(cfg.HostKey)
		if err != nil {
			return nil, err
		}
		wantHostKey = fp
		if keyType != "" {
			// A configured key line pins negotiation to that key's type.
			// A server with several host keys would otherwise present its
			// preferred one, the check would report a mismatch, and the
			// fix people reach for is to paste whatever the server shows.
			hostKeyAlgs = hostKeyAlgorithmsFor(keyType, algs.HostKeys)
			if len(hostKeyAlgs) == 0 {
				return nil, fmt.Errorf("sftp: the configured host key is a %s key, which is refused as insecure", keyType)
			}
		}
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	idle := cfg.IdleTimeout
	if idle <= 0 {
		idle = 2 * time.Minute
	}
	dial := cfg.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: timeout}).DialContext
	}
	raw, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("sftp: connect to %s: %w", addr, err)
	}

	raw = &idleConn{Conn: raw, idle: idle}

	var presented string
	sshCfg := &ssh.ClientConfig{
		Config: ssh.Config{
			KeyExchanges: algs.KeyExchanges,
			Ciphers:      algs.Ciphers,
			MACs:         algs.MACs,
		},
		HostKeyAlgorithms: hostKeyAlgs,
		User:              cfg.User,
		Auth:              auth,
		Timeout:           timeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			presented = ssh.FingerprintSHA256(key)
			return checkHostKey(cfg, wantHostKey, key)
		},
	}
	// The handshake as a whole is bounded, not only each read in it.
	handshakeTimer := time.AfterFunc(timeout, func() { _ = raw.Close() })
	conn, chans, reqs, err := ssh.NewClientConn(raw, addr, sshCfg)
	handshakeTimer.Stop()
	if err != nil {
		_ = raw.Close()
		var hk *HostKeyError
		if errors.As(err, &hk) {
			return nil, hk
		}
		return nil, fmt.Errorf("sftp: %s: %w", addr, err)
	}

	sshClient := ssh.NewClient(conn, chans, reqs)
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return nil, fmt.Errorf("sftp: %s accepted SSH but not the SFTP subsystem: %w", addr, err)
	}
	c := &Client{
		cfg: cfg, ssh: sshClient, sftp: sftpClient, HostKey: presented,
		done: make(chan struct{}), nonce: randomNonce,
	}
	c.stopWatch = context.AfterFunc(ctx, func() { _ = sshClient.Close() })
	go c.keepalive(idle / 3)
	return c, nil
}

// keepalive asks the server for a reply at an interval. Each reply is
// traffic, so a connection that is quiet only because this side has
// nothing to send yet outlives the idle timeout; a server that has
// stopped answering does not, and the idle timeout closes it.
func (c *Client) keepalive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			if _, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				return
			}
		}
	}
}

// idleConn fails a read or write that waits longer than idle.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(p)
}

func (c *idleConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.idle))
	return c.Conn.Write(p)
}

func randomNonce() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func authMethods(cfg Config) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if strings.TrimSpace(cfg.PrivateKey) != "" {
		var signer ssh.Signer
		var err error
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("sftp: the private key could not be read: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		pw := cfg.Password
		methods = append(methods,
			ssh.Password(pw),
			// Some servers ask for the password as a keyboard-interactive
			// prompt rather than accepting password authentication.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = pw
				}
				return answers, nil
			}),
		)
	}
	return methods, nil
}

// checkHostKey is the whole of ADR-040's host key rule. want is the
// configured key's fingerprint, "" when none is configured.
func checkHostKey(cfg Config, want string, key ssh.PublicKey) error {
	got := ssh.FingerprintSHA256(key)
	if cfg.InsecureSkipHostKeyCheck {
		if cfg.Warn != nil {
			cfg.Warn(fmt.Sprintf("the host key of %s was not checked (the server presented %s) because insecure_skip_host_key_check is set", cfg.Host, got))
		}
		return nil
	}
	if want == "" {
		return &HostKeyError{Presented: got, Err: ErrHostKeyUnknown}
	}
	if got != want {
		return &HostKeyError{Presented: got, Expected: want, Err: ErrHostKeyMismatch}
	}
	return nil
}

// expectedHostKey reads the configured host key in any of the forms an
// operator is likely to paste, returning its fingerprint and, when the
// full key was given, its type.
func expectedHostKey(configured string) (fingerprint, keyType string, err error) {
	s := strings.TrimSpace(configured)
	if s == "" {
		return "", "", nil
	}
	if strings.HasPrefix(s, "SHA256:") {
		return s, "", nil
	}
	if pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s)); err == nil {
		return ssh.FingerprintSHA256(pk), pk.Type(), nil
	}
	if marker, _, pk, _, _, err := ssh.ParseKnownHosts([]byte(s)); err == nil {
		// "@revoked" names a key that must NOT be trusted, and
		// "@cert-authority" a signer, not the server's own key.
		if marker != "" {
			return "", "", fmt.Errorf("sftp: the configured host key is a known_hosts line marked @%s, which is not a key to trust", strings.TrimPrefix(marker, "@"))
		}
		return ssh.FingerprintSHA256(pk), pk.Type(), nil
	}
	// Refused rather than treated as absent: a host key somebody set and
	// mistyped must not quietly become "no host key".
	return "", "", errors.New("sftp: the configured host key is not a SHA256 fingerprint, a public key line, or a known_hosts line")
}

// hostKeyAlgorithmsFor lists the allowed signature algorithms for a host
// key of keyType, in allowed's order of preference.
func hostKeyAlgorithmsFor(keyType string, allowed []string) []string {
	want := []string{keyType}
	if keyType == ssh.KeyAlgoRSA {
		// An RSA key signs with SHA-2 here; SHA-1 is not in allowed.
		want = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	var out []string
	for _, a := range allowed {
		for _, w := range want {
			if a == w {
				out = append(out, a)
			}
		}
	}
	return out
}

// Resolve turns a node's path into a path on the server.
func (c *Client) Resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("sftp: empty path")
	}
	// Backslash counts as a separator too: a Windows server may treat it
	// as one, and "..\\x" must not walk out where "../x" cannot.
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return "", ErrPathEscapes
		}
	}
	base := c.cfg.BaseDir
	if base != "" && !strings.HasPrefix(base, "/") {
		// A relative base directory is relative to the login directory.
		if wd, err := c.sftp.Getwd(); err == nil {
			base = path.Join(wd, base)
		}
	}
	if strings.HasPrefix(p, "/") {
		abs := path.Clean(p)
		if base != "" && !within(abs, path.Clean(base)) {
			return "", ErrOutsideBaseDir
		}
		return abs, nil
	}
	if base == "" {
		if wd, err := c.sftp.Getwd(); err == nil {
			base = wd
		} else {
			base = "."
		}
	}
	return path.Join(base, p), nil
}

// within reports whether p is dir or inside it. "/upload-old" is not
// inside "/upload".
func within(p, dir string) bool {
	return dir == "/" || p == dir || strings.HasPrefix(p, dir+"/")
}

// UploadResult says where a file landed and how it got there.
type UploadResult struct {
	Path  string
	Bytes int64
	// Atomic is false when the server lacked posix-rename and an existing
	// file had to be removed before the new one was renamed in.
	Atomic bool
}

// Upload writes a file by calling write with a writer on a temporary file
// beside the destination, then renames it into place. On any failure the
// temporary file is removed, if the connection still allows it, and the
// destination is left as it was. tag names the writer in the temporary
// name, a run id in practice; a random part makes the name unguessable.
func (c *Client) Upload(remotePath, tag string, write func(io.Writer) error) (UploadResult, error) {
	dst, err := c.Resolve(remotePath)
	if err != nil {
		return UploadResult{}, err
	}
	dir, name := path.Dir(dst), path.Base(dst)
	if err := c.sftp.MkdirAll(dir); err != nil {
		return UploadResult{}, fmt.Errorf("sftp: create %s: %w", dir, err)
	}
	tmp := path.Join(dir, "."+name+"."+safeTag(tag)+"-"+c.nonce()+".part")

	// Exclusive: on a server shared with other accounts, a file or symlink
	// somebody placed at this name makes the upload refuse, rather than
	// write through it to wherever it points.
	f, err := c.sftp.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return UploadResult{}, fmt.Errorf("sftp: open %s: %w", tmp, err)
	}
	counted := &countingWriter{w: f}
	buffered := bufio.NewWriterSize(counted, 256<<10)
	werr := write(buffered)
	if werr == nil {
		werr = buffered.Flush()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = c.sftp.Remove(tmp)
		return UploadResult{}, fmt.Errorf("sftp: write %s: %w", dst, werr)
	}

	if _, ok := c.sftp.HasExtension("posix-rename@openssh.com"); ok {
		if err := c.sftp.PosixRename(tmp, dst); err != nil {
			_ = c.sftp.Remove(tmp)
			return UploadResult{}, fmt.Errorf("sftp: rename into %s: %w", dst, err)
		}
		return UploadResult{Path: dst, Bytes: counted.n, Atomic: true}, nil
	}
	if err := c.sftp.Remove(dst); err != nil && !isNotExist(err) {
		_ = c.sftp.Remove(tmp)
		return UploadResult{}, fmt.Errorf("sftp: replace %s: %w", dst, err)
	}
	if err := c.sftp.Rename(tmp, dst); err != nil {
		_ = c.sftp.Remove(tmp)
		return UploadResult{}, fmt.Errorf("sftp: rename into %s: %w", dst, err)
	}
	return UploadResult{Path: dst, Bytes: counted.n, Atomic: false}, nil
}

// Download copies a remote file to localPath and returns the resolved
// remote path and the bytes copied.
func (c *Client) Download(remotePath, localPath string) (string, int64, error) {
	src, err := c.Resolve(remotePath)
	if err != nil {
		return "", 0, err
	}
	in, err := c.sftp.Open(src)
	if err != nil {
		return src, 0, fmt.Errorf("sftp: open %s: %w", src, err)
	}
	defer in.Close()
	limit := c.cfg.MaxDownloadBytes
	if limit <= 0 {
		limit = DefaultMaxDownloadBytes
	}
	if fi, err := in.Stat(); err == nil && fi.Size() > limit {
		return src, 0, fmt.Errorf("%w: %s is %d bytes, over the limit of %d", ErrFileTooLarge, src, fi.Size(), limit)
	}
	// #nosec G304 -- localPath is a temporary file this process created.
	out, err := os.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return src, 0, err
	}
	// The declared size is the server's word; the capped writer enforces
	// the limit on what actually arrives. io.Copy still hands the copy to
	// the file's WriteTo, which keeps several reads in flight.
	capped := &capWriter{w: out, left: limit}
	n, err := io.Copy(capped, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if errors.Is(err, errOverLimit) {
		return src, n, fmt.Errorf("%w: %s kept sending past the limit of %d bytes", ErrFileTooLarge, src, limit)
	}
	if err != nil {
		return src, n, fmt.Errorf("sftp: download %s: %w", src, err)
	}
	return src, n, nil
}

var errOverLimit = errors.New("over the download limit")

// capWriter refuses any write that would take it past its limit.
type capWriter struct {
	w    io.Writer
	left int64
}

func (c *capWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, errOverLimit
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

// CheckBaseDir confirms the base directory exists and is a directory.
func (c *Client) CheckBaseDir() (string, error) {
	dir := c.cfg.BaseDir
	if dir == "" {
		wd, err := c.sftp.Getwd()
		if err != nil {
			return "", fmt.Errorf("sftp: find the login directory: %w", err)
		}
		dir = wd
	}
	fi, err := c.sftp.Stat(dir)
	if err != nil {
		return dir, fmt.Errorf("sftp: %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return dir, fmt.Errorf("sftp: %s is not a directory", dir)
	}
	return dir, nil
}

// HasExtension reports whether the server advertised an SFTP extension.
func (c *Client) HasExtension(name string) (string, bool) { return c.sftp.HasExtension(name) }

// Close ends the session.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		c.stopWatch()
	})
	serr := c.sftp.Close()
	if err := c.ssh.Close(); err != nil && serr == nil {
		serr = err
	}
	return serr
}

func isNotExist(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var se *sftp.StatusError
	return errors.As(err, &se) && se.FxCode() == sftp.ErrSSHFxNoSuchFile
}

// safeTag keeps a temporary name to characters every server accepts.
func safeTag(tag string) string {
	var b strings.Builder
	for _, r := range tag {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "upload"
	}
	return b.String()
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
