package engine

import (
	"context"
	"database/sql"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

const databricksTestToken = "dapiSECRET0123456789abcdef"

func databricksTestURI(host string, port int, path string) string {
	c := &models.Connection{Type: models.ConnTypeDatabricks, Host: host, Port: port, Schema: path, Password: databricksTestToken}
	return c.BuildURI()
}

// A host saved with its port, a scheme or a path is the common mistake, and
// with the upstream driver each one put the token into the error: it parses
// the DSN with net/url, and a url.Error quotes its whole input. The control
// proves each input really is that footgun, so the assertion is not vacuous.
func TestDatabricksMalformedHostNeverEchoesTheToken(t *testing.T) {
	for _, host := range []string{
		"workspace.cloud.databricks.com:443",
		"https://workspace.cloud.databricks.com",
		"workspace.cloud.databricks.com/sql",
		"bad host",
	} {
		t.Run(host, func(t *testing.T) {
			uri := databricksTestURI(host, 0, "/sql/1.0/warehouses/wh")

			// Control: the upstream driver on the DSN this connector first
			// gave it echoes the token for every one of these inputs.
			if _, err := sql.Open("databricks", strings.TrimPrefix(uri, "databricks://")); err == nil ||
				!strings.Contains(err.Error(), databricksTestToken) {
				t.Fatalf("control: expected the upstream driver to echo the token, err = %v", err)
			}

			_, err := QueryDatabase(uri, "SELECT 1")
			if err == nil {
				t.Fatal("a malformed host was accepted")
			}
			if strings.Contains(err.Error(), databricksTestToken) {
				t.Fatalf("the token reached the error: %v", err)
			}
			if !strings.Contains(err.Error(), "bare hostname") {
				t.Fatalf("err = %v, want the host explained", err)
			}
			_, _, err = StreamQueryDatabase(context.Background(), uri, "SELECT 1", 0, func(*common.DataSet) error { return nil })
			if err == nil || strings.Contains(err.Error(), databricksTestToken) {
				t.Fatalf("streamed: err = %v", err)
			}
		})
	}
}

func TestDatabricksConnOptionsRefuseWhatTheyDoNotKnow(t *testing.T) {
	base := "databricks://token:" + databricksTestToken + "@workspace.cloud.databricks.com:443"
	for name, tc := range map[string]struct{ uri, want string }{
		"valid":             {base + "/sql/1.0/warehouses/wh?catalog=main&schema=raw&maxRows=100&useCloudFetch=false", ""},
		"no token":          {"databricks://token:@workspace.cloud.databricks.com:443/sql/1.0/warehouses/wh", "personal access token"},
		"no path":           {base, "HTTP path"},
		"path escape":       {base + "/sql/../admin", "HTTP path"},
		"unknown option":    {base + "/sql/1.0/warehouses/wh?authType=OauthU2M", `"authType" is not supported`},
		"bad number":        {base + "/sql/1.0/warehouses/wh?maxRows=many", "whole number"},
		"zero threads":      {base + "/sql/1.0/warehouses/wh?maxDownloadThreads=0", "positive"},
		"no timeout":        {base + "/sql/1.0/warehouses/wh?timeout=0", ""},
		"bad bool":          {base + "/sql/1.0/warehouses/wh?useCloudFetch=maybe", "true or false"},
		"another scheme":    {"https://token:" + databricksTestToken + "@workspace.cloud.databricks.com/sql/1.0/warehouses/wh", "bare hostname"},
		"port out of range": {"databricks://token:" + databricksTestToken + "@workspace.cloud.databricks.com:70000/sql/1.0/warehouses/wh", "1 to 65535"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := databricksConnOptions(tc.uri)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), databricksTestToken) {
				t.Fatalf("the token reached the error: %v", err)
			}
		})
	}
}

// countingListener accepts TCP connections, counts them and hangs up: enough
// to tell whether the driver dialled at all, which is what the outbound
// policy decides.
func countingListener(t *testing.T) (port int, accepted *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted = &atomic.Int64{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, accepted
}

// Every request goes through the outbound policy: under the default policy a
// warehouse on loopback is never dialled, and allowing loopback is what lets
// the same connection reach it. The second half is the control that shows
// the first is the policy's doing and not an unreachable address.
func TestDatabricksGoesThroughTheNetworkPolicy(t *testing.T) {
	port, accepted := countingListener(t)
	uri := databricksTestURI("127.0.0.1", port, "/sql/1.0/warehouses/wh")

	ping := func() error {
		driverName, dsn, err := DetectDriver(uri)
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open(driverName, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		// The driver retries a refused dial with backoff, so the refusal
		// surfaces as the deadline; the dial count is the evidence.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return db.PingContext(ctx)
	}

	restore := netguard.SetOutboundForTesting(netguard.Policy{})
	err := ping()
	restore()
	if err == nil {
		t.Fatal("ping succeeded against a listener that hangs up")
	}
	if n := accepted.Load(); n != 0 {
		t.Fatalf("the default policy let the driver dial loopback %d time(s)", n)
	}

	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	_ = ping()
	if accepted.Load() == 0 {
		t.Fatal("with loopback allowed the driver never dialled: the control did not run")
	}
}

// Writes are refused by name on every path that reaches the database,
// before any connection is opened: the driver has no transactions, so a
// failed write could not be undone.
func TestDatabricksWritesAreRefusedByName(t *testing.T) {
	uri := databricksTestURI("workspace.cloud.databricks.com", 0, "/sql/1.0/warehouses/wh")
	for _, mode := range []string{"", ModeAppend, ModeOverwrite, ModeUpsert, "replace"} {
		if err := refuseUnearnedWrite(uri, mode); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("mode %q: err = %v, want the read-only refusal", mode, err)
		}
	}
	if _, err := ExecuteSQL(uri, "CREATE TABLE t (a INT)"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("ExecuteSQL: err = %v, want the read-only refusal", err)
	}
	// Other backends are untouched by the refusal.
	if err := refuseDatabricksWrite("postgres://u:p@h/db"); err != nil {
		t.Errorf("postgres: %v", err)
	}
}

func TestDatabricksSinkIsRefusedAtValidation(t *testing.T) {
	uri := databricksTestURI("workspace.cloud.databricks.com", 0, "/sql/1.0/warehouses/wh")
	p := &models.Pipeline{Name: "p", Nodes: []models.Node{{
		ID: "sink", Name: "Write", Type: models.NodeTypeSinkDB,
		Config: map[string]interface{}{"uri": uri, "table": "t", "mode": "append"},
	}}}
	ve := ValidatePipeline(p)
	if ve == nil || !strings.Contains(ve.Error(), "read-only") {
		t.Fatalf("err = %v, want the read-only refusal", ve)
	}
	if strings.Contains(ve.Error(), databricksTestToken) {
		t.Fatalf("the token reached the validation error: %v", ve)
	}
}

func TestDatabricksDefaultPortIs443(t *testing.T) {
	uri := databricksTestURI("workspace.cloud.databricks.com", 0, "sql/1.0/warehouses/wh")
	if !strings.Contains(uri, "@workspace.cloud.databricks.com:"+strconv.Itoa(443)+"/sql/1.0/warehouses/wh") {
		t.Fatalf("uri = %q", uri)
	}
}
