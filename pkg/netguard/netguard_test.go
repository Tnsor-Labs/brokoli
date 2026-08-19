package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClient_BlocksLoopbackByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := Default.Client(2 * time.Second)
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("expected loopback request to be blocked, got nil error")
	}
	if !errors.Is(err, ErrBlockedTarget) {
		// http.Client wraps dial errors in a *url.Error; unwrap manually
		// in case errors.Is's chain-walking doesn't see through it.
		var urlErr interface{ Unwrap() error }
		if ue, ok := err.(interface{ Unwrap() error }); ok {
			urlErr = ue
		}
		if urlErr == nil || !errors.Is(urlErr.Unwrap(), ErrBlockedTarget) {
			t.Fatalf("expected ErrBlockedTarget, got: %v", err)
		}
	}
}

func TestClient_AllowLoopback_PermitsIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := Policy{AllowLoopback: true}.Client(2 * time.Second)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("expected loopback to be allowed with AllowLoopback: true, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestClient_BlocksCloudMetadataHostname(t *testing.T) {
	client := Default.Client(2 * time.Second)
	req, _ := http.NewRequest(http.MethodGet, "http://metadata.google.internal/computeMetadata/v1/", nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected the metadata hostname to be blocked, got nil error")
	}
}

func TestClient_RedirectToLoopbackIsRejected(t *testing.T) {
	// The redirect target server (would-be attack destination).
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	// A "safe-looking" server that 302s to the loopback target above,
	// simulating a URL that might pass an initial hostname-only check
	// but tries to redirect into a blocked address.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	client := Default.Client(2 * time.Second)
	_, err := client.Get(redirector.URL)
	if err == nil {
		t.Fatal("expected the redirect hop into loopback to be rejected, got nil error")
	}
}

func TestChecker_UnspecifiedAddressBlocked(t *testing.T) {
	if err := Default.checkIP(net.IPv4zero); err == nil {
		t.Fatal("expected 0.0.0.0 to be blocked")
	}
	if err := Default.checkIP(net.IPv6unspecified); err == nil {
		t.Fatal("expected :: to be blocked")
	}
}

func TestChecker_PrivateRangesBlocked(t *testing.T) {
	cases := []string{"10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254"}
	for _, ipStr := range cases {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			t.Fatalf("test bug: %q didn't parse", ipStr)
		}
		if err := Default.checkIP(ip); err == nil {
			t.Errorf("expected %s to be blocked", ipStr)
		}
	}
}

func TestChecker_PublicAddressAllowed(t *testing.T) {
	ip := net.ParseIP("8.8.8.8")
	if err := Default.checkIP(ip); err != nil {
		t.Errorf("expected a public IP to be allowed, got: %v", err)
	}
}

func TestClient_DialContextValidatesTheResolvedIPNotJustTheHostname(t *testing.T) {
	// Regression guard for the DNS-rebinding shape of TOCTOU: a resolver
	// that returns a blocked address must be rejected even though the
	// hostname itself isn't in any blocklist. Simulated by dialing an
	// address directly (bypassing DNS) through the same DialContext the
	// real Client uses, confirming the IP-level check is what's load
	// bearing, not a hostname string comparison alone.
	client := Default.Client(2 * time.Second)
	transport := client.Transport.(*http.Transport)
	_, err := transport.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", "80"))
	if err == nil {
		t.Fatal("expected a direct dial to 127.0.0.1 to be blocked by IP, regardless of hostname")
	}
}
