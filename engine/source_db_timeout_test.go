package engine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// source_db queried with no context of the node's (a background one with
// a five-minute ceiling), so when its attempt timed out the query went on
// waiting in the database, holding a connection and whatever it was
// waiting for. The source table is locked for 8s and the node times out
// after 2s; once the node has given up, its query is no longer running.
func TestSourceDBTimeoutCancelsTheQuery(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tag := time.Now().UnixNano()
	table := fmt.Sprintf("brokoli_src_timeout_%d", tag)
	marker := fmt.Sprintf("brokoli-source-timeout-%d", tag)
	if _, err := db.Exec(`CREATE TABLE ` + table + ` (id bigint); INSERT INTO ` + table + ` VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + table) })

	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	pipe := &models.Pipeline{ID: "source-timeout", Name: "source timeout", CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{{ID: "src", Type: models.NodeTypeSourceDB, Name: "src", Config: map[string]interface{}{
			"uri": uri, "query": "SELECT id FROM " + table + " /* " + marker + " */", "timeout": float64(2),
		}}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lockConn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	tx, err := lockConn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`LOCK TABLE ` + table + ` IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	if _, err := NewRunner(s, nil, pipe, nil, nil, nil, nil, "", nil).Execute(); err == nil {
		t.Fatal("the node did not time out")
	}
	time.Sleep(500 * time.Millisecond) // let a cancellation reach the server
	var running int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE query LIKE '%' || $1 || '%' AND pid <> pg_backend_pid() AND state = 'active'`, marker).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 0 {
		t.Fatalf("%d copies of the source query are still running after the node timed out", running)
	}
}
