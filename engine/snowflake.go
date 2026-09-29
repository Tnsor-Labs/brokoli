package engine

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	gosnowflake "github.com/snowflakedb/gosnowflake/v2"
)

// snowflakeDriverName is the database/sql name Brokoli opens Snowflake
// through. It is not gosnowflake's own "snowflake" registration: that one
// takes any DSN parameter the driver knows, and several of those make the
// driver read or write files on the worker. This one wraps it, admitting
// only the options a connection can carry and refusing the statements that
// move files (see snowflakeGuard).
const snowflakeDriverName = "brokoli-snowflake"

func init() {
	// gosnowflake extracts an embedded native library (minicore) to disk and
	// loads it at the first connection. Release builds leave it out with
	// -tags minicore_disabled; this stops a build without the tag loading
	// it too. The driver reads the variable when it first connects, which
	// is always after init.
	_ = os.Setenv("SF_DISABLE_MINICORE", "true")
	sql.Register(snowflakeDriverName, snowflakeGuard{})
}

// snowflakeOptions are the only query parameters a Snowflake URI may carry:
// the ones models.Connection.BuildURI writes from the connection's extra
// settings. Anything else -- a token file, a client config file, a
// diagnostics allowlist file, a temp directory, a proxy, OCSP switches,
// arbitrary session parameters -- would let a pipeline author reach the
// worker's filesystem or loosen TLS through a hand-written URI, and is
// refused by name.
var snowflakeOptions = map[string]bool{
	"warehouse":     true,
	"role":          true,
	"authenticator": true,
	"loginTimeout":  true,
	"application":   true,
}

// snowflakeConfigFromDSN parses the DSN a snowflake:// URI becomes
// (user:password@account/database/schema?options) into a driver config,
// after checking it only says what a connection can say. Errors never
// quote the DSN: it carries the password.
func snowflakeConfigFromDSN(dsn string) (*gosnowflake.Config, error) {
	u, err := url.Parse("snowflake://" + dsn)
	if err != nil {
		// url.Parse's message quotes its input, password included.
		return nil, errors.New("Snowflake connection URI is not a valid URI (user:password@account/database/schema)")
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, errors.New("Snowflake connection URI names no user")
	}
	password, _ := u.User.Password()
	query := u.Query()
	for key := range query {
		if !snowflakeOptions[key] {
			return nil, fmt.Errorf("Snowflake connection option %q is not supported (supported: warehouse, role, authenticator, loginTimeout, application)", key)
		}
	}
	switch authenticator := strings.TrimSpace(query.Get("authenticator")); strings.ToLower(authenticator) {
	case "", "snowflake":
	default:
		// Refused by name rather than passed to the driver: externalbrowser
		// and the MFA and OAuth code flows wait for a person, key pair and
		// token authenticators need credentials a connection has no field
		// for, and an Okta URL sends the password to a host the URI names.
		return nil, fmt.Errorf("Snowflake authenticator %q is not supported; the only supported authenticator is \"snowflake\" (user and password)", authenticator)
	}
	cfg, err := gosnowflake.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("Snowflake connection URI: %s", redactSecret(err.Error(), dsn, password))
	}
	return cfg, nil
}

// redactSecret removes the DSN and the password, raw and escaped, from a
// driver message.
func redactSecret(msg, dsn, password string) string {
	if dsn != "" {
		msg = strings.ReplaceAll(msg, dsn, "[redacted]")
	}
	if password != "" {
		// As a URI's userinfo spells it (BuildURI's form), then as a query
		// and a path would.
		userinfo := strings.TrimPrefix(url.UserPassword("", password).String(), ":")
		msg = strings.ReplaceAll(msg, userinfo, "[redacted]")
		msg = strings.ReplaceAll(msg, url.QueryEscape(password), "[redacted]")
		msg = strings.ReplaceAll(msg, url.PathEscape(password), "[redacted]")
		msg = strings.ReplaceAll(msg, password, "[redacted]")
	}
	return msg
}

// Snowflake's PUT and GET copy files between the client's filesystem and a
// stage, and gosnowflake runs them on the client: a query "GET @stage
// file:///..." writes files on the worker, and "PUT file:///... @stage"
// uploads any file the worker can read. The driver decides a statement is a
// file transfer by matching its start (driverFileTransfer, copied from
// gosnowflake v2 file_transfer_agent.go). snowflakeFileTransfer refuses a
// superset: the driver's own patterns, and PUT or GET as the first keyword
// once any leading comments of either kind are skipped.
var (
	driverPutRegexp = regexp.MustCompile(`(?i)^(?:/\*.*\*/\s*)*\s*put\s+`)
	driverGetRegexp = regexp.MustCompile(`(?i)^(?:/\*.*\*/\s*)*\s*get\s+`)
)

func snowflakeFileTransfer(query string) bool {
	if driverPutRegexp.MatchString(query) || driverGetRegexp.MatchString(query) {
		return true
	}
	rest := query
	for {
		rest = strings.TrimLeft(rest, " \t\r\n\f\v")
		switch {
		case strings.HasPrefix(rest, "/*"):
			end := strings.Index(rest[2:], "*/")
			if end < 0 {
				return false
			}
			rest = rest[2+end+2:]
		case strings.HasPrefix(rest, "--"), strings.HasPrefix(rest, "//"):
			end := strings.IndexByte(rest, '\n')
			if end < 0 {
				return false
			}
			rest = rest[end+1:]
		default:
			word := rest
			if i := strings.IndexAny(word, " \t\r\n\f\v'\"@(;"); i >= 0 {
				word = word[:i]
			}
			return strings.EqualFold(word, "put") || strings.EqualFold(word, "get")
		}
	}
}

// errSnowflakeWrite refuses every sink_db and migrate write to Snowflake
// (refuseUnearnedWrite). None has been run against a real Snowflake
// account: the generated statements quote identifiers, which Snowflake
// treats as case-sensitive, so a column "id" would not match the ID an
// unquoted CREATE TABLE made; overwrite's TRUNCATE commits on its own
// outside the write's transaction; and neither the type spellings nor the
// timestamp literals have been checked. Query only, until a live test
// proves each mode.
var errSnowflakeWrite = errors.New("Snowflake connections are query-only in this build: writes (append, overwrite, upsert, create_table) are not supported yet")

var errSnowflakeFileTransfer = errors.New("Snowflake PUT and GET are not supported: they copy files to and from the worker's filesystem. Load staged files with COPY INTO instead")

func refuseSnowflakeFileTransfer(query string) error {
	if snowflakeFileTransfer(query) {
		return errSnowflakeFileTransfer
	}
	return nil
}

// snowflakeGuard is the brokoli-snowflake driver: gosnowflake behind the
// option allowlist above, with every connection wrapped so no statement
// that moves files reaches it.
type snowflakeGuard struct{}

func (g snowflakeGuard) Open(dsn string) (driver.Conn, error) {
	connector, err := g.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return connector.Connect(context.Background())
}

func (g snowflakeGuard) OpenConnector(dsn string) (driver.Connector, error) {
	cfg, err := snowflakeConfigFromDSN(dsn)
	if err != nil {
		return nil, err
	}
	return snowflakeGuardConnector{inner: gosnowflake.NewConnector(gosnowflake.SnowflakeDriver{}, *cfg)}, nil
}

type snowflakeGuardConnector struct{ inner driver.Connector }

func (c snowflakeGuardConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &snowflakeGuardConn{inner: conn}, nil
}

func (c snowflakeGuardConnector) Driver() driver.Driver { return snowflakeGuard{} }

// snowflakeGuardConn checks every statement text before gosnowflake sees
// it. Prepared statements are checked when prepared: that is where their
// text is fixed.
type snowflakeGuardConn struct{ inner driver.Conn }

func (c *snowflakeGuardConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *snowflakeGuardConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := refuseSnowflakeFileTransfer(query); err != nil {
		return nil, err
	}
	if p, ok := c.inner.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, query)
	}
	return c.inner.Prepare(query)
}

func (c *snowflakeGuardConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := refuseSnowflakeFileTransfer(query); err != nil {
		return nil, err
	}
	e, ok := c.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return e.ExecContext(ctx, query, args)
}

func (c *snowflakeGuardConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := refuseSnowflakeFileTransfer(query); err != nil {
		return nil, err
	}
	q, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return q.QueryContext(ctx, query, args)
}

func (c *snowflakeGuardConn) Close() error { return c.inner.Close() }

func (c *snowflakeGuardConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *snowflakeGuardConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.inner.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.inner.Begin() //nolint:staticcheck // fallback for a driver without BeginTx
}

func (c *snowflakeGuardConn) Ping(ctx context.Context) error {
	if p, ok := c.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *snowflakeGuardConn) CheckNamedValue(nv *driver.NamedValue) error {
	if n, ok := c.inner.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}
