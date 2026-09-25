package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// "this store cannot look connections up" and "there is no such
// connection" are different facts, and they were wearing the same
// sentence. A worker on an HTTP-backed store refuses GetConnection
// outright, so an operator was told `conn_id "x" not found` -- a claim
// about their data that was really a property of their deployment --
// while the node ran on with unresolved credentials and failed somewhere
// less obvious.

type connStore struct {
	store.Store
	err error
}

func (c *connStore) GetConnection(string) (*models.Connection, error) { return nil, c.err }

func resolveWith(t *testing.T, err error) []string {
	t.Helper()
	cr := &ConnectionResolver{store: &connStore{err: err}}
	_, warnings := cr.ResolveWithWarnings(
		map[string]interface{}{"conn_id": "prod-warehouse"}, models.NodeTypeSourceDB)
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
	}
	return warnings
}

func TestResolverSaysWhenTheStoreCannotResolveConnectionsAtAll(t *testing.T) {
	w := resolveWith(t, fmt.Errorf("apistore: GetConnection not supported on worker: %w", store.ErrUnsupported))[0]

	// It must not claim the connection is missing, because that sends the
	// operator to look at their connection list.
	if strings.Contains(w, "not found") {
		t.Errorf("reported a capability gap as missing data: %q", w)
	}
	for _, want := range []string{"prod-warehouse", "no access to stored connections", "without its credentials"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q does not mention %q", w, want)
		}
	}
}

func TestResolverStillSaysNotFoundForAGenuinelyMissingConnection(t *testing.T) {
	w := resolveWith(t, fmt.Errorf("sql: no rows in result set"))[0]
	if !strings.Contains(w, "not found") {
		t.Errorf("a genuinely missing connection should still read as not found: %q", w)
	}
	if strings.Contains(w, "no access to stored connections") {
		t.Errorf("a missing row was reported as a capability gap: %q", w)
	}
}
