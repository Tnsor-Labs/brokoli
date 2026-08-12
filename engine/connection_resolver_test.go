package engine

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func TestConnectionResolver_NoConnID(t *testing.T) {
	cr := &ConnectionResolver{}
	config := map[string]interface{}{
		"uri": "postgres://localhost/mydb",
	}
	result := cr.Resolve(config, models.NodeTypeSourceDB)
	if result["uri"] != "postgres://localhost/mydb" {
		t.Error("expected config unchanged when no conn_id")
	}
}

func TestConnectionResolver_EmptyConnID(t *testing.T) {
	cr := &ConnectionResolver{}
	config := map[string]interface{}{
		"conn_id": "",
		"uri":     "postgres://localhost/mydb",
	}
	result := cr.Resolve(config, models.NodeTypeSourceDB)
	if result["uri"] != "postgres://localhost/mydb" {
		t.Error("expected config unchanged when conn_id is empty")
	}
}

// newAPIConnResolver creates a resolver backed by a real SQLite store with
// one saved http connection: base URL https://api.example.com, Basic Auth
// (svc/secret), and an extra header the connection carries.
func newAPIConnResolver(t *testing.T) *ConnectionResolver {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "conn-resolver.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	conn := &models.Connection{
		ID:        "conn-1",
		ConnID:    "external-api",
		Type:      models.ConnTypeHTTP,
		Host:      "api.example.com",
		Login:     "svc",
		Password:  "secret",
		Extra:     `{"headers": {"X-From-Conn": "yes"}}`,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.CreateConnection(conn); err != nil {
		t.Fatal(err)
	}
	return NewConnectionResolver(s, nil)
}

// TestConnectionResolver_SinkAPI is brokoli#137: sink_api previously had no
// case in the resolver's switch at all, so conn_id was silently a no-op --
// no base URL, no headers, no Basic Auth ever got injected.
func TestConnectionResolver_SinkAPI(t *testing.T) {
	cr := newAPIConnResolver(t)
	config := map[string]interface{}{
		"conn_id": "external-api",
		"url":     "/v1/import",
		"headers": map[string]interface{}{"Content-Type": "application/json"},
	}

	result := cr.Resolve(config, models.NodeTypeSinkAPI)

	if got := result["url"]; got != "https://api.example.com/v1/import" {
		t.Errorf("url = %v, want base URL + path", got)
	}
	headers, ok := result["headers"].(map[string]interface{})
	if !ok {
		t.Fatal("expected headers to be resolved")
	}
	if headers["X-From-Conn"] != "yes" {
		t.Errorf("expected connection header merged in, got %v", headers)
	}
	if headers["Content-Type"] != "application/json" {
		t.Errorf("expected node-level header preserved, got %v", headers)
	}
	if result["auth_user"] != "svc" || result["auth_password"] != "secret" {
		t.Errorf("expected Basic Auth injected from connection, got auth_user=%v auth_password=%v",
			result["auth_user"], result["auth_password"])
	}
}

// TestConnectionResolver_SourceAndSinkAPI_Identical guards the refactor that
// extracted the shared resolveAPIConnectionFields helper -- source_api and
// sink_api must resolve a conn_id byte-for-byte the same way.
func TestConnectionResolver_SourceAndSinkAPI_Identical(t *testing.T) {
	cr := newAPIConnResolver(t)
	config := map[string]interface{}{
		"conn_id": "external-api",
		"url":     "/v1/events",
	}

	source := cr.Resolve(config, models.NodeTypeSourceAPI)
	sink := cr.Resolve(config, models.NodeTypeSinkAPI)

	for _, key := range []string{"url", "headers", "auth_user", "auth_password"} {
		if !reflect.DeepEqual(source[key], sink[key]) {
			t.Errorf("%s: source_api=%v sink_api=%v, want identical resolution", key, source[key], sink[key])
		}
	}
}
