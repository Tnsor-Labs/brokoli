package models

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// NativeADBCEndpoint is what a native ADBC driver is given for a connection:
// its URI, and the credentials as the standard ADBC "username" and
// "password" options where the driver takes them that way. Keeping
// credentials out of the URI means a driver that echoes its URI in an error
// cannot echo the password with it.
type NativeADBCEndpoint struct {
	URI      string
	Username string
	Password string
}

// clickhouseNativeTCPPorts are ClickHouse's native-protocol ports. The
// native ADBC driver speaks the HTTP interface instead.
var clickhouseNativeTCPPorts = map[int]bool{9000: true, 9440: true}

// NativeADBCProblem reports why this connection's settings cannot be read
// through a native driver, or "" when they can. It needs no credentials, so
// it is safe to call when a connection is saved.
func (c *Connection) NativeADBCProblem() string {
	if c.Type == ConnTypeClickHouse && clickhouseNativeTCPPorts[c.Port] {
		return fmt.Sprintf("port %d is ClickHouse's native TCP protocol; its native ADBC driver uses the HTTP interface, on 8123, or 8443 with TLS (\"secure\": true)", c.Port)
	}
	if (c.Type == ConnTypeMySQL || c.Type == ConnTypeClickHouse) && strings.TrimSpace(c.Host) == "" {
		return "a host is required"
	}
	return ""
}

// NativeADBCEndpoint returns the URI and credentials a native ADBC driver
// expects for this connection. Credentials must already be resolved.
//
//   - MySQL (ADBC Driver Foundry mysql driver): mysql://host:port/schema with
//     the connection's driver options (tls, charset, timeouts...) as query
//     parameters, which that driver hands to go-sql-driver/mysql unchanged;
//     credentials as the username and password options.
//   - ClickHouse (ClickHouse's adbc_clickhouse): http://host:8123/ or, with
//     "secure": true, https://host:8443/, with ?database=<schema>; the driver
//     treats any other query parameter as a ClickHouse setting, so the
//     clickhouse-go client options (dial_timeout, read_timeout, compress)
//     are not passed. Credentials as the username and password options.
//   - Every other type: the URI it already builds (BuildURI), credentials
//     included, as those drivers expect.
func (c *Connection) NativeADBCEndpoint() (NativeADBCEndpoint, error) {
	if problem := c.NativeADBCProblem(); problem != "" {
		return NativeADBCEndpoint{}, fmt.Errorf("connection %q: %s", c.ConnID, problem)
	}
	opts := c.driverOptions()
	switch c.Type {
	case ConnTypeMySQL:
		u := &url.URL{Scheme: "mysql", Host: c.hostPort(3306), RawQuery: encodeOptions(opts)}
		if c.Schema != "" {
			u.Path = "/" + c.Schema
		}
		return NativeADBCEndpoint{URI: u.String(), Username: c.Login, Password: c.Password}, nil
	case ConnTypeClickHouse:
		secure := false
		if v := opts.Get("secure"); v != "" {
			secure, _ = strconv.ParseBool(v)
		}
		scheme, port := "http", 8123
		if secure {
			scheme, port = "https", 8443
		}
		u := &url.URL{Scheme: scheme, Host: c.hostPort(port), Path: "/"}
		if c.Schema != "" {
			u.RawQuery = url.Values{"database": {c.Schema}}.Encode()
		}
		return NativeADBCEndpoint{URI: u.String(), Username: c.Login, Password: c.Password}, nil
	default:
		return NativeADBCEndpoint{URI: c.BuildURI()}, nil
	}
}
