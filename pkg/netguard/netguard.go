// Package netguard is the one sanctioned way to build an *http.Client for
// a server-initiated outbound request (source_api, sink_api, webhook
// hooks, connection tests, and anything else a pipeline or its config can
// point at an arbitrary URL). See ADR-022 for the full context: this
// replaces two separate, near-identical, both-incomplete SSRF guards
// (pkg/fetchers.isBlockedHost and api.validateExternalURL) that each
// allowed loopback unconditionally, resolved a hostname once and let the
// HTTP client re-resolve it independently at request time (a
// DNS-rebinding TOCTOU), and never re-validated a redirect target.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrBlockedTarget is returned (wrapped, for the real reason) whenever a
// resolved address or hostname fails policy.
var ErrBlockedTarget = errors.New("request target is a blocked private/internal address")

// blockedHostnames are cloud metadata hostnames blocked by name,
// independent of what they resolve to today -- they are never a
// legitimate target for a pipeline/webhook/connection-test request.
var blockedHostnames = []string{
	"metadata.google.internal",
	"metadata.internal",
}

// Policy controls what a Client built from it will and won't dial.
type Policy struct {
	// AllowLoopback opts into reaching 127.0.0.0/8 and ::1. Off by
	// default. Turn it on only for a caller with its own narrow,
	// deliberate reason.
	AllowLoopback bool

	// AllowPrivate opts into reaching RFC1918/link-local addresses
	// (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16) and the
	// shared/reserved ranges in sharedRanges. Off by default. It never
	// opens a cloud metadata endpoint (metadataAddrs).
	//
	// The one known legitimate case for either of these two flags
	// together is pkg/fetchers' trustedSelfRef path (a source_api node
	// self-referencing the Brokoli server for sample data): in a plain
	// docker/bare-metal deployment BROKOLI_SERVER_URL resolves to
	// loopback, but in k8s it's the in-cluster Service DNS name, which
	// resolves to a ClusterIP -- a private address, not a loopback one.
	// Both flags are needed to cover both deployment shapes for that
	// one resolved, operator-controlled destination. Turn this on
	// elsewhere only for a caller with its own equally narrow, equally
	// deliberate reason.
	AllowPrivate bool

	// AllowedCIDRs permits specific private ranges without opening all
	// of them. A self-hosted deployment whose APIs live on the internal
	// network needs to reach 10.20.0.0/16; it does not need to reach
	// the cloud metadata endpoint or every other pod in its cluster,
	// and AllowPrivate cannot express that difference.
	//
	// A range listed here is allowed even when AllowPrivate is off, so
	// this is the narrow tool: name what you need, leave the rest
	// blocked. Loopback still requires AllowLoopback, and the blocked
	// hostname list still applies by name.
	AllowedCIDRs []*net.IPNet
}

// allowedByCIDR reports whether an address falls inside one of the
// explicitly permitted ranges.
func (p Policy) allowedByCIDR(ip net.IP) bool {
	for _, n := range p.AllowedCIDRs {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

var (
	outboundOnce   sync.Once
	outboundPolicy Policy
)

// Outbound is the operator-configured policy for traffic a pipeline
// causes: fetching from an API, posting to one, calling a webhook.
//
// Resolved once from the environment. Every one of those paths has to
// agree — an allowlist that lets a pipeline read from an internal
// service but not write back to it is a confusing half-permission, and
// that is what happened when only the fetcher consulted the
// environment while the sink and webhook paths used the closed default.
func Outbound() Policy {
	outboundOnce.Do(func() { outboundPolicy = FromEnv() })
	return outboundPolicy
}

// FromEnv builds the outbound policy an operator configured.
//
// Default is the safe one: nothing private, nothing loopback. Two
// environment variables widen it, because a self-hosted install whose
// source systems sit on the internal network otherwise cannot reach
// them at all, and the only alternative was a test-only override.
//
//	BROKOLI_OUTBOUND_ALLOW_CIDRS=10.20.0.0/16,192.168.5.0/24
//	    Permit exactly these ranges. Preferred: it states what the
//	    deployment actually talks to.
//	BROKOLI_OUTBOUND_ALLOW_PRIVATE=true
//	    Permit every RFC1918 and link-local address. Blunt, and unsafe
//	    on a multi-tenant instance where a pipeline author is not
//	    necessarily trusted with the cluster's internal network.
//	BROKOLI_OUTBOUND_ALLOW_LOOPBACK=true
//	    Permit loopback explicitly. Intended for local integration services;
//	    it is separate from private-network access and remains off by default.
//
// An unparseable CIDR is skipped with a warning rather than silently
// widening or narrowing what the operator asked for.
func FromEnv() Policy {
	p := Default
	if v := os.Getenv("BROKOLI_OUTBOUND_ALLOW_PRIVATE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			p.AllowPrivate = b
		}
	}
	if v := os.Getenv("BROKOLI_OUTBOUND_ALLOW_LOOPBACK"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			p.AllowLoopback = b
		}
	}
	if v := os.Getenv("BROKOLI_OUTBOUND_ALLOW_CIDRS"); v != "" {
		for _, raw := range strings.Split(v, ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			_, network, err := net.ParseCIDR(raw)
			if err != nil {
				log.Printf("netguard: ignoring invalid CIDR %q in BROKOLI_OUTBOUND_ALLOW_CIDRS: %v", raw, err)
				continue
			}
			p.AllowedCIDRs = append(p.AllowedCIDRs, network)
		}
	}
	return p
}

// sharedRanges are IPv4 ranges net.IP's classifiers do not call private
// but a pipeline has no business reaching by default: "this network"
// (0.0.0.0/8, of which only the first address is IsUnspecified),
// carrier-grade NAT (100.64.0.0/10: Tailscale gives every device an
// address there, and one cloud's metadata endpoint lives there too), IETF
// protocol assignments (192.0.0.0/24), and local-use NAT64
// (64:ff9b:1::/48, whose IPv4 embedding position varies by prefix length,
// so it cannot be unpacked the way the well-known prefix can). Like the
// private ranges, AllowPrivate or an allowlisted CIDR opens them.
var sharedRanges = mustParseCIDRs("0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "64:ff9b:1::/48")

// metadataAddrs are cloud instance-metadata endpoints: AWS, GCP, Azure and
// most others at 169.254.169.254, AWS's IPv6 address and its ECS task
// credentials endpoint, and Alibaba Cloud's. They answer with credentials
// for the machine Brokoli runs on, so they stay blocked even under
// AllowPrivate, and an allowlisted range that merely contains one does
// not open it: only a CIDR naming that single address does.
var metadataAddrs = []net.IP{
	net.ParseIP("169.254.169.254"),
	net.ParseIP("169.254.170.2"),
	net.ParseIP("fd00:ec2::254"),
	net.ParseIP("100.100.100.200"),
}

var (
	nat64WellKnown = mustParseCIDRs("64:ff9b::/96")[0]
	sixToFour      = mustParseCIDRs("2002::/16")[0]
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

func inAny(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// embeddedIPv4 returns the IPv4 address an IPv6 address routes to, for the
// forms that carry one: well-known NAT64 (64:ff9b::/96), 6to4 (2002::/16)
// and the deprecated IPv4-compatible form (::a.b.c.d). Without this,
// 64:ff9b::a9fe:a9fe reaches 169.254.169.254 on any network with NAT64
// while looking like a public IPv6 address. IPv4-mapped addresses
// (::ffff:a.b.c.d) need no help: net.IP's classifiers already see
// through them.
func embeddedIPv4(ip net.IP) net.IP {
	if ip.To4() != nil {
		return nil
	}
	b := ip.To16()
	if b == nil {
		return nil
	}
	switch {
	case nat64WellKnown.Contains(b):
		return net.IPv4(b[12], b[13], b[14], b[15])
	case sixToFour.Contains(b):
		return net.IPv4(b[2], b[3], b[4], b[5])
	}
	for _, x := range b[:12] {
		if x != 0 {
			return nil
		}
	}
	// :: and ::1 are the unspecified and loopback addresses, not
	// IPv4-compatible ones, and have their own rules below.
	if b[12] == 0 && b[13] == 0 && b[14] == 0 && b[15] <= 1 {
		return nil
	}
	return net.IPv4(b[12], b[13], b[14], b[15])
}

// namesExactly reports whether an allowlisted CIDR is exactly this single
// address, the only way a metadata endpoint is opened.
func (p Policy) namesExactly(ip net.IP) bool {
	for _, n := range p.AllowedCIDRs {
		if n == nil {
			continue
		}
		ones, bits := n.Mask.Size()
		if ones == bits && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func (p Policy) checkIP(ip net.IP) error {
	if v4 := embeddedIPv4(ip); v4 != nil {
		if err := p.checkIP(v4); err != nil {
			return fmt.Errorf("%w (reached through %s)", err, ip)
		}
	}
	for _, m := range metadataAddrs {
		if ip.Equal(m) && !p.namesExactly(m) {
			return fmt.Errorf("%w: %s (cloud metadata endpoint)", ErrBlockedTarget, ip)
		}
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("%w: %s (unspecified)", ErrBlockedTarget, ip)
	}
	if ip.IsLoopback() {
		if p.AllowLoopback {
			return nil
		}
		return fmt.Errorf("%w: %s (loopback)", ErrBlockedTarget, ip)
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		if p.AllowPrivate || p.allowedByCIDR(ip) {
			return nil
		}
		return fmt.Errorf("%w: %s (private/link-local)", ErrBlockedTarget, ip)
	}
	if inAny(sharedRanges, ip) {
		if p.AllowPrivate || p.allowedByCIDR(ip) {
			return nil
		}
		return fmt.Errorf("%w: %s (shared/reserved range)", ErrBlockedTarget, ip)
	}
	return nil
}

func (p Policy) checkHost(host string) error {
	for _, b := range blockedHostnames {
		if strings.EqualFold(host, b) {
			return fmt.Errorf("%w: %s", ErrBlockedTarget, host)
		}
	}
	return nil
}

// DialContext connects to addr only if this policy allows the address it
// resolves to, and dials that exact checked IP.
//
// The same check-then-dial Client uses, exported for protocols that are
// not HTTP -- SSH for SFTP delivery is the first. One implementation for
// both, rather than a copy for each protocol, so the DNS-rebinding
// protection cannot hold for HTTP and quietly lapse for everything else:
// the IP that was checked is the IP that is dialed, never the hostname
// resolved a second time.
func (p Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if err := p.checkHost(host); err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, ipAddr := range ips {
		if err := p.checkIP(ipAddr.IP); err != nil {
			lastErr = err
			continue
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%w: no addresses resolved for %s", ErrBlockedTarget, host)
	}
	return nil, lastErr
}

// Client returns an *http.Client that only ever connects to an address
// this policy allows.
//
// The check runs inside the Transport's DialContext, against the exact
// resolved IP the connection is about to be made to -- not a hostname
// re-resolved a second time by the HTTP stack after an earlier,
// separate check passed. That's what closes the DNS-rebinding gap: there
// is no window between "validate" and "connect" where a different
// answer from DNS can substitute a blocked address for the one that was
// checked, because the validated net.IPAddr is what's dialed, not the
// hostname again.
//
// Redirects are covered by the same mechanism, not a special case: every
// redirect Go's http.Client follows is a fresh request through this same
// Transport, so DialContext (and therefore this policy) runs again for
// each hop. CheckRedirect below adds a fast, friendly rejection at the
// hostname level before DNS is even consulted, plus a hop-count cap --
// belt and suspenders on top of the dial-time check, not the only thing
// enforcing it.
func (p Policy) Client(timeout time.Duration) *http.Client {
	safeDial := p.DialContext

	return &http.Client{
		Timeout: timeout,
		// Every outbound client is built here, so this is where the
		// process says who it is (see useragent.go). Wrapping the
		// transport covers the request sites uniformly instead of relying
		// on each one to remember.
		Transport: &userAgentTransport{base: &http.Transport{
			DialContext: safeDial,
		}},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return p.checkHost(req.URL.Hostname())
		},
	}
}

// Default is the policy every outbound pipeline/hook/connection-test
// caller should use unless it has its own narrow, documented reason not
// to (see AllowLoopback's doc comment).
var Default = Policy{AllowLoopback: false}

// resetOutboundForTest clears the memoised policy so a test can resolve
// it again under different environment settings.
// SetOutboundForTesting overrides the memoised outbound policy and returns a
// function restoring it, so a test in another package can exercise code paths
// that consult Outbound() without depending on process-wide env ordering.
func SetOutboundForTesting(p Policy) (restore func()) {
	prev := outboundPolicy
	outboundOnce = sync.Once{}
	outboundOnce.Do(func() { outboundPolicy = p })
	return func() {
		// Re-prime rather than restoring the saved Once: a sync.Once cannot be
		// copied, and go vet is right to say so.
		outboundOnce = sync.Once{}
		outboundOnce.Do(func() { outboundPolicy = prev })
	}
}

func resetOutboundForTest() {
	outboundOnce = sync.Once{}
	outboundPolicy = Policy{}
}
