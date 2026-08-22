package netguard

import (
	"net"
	"testing"
)

// Every path a pipeline can cause traffic on — fetching from an API,
// posting to one, calling a webhook — has to read the same policy.
// While only the fetcher consulted the environment, an operator who
// allowlisted their internal range could read from it but not post back
// to it, with no way to tell why.
func TestOutboundIsResolvedFromTheEnvironment(t *testing.T) {
	resetOutboundForTest()
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_CIDRS", "10.42.0.0/16")

	p := Outbound()
	if err := p.checkIP(net.ParseIP("10.42.0.1")); err != nil {
		t.Fatalf("allowlisted range should be reachable: %v", err)
	}
	if err := p.checkIP(net.ParseIP("10.99.0.1")); err == nil {
		t.Fatal("an unlisted private address should stay blocked")
	}
}

// Resolved once: the policy is read on every outbound request, and
// re-parsing the environment each time would be wasted work.
func TestOutboundIsResolvedOnlyOnce(t *testing.T) {
	resetOutboundForTest()
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_CIDRS", "10.42.0.0/16")
	first := Outbound()

	t.Setenv("BROKOLI_OUTBOUND_ALLOW_CIDRS", "192.168.0.0/16")
	second := Outbound()

	if len(second.AllowedCIDRs) != len(first.AllowedCIDRs) {
		t.Fatal("the policy was re-read after the first resolution")
	}
	if err := second.checkIP(net.ParseIP("10.42.0.1")); err != nil {
		t.Fatalf("the first resolution should still apply: %v", err)
	}
}

// Unconfigured stays closed.
func TestOutboundDefaultsClosed(t *testing.T) {
	resetOutboundForTest()
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_CIDRS", "")
	t.Setenv("BROKOLI_OUTBOUND_ALLOW_PRIVATE", "")

	if err := Outbound().checkIP(net.ParseIP("10.1.2.3")); err == nil {
		t.Fatal("private addresses should be blocked by default")
	}
}
