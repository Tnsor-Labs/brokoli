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
	"net"
	"net/http"
	"strings"
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
	// (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16).
	// Off by default.
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
}

func (p Policy) checkIP(ip net.IP) error {
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
		if p.AllowPrivate {
			return nil
		}
		return fmt.Errorf("%w: %s (private/link-local)", ErrBlockedTarget, ip)
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
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	safeDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
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

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: safeDial,
		},
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
