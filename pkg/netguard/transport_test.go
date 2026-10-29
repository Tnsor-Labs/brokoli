package netguard

import (
	"testing"
	"time"
)

// Callers build a client per request and drop it. With no idle timeout the
// dropped client's kept-alive connection and its goroutines lived until the
// remote server closed them: 50 dropped clients left 150 goroutines behind.
func TestClientTransportReclaimsIdleConnectionsAndBoundsTLS(t *testing.T) {
	transport := BaseTransport(Default.Client(0))
	if transport.IdleConnTimeout <= 0 {
		t.Error("no idle timeout: a dropped client's connections are never closed on our side")
	}
	if transport.TLSHandshakeTimeout <= 0 {
		t.Error("no TLS handshake timeout: a client with no overall timeout can hang forever")
	}
	if transport.IdleConnTimeout > 5*time.Minute {
		t.Errorf("idle timeout %s holds dropped connections too long", transport.IdleConnTimeout)
	}
}

// Through a proxy the policy would check the proxy's address, not the
// target's, and the target would no longer be checked at all.
func TestClientTransportNeverUsesAProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")
	if BaseTransport(Default.Client(0)).Proxy != nil {
		t.Error("the transport routes through a proxy, which bypasses the dial-time policy check")
	}
}
