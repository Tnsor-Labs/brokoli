package api

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// An operator whose internal services sit on a private range widens the
// outbound policy with BROKOLI_OUTBOUND_ALLOW_CIDRS. Pipeline nodes honour it
// -- source_api, sink_api, and hooks all go through netguard.Outbound(). The
// connection test button used the hardcoded netguard.Default instead, so it
// reported a connection as unreachable while pipelines using that same
// connection ran fine. A test that fails on a working connection is worse
// than no test button.
func TestConnectionTestHonoursOutboundPolicy(t *testing.T) {
	_, private, err := net.ParseCIDR("10.20.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	restore := netguard.SetOutboundForTesting(netguard.Policy{
		AllowedCIDRs: []*net.IPNet{private},
	})
	defer restore()

	// 10.20.0.1 is inside the allowed range. Nothing is listening, so the
	// request fails -- but it must fail on connectivity, not on policy.
	c := &models.Connection{Type: "http", Host: "10.20.0.1", Port: 9}
	result := testHTTPAuth(context.Background(), c, nil)

	errMsg, _ := result["error"].(string)
	if strings.Contains(errMsg, "blocked") {
		t.Errorf("an explicitly allowed CIDR was still refused by the connection test: %s", errMsg)
	}
}

// The other direction: widening the policy for one range must not turn the
// guard off. An address outside the allowlist stays blocked.
func TestConnectionTestStillBlocksOutsideTheAllowlist(t *testing.T) {
	_, private, err := net.ParseCIDR("10.20.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	restore := netguard.SetOutboundForTesting(netguard.Policy{
		AllowedCIDRs: []*net.IPNet{private},
	})
	defer restore()

	c := &models.Connection{Type: "http", Host: "127.0.0.1", Port: 1}
	result := testHTTPAuth(context.Background(), c, nil)

	if success, _ := result["success"].(bool); success {
		t.Fatal("loopback should still be refused when only 10.20.0.0/16 is allowed")
	}
	errMsg, _ := result["error"].(string)
	if !strings.Contains(errMsg, "blocked") {
		t.Errorf("error = %q, want it to say the target was blocked", errMsg)
	}
}

// TestS3ConnectionTestHonoursOutboundPolicy verifies that the S3 connection
// test uses the operator-configured outbound policy. The crafted bucket puts
// 10.20.0.1 in the URL authority; connectivity may fail, but the policy must
// not reject an explicitly allowed CIDR.
func TestS3ConnectionTestHonoursOutboundPolicy(t *testing.T) {
	_, private, err := net.ParseCIDR("10.20.0.0/16")
	if err != nil {
		t.Fatal(err)
	}

	restore := netguard.SetOutboundForTesting(netguard.Policy{
		AllowedCIDRs: []*net.IPNet{private},
	})
	defer restore()

	result := testS3(context.Background(), map[string]interface{}{
		"bucket":     "ignored@10.20.0.1:9/",
		"region":     "us-east-1",
		"access_key": "test-key",
		"secret_key": "test-secret",
	})

	errMsg, _ := result["error"].(string)
	if strings.Contains(errMsg, "blocked") {
		t.Errorf(
			"an explicitly allowed CIDR was still refused by the S3 connection test: %s",
			errMsg,
		)
	}
}
