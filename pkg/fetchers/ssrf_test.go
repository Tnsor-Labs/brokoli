package fetchers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

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
