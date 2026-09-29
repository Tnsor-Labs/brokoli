package engine

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	gosnowflake "github.com/snowflakedb/gosnowflake/v2"
)

// The snowflake:// scheme opens through Brokoli's guarded registration.
// Removing the registration (or the scheme's claim) fails this: DetectDriver
// then refuses the scheme, and sql.Open no longer returns the guard.
func TestSnowflakeOpensThroughTheGuardedDriver(t *testing.T) {
	driverName, dsn, err := DetectDriver("snowflake://svc:p%40ss@acme.snowflakecomputing.com/ANALYTICS/RAW?warehouse=ETL_WH&role=LOADER")
	if err != nil {
		t.Fatal(err)
	}
	if driverName != snowflakeDriverName {
		t.Fatalf("driver = %q, want %q", driverName, snowflakeDriverName)
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, ok := db.Driver().(snowflakeGuard); !ok {
		t.Fatalf("driver = %T, want snowflakeGuard", db.Driver())
	}
}

func TestSnowflakeConfigFromDSN(t *testing.T) {
	cfg, err := snowflakeConfigFromDSN("svc:p%40ss@acme.snowflakecomputing.com/ANALYTICS/RAW?warehouse=ETL_WH&role=LOADER&authenticator=snowflake&loginTimeout=30&application=brokoli")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Account != "acme" || cfg.Database != "ANALYTICS" || cfg.Schema != "RAW" || cfg.Warehouse != "ETL_WH" || cfg.Role != "LOADER" {
		t.Fatalf("config = account %q database %q schema %q warehouse %q role %q", cfg.Account, cfg.Database, cfg.Schema, cfg.Warehouse, cfg.Role)
	}
	if cfg.User != "svc" || cfg.Password != "p@ss" {
		t.Fatal("user or password was not decoded")
	}
	if cfg.Authenticator != gosnowflake.AuthTypeSnowflake {
		t.Fatalf("authenticator = %v", cfg.Authenticator)
	}
}

// Every DSN parameter gosnowflake knows is reachable from a hand-written
// URI in a node's config. The ones below read or write the worker's files,
// loosen TLS, or send traffic elsewhere; only the options BuildURI writes
// are admitted.
func TestSnowflakeRefusesOptionsAConnectionCannotSet(t *testing.T) {
	for _, key := range []string{
		"tokenFilePath", "privateKeyFile", "clientConfigFile", "connectionDiagnosticsAllowlistFile",
		"tmpDirPath", "tracing", "disableOCSPChecks", "insecureMode", "proxyHost", "host", "protocol",
		"token", "passcode", "CLIENT_SESSION_KEEP_ALIVE",
	} {
		_, err := snowflakeConfigFromDSN("svc:hunter2@acme/db/sch?" + key + "=x")
		if err == nil {
			t.Errorf("%s: accepted", key)
			continue
		}
		if !strings.Contains(err.Error(), key) {
			t.Errorf("%s: error does not name the option: %v", key, err)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: error leaks the password: %v", key, err)
		}
	}
}

func TestSnowflakeAuthenticatorAllowlist(t *testing.T) {
	for _, ok := range []string{"", "snowflake", "SNOWFLAKE"} {
		if _, err := snowflakeConfigFromDSN("svc:pw@acme/db/sch?authenticator=" + ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, refused := range []string{
		"externalbrowser", "EXTERNALBROWSER", "oauth", "snowflake_jwt", "programmatic_access_token",
		"username_password_mfa", "oauth_authorization_code", "oauth_client_credentials",
		"workload_identity", "https%3A%2F%2Facme.okta.com",
	} {
		_, err := snowflakeConfigFromDSN("svc:pw@acme/db/sch?authenticator=" + refused)
		if err == nil || !strings.Contains(err.Error(), "authenticator") {
			t.Errorf("%q: err = %v, want a refusal naming the authenticator", refused, err)
		}
	}
}

// Neither url.Parse's nor the driver's own messages may carry the DSN.
func TestSnowflakeDSNErrorsDoNotLeakThePassword(t *testing.T) {
	for _, dsn := range []string{
		"svc:hunter2%zz@acme/db/sch", // invalid escape: url.Parse quotes its input
		"svc:hunter2@/db/sch",        // no account: the driver refuses
		"svc:hunter2@acme/db/sch?loginTimeout=notanumber",
		":hunter2@acme/db",
	} {
		_, err := snowflakeConfigFromDSN(dsn)
		if err == nil {
			t.Errorf("%q: accepted", dsn)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%q: error leaks the password: %v", dsn, err)
		}
	}
}

// No driver message in gosnowflake v2.2.0 quotes the password, as the test
// above shows, so redaction is checked directly: a future driver message
// that did quote it, raw or escaped, or the whole DSN, is scrubbed.
func TestSnowflakeRedactSecret(t *testing.T) {
	dsn := "svc:p%40ss w@acme/db"
	for _, msg := range []string{
		"bad dsn " + dsn,
		"password p@ss w rejected",
		"password p%40ss+w rejected",
		"password p%40ss%20w rejected",
	} {
		got := redactSecret(msg, dsn, "p@ss w")
		if strings.Contains(got, "ss w") || strings.Contains(got, "ss+w") || strings.Contains(got, "ss%20w") {
			t.Errorf("redactSecret(%q) = %q", msg, got)
		}
	}
}

func TestSnowflakeFileTransferDetection(t *testing.T) {
	refused := []string{
		"PUT file:///etc/passwd @~",
		"put file:///etc/passwd @~",
		"  GET @~/x file:///tmp/",
		"\n\tget @~/x file:///tmp/",
		"/* c */ PUT file:///etc/passwd @~",
		"/* a */ /* b */put file:///etc/passwd @~",
		"/*\nmulti-line\n*/ put file:///etc/passwd @~", // the driver's own pattern misses this one
		"-- note\nGET @~/x file:///tmp/",
		"// note\nput file:///etc/passwd @~",
		"/* a */ select 1 /* b */ put file:///etc/passwd @~", // the driver's greedy pattern matches this
		"PUT\tfile:///etc/passwd @~",
	}
	for _, q := range refused {
		if !snowflakeFileTransfer(q) {
			t.Errorf("not refused: %q", q)
		}
	}
	allowed := []string{
		"SELECT 'put ' AS x",
		"SELECT GET_DDL('table', 't')",
		"select * from put_orders",
		"SELECT 1 -- put file:///etc/passwd @~",
		"COPY INTO t FROM @stage",
		"getaway",
		"/* unterminated put",
	}
	for _, q := range allowed {
		if snowflakeFileTransfer(q) {
			t.Errorf("refused: %q", q)
		}
	}
}

// recordingConn stands in for gosnowflake's connection: it records every
// statement that reaches it.
type recordingConn struct{ seen []string }

func (c *recordingConn) Prepare(q string) (driver.Stmt, error) {
	c.seen = append(c.seen, q)
	return nil, errors.New("recorded")
}
func (c *recordingConn) Close() error              { return nil }
func (c *recordingConn) Begin() (driver.Tx, error) { return nil, errors.New("recorded") }
func (c *recordingConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.seen = append(c.seen, q)
	return nil, errors.New("recorded")
}
func (c *recordingConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.seen = append(c.seen, q)
	return nil, errors.New("recorded")
}

type recordingConnector struct{ conn *recordingConn }

func (c recordingConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c recordingConnector) Driver() driver.Driver                        { return snowflakeGuard{} }

// PUT and GET are refused on every path a statement takes into the
// driver, before the driver sees them; anything else passes through. The
// connection comes from the same connector wrapper sql.Open uses.
func TestSnowflakeGuardStopsFileTransfersBeforeTheDriver(t *testing.T) {
	inner := &recordingConn{}
	db := sql.OpenDB(snowflakeGuardConnector{inner: recordingConnector{conn: inner}})
	defer db.Close()
	ctx := context.Background()

	put := "/* x */ put file:///etc/passwd @~"
	if _, err := db.QueryContext(ctx, put); !errors.Is(err, errSnowflakeFileTransfer) {
		t.Errorf("query: err = %v", err)
	}
	if _, err := db.ExecContext(ctx, "GET @~/x file:///tmp/"); !errors.Is(err, errSnowflakeFileTransfer) {
		t.Errorf("exec: err = %v", err)
	}
	if _, err := db.PrepareContext(ctx, "put file:///etc/passwd @~"); !errors.Is(err, errSnowflakeFileTransfer) {
		t.Errorf("prepare: err = %v", err)
	}
	if len(inner.seen) != 0 {
		t.Fatalf("the driver saw %q", inner.seen)
	}

	_, _ = db.QueryContext(ctx, "SELECT 1")
	_, _ = db.ExecContext(ctx, "CREATE TEMP TABLE t (x INT)")
	if len(inner.seen) != 2 {
		t.Fatalf("ordinary statements did not reach the driver: %q", inner.seen)
	}
}

// OpenConnector hands back the wrapper, so sql.Open's connections are
// guarded ones.
func TestSnowflakeOpenConnectorWrapsTheDriver(t *testing.T) {
	c, err := snowflakeGuard{}.OpenConnector("svc:pw@acme/db/sch")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(snowflakeGuardConnector); !ok {
		t.Fatalf("connector = %T, want snowflakeGuardConnector", c)
	}
	if _, err := (snowflakeGuard{}).OpenConnector("svc:pw@acme/db/sch?tokenFilePath=/etc/passwd"); err == nil {
		t.Fatal("OpenConnector accepted a refused option")
	}
}

func TestSnowflakeMinicoreIsDisabled(t *testing.T) {
	if got := os.Getenv("SF_DISABLE_MINICORE"); got != "true" {
		t.Fatalf("SF_DISABLE_MINICORE = %q, want true", got)
	}
}

// Every write mode is refused by name, at run time and, for a URI written
// into the node, at validation.
func TestSnowflakeWritesAreRefused(t *testing.T) {
	uri := "snowflake://svc:pw@acme/db/sch"
	for _, mode := range []string{"", ModeAppend, ModeOverwrite, ModeUpsert, "replace"} {
		if err := refuseUnearnedWrite(uri, mode); !errors.Is(err, errSnowflakeWrite) {
			t.Errorf("mode %q: err = %v", mode, err)
		}
	}
	if err := refuseUnearnedWrite("postgres://u:p@h/db", ModeAppend); err != nil {
		t.Errorf("postgres append refused: %v", err)
	}

	p := &models.Pipeline{ID: "p", Name: "p", Nodes: []models.Node{
		{ID: "sink", Name: "sink", Type: models.NodeTypeSinkDB, Config: map[string]interface{}{"uri": uri, "table": "t"}},
		{ID: "mig", Name: "mig", Type: models.NodeTypeMigrate, Config: map[string]interface{}{
			"source_uri": "postgres://u:p@h/db", "source_query": "SELECT 1", "dest_uri": uri, "dest_table": "t"}},
	}}
	ve := ValidatePipeline(p)
	msg := ve.Error()
	for _, name := range []string{`"sink"`, `"mig"`} {
		if !strings.Contains(msg, "Node "+name+": "+errSnowflakeWrite.Error()) {
			t.Errorf("validation does not refuse %s: %s", name, msg)
		}
	}
}

// A backslash escapes in a Snowflake string literal, so a value ending in
// one must not swallow the closing quote.
func TestSnowflakeLiteralsEscapeBackslashes(t *testing.T) {
	d := getDialect("snowflake")
	if got := d.quoteString(`x\`); got != `'x\\'` {
		t.Errorf(`quoteString(x\) = %s`, got)
	}
	if got := d.quoteString(`a\'; DROP TABLE t; --`); got != `'a\\''; DROP TABLE t; --'` {
		t.Errorf("quoteString = %s", got)
	}
}
