package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
)

// ErrNativeDriverNotPinned reports a connection that can only be read through
// a native driver but names none.
var ErrNativeDriverNotPinned = errors.New("this connection must be pinned to an installed native ADBC driver")

// SetDriverManager sets the native-driver inventory this resolver checks
// pinned identities against. By default it is the process-wide inventory of
// drivers.DefaultDir(), shared with the driver API, so an install is seen
// without a restart.
func (cr *ConnectionResolver) SetDriverManager(manager *drivers.Manager) { cr.driverManager = manager }

func (cr *ConnectionResolver) drivers() (*drivers.Manager, error) {
	if cr.driverManager != nil {
		return cr.driverManager, nil
	}
	manager, err := drivers.Shared(drivers.DefaultDir())
	if err != nil {
		return nil, fmt.Errorf("native driver inventory: %w", err)
	}
	return manager, nil
}

// nativeWorkerAvailable is NativeADBCWorkerEnabled, replaceable in tests so
// the dispatch path can be exercised in a build without driver support.
var nativeWorkerAvailable = NativeADBCWorkerEnabled

// SetNativeWorkerAvailableForTesting makes native-connection validation
// treat this build as able (or unable) to load native drivers, and returns a
// function restoring the real answer. For tests in other packages only.
func SetNativeWorkerAvailableForTesting(available bool) (restore func()) {
	previous := nativeWorkerAvailable
	nativeWorkerAvailable = func() bool { return available }
	return func() { nativeWorkerAvailable = previous }
}

// nativeADBCConnectionType is intentionally a short allowlist. Existing
// database/sql connection types do not become native-driver capable merely by
// carrying a DriverIdentity.
func nativeADBCConnectionType(kind models.ConnectionType) bool {
	return kind == models.ConnTypeFlightSQL || kind == models.ConnTypePostgres || kind == models.ConnTypeSQLite
}

// IsNativeADBCConnection reports whether a source_db node reading conn goes
// through a native driver. Flight SQL has no other path, so it always does;
// PostgreSQL and SQLite do only when pinned to a driver build.
func IsNativeADBCConnection(conn *models.Connection) bool {
	return conn != nil && nativeADBCConnectionType(conn.Type) && (conn.Type == models.ConnTypeFlightSQL || conn.DriverIdentity != nil)
}

// ValidateNativeConnection checks, before any credential is read, that this
// process can run conn's native driver: it is pinned, this build can load
// native drivers, and exactly the pinned build is installed here.
func (cr *ConnectionResolver) ValidateNativeConnection(conn *models.Connection) error {
	if !IsNativeADBCConnection(conn) {
		return nil
	}
	if conn.DriverIdentity == nil {
		return ErrNativeDriverNotPinned
	}
	if !nativeWorkerAvailable() {
		return ErrNativeADBCUnavailable
	}
	manager, err := cr.drivers()
	if err != nil {
		return err
	}
	if err := manager.CheckInstalled(*conn.DriverIdentity); err != nil {
		return fmt.Errorf("native ADBC driver: %w", err)
	}
	return nil
}

// PipelineRequiredCapabilities returns the worker capability tags the
// pipeline's native sources need, so a queued run is placed only on a worker
// that has each pinned driver build.
//
// Only the identity is checked here, not that it is installed: this runs
// where the run is submitted, which in a distributed deployment is a control
// plane that has no drivers of its own. The worker that executes the node
// checks installation (ValidateNativeConnection).
func (cr *ConnectionResolver) PipelineRequiredCapabilities(pipe *models.Pipeline) ([]string, error) {
	if cr == nil || pipe == nil {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var capabilities []string
	for _, node := range pipe.Nodes {
		if node.Type != models.NodeTypeSourceDB {
			continue
		}
		connID, _ := node.Config["conn_id"].(string)
		if connID == "" {
			continue
		}
		conn, err := cr.store.GetConnection(connID)
		if err != nil {
			// The node runs without a stored connection or reports the
			// missing one itself; neither needs a native worker.
			continue
		}
		if !IsNativeADBCConnection(conn) {
			continue
		}
		if conn.DriverIdentity == nil {
			return nil, fmt.Errorf("connection %q: %w", connID, ErrNativeDriverNotPinned)
		}
		tags, err := drivers.Capabilities(*conn.DriverIdentity)
		if err != nil {
			return nil, fmt.Errorf("connection %q: %w", connID, err)
		}
		for _, tag := range tags {
			if _, ok := seen[tag]; !ok {
				seen[tag] = struct{}{}
				capabilities = append(capabilities, tag)
			}
		}
	}
	return capabilities, nil
}

// NativeSource returns the native ADBC request for a source_db node's
// config, or nil when the node's saved connection does not read through a
// native driver. Query is left for the caller to set.
//
// The decision is made from the stored connection named by conn_id and
// nothing else in config.
func (cr *ConnectionResolver) NativeSource(ctx context.Context, config map[string]interface{}, scope secrets.Scope) (*NativeADBCRequest, error) {
	connID, _ := config["conn_id"].(string)
	if connID == "" {
		return nil, nil
	}
	conn, err := cr.store.GetConnection(connID)
	if err != nil || conn == nil || !sameWorkspace(conn, scope.WorkspaceID) || !IsNativeADBCConnection(conn) {
		// Resolve already reported a missing or foreign connection to the
		// node's log; such a node is not a native source.
		return nil, nil
	}
	return cr.NativeRequest(ctx, conn, scope)
}

// NativeRequest builds the native ADBC request for conn: it validates the
// pinned driver, checks the endpoint against the outbound policy, resolves
// credentials, and collects the values to scrub from any error. conn is
// modified (its credentials are resolved into it); pass a copy to keep the
// original.
func (cr *ConnectionResolver) NativeRequest(ctx context.Context, conn *models.Connection, scope secrets.Scope) (*NativeADBCRequest, error) {
	if err := cr.ValidateNativeConnection(conn); err != nil {
		return nil, fmt.Errorf("connection %q: %w", conn.ConnID, err)
	}
	// A Flight SQL driver does its own networking in the native child, out
	// of reach of the dial-time guard every Go HTTP client here goes
	// through. Check the endpoint before handing it over, as HTTP-based
	// database connectors are checked; see netguard.CheckHost for what
	// this cannot catch. PostgreSQL and SQLite keep the policy their
	// database/sql paths have, which is none.
	if conn.Type == models.ConnTypeFlightSQL {
		if err := netguard.Outbound().CheckHost(ctx, conn.Host); err != nil {
			return nil, fmt.Errorf("connection %q: %w", conn.ConnID, err)
		}
	}
	if err := cr.resolveCredentials(conn, scope); err != nil {
		return nil, err
	}
	var extra map[string]interface{}
	if conn.Extra != "" {
		if err := json.Unmarshal([]byte(conn.Extra), &extra); err != nil {
			return nil, fmt.Errorf("parse connection extra: %w", err)
		}
	}
	options := nativeADBCOptions(conn, extra)
	uri := conn.BuildURI()
	request := &NativeADBCRequest{Driver: conn.DriverIdentity.Normalized(), URI: uri, Options: options}
	request.secrets = append(sortedOptionValues(options), conn.Password)
	if u, err := url.Parse(uri); err == nil && u.User != nil {
		if password, ok := u.User.Password(); ok {
			request.secrets = append(request.secrets, password, url.QueryEscape(password))
		}
	}
	return request, nil
}

func flightSQLHeaders(conn *models.Connection, extra map[string]interface{}) map[string]string {
	headers := make(map[string]string)
	if raw, ok := extra["headers"].(map[string]interface{}); ok {
		for name, value := range raw {
			if text, ok := value.(string); ok {
				headers[name] = text
			}
		}
	}
	if conn.Password != "" {
		if conn.Login == "" {
			headers["authorization"] = "Bearer " + conn.Password
		} else {
			headers["authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(conn.Login+":"+conn.Password))
		}
	}
	return headers
}

// nativeADBCOptions carries only string driver options. The worker enforces
// bounds again at its process boundary before passing them to drivermgr.
func nativeADBCOptions(conn *models.Connection, extra map[string]interface{}) map[string]string {
	options := make(map[string]string)
	if raw, ok := extra["adbc_options"].(map[string]interface{}); ok {
		for name, value := range raw {
			if text, ok := value.(string); ok {
				options[name] = text
			}
		}
	}
	if conn.Type == models.ConnTypeFlightSQL {
		for name, value := range flightSQLHeaders(conn, extra) {
			options["adbc.flight.sql.rpc.call_header."+name] = value
		}
	}
	return options
}
