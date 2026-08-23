package api

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"

	"github.com/go-chi/chi/v5"
)

// connRoundTripEnv builds a handler over a real SQLite store and a real key,
// because the bug this covers lives in the encrypt/mask/store round trip and a
// fake store would define it away.
func connRoundTripEnv(t *testing.T) (*ConnectionHandler, store.Store) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "conn.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return NewConnectionHandler(s, &crypto.Config{Key: key}), s
}

func routeConn(h *ConnectionHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/connections", h.Create)
	r.Get("/api/connections/{connId}", h.Get)
	r.Put("/api/connections/{connId}", h.Update)
	return r
}

func doJSON(t *testing.T, r *chi.Mux, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withOpenMode(req.Context()))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Read-modify-write is the ordinary way to use a REST API and the way the docs
// describe: GET the connection, change a field, PUT it back. The read masks
// the credential refs to "encrypted://********", and the write accepted a
// non-empty ref as authoritative -- so the mask was stored as the credential
// and the real one was gone. Nothing in the response says so; the connection
// only fails the next time a pipeline tries to use it.
func TestConnectionSurvivesReadModifyWrite(t *testing.T) {
	h, s := connRoundTripEnv(t)
	r := routeConn(h)

	created := doJSON(t, r, "POST", "/api/connections", map[string]interface{}{
		"conn_id": "prod-postgres",
		"type":    "postgres",
		"host":    "db.example.com",
		"port":    5432,
		"schema":  "analytics",
		"login":   "etl_user",
		"extra":   `{"sslmode":"require"}`,
	})
	if created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}

	before, err := s.GetConnection("prod-postgres")
	if err != nil {
		t.Fatal(err)
	}
	if before.ExtraRef == "" {
		t.Fatal("create stored no extra_ref; the rest of this test would prove nothing")
	}

	// GET, change one unrelated field, PUT the whole object back.
	got := doJSON(t, r, "GET", "/api/connections/prod-postgres", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get: %d %s", got.Code, got.Body.String())
	}
	var record map[string]interface{}
	if err := json.Unmarshal(got.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	record["host"] = "db2.example.com"

	put := doJSON(t, r, "PUT", "/api/connections/prod-postgres", record)
	if put.Code != http.StatusOK {
		t.Fatalf("put: %d %s", put.Code, put.Body.String())
	}

	after, err := s.GetConnection("prod-postgres")
	if err != nil {
		t.Fatal(err)
	}
	if after.Host != "db2.example.com" {
		t.Errorf("the edit did not take: host = %q", after.Host)
	}
	if after.ExtraRef != before.ExtraRef {
		t.Errorf("read-modify-write destroyed the credential:\n before %q\n after  %q",
			before.ExtraRef, after.ExtraRef)
	}

	// The decisive check: does the stored ref still decrypt to the options
	// the operator set? A ref that survived as the literal mask does not.
	var c models.Connection = *after
	if got := decryptRefForTest(t, h, c.ExtraRef); got != `{"sslmode":"require"}` {
		t.Errorf("extra no longer decrypts to what was stored: %q", got)
	}
}

func decryptRefForTest(t *testing.T, h *ConnectionHandler, ref string) string {
	t.Helper()
	const prefix = "encrypted://"
	if len(ref) <= len(prefix) || ref[:len(prefix)] != prefix {
		return "<not an encrypted ref: " + ref + ">"
	}
	plain, err := h.crypto.Decrypt(ref[len(prefix):])
	if err != nil {
		return "<undecryptable: " + err.Error() + ">"
	}
	return plain
}

// The password takes the same route as extra and was destroyed the same way.
// Its bare form already had a "********" guard, which is what makes the ref
// case easy to miss: the obvious sentinel was handled and the one introduced
// with maskRef was not.
func TestPasswordSurvivesReadModifyWrite(t *testing.T) {
	h, s := connRoundTripEnv(t)
	r := routeConn(h)

	doJSON(t, r, "POST", "/api/connections", map[string]interface{}{
		"conn_id": "prod-mysql", "type": "mysql", "host": "mysql.internal",
		"port": 3306, "schema": "app", "login": "etl", "password": "s3cret-pw",
	})
	before, err := s.GetConnection("prod-mysql")
	if err != nil {
		t.Fatal(err)
	}

	got := doJSON(t, r, "GET", "/api/connections/prod-mysql", nil)
	var record map[string]interface{}
	if err := json.Unmarshal(got.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	record["port"] = 3307
	if put := doJSON(t, r, "PUT", "/api/connections/prod-mysql", record); put.Code != http.StatusOK {
		t.Fatalf("put: %d %s", put.Code, put.Body.String())
	}

	after, err := s.GetConnection("prod-mysql")
	if err != nil {
		t.Fatal(err)
	}
	if after.Port != 3307 {
		t.Errorf("the edit did not take: port = %d", after.Port)
	}
	if after.PasswordRef != before.PasswordRef {
		t.Fatalf("read-modify-write destroyed the password:\n before %q\n after  %q",
			before.PasswordRef, after.PasswordRef)
	}
	if got := decryptRefForTest(t, h, after.PasswordRef); got != "s3cret-pw" {
		t.Errorf("password no longer decrypts to what was stored: %q", got)
	}
}

// The other half of the contract: preserving on a mask must not block a real
// rotation. If this passes only because the update path ignores credentials
// entirely, the fix would be worse than the bug.
func TestCredentialRotationStillApplies(t *testing.T) {
	h, s := connRoundTripEnv(t)
	r := routeConn(h)

	doJSON(t, r, "POST", "/api/connections", map[string]interface{}{
		"conn_id": "rotate-me", "type": "postgres", "host": "db", "port": 5432,
		"schema": "d", "login": "u", "password": "old-password",
		"extra": `{"sslmode":"prefer"}`,
	})

	put := doJSON(t, r, "PUT", "/api/connections/rotate-me", map[string]interface{}{
		"type": "postgres", "host": "db", "port": 5432, "schema": "d", "login": "u",
		"password": "new-password",
		"extra":    `{"sslmode":"verify-full"}`,
	})
	if put.Code != http.StatusOK {
		t.Fatalf("put: %d %s", put.Code, put.Body.String())
	}

	after, err := s.GetConnection("rotate-me")
	if err != nil {
		t.Fatal(err)
	}
	if got := decryptRefForTest(t, h, after.PasswordRef); got != "new-password" {
		t.Errorf("rotation did not apply: password = %q", got)
	}
	if got := decryptRefForTest(t, h, after.ExtraRef); got != `{"sslmode":"verify-full"}` {
		t.Errorf("rotation did not apply: extra = %q", got)
	}
}
