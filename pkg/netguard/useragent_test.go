package netguard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

/*
 * Every outbound client is built by Policy.Client, so this is where a
 * missing User-Agent was fixed and where it has to stay fixed. Before
 * this, requests went out as Go's default and endpoints rejected them
 * (#737) -- a 406 that named the user's URL rather than the header we
 * never sent.
 */

// captureUA runs a request through Policy.Client and reports what the
// server saw, which is the only thing that matters here.
func captureUA(t *testing.T, decorate func(*http.Request)) string {
	t.Helper()
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
	}))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decorate != nil {
		decorate(req)
	}
	resp, err := Policy{AllowLoopback: true}.Client(10 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return seen
}

// The headline: a request that sets no agent no longer arrives as Go's.
func TestOutboundRequestsIdentifyThemselves(t *testing.T) {
	seen := captureUA(t, nil)

	if strings.Contains(seen, "Go-http-client") {
		t.Fatalf("outbound request still identifies as %q, which is what endpoints reject", seen)
	}
	if !strings.HasPrefix(seen, "brokoli/") {
		t.Errorf("User-Agent = %q, want it to name brokoli", seen)
	}
	if !strings.Contains(seen, "github.com/Tnsor-Labs/brokoli") {
		t.Errorf("User-Agent = %q, want a URL an operator can look up", seen)
	}
}

// A caller that names its own agent keeps it: some endpoints want an
// exact string, and a node config can supply one.
func TestAnExplicitUserAgentWins(t *testing.T) {
	seen := captureUA(t, func(r *http.Request) {
		r.Header.Set("User-Agent", "acme-integration/2.1")
	})

	if seen != "acme-integration/2.1" {
		t.Errorf("User-Agent = %q, want the caller's own string preserved", seen)
	}
}

// The build version reaches the endpoint, so an operator reading their
// access log can tell which release is calling them.
func TestUserAgentCarriesTheBuildVersion(t *testing.T) {
	prev := UserAgent()
	t.Cleanup(func() { userAgent.Store(prev) })

	SetUserAgentVersion("1.2.3")
	if seen := captureUA(t, nil); !strings.Contains(seen, "brokoli/1.2.3") {
		t.Errorf("User-Agent = %q, want it to carry version 1.2.3", seen)
	}
}

// An unstamped build says so instead of pretending to be a release.
func TestUnstampedBuildIdentifiesAsDev(t *testing.T) {
	if got := buildUserAgent(""); !strings.Contains(got, "brokoli/dev") {
		t.Errorf("buildUserAgent(\"\") = %q, want it to report dev", got)
	}
}

// RoundTrip must not mutate the request it is handed. Without the clone
// the caller's own request object grows a header it never set, which a
// retry or a caller inspecting its request afterwards would see.
func TestTheCallersRequestIsNotMutated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Policy{AllowLoopback: true}.Client(10 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := req.Header.Get("User-Agent"); got != "" {
		t.Errorf("the transport wrote %q onto the caller's request; it must set the header on a copy", got)
	}
}

// The policy still applies. A user agent is not a reason to reach
// somewhere the deployment forbids.
func TestUserAgentDoesNotWeakenThePolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	_, err := Policy{}.Client(10 * time.Second).Get(srv.URL)
	if err == nil {
		t.Fatal("the default policy reached loopback; wrapping the transport must not bypass the dialer")
	}
}
