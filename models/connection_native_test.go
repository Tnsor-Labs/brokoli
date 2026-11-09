package models

import (
	"strings"
	"testing"
)

func TestNativeADBCEndpointMySQL(t *testing.T) {
	c := Connection{ConnID: "m", Type: ConnTypeMySQL, Host: "db.example.com", Schema: "app", Login: "loader", Password: "p@ss:w/rd",
		Extra: `{"tls":"true","charset":"utf8mb4"}`}
	got, err := c.NativeADBCEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if got.URI != "mysql://db.example.com:3306/app?charset=utf8mb4&tls=true" {
		t.Errorf("URI = %q", got.URI)
	}
	if got.Username != "loader" || got.Password != "p@ss:w/rd" {
		t.Errorf("credentials = %q/%q", got.Username, got.Password)
	}
	if strings.Contains(got.URI, "loader") || strings.Contains(got.URI, "p@ss") {
		t.Errorf("credentials in the URI: %q", got.URI)
	}
	c.Port = 3307
	if got, _ := c.NativeADBCEndpoint(); !strings.HasPrefix(got.URI, "mysql://db.example.com:3307/app") {
		t.Errorf("explicit port not used: %q", got.URI)
	}
}

func TestNativeADBCEndpointClickHouse(t *testing.T) {
	c := Connection{ConnID: "c", Type: ConnTypeClickHouse, Host: "ch.example.com", Schema: "analytics", Login: "reader", Password: "secret-value",
		Extra: `{"dial_timeout":"5s","compress":"true"}`}
	got, err := c.NativeADBCEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	// The clickhouse-go options would reach the server as settings; they
	// must not be passed.
	if got.URI != "http://ch.example.com:8123/?database=analytics" {
		t.Errorf("URI = %q", got.URI)
	}
	if got.Username != "reader" || got.Password != "secret-value" {
		t.Errorf("credentials = %q/%q", got.Username, got.Password)
	}
	c.Extra = `{"secure":true}`
	if got, _ := c.NativeADBCEndpoint(); got.URI != "https://ch.example.com:8443/?database=analytics" {
		t.Errorf("secure URI = %q", got.URI)
	}
}

// Port 9000 is ClickHouse's native protocol, which the legacy driver uses
// and the native one does not: a connection pinned with it is refused with
// a reason, not sent to fail on an HTTP request.
func TestNativeADBCRefusesClickHouseNativePort(t *testing.T) {
	for _, port := range []int{9000, 9440} {
		c := Connection{ConnID: "c", Type: ConnTypeClickHouse, Host: "ch", Port: port}
		if p := c.NativeADBCProblem(); !strings.Contains(p, "HTTP interface") {
			t.Errorf("port %d problem = %q", port, p)
		}
		if _, err := c.NativeADBCEndpoint(); err == nil {
			t.Errorf("port %d accepted", port)
		}
	}
	if p := (&Connection{Type: ConnTypeClickHouse, Host: "ch", Port: 8123}).NativeADBCProblem(); p != "" {
		t.Errorf("port 8123 refused: %q", p)
	}
}

// Every other type keeps the URI it already builds.
func TestNativeADBCEndpointOtherTypesUseTheirURI(t *testing.T) {
	c := Connection{ConnID: "p", Type: ConnTypePostgres, Host: "pg", Schema: "db", Login: "u", Password: "pw"}
	got, err := c.NativeADBCEndpoint()
	if err != nil || got.URI != c.BuildURI() || got.Username != "" || got.Password != "" {
		t.Fatalf("postgres endpoint = %+v, %v", got, err)
	}
}

// An Oracle connection pinned to the native driver connects to its service
// with the credentials as options, never inside the URI, and with none of
// go-ora's driver options.
func TestNativeADBCEndpointOracle(t *testing.T) {
	c := Connection{ConnID: "o", Type: ConnTypeOracle, Host: "ora.example.com", Schema: "FREEPDB1", Login: "etl_reader", Password: "p@ss:w/rd"}
	got, err := c.NativeADBCEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if got.URI != "oracle://ora.example.com:1521/FREEPDB1" {
		t.Errorf("URI = %q", got.URI)
	}
	if got.Username != "etl_reader" || got.Password != "p@ss:w/rd" {
		t.Errorf("credentials = %q/%q", got.Username, got.Password)
	}
	c.Port = 1522
	if got, _ := c.NativeADBCEndpoint(); got.URI != "oracle://ora.example.com:1522/FREEPDB1" {
		t.Errorf("explicit port not used: %q", got.URI)
	}
	c.Extra = `{"adbc_options":{"oracle.call_timeout_ms":"30000","oracle.unconstrained_number":"decimal"}}`
	if p := c.NativeADBCProblem(); p != "" {
		t.Errorf("allowed adbc_options refused: %q", p)
	}
}

// Each setting the native Oracle driver cannot honour is refused with its
// name, so a pinned connection never connects differently from what it
// says (without TLS, say, or to a different server).
func TestNativeADBCRefusesOracleSettingsItCannotHonour(t *testing.T) {
	base := Connection{ConnID: "o", Type: ConnTypeOracle, Host: "ora", Schema: "FREEPDB1"}
	for _, tc := range []struct {
		name, host, schema, extra, want string
	}{
		{"no host", " ", "FREEPDB1", "", "a host is required"},
		{"no service", "ora", "", "", "service name is required"},
		{"sid", "ora", "FREEPDB1", `{"sid":"FREE"}`, `"sid"`},
		{"tls", "ora", "FREEPDB1", `{"ssl":true,"ssl verify":false}`, `"ssl", "ssl verify"`},
		{"native encryption", "ora", "FREEPDB1", `{"encryption":"required"}`, `"encryption"`},
		{"wallet file", "ora", "FREEPDB1", `{"adbc_options":{"oracle.wallet_location":"/other/workspace"}}`, `"oracle.wallet_location"`},
		{"other server", "ora", "FREEPDB1", `{"adbc_options":{"oracle.connect_string":"evil:1521/X","uri":"oracle://x/y"}}`, `"oracle.connect_string", "uri"`},
		{"privileged logon", "ora", "FREEPDB1", `{"adbc_options":{"oracle.auth_mode":"sysdba"}}`, `"oracle.auth_mode"`},
		{"malformed options", "ora", "FREEPDB1", `{"adbc_options":"x"}`, "JSON object"},
		{"malformed extra", "ora", "FREEPDB1", `{not json`, "not a JSON object"},
	} {
		c := base
		c.Host, c.Schema, c.Extra = tc.host, tc.schema, tc.extra
		if p := c.NativeADBCProblem(); !strings.Contains(p, tc.want) {
			t.Errorf("%s: problem = %q, want it to mention %s", tc.name, p, tc.want)
		}
		if _, err := c.NativeADBCEndpoint(); err == nil {
			t.Errorf("%s: endpoint built for a connection with a problem", tc.name)
		}
	}
}

// Extra settings stored encrypted are a reference until a run resolves
// them. The check before resolution must let them through, and the check
// after resolution (in NativeADBCEndpoint) must still refuse what they hold.
func TestNativeADBCOracleChecksExtraOnceResolved(t *testing.T) {
	c := Connection{ConnID: "o", Type: ConnTypeOracle, Host: "ora", Schema: "FREEPDB1", Extra: "encrypted://v1:abc"}
	if p := c.NativeADBCProblem(); p != "" {
		t.Fatalf("unresolved extra refused before resolution: %q", p)
	}
	c.Extra = `{"adbc_options":{"oracle.unconstrained_number":"float64"}}`
	if _, err := c.NativeADBCEndpoint(); err != nil {
		t.Fatalf("resolved allowed options refused: %v", err)
	}
	c.Extra = `{"sid":"FREE"}`
	if _, err := c.NativeADBCEndpoint(); err == nil {
		t.Fatal("resolved extra with a refused option built an endpoint")
	}
}
