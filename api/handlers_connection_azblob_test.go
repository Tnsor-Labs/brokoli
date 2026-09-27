package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
)

// testAzureBlobThroughHandler creates an azure_blob connection through the
// API -- so its extra is encrypted on save and decrypted for the test, as
// in production -- and runs the connection test on it.
func testAzureBlobThroughHandler(t *testing.T, id string, extra map[string]interface{}) map[string]interface{} {
	t.Helper()
	h, _ := connRoundTripEnv(t)
	r := routeConn(h)
	r.Post("/api/connections/{connId}/test", h.Test)
	encoded, err := json.Marshal(extra)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]interface{}{"conn_id": id, "type": "azure_blob", "extra": string(encoded)}
	if w := doJSON(t, r, "POST", "/api/connections", body); w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", id, w.Code, w.Body.String())
	}
	w := doJSON(t, r, "POST", "/api/connections/"+id+"/test", nil)
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("test %s: %v (%s)", id, err, w.Body.String())
	}
	return out
}

// The connection test must reach the Azure check. Before #687 an
// azure_blob connection fell through to the generic probe, which could
// report success without ever authenticating. Only the Azure validator
// produces this message, so seeing it proves the routing.
func TestAzureBlobConnectionTestIsRoutedToTheRealCheck(t *testing.T) {
	out := testAzureBlobThroughHandler(t, "az-bad-container", map[string]interface{}{
		"account": "acme", "container": "Not_A_Valid_Name", "key": "a2V5",
	})
	if ok, _ := out["success"].(bool); ok {
		t.Fatalf("an invalid container name passed the connection test: %v", out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "container name is invalid") {
		t.Fatalf("error = %q, want the Azure validator's message", msg)
	}
}

// Both directions of the outbound policy: the default refuses a private
// endpoint, and an explicitly allowed CIDR is not refused by the policy.
func TestAzureBlobConnectionTestHonoursOutboundPolicy(t *testing.T) {
	cfg := map[string]interface{}{
		"account": "acme", "container": "exports", "key": "a2V5",
		"endpoint": "http://10.20.0.1:9/acme",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	restore := netguard.SetOutboundForTesting(netguard.Policy{})
	res := testAzureBlob(ctx, cfg)
	restore()
	if msg, _ := res["error"].(string); !strings.Contains(msg, "blocked") {
		t.Fatalf("the default policy did not refuse a private endpoint: %v", res)
	}

	_, allowed, err := net.ParseCIDR("10.20.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	restore = netguard.SetOutboundForTesting(netguard.Policy{AllowedCIDRs: []*net.IPNet{allowed}})
	defer restore()
	// Allowed, the request goes out to an address that never answers; a
	// short deadline is enough to see it was not refused by the policy.
	short, cancelShort := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelShort()
	res = testAzureBlob(short, cfg)
	if msg, _ := res["error"].(string); strings.Contains(msg, "blocked") {
		t.Fatalf("an explicitly allowed CIDR was still refused by the policy: %s", msg)
	}
}

// End to end against Azurite: saved through the API, tested through the
// handler, authenticated for real.
func TestAzureBlobConnectionTestAgainstAzurite(t *testing.T) {
	endpoint := os.Getenv("BROKOLI_TEST_AZURE_BLOB_ENDPOINT")
	if endpoint == "" {
		t.Skip("set BROKOLI_TEST_AZURE_BLOB_ENDPOINT to run the Azurite connection test")
	}
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))

	const account = "devstoreaccount1"
	const key = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
	cred, err := azblob.NewSharedKeyCredential(account, key)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := azblob.NewClientWithSharedKeyCredential(strings.TrimRight(endpoint, "/")+"/", cred, nil)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("api-%d", time.Now().UnixNano()%1_000_000_000)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.CreateContainer(ctx, name, nil); err != nil {
		t.Fatalf("create container: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.DeleteContainer(context.Background(), name, nil) })

	good := testAzureBlobThroughHandler(t, "az-good", map[string]interface{}{
		"account": account, "container": name, "key": key, "endpoint": endpoint,
	})
	if ok, _ := good["success"].(bool); !ok {
		t.Fatalf("correct credentials failed the connection test: %v", good)
	}

	bad := testAzureBlobThroughHandler(t, "az-wrong-key", map[string]interface{}{
		"account": account, "container": name, "key": "d3Jvbmcta2V5LXRoYXQtaXMtbm90LXRoZS1hY2NvdW50cy1rZXk=", "endpoint": endpoint,
	})
	if ok, _ := bad["success"].(bool); ok {
		t.Fatalf("a wrong key passed the connection test: %v", bad)
	}
}

// #752, end to end against Azurite: a connection whose whole extra
// settings -- account, container, key, endpoint -- come from an env://
// reference tests green only if the test resolves the reference. Before,
// it was tested with no extra at all and failed on a missing account.
func TestAzureBlobConnectionTestResolvesAnExtraReference(t *testing.T) {
	endpoint := os.Getenv("BROKOLI_TEST_AZURE_BLOB_ENDPOINT")
	if endpoint == "" {
		t.Skip("set BROKOLI_TEST_AZURE_BLOB_ENDPOINT to run the Azurite connection test")
	}
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))

	const account = "devstoreaccount1"
	const key = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
	cred, err := azblob.NewSharedKeyCredential(account, key)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := azblob.NewClientWithSharedKeyCredential(strings.TrimRight(endpoint, "/")+"/", cred, nil)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ref-%d", time.Now().UnixNano()%1_000_000_000)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.CreateContainer(ctx, name, nil); err != nil {
		t.Fatalf("create container: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.DeleteContainer(context.Background(), name, nil) })

	extra, err := json.Marshal(map[string]string{"account": account, "container": name, "key": key, "endpoint": endpoint})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROKOLI_TEST_752_AZURE_EXTRA", string(extra))
	t.Setenv(secrets.EnvRefAllowEnv, "BROKOLI_TEST_752_AZURE_EXTRA")

	h, _ := connRoundTripEnv(t)
	r := routeConn(h)
	r.Post("/api/connections/{connId}/test", h.Test)
	body := map[string]interface{}{"conn_id": "az-by-ref", "type": "azure_blob", "extra_ref": "env://BROKOLI_TEST_752_AZURE_EXTRA"}
	if w := doJSON(t, r, "POST", "/api/connections", body); w.Code >= 300 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	w := doJSON(t, r, "POST", "/api/connections/az-by-ref/test", nil)
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("test: %v (%s)", err, w.Body.String())
	}
	if ok, _ := out["success"].(bool); !ok {
		t.Fatalf("a connection whose extra settings are an env:// reference failed the test: %v", out)
	}
	if note, _ := out["note"].(string); note == "" {
		t.Errorf("no note that the reference was resolved on the server: %v", out)
	}
}
