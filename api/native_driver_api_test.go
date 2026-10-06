package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
	"github.com/Tnsor-Labs/brokoli/pkg/sodp"
	"github.com/Tnsor-Labs/brokoli/store"
)

func newDriverTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLiteStore(t.TempDir() + "/brokoli.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func pinnedConnection(connID, workspaceID string, identity models.DriverIdentity) *models.Connection {
	return &models.Connection{ID: connID, ConnID: connID, Type: models.ConnTypeFlightSQL, Host: "93.184.216.34",
		WorkspaceID: workspaceID, DriverIdentity: &identity, CreatedAt: time.Now(), UpdatedAt: time.Now()}
}

// A driver is host-wide but its users are not: a refused removal names the
// caller's own connections and only counts everyone else's.
func TestDriverRemoveNamesOnlyTheCallersConnections(t *testing.T) {
	driverDir := t.TempDir()
	identity := installTestDriverBuild(t, driverDir, "flightsql")
	manager, err := drivers.NewManager(driverDir)
	if err != nil {
		t.Fatal(err)
	}
	s := newDriverTestStore(t)
	for _, c := range []*models.Connection{pinnedConnection("mine", "default", identity), pinnedConnection("theirs", "ws-other-tenant", identity)} {
		if err := s.CreateConnection(c); err != nil {
			t.Fatal(err)
		}
	}
	h := &DriverHandler{store: s, manager: manager}
	rec := httptest.NewRecorder()
	h.Remove(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/drivers/flightsql", nil), "name", "flightsql"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Conns []string `json:"conns"`
		Other int      `json:"other_workspace_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if strings.Join(body.Conns, ",") != "mine" || body.Other != 1 || strings.Contains(rec.Body.String(), "theirs") {
		t.Fatalf("conflict = %s, want only the caller's connection named and the other counted", rec.Body.String())
	}
	if manager.GetIdentity(identity) == nil {
		t.Fatal("a refused removal deleted the driver")
	}
}

// A pin stored with an uppercase digest is the same build.
func TestDriverRemoveSeesAPinWhateverTheDigestCase(t *testing.T) {
	driverDir := t.TempDir()
	identity := installTestDriverBuild(t, driverDir, "flightsql")
	manager, err := drivers.NewManager(driverDir)
	if err != nil {
		t.Fatal(err)
	}
	upper := identity
	upper.LibrarySHA256 = strings.ToUpper(upper.LibrarySHA256)
	h := &DriverHandler{store: &listOnlyStore{conns: []models.Connection{*pinnedConnection("mine", "", upper)}}, manager: manager}
	rec := httptest.NewRecorder()
	h.Remove(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/drivers/flightsql", nil), "name", "flightsql"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for an uppercase pin: %s", rec.Code, rec.Body.String())
	}
}

type listOnlyStore struct {
	store.Store
	conns []models.Connection
	err   error
}

func (s *listOnlyStore) ListConnections() ([]models.Connection, error) { return s.conns, s.err }
func (s *listOnlyStore) ListConnectionsByWorkspace(string) ([]models.Connection, error) {
	return s.conns, s.err
}

func TestDriverRemoveRefusesWhenPinsCannotBeChecked(t *testing.T) {
	driverDir := t.TempDir()
	installTestDriverBuild(t, driverDir, "flightsql")
	manager, err := drivers.NewManager(driverDir)
	if err != nil {
		t.Fatal(err)
	}
	h := &DriverHandler{store: &listOnlyStore{err: errors.New("database is down")}, manager: manager}
	rec := httptest.NewRecorder()
	h.Remove(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/drivers/flightsql", nil), "name", "flightsql"))
	if rec.Code != http.StatusServiceUnavailable || len(manager.List()) != 1 {
		t.Fatalf("status = %d, installed = %d: an unchecked removal went through", rec.Code, len(manager.List()))
	}
}

func TestInstalledDriversListsBuildsForPinning(t *testing.T) {
	driverDir := t.TempDir()
	identity := installTestDriverBuild(t, driverDir, "flightsql")
	manager, err := drivers.NewManager(driverDir)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	(&DriverHandler{manager: manager}).Installed(rec, httptest.NewRequest(http.MethodGet, "/api/drivers", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), identity.LibrarySHA256) {
		t.Fatalf("installed = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDriverInstallWithoutACatalogIsAConflictNotABadRequest(t *testing.T) {
	t.Setenv(drivers.IndexEnvVar, "")
	manager, err := drivers.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	(&DriverHandler{manager: manager}).Install(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/drivers/catalog/flightsql/install", nil), "name", "flightsql"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), drivers.IndexEnvVar) {
		t.Fatalf("install = %d %s", rec.Code, rec.Body.String())
	}
}

// Changing native drivers asks for drivers.manage, so a distribution can
// give it to platform operators only; browsing does not.
func TestDriverChangesAskForTheirOwnPermission(t *testing.T) {
	t.Setenv(drivers.IndexEnvVar, "")
	t.Setenv("BROKOLI_DRIVER_DIR", t.TempDir())
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, &extensions.Registry{Team: refuseOneTeam{refuse: string(models.PermDriversManage)}}, nil)
	for _, tc := range []struct {
		method, path string
		refused      bool
	}{
		{http.MethodPost, "/api/drivers/catalog/flightsql/install", true},
		{http.MethodDelete, "/api/drivers/flightsql", true},
		{http.MethodGet, "/api/drivers", false},
		{http.MethodGet, "/api/drivers/catalog", false},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, reqAsRole(tc.method, tc.path, "admin"))
		refused := rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), string(models.PermDriversManage))
		if refused != tc.refused {
			t.Errorf("%s %s: status %d, refused for drivers.manage = %v, want %v", tc.method, tc.path, rec.Code, refused, tc.refused)
		}
	}
}

func TestOnlyAdminsHoldDriversManage(t *testing.T) {
	for _, role := range models.DefaultRoles() {
		holds := false
		for _, p := range role.Permissions {
			holds = holds || p == models.PermDriversManage
		}
		if holds != (role.ID == "admin") {
			t.Errorf("role %q holds drivers.manage = %v", role.ID, holds)
		}
	}
}

func connectionRouter(h *ConnectionHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/connections", h.Create)
	r.Put("/connections/{connId}", h.Update)
	r.Post("/connections/{connId}/test", h.Test)
	return r
}

func sendJSON(t *testing.T, r http.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(raw)))
	return rec
}

func TestConnectionDriverIdentityIsValidatedOnSave(t *testing.T) {
	s := newDriverTestStore(t)
	r := connectionRouter(NewConnectionHandler(s, nil))
	valid := models.DriverIdentity{Name: "flightsql", Version: "1.0.0", LibrarySHA256: strings.Repeat("AB", 32)}
	for name, tc := range map[string]struct {
		body map[string]interface{}
		ok   bool
	}{
		"flight sql without a pin": {map[string]interface{}{"conn_id": "f1", "type": "flightsql", "host": "x"}, false},
		"malformed pin":            {map[string]interface{}{"conn_id": "f2", "type": "flightsql", "host": "x", "driver_identity": map[string]string{"name": "flightsql", "version": "../x", "library_sha256": "00"}}, false},
		"pin on mysql":             {map[string]interface{}{"conn_id": "m1", "type": "mysql", "host": "x", "driver_identity": valid}, false},
		"valid pin":                {map[string]interface{}{"conn_id": "f3", "type": "flightsql", "host": "x", "driver_identity": valid}, true},
	} {
		rec := sendJSON(t, r, http.MethodPost, "/connections", tc.body)
		if ok := rec.Code == http.StatusCreated; ok != tc.ok {
			t.Errorf("%s: status %d %s", name, rec.Code, rec.Body.String())
		}
	}
	saved, err := s.GetConnection("f3")
	if err != nil {
		t.Fatal(err)
	}
	if saved.DriverIdentity == nil || saved.DriverIdentity.LibrarySHA256 != strings.Repeat("ab", 32) {
		t.Fatalf("saved pin = %+v, want a lowercased digest", saved.DriverIdentity)
	}
}

// An update that does not mention the pin keeps it. An older client, or a
// script that sends only the fields it changes, must not move a connection's
// runs to another driver; an explicit null unpins.
func TestConnectionUpdateKeepsThePinUnlessItIsSent(t *testing.T) {
	s := newDriverTestStore(t)
	r := connectionRouter(NewConnectionHandler(s, nil))
	pin := models.DriverIdentity{Name: "postgresql", Version: "1.0.0", LibrarySHA256: strings.Repeat("ab", 32)}
	if rec := sendJSON(t, r, http.MethodPost, "/connections", map[string]interface{}{"conn_id": "pg", "type": "postgres", "host": "db", "driver_identity": pin}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := sendJSON(t, r, http.MethodPut, "/connections/pg", map[string]interface{}{"type": "postgres", "host": "db", "description": "renamed"}); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := s.GetConnection("pg")
	if got.DriverIdentity == nil || *got.DriverIdentity != pin {
		t.Fatalf("pin after an update that did not mention it = %+v", got.DriverIdentity)
	}
	if rec := sendJSON(t, r, http.MethodPut, "/connections/pg", map[string]interface{}{"type": "postgres", "host": "db", "driver_identity": nil}); rec.Code != http.StatusOK {
		t.Fatalf("unpin: %d %s", rec.Code, rec.Body.String())
	}
	got, _ = s.GetConnection("pg")
	if got.DriverIdentity != nil {
		t.Fatalf("an explicit null did not unpin: %+v", got.DriverIdentity)
	}
}

type fakeNativeWorker struct {
	request *engine.NativeADBCRequest
	err     error
}

func (f *fakeNativeWorker) RunNativeADBC(_ context.Context, request engine.NativeADBCRequest, _ io.Writer) (engine.NativeADBCResult, error) {
	f.request = &request
	return engine.NativeADBCResult{Rows: 1, Columns: []string{"1"}}, f.err
}

// A connection read through a native driver is tested through one, with the
// same query path a run takes.
func TestNativeConnectionTestRunsThroughTheWorker(t *testing.T) {
	t.Cleanup(engine.SetNativeWorkerAvailableForTesting(true))
	driverDir := t.TempDir()
	identity := installTestDriverBuild(t, driverDir, "flightsql")
	manager, err := drivers.NewManager(driverDir)
	if err != nil {
		t.Fatal(err)
	}
	s := newDriverTestStore(t)
	if err := s.CreateConnection(pinnedConnection("flight", "", identity)); err != nil {
		t.Fatal(err)
	}
	h := NewConnectionHandler(s, nil)
	h.creds.SetDriverManager(manager)
	r := connectionRouter(h)

	rec := sendJSON(t, r, http.MethodPost, "/connections/flight/test", nil)
	if !strings.Contains(rec.Body.String(), "no isolated native worker") {
		t.Fatalf("without a worker: %s", rec.Body.String())
	}

	worker := &fakeNativeWorker{}
	h.nativeWorker = func() engine.NativeADBCRunner { return worker }
	rec = sendJSON(t, r, http.MethodPost, "/connections/flight/test", nil)
	if !strings.Contains(rec.Body.String(), `"success":true`) || worker.request == nil || worker.request.Driver != identity {
		t.Fatalf("test = %s, worker request = %+v", rec.Body.String(), worker.request)
	}

	worker.err = errors.New("native ADBC connect failed: refused")
	rec = sendJSON(t, r, http.MethodPost, "/connections/flight/test", nil)
	if !strings.Contains(rec.Body.String(), `"success":false`) || !strings.Contains(rec.Body.String(), "connect failed") {
		t.Fatalf("failing test = %s", rec.Body.String())
	}
}
