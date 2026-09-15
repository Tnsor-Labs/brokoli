package netguard

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// The raw dialer enforces the same policy as the HTTP client. SFTP
// delivery dials SSH through it, so a policy that held for HTTP and not
// here would leave every non-HTTP protocol unguarded.

func TestDialContextRefusesABlockedAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Loopback is refused unless the policy opts in.
	_, err = Policy{}.DialContext(ctx, "tcp", ln.Addr().String())
	if !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("dialing loopback with the default policy: err = %v, want ErrBlockedTarget", err)
	}

	// And allowed when it does: the check is a policy, not a blanket refusal.
	conn, err := Policy{AllowLoopback: true}.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialing loopback with AllowLoopback: %v", err)
	}
	conn.Close()
}

func TestDialContextRefusesABlockedHostname(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, h := range blockedHostnames {
		if _, err := (Policy{AllowLoopback: true, AllowPrivate: true}).DialContext(ctx, "tcp", net.JoinHostPort(h, "22")); !errors.Is(err, ErrBlockedTarget) {
			t.Errorf("dialing %s: err = %v, want ErrBlockedTarget before any DNS lookup", h, err)
		}
	}
}
