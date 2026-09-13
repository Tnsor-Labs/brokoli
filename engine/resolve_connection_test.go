package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/store"
)

type oneConnStore struct {
	store.Store
	conn *models.Connection
	err  error
}

func (o *oneConnStore) GetConnection(string) (*models.Connection, error) {
	return o.conn, o.err
}

// The control plane resolves on a worker's behalf precisely so the worker
// never needs the encryption key, which decrypts every stored credential
// in the deployment rather than the one connection a job needs.
func TestResolveConnectionByIDReturnsPlaintext(t *testing.T) {
	t.Setenv("PROD_DB_PASSWORD", "s3cr3t-from-env")
	cr := NewConnectionResolver(
		&oneConnStore{conn: &models.Connection{
			ID: "c1", ConnID: "prod", Host: "db.internal",
			PasswordRef: "env://PROD_DB_PASSWORD",
		}},
		secrets.NewDefaultChain(nil),
	)

	conn, err := cr.ResolveConnectionByID("prod")
	if err != nil {
		t.Fatal(err)
	}
	if conn.Password != "s3cr3t-from-env" {
		t.Errorf("password = %q, want the resolved plaintext", conn.Password)
	}
}

// A plaintext password must survive untouched. A worker with no
// encryption key has no fallback resolver, so this is the shape the value
// arrives in and it must not be mangled on the way through.
func TestResolveConnectionByIDLeavesPlaintextAlone(t *testing.T) {
	cr := NewConnectionResolver(
		&oneConnStore{conn: &models.Connection{ID: "c1", ConnID: "prod", Password: "already-plain"}},
		secrets.NewDefaultChain(nil),
	)
	conn, err := cr.ResolveConnectionByID("prod")
	if err != nil {
		t.Fatal(err)
	}
	if conn.Password != "already-plain" {
		t.Errorf("password = %q, want it unchanged", conn.Password)
	}
}

func TestResolveConnectionByIDReportsAMissingConnection(t *testing.T) {
	cr := NewConnectionResolver(&oneConnStore{conn: nil}, secrets.NewDefaultChain(nil))
	_, err := cr.ResolveConnectionByID("nope")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v, want it to name the connection", err)
	}
}

// A resolver with no store must say so rather than panic: the API-only
// worker's own resolver is constructed exactly that way.
func TestResolveConnectionByIDWithNoStore(t *testing.T) {
	cr := &ConnectionResolver{}
	if _, err := cr.ResolveConnectionByID("x"); err == nil {
		t.Fatal("a resolver with no store returned success")
	}
}
