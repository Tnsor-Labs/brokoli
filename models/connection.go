package models

import (
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ConnectionType identifies the kind of external system.
type ConnectionType string

const (
	ConnTypePostgres   ConnectionType = "postgres"
	ConnTypeMySQL      ConnectionType = "mysql"
	ConnTypeSQLite     ConnectionType = "sqlite"
	ConnTypeHTTP       ConnectionType = "http"
	ConnTypeSFTP       ConnectionType = "sftp"
	ConnTypeS3         ConnectionType = "s3"
	ConnTypeSnowflake  ConnectionType = "snowflake"
	ConnTypeRedshift   ConnectionType = "redshift"
	ConnTypeBigQuery   ConnectionType = "bigquery"
	ConnTypeAzureBlob  ConnectionType = "azure_blob"
	ConnTypeGCS        ConnectionType = "gcs"
	ConnTypeDatabricks ConnectionType = "databricks"
	ConnTypeOracle     ConnectionType = "oracle"
	ConnTypeMSSQL      ConnectionType = "mssql"
	ConnTypeGeneric    ConnectionType = "generic"
)

// Connection stores credentials and config for an external system.
// Sensitive fields (password, extra) are stored as credential references
// pointing to external secret stores (env://, vault://, k8s://, encrypted://).
// The plaintext Password/Extra fields are only populated in-memory after
// the secrets resolver resolves the refs at execution time.
type Connection struct {
	ID          string         `json:"id"`
	ConnID      string         `json:"conn_id"` // human-readable slug, e.g. "prod_postgres"
	Type        ConnectionType `json:"type"`
	Description string         `json:"description"`
	Host        string         `json:"host"`
	Port        int            `json:"port,omitempty"`
	Schema      string         `json:"schema"` // database name or path
	Login       string         `json:"login"`
	Password    string         `json:"password,omitempty"`     // resolved plaintext (in-memory only, never persisted)
	Extra       string         `json:"extra,omitempty"`        // resolved plaintext (in-memory only, never persisted)
	PasswordRef string         `json:"password_ref,omitempty"` // credential ref: env://VAR, vault://path#key, k8s://ns/secret/key, encrypted://...
	ExtraRef    string         `json:"extra_ref,omitempty"`    // credential ref for extra/type-specific fields
	WorkspaceID string         `json:"workspace_id,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// HasCredentialRefs returns true if the connection uses external secret references.
func (c *Connection) HasCredentialRefs() bool {
	return c.PasswordRef != "" || c.ExtraRef != ""
}

// driverOptionKeys lists, per connection type, the driver parameters an
// operator may set through the connection's Extra blob. It is an allowlist of
// KEYS, not of values: the keys are restricted because libpq and the MySQL
// driver both accept connection parameters that would otherwise let an Extra
// blob override the host, user, or database the connection record declares.
// The VALUES are passed to the driver verbatim and the driver validates them,
// so a misspelled "sslmode" value fails the connection loudly instead of being
// dropped and silently downgrading to an unencrypted link.
var driverOptionKeys = map[ConnectionType][]string{
	ConnTypePostgres: {
		"sslmode", "sslrootcert", "sslcert", "sslkey",
		"application_name", "connect_timeout", "target_session_attrs",
	},
	ConnTypeRedshift: {
		"sslmode", "sslrootcert", "sslcert", "sslkey",
		"application_name", "connect_timeout",
	},
	ConnTypeMySQL: {
		"tls", "charset", "collation", "parseTime", "loc",
		"timeout", "readTimeout", "writeTimeout",
	},
	ConnTypeMSSQL: {
		"encrypt", "TrustServerCertificate", "hostNameInCertificate",
		"connection timeout", "dial timeout", "app name",
	},
	ConnTypeSnowflake: {
		"warehouse", "role", "authenticator", "loginTimeout", "application",
	},
}

// driverOptions returns the driver parameters this connection carries in its
// Extra blob, keyed and ordered so the resulting URI is deterministic. Extra
// is expected to hold decrypted JSON by the time this runs (the connection
// resolver resolves credential refs first); anything that is not a JSON object
// yields no options rather than an error, because a connection with an
// unreadable Extra should still connect with its declared host and credentials.
func (c *Connection) driverOptions() url.Values {
	allowed := driverOptionKeys[c.Type]
	if len(allowed) == 0 || strings.TrimSpace(c.Extra) == "" {
		return nil
	}
	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(c.Extra), &extra); err != nil {
		return nil
	}
	opts := url.Values{}
	for _, key := range allowed {
		raw, ok := extra[key]
		if !ok {
			continue
		}
		var val string
		switch v := raw.(type) {
		case string:
			val = v
		case bool:
			val = strconv.FormatBool(v)
		case float64:
			// JSON numbers decode as float64; keep integers integral so
			// connect_timeout=10 does not reach the driver as "10".
			if v == float64(int64(v)) {
				val = strconv.FormatInt(int64(v), 10)
			} else {
				val = strconv.FormatFloat(v, 'f', -1, 64)
			}
		default:
			continue
		}
		if val != "" {
			opts.Set(key, val)
		}
	}
	if len(opts) == 0 {
		return nil
	}
	return opts
}

// encodeOptions renders driver options as a query string with keys in a
// stable order. url.Values.Encode already sorts, but MySQL's DSN parser and
// the SQL Server driver both accept keys containing spaces, so the encoding
// goes through url.Values to keep escaping correct.
func encodeOptions(opts url.Values) string {
	if len(opts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(opts.Get(k)))
	}
	return b.String()
}

// userinfo builds the credential half of a URI, escaping the login and
// password so a credential containing ":", "@", or "/" cannot alter which
// host the URI points at.
func (c *Connection) userinfo() *url.Userinfo {
	if c.Login == "" {
		return nil
	}
	if c.Password != "" {
		return url.UserPassword(c.Login, c.Password)
	}
	return url.User(c.Login)
}

// hostPort returns host:port, or bare host when no port is set, letting the
// driver apply its own default.
func (c *Connection) hostPort(defaultPort int) string {
	port := c.Port
	if port == 0 {
		port = defaultPort
	}
	if port > 0 {
		return c.Host + ":" + strconv.Itoa(port)
	}
	return c.Host
}

// BuildsURI reports whether this connection type has a URI representation the
// engine can hand to a driver. Types in the connection catalog that have no
// engine driver (BigQuery, Databricks, Oracle, and the object stores) return
// false: callers must not fabricate a URI for them, because a bare hostname
// reaches the Postgres driver as a malformed DSN and the failure names neither
// the connection nor the real reason.
func (c *Connection) BuildsURI() bool {
	switch c.Type {
	case ConnTypePostgres, ConnTypeRedshift, ConnTypeMySQL, ConnTypeSQLite,
		ConnTypeMSSQL, ConnTypeSnowflake, ConnTypeHTTP, ConnTypeSFTP, ConnTypeS3:
		return true
	default:
		return false
	}
}

// BuildURI constructs a connection URI from the connection fields.
//
// This is the only URI builder in the codebase. It is deliberately the single
// implementation: the engine previously carried a second one that supported
// Redshift, Snowflake, and SQL Server and pinned sslmode=require, but nothing
// ever called it, so those connection types fell through to a bare hostname
// and no Postgres connection ever asked for TLS.
//
// Driver options come from the connection's Extra blob (see driverOptionKeys).
// No option is applied by default, including sslmode: libpq's own default of
// "prefer" is what an unconfigured connection gets, and an operator who needs
// a guarantee sets sslmode explicitly.
func (c *Connection) BuildURI() string {
	opts := c.driverOptions()

	switch c.Type {
	case ConnTypePostgres, ConnTypeRedshift:
		scheme, defPort := "postgres", 5432
		if c.Type == ConnTypeRedshift {
			scheme, defPort = "redshift", 5439
		}
		u := &url.URL{Scheme: scheme, User: c.userinfo(), Host: c.hostPort(defPort)}
		if c.Schema != "" {
			u.Path = "/" + c.Schema
		}
		u.RawQuery = encodeOptions(opts)
		return u.String()

	case ConnTypeMySQL:
		// MySQL DSN format: user:password@tcp(host:port)/dbname
		u := &url.URL{Scheme: "mysql", User: c.userinfo(), Host: "tcp(" + c.hostPort(3306) + ")"}
		if c.Schema != "" {
			u.Path = "/" + c.Schema
		}
		u.RawQuery = encodeOptions(opts)
		return u.String()

	case ConnTypeMSSQL:
		u := &url.URL{Scheme: "sqlserver", User: c.userinfo(), Host: c.hostPort(1433)}
		q := url.Values{}
		for k, v := range opts {
			q[k] = v
		}
		if c.Schema != "" {
			q.Set("database", c.Schema)
		}
		u.RawQuery = encodeOptions(q)
		return u.String()

	case ConnTypeSnowflake:
		// Snowflake DSN: user:password@account/database/schema?warehouse=X
		u := &url.URL{Scheme: "snowflake", User: c.userinfo(), Host: c.Host}
		if c.Schema != "" {
			u.Path = "/" + c.Schema
		}
		u.RawQuery = encodeOptions(opts)
		return u.String()

	case ConnTypeSQLite:
		return c.Host // host is the file path

	case ConnTypeHTTP:
		scheme := "https"
		if c.Port == 80 {
			scheme = "http"
		}
		u := &url.URL{Scheme: scheme, Host: c.Host}
		if c.Port > 0 && c.Port != 80 && c.Port != 443 {
			u.Host = c.Host + ":" + strconv.Itoa(c.Port)
		}
		return u.String()

	case ConnTypeSFTP:
		u := &url.URL{Scheme: "sftp", User: c.userinfo(), Host: c.hostPort(0)}
		if c.Schema != "" {
			u.Path = "/" + c.Schema
		}
		return u.String()

	case ConnTypeS3:
		u := &url.URL{Scheme: "s3", Host: c.Host}
		if c.Schema != "" {
			u.Path = "/" + c.Schema
		}
		return u.String()

	default:
		return c.Host
	}
}
