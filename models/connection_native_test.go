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
