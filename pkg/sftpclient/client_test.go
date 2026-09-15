package sftpclient_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/Tnsor-Labs/brokoli/pkg/sftpclient"
	"github.com/Tnsor-Labs/brokoli/pkg/sftpclient/sftptest"
)

func configFor(s *sftptest.Server) sftpclient.Config {
	return sftpclient.Config{
		Host: s.Host, Port: s.Port, User: s.User, Password: s.Password,
		HostKey: s.Fingerprint(), BaseDir: s.Root,
	}
}

func dial(t *testing.T, cfg sftpclient.Config) *sftpclient.Client {
	t.Helper()
	c, err := sftpclient.Dial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// dialErr is for dials that must fail. A client returned by mistake is
// closed so the failure is reported instead of hanging.
func dialErr(cfg sftpclient.Config) error {
	c, err := sftpclient.Dial(context.Background(), cfg)
	if c != nil {
		_ = c.Close()
	}
	return err
}

func writeString(s string) func(io.Writer) error {
	return func(w io.Writer) error { _, err := io.WriteString(w, s); return err }
}

func TestUploadDeliversTheFileAndLeavesNoTemporaryFile(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	c := dial(t, configFor(srv))

	res, err := c.Upload("outbound/2026/orders.csv", "run-1", writeString("id,total\n1,10\n"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	want := filepath.Join(srv.Root, "outbound", "2026", "orders.csv")
	if res.Path != want {
		t.Fatalf("path %q, want %q", res.Path, want)
	}
	if !res.Atomic {
		t.Fatalf("this server supports posix-rename, so the delivery must be atomic")
	}
	if res.Bytes != int64(len("id,total\n1,10\n")) {
		t.Fatalf("bytes %d", res.Bytes)
	}
	got, err := os.ReadFile(want)
	if err != nil || string(got) != "id,total\n1,10\n" {
		t.Fatalf("delivered %q, %v", got, err)
	}
	assertOnlyFiles(t, filepath.Dir(want), "orders.csv")
}

func TestUploadReplacesAnExistingFile(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	c := dial(t, configFor(srv))
	if _, err := c.Upload("orders.csv", "run-1", writeString("old")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upload("orders.csv", "run-2", writeString("new")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(srv.Root, "orders.csv"))
	if string(got) != "new" {
		t.Fatalf("got %q", got)
	}
	assertOnlyFiles(t, srv.Root, "orders.csv")
}

// The partner polling the directory must never see a half-written file,
// and a failed run must leave the previous delivery exactly as it was.
func TestAFailedUploadLeavesTheDestinationUntouched(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	c := dial(t, configFor(srv))
	if _, err := c.Upload("orders.csv", "run-1", writeString("yesterday")); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("encoder failed half way")
	_, err := c.Upload("orders.csv", "run-2", func(w io.Writer) error {
		if _, err := io.WriteString(w, strings.Repeat("x", 1<<20)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the writer's error", err)
	}
	got, _ := os.ReadFile(filepath.Join(srv.Root, "orders.csv"))
	if string(got) != "yesterday" {
		t.Fatalf("the destination changed to %d bytes", len(got))
	}
	assertOnlyFiles(t, srv.Root, "orders.csv")
}

// While the writer runs, the data is under a hidden temporary name and the
// destination does not exist yet.
func TestTheDestinationAppearsOnlyWhenComplete(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	c := dial(t, configFor(srv))
	dst := filepath.Join(srv.Root, "orders.csv")

	_, err := c.Upload("orders.csv", "run-7", func(w io.Writer) error {
		if _, err := io.WriteString(w, strings.Repeat("y", 600<<10)); err != nil {
			return err
		}
		if _, err := os.Stat(dst); !os.IsNotExist(err) {
			t.Errorf("the destination exists mid-write: %v", err)
		}
		entries, _ := os.ReadDir(srv.Root)
		if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".orders.csv.run-7-") || !strings.HasSuffix(entries[0].Name(), ".part") {
			t.Errorf("mid-write the directory holds %v, want only .orders.csv.run-7-<random>.part", names(entries))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil || fi.Size() != 600<<10 {
		t.Fatalf("after: %v %v", fi, err)
	}
}

// A server without posix-rename still gets the file, and the caller is told
// the replacement was not atomic so it can say so.
func TestWithoutPosixRenameTheReplacementIsReportedNotAtomic(t *testing.T) {
	if err := sftp.SetSFTPExtensions("statvfs@openssh.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sftp.SetSFTPExtensions("hardlink@openssh.com", "posix-rename@openssh.com", "statvfs@openssh.com")
	})
	srv := sftptest.Start(t, sftptest.Options{})
	c := dial(t, configFor(srv))
	if _, ok := hasPosixRename(c); ok {
		t.Fatal("setup: the server still advertises posix-rename")
	}
	for i, body := range []string{"first", "second"} {
		res, err := c.Upload("orders.csv", "run", writeString(body))
		if err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
		if res.Atomic {
			t.Fatalf("upload %d reported atomic without posix-rename", i)
		}
	}
	got, _ := os.ReadFile(filepath.Join(srv.Root, "orders.csv"))
	if string(got) != "second" {
		t.Fatalf("got %q", got)
	}
	assertOnlyFiles(t, srv.Root, "orders.csv")
}

func hasPosixRename(c *sftpclient.Client) (string, bool) {
	return c.HasExtension("posix-rename@openssh.com")
}

func TestDownloadCopiesTheRemoteFile(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	if err := os.MkdirAll(filepath.Join(srv.Root, "inbound"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srv.Root, "inbound", "rates.json"), []byte(`[{"a":1}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := dial(t, configFor(srv))
	local := filepath.Join(t.TempDir(), "rates.json")
	remote, n, err := c.Download("inbound/rates.json", local)
	if err != nil {
		t.Fatal(err)
	}
	if remote != filepath.Join(srv.Root, "inbound", "rates.json") || n != 9 {
		t.Fatalf("remote %q n %d", remote, n)
	}
	got, _ := os.ReadFile(local)
	if string(got) != `[{"a":1}]` {
		t.Fatalf("got %q", got)
	}
}

func TestPathsWithParentSegmentsAreRefused(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	c := dial(t, configFor(srv))
	for _, p := range []string{"../etc/passwd", "a/../../b", "/srv/../etc", "..", "a/..", "..\\etc\\passwd", "a\\..\\..\\b"} {
		if _, err := c.Resolve(p); !errors.Is(err, sftpclient.ErrPathEscapes) {
			t.Errorf("Resolve(%q) = %v, want ErrPathEscapes", p, err)
		}
		if _, err := c.Upload(p, "r", writeString("x")); !errors.Is(err, sftpclient.ErrPathEscapes) {
			t.Errorf("Upload(%q) = %v, want ErrPathEscapes", p, err)
		}
	}
	// Names that merely contain dots are ordinary files.
	for _, p := range []string{"a..b.csv", "..hidden", "x/.../y"} {
		if _, err := c.Resolve(p); err != nil {
			t.Errorf("Resolve(%q) = %v, want accepted", p, err)
		}
	}
	// Without a base directory an absolute path is used as given; the
	// account's permissions on the server are the only boundary.
	open := configFor(srv)
	open.BaseDir = ""
	if got, err := dial(t, open).Resolve("/abs/file.csv"); err != nil || got != "/abs/file.csv" {
		t.Errorf("absolute path with no base directory resolved to %q, %v", got, err)
	}
}

// Where a base directory is set it is a boundary: an absolute path cannot
// leave it any more than a ".." can.
func TestAbsolutePathsStayInsideTheBaseDirectory(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	c := dial(t, configFor(srv))
	for _, p := range []string{srv.Root + "/out/orders.csv", srv.Root} {
		if _, err := c.Resolve(p); err != nil {
			t.Errorf("Resolve(%q) = %v, want accepted", p, err)
		}
	}
	for _, p := range []string{"/etc/passwd", srv.Root + "-old/orders.csv", "/"} {
		if _, err := c.Resolve(p); !errors.Is(err, sftpclient.ErrOutsideBaseDir) {
			t.Errorf("Resolve(%q) = %v, want ErrOutsideBaseDir", p, err)
		}
		if _, err := c.Upload(p+"/x.csv", "r", writeString("x")); !errors.Is(err, sftpclient.ErrOutsideBaseDir) {
			t.Errorf("Upload under %q = %v, want ErrOutsideBaseDir", p, err)
		}
	}
}

// Nobody on a shared server can predict the temporary name, so nobody can
// plant anything at it in advance.
func TestTemporaryNamesAreUnguessable(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	c := dial(t, configFor(srv))
	var seen []string
	for i := 0; i < 2; i++ {
		_, err := c.Upload("orders.csv", "run-1", func(w io.Writer) error {
			entries, _ := os.ReadDir(srv.Root)
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".part") {
					seen = append(seen, e.Name())
				}
			}
			return writeString("x")(w)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0] == seen[1] || !strings.HasPrefix(seen[0], ".orders.csv.run-1-") {
		t.Fatalf("temporary names %v: want two different .orders.csv.run-1-<random>.part", seen)
	}
}

type zeros struct{}

func (zeros) Read(b []byte) (int, error) { clear(b); return len(b), nil }

// uploadWithin runs an upload that freezes the proxy and then writes, and
// reports its error, or fails the test if it is still blocked after d.
func uploadWithin(t *testing.T, c *sftpclient.Client, p *sftptest.Proxy, d time.Duration, during func()) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := c.Upload("orders.csv", "r", func(w io.Writer) error {
			p.Freeze()
			if during != nil {
				during()
			}
			_, err := io.Copy(w, io.LimitReader(zeros{}, 16<<20))
			return err
		})
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		p.Close()
		t.Fatalf("the transfer was still blocked after %v", d)
		return nil
	}
}

// A server that stops responding mid-transfer must not hold the transfer,
// and the worker running it, open forever.
func TestASilentServerFailsTheTransfer(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	p := sftptest.StartProxy(t, net.JoinHostPort(srv.Host, itoa(srv.Port)))
	cfg := configFor(srv)
	cfg.Dial = p.Dial
	cfg.IdleTimeout = 500 * time.Millisecond
	c := dial(t, cfg)
	if err := uploadWithin(t, c, p, 10*time.Second, nil); err == nil {
		t.Fatal("an upload to a server that stopped responding reported success")
	}
}

// Cancelling the context (a node timeout, a cancelled run) ends a transfer
// that is blocked on the network, well before the idle timeout would.
func TestCancellingTheContextEndsATransfer(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	p := sftptest.StartProxy(t, net.JoinHostPort(srv.Host, itoa(srv.Port)))
	cfg := configFor(srv)
	cfg.Dial = p.Dial
	cfg.IdleTimeout = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := sftpclient.Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = uploadWithin(t, c, p, 10*time.Second, func() {
		time.AfterFunc(300*time.Millisecond, cancel)
	})
	if err == nil {
		t.Fatal("a transfer whose context was cancelled reported success")
	}
}

// Quiet is not dead: a connection with nothing to send yet on this side
// outlives the idle timeout, because the server keeps answering keepalives.
func TestAQuietConnectionStaysUp(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	cfg := configFor(srv)
	cfg.IdleTimeout = 600 * time.Millisecond
	c := dial(t, cfg)
	_, err := c.Upload("orders.csv", "r", func(w io.Writer) error {
		time.Sleep(2 * time.Second)
		return writeString("late but fine")(w)
	})
	if err != nil {
		t.Fatalf("a healthy connection that was only quiet was dropped: %v", err)
	}
}

// A listener that accepts the connection and never speaks SSH fails the
// dial within Timeout, not after the much longer idle timeout.
func TestTheHandshakeIsBounded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	cfg := sftpclient.Config{Host: host, Port: portN, User: "u", Password: "p", HostKey: "SHA256:x",
		Timeout: 500 * time.Millisecond, IdleTimeout: time.Hour}
	done := make(chan error, 1)
	go func() { done <- dialErr(cfg) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a server that never spoke SSH was accepted")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the dial was still blocked after 10s against a server that never spoke SSH")
	}
}

// A server offering only what x/crypto classifies as insecure is refused,
// even with the right host key configured.
func TestInsecureAlgorithmsAreRefused(t *testing.T) {
	for name, opts := range map[string]sftptest.Options{
		"sha1 key exchange": {KeyExchanges: []string{ssh.InsecureKeyExchangeDH14SHA1}},
		"dsa host key":      {DSAHostKey: true},
	} {
		t.Run(name, func(t *testing.T) {
			srv := sftptest.Start(t, opts)
			err := dialErr(configFor(srv))
			if err == nil || !strings.Contains(err.Error(), "no common algorithm") {
				t.Fatalf("want refused for want of a common algorithm, got: %v", err)
			}
		})
	}
}

func TestHostKeyForms(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	known := "[" + srv.Host + "]:" + itoa(srv.Port) + " " + strings.TrimSpace(srv.AuthorizedKeyLine())
	for name, hk := range map[string]string{
		"fingerprint":     srv.Fingerprint(),
		"public key line": srv.AuthorizedKeyLine(),
		"known_hosts":     known,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := configFor(srv)
			cfg.HostKey = hk
			c := dial(t, cfg)
			if c.HostKey != srv.Fingerprint() {
				t.Fatalf("HostKey %q", c.HostKey)
			}
		})
	}
}

func TestAMissingHostKeyRefusesAndNamesTheServersKey(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	cfg := configFor(srv)
	cfg.HostKey = ""
	err := dialErr(cfg)
	if !errors.Is(err, sftpclient.ErrHostKeyUnknown) {
		t.Fatalf("err = %v, want ErrHostKeyUnknown", err)
	}
	if !strings.Contains(err.Error(), srv.Fingerprint()) {
		t.Fatalf("the error does not name the presented key %s: %v", srv.Fingerprint(), err)
	}
}

func TestAMismatchedHostKeyRefuses(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	other := sftptest.Start(t, sftptest.Options{})
	cfg := configFor(srv)
	cfg.HostKey = other.Fingerprint()
	err := dialErr(cfg)
	if !errors.Is(err, sftpclient.ErrHostKeyMismatch) {
		t.Fatalf("err = %v, want ErrHostKeyMismatch", err)
	}
	var hk *sftpclient.HostKeyError
	if !errors.As(err, &hk) || hk.Presented != srv.Fingerprint() || hk.Expected != other.Fingerprint() {
		t.Fatalf("HostKeyError = %+v", hk)
	}
	if _, err := os.ReadDir(srv.Root); err != nil {
		t.Fatal(err)
	}
}

// A host key somebody set and mistyped must not quietly become "none".
func TestAMalformedHostKeyRefuses(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	cfg := configFor(srv)
	cfg.HostKey = "AAAAC3NzaC1lZDI1NTE5-not-a-key"
	// Refused as malformed, not as missing: "configure a key" is the wrong
	// instruction for somebody who configured one and mistyped it.
	err := dialErr(cfg)
	if err == nil || errors.Is(err, sftpclient.ErrHostKeyUnknown) || !strings.Contains(err.Error(), "is not a SHA256 fingerprint") {
		t.Fatalf("a malformed host key must be refused as malformed, got: %v", err)
	}
}

func TestSkippingTheHostKeyCheckWarnsEveryTime(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	var mu sync.Mutex
	var warnings []string
	cfg := configFor(srv)
	cfg.HostKey = ""
	cfg.InsecureSkipHostKeyCheck = true
	cfg.Warn = func(m string) { mu.Lock(); warnings = append(warnings, m); mu.Unlock() }
	dial(t, cfg)
	dial(t, cfg)
	if len(warnings) != 2 || !strings.Contains(warnings[0], srv.Fingerprint()) {
		t.Fatalf("warnings = %q", warnings)
	}
}

func TestAWrongPasswordIsRefused(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	cfg := configFor(srv)
	cfg.Password = "not-it"
	if err := dialErr(cfg); err == nil {
		t.Fatal("a wrong password was accepted")
	}
}

func TestPrivateKeyAuthentication(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, _ := ssh.NewPublicKey(pub)
	srv := sftptest.Start(t, sftptest.Options{AuthorizedKey: sshPub})

	plain, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}

	cfg := configFor(srv)
	cfg.Password = ""
	cfg.PrivateKey = string(pem.EncodeToMemory(plain))
	dial(t, cfg)

	cfg.PrivateKey = string(pem.EncodeToMemory(enc))
	cfg.Passphrase = "hunter2"
	dial(t, cfg)

	cfg.Passphrase = "wrong"
	if err := dialErr(cfg); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("a wrong passphrase: %v", err)
	}
}

func TestNoCredentialsIsRefusedBeforeConnecting(t *testing.T) {
	cfg := sftpclient.Config{Host: "127.0.0.1", Port: 1, User: "u", HostKey: "SHA256:x"}
	err := dialErr(cfg)
	if err == nil || !strings.Contains(err.Error(), "neither a password nor a private key") {
		t.Fatalf("err = %v", err)
	}
}

func TestAServerWithoutSFTPIsNamed(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{NoSFTP: true})
	err := dialErr(configFor(srv))
	if err == nil || !strings.Contains(err.Error(), "not the SFTP subsystem") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckBaseDir(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	if _, err := dial(t, configFor(srv)).CheckBaseDir(); err != nil {
		t.Fatalf("existing directory: %v", err)
	}
	cfg := configFor(srv)
	cfg.BaseDir = filepath.Join(srv.Root, "missing")
	if _, err := dial(t, cfg).CheckBaseDir(); err == nil {
		t.Fatal("a missing base directory passed")
	}
	f := filepath.Join(srv.Root, "file")
	_ = os.WriteFile(f, nil, 0o644)
	cfg.BaseDir = f
	if _, err := dial(t, cfg).CheckBaseDir(); err == nil {
		t.Fatal("a file passed as the base directory")
	}
}

func TestDialUsesTheSuppliedDialer(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	cfg := configFor(srv)
	refused := errors.New("refused by policy")
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, refused }
	if err := dialErr(cfg); !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the dialer's refusal", err)
	}
}

func assertOnlyFiles(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(entries); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s holds %v, want %v", dir, got, want)
	}
}

func names(entries []os.DirEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

// A known_hosts line with a marker is not a host key to trust: @revoked
// names a key that must never be accepted. Refused before connecting.
func TestAKnownHostsMarkerIsNotAHostKey(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	for _, marker := range []string{"@revoked", "@cert-authority"} {
		cfg := configFor(srv)
		cfg.HostKey = marker + " * " + strings.TrimSpace(srv.AuthorizedKeyLine())
		err := dialErr(cfg)
		if err == nil || !strings.Contains(err.Error(), "known_hosts line marked "+marker) {
			t.Errorf("%s: want refused as a marked line, got %v", marker, err)
		}
	}
	if n := srv.Accepted(); n != 0 {
		t.Fatalf("a marked host key reached the network (%d connections)", n)
	}
}

// With several host keys the server presents its preferred one; a
// configured key line makes it present that key instead of failing.
func TestAConfiguredKeyLinePinsTheServersKey(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{SecondHostKey: true})
	for name, key := range map[string]ssh.PublicKey{"ed25519": srv.HostKey, "ecdsa": srv.SecondKey} {
		t.Run(name, func(t *testing.T) {
			cfg := configFor(srv)
			cfg.HostKey = string(ssh.MarshalAuthorizedKey(key))
			c := dial(t, cfg)
			if c.HostKey != ssh.FingerprintSHA256(key) {
				t.Fatalf("presented %s, want %s", c.HostKey, ssh.FingerprintSHA256(key))
			}
		})
	}
}

// A download over the limit is refused on the declared size; exactly the
// limit is fine.
func TestADownloadOverTheLimitIsRefused(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{})
	if err := os.WriteFile(filepath.Join(srv.Root, "big.csv"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	for limit, wantErr := range map[int64]bool{999: true, 1000: false} {
		cfg := configFor(srv)
		cfg.MaxDownloadBytes = limit
		_, n, err := dial(t, cfg).Download("big.csv", filepath.Join(t.TempDir(), "big.csv"))
		// Refused on the declared size, before a byte is copied.
		if wantErr && (!errors.Is(err, sftpclient.ErrFileTooLarge) || !strings.Contains(err.Error(), "is 1000 bytes") || n != 0) {
			t.Errorf("limit %d: %d bytes, err = %v; want refused up front on the declared size", limit, n, err)
		}
		if !wantErr && (err != nil || n != 1000) {
			t.Errorf("limit %d: %d bytes, %v", limit, n, err)
		}
	}
}

// The declared size is the server's word. A server that reports a file as
// empty and then sends more than the limit is cut off at the limit anyway.
func TestAServerLyingAboutSizeIsCutOff(t *testing.T) {
	srv := sftptest.Start(t, sftptest.Options{LyingSizes: true})
	if err := os.WriteFile(filepath.Join(srv.Root, "stream.csv"), make([]byte, 50000), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := configFor(srv)
	cfg.MaxDownloadBytes = 10000
	_, n, err := dial(t, cfg).Download(srv.Root+"/stream.csv", filepath.Join(t.TempDir(), "stream.csv"))
	if !errors.Is(err, sftpclient.ErrFileTooLarge) {
		t.Fatalf("err = %v after %d bytes, want ErrFileTooLarge", err, n)
	}
	if n > 10000 {
		t.Fatalf("%d bytes written locally, past the limit", n)
	}
	// Setup check: the size really was hidden, so only the capped copy
	// could have refused it.
	cfg.MaxDownloadBytes = 1 << 20
	if _, n, err := dial(t, cfg).Download(srv.Root+"/stream.csv", filepath.Join(t.TempDir(), "ok.csv")); err != nil || n != 50000 {
		t.Fatalf("setup: under a generous limit got %d bytes, %v", n, err)
	}
}
