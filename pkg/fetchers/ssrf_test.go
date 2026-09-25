package fetchers

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// TestMain relaxes outboundPolicy for this package's tests: every other
// test in this package fetches from httptest.Server (loopback) to
// simulate an external API, not to exercise the SSRF guard itself --
// that's netguard's own job, covered by pkg/netguard's dedicated test
// suite. Production code always runs with outboundPolicy's real
// initializer (netguard.Default, AllowLoopback: false); only this
// package's test binary overrides it.
func TestMain(m *testing.M) {
	SetOutboundPolicyForTesting(netguard.Policy{AllowLoopback: true})
	os.Exit(m.Run())
}

// TestRESTFetcher_BlocksLoopback_WhenPolicyIsDefault proves the guard is
// real by constructing a fetcher with an explicit netguard.Default
// client -- bypassing this package's TestMain relaxation -- and
// confirming a loopback target is still rejected. This is what closes
// the actual finding: previously source_api had no protection at all
// once a request wasn't a relative self-reference.
func TestRESTFetcher_BlocksLoopback_WhenPolicyIsDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	defer srv.Close()

	f := &RESTFetcher{client: netguard.Default.Client(0)}
	_, err := f.Fetch(srv.URL, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected a loopback target to be blocked under the real default policy, got nil error")
	}
}

// TestRESTFetcher_SelfRefClient_PermitsClusterIP is a regression guard for
// the production incident where the bundled sample-data pipeline broke:
// trustedSelfRef resolves a relative URL (e.g. /api/samples/data/x.json)
// against BROKOLI_SERVER_URL, which in a k8s deployment is the in-cluster
// Service DNS name -- it resolves to a ClusterIP, a private address that
// is NOT loopback. selfRefClient originally only set AllowLoopback: true,
// so that ClusterIP was still rejected even though this is exactly the
// one destination trustedSelfRef exists to reach.
//
// This dials a private, non-loopback IP directly through selfRefClient's
// own Transport (same technique as netguard's own DialContext test). The
// subject is whether the policy permits the attempt, so
// netguard.ErrBlockedTarget is the only failure: a refusal, a timeout or
// an unreachable network all mean the policy let the dial through and
// the network answered for itself.
//
// The dial outcome is deliberately not asserted. This test used to
// require the dial to fail, on the assumption that nothing listens at
// this address, which made it depend on the network the suite runs on:
// anywhere 10.43.13.252:8080 answers, a correct policy failed the test.
func TestRESTFetcher_SelfRefClient_PermitsClusterIP(t *testing.T) {
	f := &RESTFetcher{}
	f.ensureClientInitialized(nil)

	transport := f.selfRefClient.Transport.(*http.Transport)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	conn, err := transport.DialContext(ctx, "tcp", net.JoinHostPort("10.43.13.252", "8080"))
	if conn != nil {
		defer conn.Close()
	}
	if errors.Is(err, netguard.ErrBlockedTarget) {
		t.Fatalf("selfRefClient rejected a private ClusterIP-shaped address at the policy level: %v", err)
	}
}
