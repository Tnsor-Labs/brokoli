package netguard

import (
	"net"
	"testing"
)

// The default stays closed: nothing private, nothing loopback. A
// misconfigured or unconfigured instance must not reach the internal
// network.
func TestFromEnvDefaultsClosed(t *testing.T) {
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_PRIVATE", "")
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_CIDRS", "")

	p := FromEnv()
	if p.AllowPrivate || p.AllowLoopback || len(p.AllowedCIDRs) != 0 {
		t.Fatalf("expected a closed default, got %+v", p)
	}
	if err := p.checkIP(net.ParseIP("10.1.2.3")); err == nil {
		t.Fatal("expected a private address to be blocked by default")
	}
}

// A self-hosted deployment names the range its APIs live on, and only
// that range opens.
func TestFromEnvAllowsListedCIDROnly(t *testing.T) {
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_CIDRS", "10.20.0.0/16")

	p := FromEnv()
	if err := p.checkIP(net.ParseIP("10.20.5.9")); err != nil {
		t.Fatalf("expected the listed range to be allowed: %v", err)
	}
	if err := p.checkIP(net.ParseIP("10.99.5.9")); err == nil {
		t.Fatal("expected an unlisted private address to stay blocked")
	}
	if err := p.checkIP(net.ParseIP("192.168.1.1")); err == nil {
		t.Fatal("expected a different private range to stay blocked")
	}
}

// The cloud metadata endpoint is not opened by a private-range
// allowlist that does not mention it.
func TestFromEnvKeepsMetadataBlockedUnlessNamed(t *testing.T) {
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_CIDRS", "10.20.0.0/16")
	p := FromEnv()
	if err := p.checkIP(net.ParseIP("169.254.169.254")); err == nil {
		t.Fatal("expected the metadata address to stay blocked")
	}
}

// Several ranges, and whitespace around them, are accepted.
func TestFromEnvParsesMultipleCIDRs(t *testing.T) {
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_CIDRS", "10.20.0.0/16, 192.168.5.0/24 ")
	p := FromEnv()
	if len(p.AllowedCIDRs) != 2 {
		t.Fatalf("expected 2 ranges, got %d", len(p.AllowedCIDRs))
	}
	if err := p.checkIP(net.ParseIP("192.168.5.7")); err != nil {
		t.Fatalf("second range should be allowed: %v", err)
	}
}

// An unparseable entry is skipped, and the valid ones still apply —
// a typo must not silently disable the whole allowlist.
func TestFromEnvSkipsInvalidCIDR(t *testing.T) {
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_CIDRS", "not-a-cidr,10.20.0.0/16")
	p := FromEnv()
	if len(p.AllowedCIDRs) != 1 {
		t.Fatalf("expected the valid range to survive, got %d", len(p.AllowedCIDRs))
	}
	if err := p.checkIP(net.ParseIP("10.20.0.1")); err != nil {
		t.Fatalf("valid range should still be allowed: %v", err)
	}
}

// The blunt switch still exists for deployments that want it.
func TestFromEnvAllowPrivateOpensRFC1918(t *testing.T) {
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_PRIVATE", "true")
	p := FromEnv()
	for _, ip := range []string{"10.1.1.1", "172.16.0.5", "192.168.9.9"} {
		if err := p.checkIP(net.ParseIP(ip)); err != nil {
			t.Fatalf("%s should be allowed: %v", ip, err)
		}
	}
}

// Public addresses are unaffected either way.
func TestFromEnvLeavesPublicAddressesAlone(t *testing.T) {
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_CIDRS", "10.20.0.0/16")
	if err := FromEnv().checkIP(net.ParseIP("93.184.216.34")); err != nil {
		t.Fatalf("public address should be allowed: %v", err)
	}
}

// Loopback still needs its own flag — the CIDR list does not imply it.
func TestFromEnvDoesNotOpenLoopback(t *testing.T) {
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_PRIVATE", "true")
	if err := FromEnv().checkIP(net.ParseIP("127.0.0.1")); err == nil {
		t.Fatal("expected loopback to remain blocked")
	}
}
