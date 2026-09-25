package netguard

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

/*
 * How this process introduces itself on every outbound HTTP request.
 *
 * Nothing set a User-Agent, so every request a pipeline made went out as
 * Go's default "Go-http-client/1.1". A great many endpoints reject that
 * outright: measured against one public API, the Go default answered 406
 * and a browser-like agent answered 200 on the same URL with the same
 * Accept header. Fifteen production pipeline failures in one week came
 * from it, surfacing as an opaque "HTTP request failed: status code 406"
 * that pointed at the user's URL rather than at the header we did not
 * send (#737).
 *
 * This lives in netguard because Policy.Client is the one place outbound
 * HTTP clients are built -- source_api reads, sink_api writes, webhooks,
 * wait-node polls, alerts and notifications all come through it. Setting
 * the header per call site would mean setting it in five places today and
 * missing the sixth that gets added next.
 *
 * Identifying, not impersonating. Sending "Mozilla/5.0" would raise the
 * pass rate and is the wrong thing to ship: it lies to the operator on the
 * other end and stops working the moment anyone looks. A named agent with
 * a URL lets them see Brokoli traffic in their logs and allowlist or
 * rate-limit it deliberately.
 */

// userAgentProductURL points an operator reading their access log at what
// is calling them.
const userAgentProductURL = "+https://github.com/Tnsor-Labs/brokoli"

// userAgent is read on every outbound request and written once at
// startup, so it is atomic rather than a plain string: a test that
// exercises a version change while a request is in flight would otherwise
// be a data race, and the race detector runs over this package.
var userAgent atomic.Value

func init() { userAgent.Store(buildUserAgent("")) }

func buildUserAgent(version string) string {
	// "dev" is what an unstamped build reports elsewhere (main.version),
	// so an unversioned binary is identifiable as such instead of
	// pretending to be a release.
	if version == "" {
		version = "dev"
	}
	return fmt.Sprintf("brokoli/%s (%s)", version, userAgentProductURL)
}

// SetUserAgentVersion stamps the build version into the agent string.
// Called once from cmd.SetVersion, beside the other places the version is
// published, so the agent an endpoint sees names the release calling it.
func SetUserAgentVersion(version string) { userAgent.Store(buildUserAgent(version)) }

// UserAgent returns the string sent on outbound requests that do not
// carry one of their own.
func UserAgent() string {
	v, _ := userAgent.Load().(string)
	return v
}

// userAgentTransport fills in the header when the caller has not set one.
//
// A request that carries its own User-Agent keeps it: a node config can
// name the exact string an endpoint wants, and that has to win over the
// default, the same way the Accept default in the REST fetcher yields to
// a handwritten one.
type userAgentTransport struct{ base http.RoundTripper }

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		// RoundTrip must not modify the request it is given, so the header
		// goes on a copy. Without the clone this mutates a request the
		// caller may still hold, and retries would see it change underneath
		// them.
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", UserAgent())
	}
	return t.base.RoundTrip(req)
}

// BaseTransport returns the *http.Transport underneath a client built by
// Policy.Client.
//
// Client wraps its transport to set the User-Agent, so reaching for
// client.Transport directly yields the wrapper. Anything needing the real
// transport -- to dial through the policy's own DialContext, or to tune
// it -- goes through here instead of type-asserting, which would break
// again the next time the chain gains a layer.
//
// Returns nil if the client was not built by Policy.Client.
func BaseTransport(c *http.Client) *http.Transport {
	if c == nil {
		return nil
	}
	rt := c.Transport
	if wrapped, ok := rt.(*userAgentTransport); ok {
		rt = wrapped.base
	}
	base, _ := rt.(*http.Transport)
	return base
}
