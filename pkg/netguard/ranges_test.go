package netguard

import (
	"errors"
	"net"
	"testing"
)

func cidrs(t *testing.T, cs ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, c := range cs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func check(t *testing.T, p Policy, ip string) error {
	t.Helper()
	parsed := net.ParseIP(ip)
	if parsed == nil {
		t.Fatalf("test bug: %q does not parse", ip)
	}
	return p.checkIP(parsed)
}

// Ranges net.IP's classifiers do not call private, blocked by default and
// opened the same two ways as the private ranges.
func TestSharedAndReservedRangesAreBlocked(t *testing.T) {
	blocked := []string{"0.1.2.3", "0.255.255.255", "100.64.0.1", "100.100.100.100", "100.127.255.254", "192.0.0.8", "64:ff9b:1::1"}
	for _, ip := range blocked {
		if err := check(t, Default, ip); !errors.Is(err, ErrBlockedTarget) {
			t.Errorf("%s: err = %v, want blocked", ip, err)
		}
		if err := check(t, Policy{AllowPrivate: true}, ip); err != nil {
			t.Errorf("%s under AllowPrivate: %v", ip, err)
		}
	}
	// The neighbours of each range are ordinary public addresses.
	for _, ip := range []string{"1.0.0.1", "100.63.255.255", "100.128.0.1", "192.0.1.1", "64:ff9b:2::1"} {
		if err := check(t, Default, ip); err != nil {
			t.Errorf("%s is public: %v", ip, err)
		}
	}
	// Tailscale: allowlisting the range opens it.
	ts := Policy{AllowedCIDRs: cidrs(t, "100.64.0.0/10")}
	if err := check(t, ts, "100.101.102.103"); err != nil {
		t.Errorf("an allowlisted CGNAT address: %v", err)
	}
}

// Cloud metadata endpoints answer with the host's own credentials. No
// broad allowance opens them; only a CIDR naming the single address does.
func TestMetadataEndpointsStayBlocked(t *testing.T) {
	for _, ip := range []string{"169.254.169.254", "169.254.170.2", "fd00:ec2::254", "100.100.100.200"} {
		for name, p := range map[string]Policy{
			"default":               Default,
			"AllowPrivate":          {AllowPrivate: true},
			"AllowPrivate+Loopback": {AllowPrivate: true, AllowLoopback: true},
			"a range containing it": {AllowedCIDRs: cidrs(t, "169.254.0.0/16", "100.64.0.0/10", "fd00::/8")},
		} {
			if err := check(t, p, ip); !errors.Is(err, ErrBlockedTarget) {
				t.Errorf("%s under %s: err = %v, want blocked", ip, name, err)
			}
		}
		bits := "/32"
		if net.ParseIP(ip).To4() == nil {
			bits = "/128"
		}
		if err := check(t, Policy{AllowedCIDRs: cidrs(t, ip+bits)}, ip); err != nil {
			t.Errorf("%s named exactly: %v", ip, err)
		}
	}
	// A link-local neighbour is still opened by AllowPrivate.
	if err := check(t, Policy{AllowPrivate: true}, "169.254.1.1"); err != nil {
		t.Errorf("169.254.1.1 under AllowPrivate: %v", err)
	}
}

// An IPv6 address that routes to an IPv4 one is judged as that IPv4
// address: 64:ff9b::a9fe:a9fe is 169.254.169.254 on any NAT64 network.
func TestIPv4InsideIPv6IsCheckedAsIPv4(t *testing.T) {
	for _, ip := range []string{
		"64:ff9b::a9fe:a9fe", // NAT64 -> 169.254.169.254
		"64:ff9b::7f00:1",    // NAT64 -> 127.0.0.1
		"64:ff9b::a00:1",     // NAT64 -> 10.0.0.1
		"2002:a9fe:a9fe::1",  // 6to4  -> 169.254.169.254
		"2002:a00:1::1",      // 6to4  -> 10.0.0.1
		"::a00:1",            // IPv4-compatible -> 10.0.0.1
		"::6440:1",           // IPv4-compatible -> 100.64.0.1
	} {
		if err := check(t, Default, ip); !errors.Is(err, ErrBlockedTarget) {
			t.Errorf("%s: err = %v, want blocked", ip, err)
		}
	}
	for _, ip := range []string{"64:ff9b::808:808", "2002:808:808::1", "::ffff:8.8.8.8", "2606:4700::1111"} {
		if err := check(t, Default, ip); err != nil {
			t.Errorf("%s routes to a public address: %v", ip, err)
		}
	}
	// The embedded address follows the policy, allowances included.
	if err := check(t, Policy{AllowedCIDRs: cidrs(t, "10.20.0.0/16")}, "64:ff9b::a14:1"); err != nil {
		t.Errorf("NAT64 to an allowlisted private address: %v", err)
	}
	// :: and ::1 are not IPv4-compatible addresses; ::1 keeps its own flag.
	if err := check(t, Policy{AllowLoopback: true}, "::1"); err != nil {
		t.Errorf("::1 under AllowLoopback: %v", err)
	}
	if err := check(t, Default, "::"); err == nil {
		t.Error(":: allowed")
	}
}
