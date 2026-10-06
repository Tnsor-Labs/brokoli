package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
)

type countingCredentialResolver struct{ calls int }

func (r *countingCredentialResolver) Scheme() string { return "count" }
func (r *countingCredentialResolver) Resolve(context.Context, secrets.Scope, string) (string, error) {
	r.calls++
	return "secret-password-value", nil
}

// installTestDriver writes one driver build in the manager's on-disk layout
// and returns the manager and the build's identity.
func installTestDriver(t *testing.T) (*drivers.Manager, models.DriverIdentity) {
	t.Helper()
	root := t.TempDir()
	library := []byte("native driver library")
	sum := sha256.Sum256(library)
	identity := models.DriverIdentity{Name: "flightsql", Version: "1.0.0", LibrarySHA256: hex.EncodeToString(sum[:])}
	dir := filepath.Join(root, identity.Name, identity.Version, identity.LibrarySHA256)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "driver.so"), library, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(drivers.Manifest{Name: identity.Name, Version: identity.Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Library: "driver.so", Entrypoint: "AdbcDriverFlightSQLInit", LibrarySHA256: identity.LibrarySHA256, ArchiveSHA256: strings.Repeat("0", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := drivers.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if manager.GetIdentity(identity) == nil {
		t.Fatal("test driver did not load")
	}
	return manager, identity
}

// withNativeWorkerAvailable pretends this build can load native drivers, so
// the dispatch path is exercised without one.
func withNativeWorkerAvailable(t *testing.T) {
	t.Helper()
	previous := nativeWorkerAvailable
	nativeWorkerAvailable = func() bool { return true }
	t.Cleanup(func() { nativeWorkerAvailable = previous })
}

// recordingNativeWorker stands in for the isolated child: it records what it
// was asked to run and answers with rows.
type recordingNativeWorker struct {
	mu       sync.Mutex
	requests []NativeADBCRequest
	rows     int
	// writeErr is what writing the result returned: a reader that stopped
	// early shows up here.
	writeErr error
}

func (w *recordingNativeWorker) RunNativeADBC(_ context.Context, request NativeADBCRequest, out io.Writer) (NativeADBCResult, error) {
	w.mu.Lock()
	w.requests = append(w.requests, request)
	w.mu.Unlock()
	ds := &common.DataSet{Columns: []string{"id"}}
	for i := 0; i < w.rows; i++ {
		ds.Rows = append(ds.Rows, common.DataRow{"id": int64(i)})
	}
	if err := EncodeArrowIPC(out, ds); err != nil {
		w.mu.Lock()
		w.writeErr = err
		w.mu.Unlock()
		return NativeADBCResult{}, err
	}
	return NativeADBCResult{Rows: int64(w.rows), Columns: ds.Columns}, nil
}

func (w *recordingNativeWorker) calls() []NativeADBCRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]NativeADBCRequest(nil), w.requests...)
}

// The regression for the library-selection hole: node config is written by
// a pipeline author, and nothing in it may decide that the native worker
// runs, let alone which library it loads.
func TestNodeConfigCannotSelectANativeLibrary(t *testing.T) {
	withNativeWorkerAvailable(t)
	eng, s := newExecCtxTestEngine(t)
	worker := &recordingNativeWorker{rows: 1}
	eng.NativeADBCWorker = worker
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	eng.SpillThresholdBytes, eng.StreamThresholdBytes = 1, 1
	pipeline := &models.Pipeline{ID: "p-native-inject", Name: "inject", Enabled: true, Nodes: []models.Node{{
		ID: "src", Type: models.NodeTypeSourceDB, Name: "Source",
		Config: map[string]interface{}{
			"uri": "grpc://169.254.169.254:80", "query": "SELECT 1",
			"native_adbc_library": "/tmp/attacker.so", "native_adbc_entrypoint": "AttackerInit",
			"native_flightsql_library": "/tmp/attacker.so",
		},
	}}}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	_, _ = eng.RunPipeline(pipeline.ID) // fails in the database/sql path; that is fine
	if calls := worker.calls(); len(calls) != 0 {
		t.Fatalf("node config reached the native worker: %+v", calls)
	}
}

func createNativeConnection(t *testing.T, s interface {
	CreateConnection(*models.Connection) error
}, conn *models.Connection) {
	t.Helper()
	conn.ID = conn.ConnID
	conn.CreatedAt, conn.UpdatedAt = time.Now(), time.Now()
	if err := s.CreateConnection(conn); err != nil {
		t.Fatal(err)
	}
}

func nativeSourcePipeline(id, connID, query string) *models.Pipeline {
	return &models.Pipeline{ID: id, Name: id, Enabled: true, Nodes: []models.Node{{
		ID: "src", Type: models.NodeTypeSourceDB, Name: "Source",
		Config: map[string]interface{}{"conn_id": connID, "query": query},
	}}}
}

// A pinned connection runs through the worker on both execution paths, and
// the worker is asked for the pinned identity, never a path.
func TestPinnedConnectionRunsThroughTheNativeWorkerOnBothPaths(t *testing.T) {
	for _, streaming := range []bool{true, false} {
		name := map[bool]string{true: "streamed", false: "materialized"}[streaming]
		t.Run(name, func(t *testing.T) {
			withNativeWorkerAvailable(t)
			manager, identity := installTestDriver(t)
			eng, s := newExecCtxTestEngine(t)
			eng.ConnResolver.SetDriverManager(manager)
			worker := &recordingNativeWorker{rows: 3}
			eng.NativeADBCWorker = worker
			eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
			if streaming {
				eng.SpillThresholdBytes, eng.StreamThresholdBytes = 1, 1
			} else {
				eng.StreamThresholdBytes = -1
			}
			createNativeConnection(t, s, &models.Connection{ConnID: "pg", Type: models.ConnTypePostgres, Host: "db.example.com", Login: "loader",
				Password: "pg-password-value", DriverIdentity: &identity})
			pipeline := nativeSourcePipeline("p-native-"+name, "pg", "SELECT id FROM t")
			if err := s.CreatePipeline(pipeline); err != nil {
				t.Fatal(err)
			}
			run, err := eng.RunPipeline(pipeline.ID)
			if err != nil {
				t.Fatalf("RunPipeline: %v", err)
			}
			if run.Status != models.RunStatusSuccess {
				t.Fatalf("run status = %s", run.Status)
			}
			calls := worker.calls()
			if len(calls) != 1 {
				t.Fatalf("worker calls = %d, want 1", len(calls))
			}
			got := calls[0]
			if got.Driver != identity || got.Query != "SELECT id FROM t" || !strings.HasPrefix(got.URI, "postgres") {
				t.Fatalf("request = %+v", got)
			}
		})
	}
}

// A dry run used to fail outright for a native source: it never streams,
// and the materialized path refused native connections.
func TestDryRunReadsANativeSourceAndStopsAtTheSample(t *testing.T) {
	withNativeWorkerAvailable(t)
	manager, identity := installTestDriver(t)
	eng, s := newExecCtxTestEngine(t)
	eng.ConnResolver.SetDriverManager(manager)
	worker := &recordingNativeWorker{rows: 50}
	eng.NativeADBCWorker = worker
	createNativeConnection(t, s, &models.Connection{ConnID: "pg", Type: models.ConnTypePostgres, Host: "db.example.com", DriverIdentity: &identity})
	pipeline := nativeSourcePipeline("p-native-dry", "pg", "SELECT id FROM t")
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	results, err := eng.DryRun(pipeline, 5)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	src := results["src"]
	if src == nil || src.Error != "" || len(src.Rows) != 5 {
		t.Fatalf("dry run result = %+v, want 5 sample rows", src)
	}
	// The sample must end the query, not just trim what it returned.
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if !errors.Is(worker.writeErr, errNativeEnoughRows) {
		t.Fatalf("worker write error = %v, want the read stopped at the sample", worker.writeErr)
	}
}

func TestNativeSourceRefusesRunParameters(t *testing.T) {
	withNativeWorkerAvailable(t)
	manager, identity := installTestDriver(t)
	eng, s := newExecCtxTestEngine(t)
	eng.ConnResolver.SetDriverManager(manager)
	worker := &recordingNativeWorker{rows: 1}
	eng.NativeADBCWorker = worker
	createNativeConnection(t, s, &models.Connection{ConnID: "pg", Type: models.ConnTypePostgres, Host: "db.example.com", DriverIdentity: &identity})
	pipeline := nativeSourcePipeline("p-native-params", "pg", "SELECT * FROM t WHERE day = '${param.day}'")
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RunPipeline(pipeline.ID); err == nil || !strings.Contains(err.Error(), "run parameters") {
		t.Fatalf("RunPipeline error = %v, want the parameter refusal", err)
	}
	if len(worker.calls()) != 0 {
		t.Fatal("a query with unbound parameters reached the driver")
	}
}

func TestNativeSourceValidatesTheDriverBeforeReadingCredentials(t *testing.T) {
	withNativeWorkerAvailable(t)
	manager, identity := installTestDriver(t)
	mismatch := identity
	mismatch.Version = "9.9.9"
	for name, pin := range map[string]*models.DriverIdentity{"unpinned": nil, "not installed": &mismatch} {
		t.Run(name, func(t *testing.T) {
			credentials := &countingCredentialResolver{}
			conn := &models.Connection{ConnID: "flight", Type: models.ConnTypeFlightSQL, Host: "93.184.216.34", DriverIdentity: pin, PasswordRef: "count://password"}
			resolver := NewConnectionResolver(&oneConnStore{conn: conn}, secrets.NewChain(nil, credentials))
			resolver.SetDriverManager(manager)
			if _, err := resolver.NativeSource(context.Background(), map[string]interface{}{"conn_id": "flight"}, secrets.Scope{}); err == nil {
				t.Fatal("NativeSource accepted a connection whose pinned driver is unavailable")
			}
			if credentials.calls != 0 {
				t.Fatalf("credentials were read %d times before the driver was validated", credentials.calls)
			}
		})
	}
}

func TestNativeSourceRefusesABlockedFlightEndpoint(t *testing.T) {
	withNativeWorkerAvailable(t)
	manager, identity := installTestDriver(t)
	credentials := &countingCredentialResolver{}
	conn := &models.Connection{ConnID: "flight", Type: models.ConnTypeFlightSQL, Host: "169.254.169.254", DriverIdentity: &identity, PasswordRef: "count://password"}
	resolver := NewConnectionResolver(&oneConnStore{conn: conn}, secrets.NewChain(nil, credentials))
	resolver.SetDriverManager(manager)
	if _, err := resolver.NativeSource(context.Background(), map[string]interface{}{"conn_id": "flight"}, secrets.Scope{}); err == nil {
		t.Fatal("NativeSource handed the metadata endpoint to a native driver")
	}
	if credentials.calls != 0 {
		t.Fatal("credentials were read for a blocked endpoint")
	}
}

func TestNativeSourceCollectsCredentialsToScrub(t *testing.T) {
	withNativeWorkerAvailable(t)
	manager, identity := installTestDriver(t)
	conn := &models.Connection{ConnID: "flight", Type: models.ConnTypeFlightSQL, Host: "93.184.216.34", Login: "loader", DriverIdentity: &identity, PasswordRef: "count://password"}
	resolver := NewConnectionResolver(&oneConnStore{conn: conn}, secrets.NewChain(nil, &countingCredentialResolver{}))
	resolver.SetDriverManager(manager)
	request, err := resolver.NativeSource(context.Background(), map[string]interface{}{"conn_id": "flight"}, secrets.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	header := request.Options["adbc.flight.sql.rpc.call_header.authorization"]
	if !strings.HasPrefix(header, "Basic ") {
		t.Fatalf("authorization option = %q", header)
	}
	message := scrubSecrets("server said: bad credentials "+strings.TrimPrefix(header, "Basic ")+" / secret-password-value", request.secrets)
	if strings.Contains(message, "secret-password-value") || strings.Contains(message, strings.TrimPrefix(header, "Basic ")) {
		t.Fatalf("credentials survive scrubbing: %q", message)
	}
}

// Routing tags come from the identity alone: the process that submits a run
// may have no drivers, and must still route to the workers that do.
func TestPipelineRequiredCapabilitiesNeedNoLocalDriver(t *testing.T) {
	identity := testDriverIdentity
	resolver := NewConnectionResolver(&oneConnStore{conn: &models.Connection{ConnID: "sqlite", Type: models.ConnTypeSQLite, Host: "/data/app.db", DriverIdentity: &identity}}, nil)
	empty, err := drivers.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolver.SetDriverManager(empty)
	got, err := resolver.PipelineRequiredCapabilities(nativeSourcePipeline("p", "sqlite", "SELECT 1"))
	if err != nil {
		t.Fatalf("PipelineRequiredCapabilities: %v", err)
	}
	want, _ := drivers.Capabilities(identity)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}

	resolver = NewConnectionResolver(&oneConnStore{conn: &models.Connection{ConnID: "flight", Type: models.ConnTypeFlightSQL, Host: "x"}}, nil)
	if _, err := resolver.PipelineRequiredCapabilities(nativeSourcePipeline("p", "flight", "SELECT 1")); !errors.Is(err, ErrNativeDriverNotPinned) {
		t.Fatalf("unpinned Flight SQL error = %v, want ErrNativeDriverNotPinned", err)
	}
}

func TestFlightSQLConnectionIsRefusedOutsideSourceDB(t *testing.T) {
	identity := testDriverIdentity
	resolver := NewConnectionResolver(&oneConnStore{conn: &models.Connection{ConnID: "flight", Type: models.ConnTypeFlightSQL, Host: "x", DriverIdentity: &identity}}, nil)
	if _, err := resolver.Resolve(map[string]interface{}{"conn_id": "flight"}, models.NodeTypeSinkDB); err == nil || !strings.Contains(err.Error(), "only a source_db node") {
		t.Fatalf("Resolve(sink_db) error = %v", err)
	}
}

// An unpinned PostgreSQL or SQLite connection keeps its database/sql path.
func TestUnpinnedDatabaseConnectionIsNotNative(t *testing.T) {
	for _, conn := range []*models.Connection{
		{ConnID: "pg", Type: models.ConnTypePostgres, Host: "db.example.com"},
		{ConnID: "sqlite", Type: models.ConnTypeSQLite, Host: "/data/app.db"},
	} {
		resolver := NewConnectionResolver(&oneConnStore{conn: conn}, nil)
		request, err := resolver.NativeSource(context.Background(), map[string]interface{}{"conn_id": conn.ConnID}, secrets.Scope{})
		if err != nil || request != nil {
			t.Fatalf("%s: NativeSource = %+v, %v; want not native", conn.ConnID, request, err)
		}
	}
}
