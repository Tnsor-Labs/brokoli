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

// sink_db wrote through context.Background() with its own fixed timeout,
// so a node's timeout did not reach the database: a write blocked on a
// lock kept going after its attempt timed out, and the retry wrote too.
// Here another session holds the table for 3s, the node times out after
// 2s and retries once. The timed-out write is now cancelled, and the rows
// land exactly once.
func TestSinkDBTimeoutCancelsTheWrite(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	table := fmt.Sprintf("brokoli_sink_timeout_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE TABLE ` + table + ` (id bigint, name text)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + table) })

	dir := t.TempDir()
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id,name\n1,a\n2,b\n3,c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	pipe := &models.Pipeline{ID: "sink-timeout", Name: "sink timeout", CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "src", Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "sink", Config: map[string]interface{}{
				"uri": uri, "table": table, "mode": "append",
				"timeout": float64(2), "max_retries": float64(1), "retry_delay": float64(10),
			}},
		},
		Edges: []models.Edge{{From: "src", To: "sink"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}

	// Hold the table for 3s from another session.
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
	if _, err := tx.Exec(`LOCK TABLE ` + table + ` IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(3 * time.Second)
		_ = tx.Rollback()
		close(released)
	}()

	run, runErr := NewRunner(s, nil, pipe, nil, nil, nil, nil, "", nil).Execute()
	<-released
	// Give a write that was not cancelled time to land after the lock goes.
	time.Sleep(2 * time.Second)

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("table has %d rows, want 3 (run %v, err %v): the timed-out attempt's write landed as well", n, run.Status, runErr)
	}
	if runErr != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %v, %v; want success on the retry", run.Status, runErr)
	}
}
