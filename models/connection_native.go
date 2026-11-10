package models

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
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
	if (c.Type == ConnTypeMySQL || c.Type == ConnTypeClickHouse || c.Type == ConnTypeOracle) && strings.TrimSpace(c.Host) == "" {
		return "a host is required"
	}
	if c.Type == ConnTypeOracle {
		return c.oracleNativeProblem()
	}
	return ""
}

// oracleNativeADBCOptions are the adbc_options an Oracle connection may
// pass to the native driver (Orkidea): session, tracing, fetch tuning and
// number handling. The rest are refused rather than passed, for a reason
// each:
//
//   - uri, username, password and oracle.connect_string would replace the
//     server or login the connection declares;
//   - oracle.config_dir, oracle.wallet_location and oracle.wallet_password
//     name files on the worker, which a connection could point at another
//     workspace's wallet (the reason go-ora's "wallet" option is refused);
//   - oracle.auth_mode, oracle.new_password and oracle.cclass change how the
//     session logs on, which a read node has no reason to do.
var oracleNativeADBCOptions = map[string]bool{
	"oracle.connect_timeout_ms":         true,
	"oracle.call_timeout_ms":            true,
	"oracle.stmt_cache_size":            true,
	"oracle.time_zone":                  true,
	"oracle.edition":                    true,
	"oracle.program":                    true,
	"oracle.machine":                    true,
	"oracle.terminal":                   true,
	"oracle.module":                     true,
	"oracle.action":                     true,
	"oracle.client_identifier":          true,
	"oracle.client_info":                true,
	"oracle.dbop":                       true,
	"oracle.batch_size":                 true,
	"oracle.fetch_array_size":           true,
	"oracle.prefetch_rows":              true,
	"oracle.unconstrained_number":       true,
	"oracle.unconstrained_number_scale": true,
}

// oracleNativeProblem reports the settings of an Oracle connection that the
// native driver cannot honour. go-ora's driver options (SID, TLS, native
// encryption, timeouts) have no tested equivalent in the native driver
// yet; dropping one would connect differently from what the connection
// says, for example without TLS, so each is refused by name.
func (c *Connection) oracleNativeProblem() string {
	if strings.TrimSpace(c.Schema) == "" {
		return "a service name is required (the schema field); the native Oracle driver does not connect by SID"
	}
	// Until a run resolves them, the extra settings can be a reference
	// (encrypted://..., a secret store path) rather than JSON. Those are
	// checked again once resolved: NativeADBCEndpoint runs this check on
	// the resolved connection.
	text := strings.TrimSpace(c.Extra)
	if !strings.HasPrefix(text, "{") {
		return ""
	}
	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(text), &extra); err != nil {
		return "the extra settings are not a JSON object"
	}
	var goOra []string
	for _, key := range driverOptionKeys[ConnTypeOracle] {
		if _, ok := extra[key]; ok {
			goOra = append(goOra, fmt.Sprintf("%q", key))
		}
	}
	if len(goOra) > 0 {
		return fmt.Sprintf("the native Oracle driver does not support the %s option yet; remove it, or read through the built-in driver by unpinning this connection", strings.Join(goOra, ", "))
	}
	raw, ok := extra["adbc_options"]
	if !ok {
		return ""
	}
	options, ok := raw.(map[string]interface{})
	if !ok {
		return "adbc_options must be a JSON object of string values"
	}
	var refused []string
	for name := range options {
		if !oracleNativeADBCOptions[name] {
			refused = append(refused, fmt.Sprintf("%q", name))
		}
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		return fmt.Sprintf("adbc_options %s cannot be set on an Oracle connection; see the native drivers documentation for the options it accepts", strings.Join(refused, ", "))
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
//   - Oracle (Orkidea): oracle://host:port/service, with no driver options
//     (oracleNativeProblem refuses go-ora's); credentials as the username
//     and password options.
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
	case ConnTypeOracle:
		u := &url.URL{Scheme: "oracle", Host: c.hostPort(1521), Path: "/" + c.Schema}
		return NativeADBCEndpoint{URI: u.String(), Username: c.Login, Password: c.Password}, nil
	default:
		return NativeADBCEndpoint{URI: c.BuildURI()}, nil
	}
}
