package engine

import (
	"os"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/fetchers"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// TestMain relaxes the outbound SSRF policy pkg/fetchers.RESTFetcher
// uses (see ADR-022 / netguard) for this package's own tests: pipeline
// integration tests exercise real source_api nodes against
// httptest.Server -- i.e. loopback -- to simulate an external API, not
// to test the SSRF guard itself. That's covered by pkg/netguard's and
// pkg/fetchers' own dedicated test suites. Production code is
// unaffected: this only changes fetchers.outboundPolicy inside this
// package's test binary.
func TestMain(m *testing.M) {
	fetchers.SetOutboundPolicyForTesting(netguard.Policy{AllowLoopback: true})
	os.Exit(m.Run())
}
