package engine

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	dbsql "github.com/databricks/databricks-sql-go"

	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// databricksDriverName is the database/sql name Databricks connections open
// through. It is not the upstream driver's own "databricks" registration,
// for two reasons that are both about what that driver does with a DSN:
//
//   - It parses the DSN with net/url and returns the parse error as is, and
//     a url.Error quotes the whole input -- the personal access token
//     included. A host saved as "workspace.cloud.databricks.com:443" (the
//     port also appended by BuildURI) was enough to put the token in the
//     server log and the run's error.
//   - It builds its own HTTP transport, so every request bypassed the
//     deployment's outbound policy (ADR-022).
//
// This driver parses the connection URI itself, never echoes it, and hands
// the upstream connector explicit options with the netguard transport. The
// URI claim in pkg/dbdialect names this driver and passes the URI through.
const databricksDriverName = "brokoli-databricks"

func init() { sql.Register(databricksDriverName, databricksDriver{}) }

type databricksDriver struct{}

var _ driver.DriverContext = databricksDriver{}

func (d databricksDriver) Open(uri string) (driver.Conn, error) {
	connector, err := d.OpenConnector(uri)
	if err != nil {
		return nil, err
	}
	return connector.Connect(context.Background())
}

func (databricksDriver) OpenConnector(uri string) (driver.Connector, error) {
	opts, err := databricksConnOptions(uri)
	if err != nil {
		return nil, err
	}
	// Every request -- Thrift, the driver's feature-flag and telemetry
	// calls, and Cloud Fetch result downloads -- goes through this one
	// transport (the upstream client wraps cfg.Transport and Cloud Fetch
	// uses the client WithTransport sets), so the policy is checked at
	// every dial.
	opts = append(opts, dbsql.WithTransport(netguard.Outbound().Client(0).Transport))
	return dbsql.NewConnector(opts...)
}

// databricksHostPattern is a bare DNS hostname: no scheme, port, path or
// credentials, each of which would otherwise be read as part of the URI.
var databricksHostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

// databricksPathPattern is a SQL warehouse or cluster HTTP path.
var databricksPathPattern = regexp.MustCompile(`^/sql/[A-Za-z0-9._/-]+$`)

// errDatabricksURI is every malformed-URI answer. It deliberately carries no
// part of the URI, which holds the token.
var errDatabricksURI = errors.New("Databricks connection is malformed: host must be a bare hostname " +
	"such as workspace.cloud.databricks.com (no https://, port or path; the port has its own field)")

// databricksConnOptions turns a databricks:// URI (models.Connection.BuildURI)
// into explicit connector options. Only the documented driver options are
// accepted; anything else is refused by name rather than forwarded, since the
// upstream driver turns unknown DSN parameters into session parameters and
// its auth parameters could select an interactive browser login on a server.
func databricksConnOptions(uri string) ([]dbsql.ConnOption, error) {
	if !strings.HasPrefix(uri, "databricks://") {
		return nil, errDatabricksURI
	}
	u, err := url.Parse(uri)
	if err != nil || u.Opaque != "" || u.Fragment != "" || u.User == nil {
		return nil, errDatabricksURI
	}
	host := u.Hostname()
	if !databricksHostPattern.MatchString(host) {
		return nil, errDatabricksURI
	}
	port := 443
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("Databricks port must be a number from 1 to 65535")
		}
	}
	token, _ := u.User.Password()
	if token == "" {
		return nil, errors.New("Databricks connection has no personal access token (the connection password)")
	}
	if !databricksPathPattern.MatchString(u.Path) || strings.Contains(u.Path, "..") {
		return nil, errors.New("Databricks HTTP path (the connection's schema field) must be the " +
			"warehouse's HTTP path, such as /sql/1.0/warehouses/<id>")
	}

	opts := []dbsql.ConnOption{
		dbsql.WithServerHostname(host),
		dbsql.WithPort(port),
		dbsql.WithHTTPPath(u.Path),
		dbsql.WithAccessToken(token),
	}
	q := u.Query()
	var catalog, schema string
	for key, values := range q {
		value := ""
		if len(values) > 0 {
			value = values[len(values)-1]
		}
		switch key {
		case "catalog":
			catalog = value
		case "schema":
			schema = value
		case "userAgentEntry":
			opts = append(opts, dbsql.WithUserAgentEntry(value))
		case "maxRows", "timeout", "maxDownloadThreads":
			// timeout 0 means none; zero rows or download threads per
			// fetch would never make progress.
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || (n == 0 && key != "timeout") {
				return nil, fmt.Errorf("Databricks option %s must be a positive whole number", key)
			}
			switch key {
			case "maxRows":
				opts = append(opts, dbsql.WithMaxRows(n))
			case "timeout":
				opts = append(opts, dbsql.WithTimeout(time.Duration(n)*time.Second))
			default:
				opts = append(opts, dbsql.WithMaxDownloadThreads(n))
			}
		case "useCloudFetch", "useArrowNativeDecimal":
			b, err := strconv.ParseBool(value)
			if err != nil {
				return nil, fmt.Errorf("Databricks option %s must be true or false", key)
			}
			if key == "useCloudFetch" {
				opts = append(opts, dbsql.WithCloudFetch(b))
			} else {
				opts = append(opts, dbsql.WithArrowNativeDecimal(b))
			}
		default:
			return nil, fmt.Errorf("Databricks option %q is not supported", key)
		}
	}
	if catalog != "" || schema != "" {
		opts = append(opts, dbsql.WithInitialNamespace(catalog, schema))
	}
	return opts, nil
}

// refuseDatabricksWrite refuses every write to Databricks, by name (ADR-024:
// a capability is claimed only with proof behind it). The driver has no
// transactions -- BeginTx answers "not implemented" -- so the statement path
// every other backend's overwrite and create-then-insert rely on for "a failed
// write leaves nothing behind" does not exist, and no Databricks write
// vocabulary has an equivalence corpus behind it. Reads, batch and streamed,
// are supported.
func refuseDatabricksWrite(uri string) error {
	if dialectForURI(uri) != "databricks" {
		return nil
	}
	return errors.New("Databricks connections are read-only in this build: writes (append, overwrite, " +
		"upsert, create table, SQL statements) are not supported, because the Databricks SQL driver " +
		"has no transactions to make a failed write leave nothing behind. Read from Databricks with " +
		"source_db, and write elsewhere")
}
