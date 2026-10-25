package engine

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// The statement path (ExecuteSQL, used where there is no bulk writer)
// ran under a background context with a ten-minute ceiling. Here another
// connection holds SQLite's write lock for 3s; the node times out after
// 2s and retries once. The timed-out write stops, and the rows land once.
func TestSinkDBStatementPathTimeoutCancelsTheWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.db")
	db, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE out (id INTEGER, name TEXT)`); err != nil {
		t.Fatal(err)
	}
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
	pipe := &models.Pipeline{ID: "sink-timeout-sqlite", Name: "sink timeout sqlite", CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "src", Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "sink", Config: map[string]interface{}{
				"uri": "sqlite://" + target, "table": "out", "mode": "append",
				"timeout": float64(2), "max_retries": float64(1), "retry_delay": float64(10),
			}},
		},
		Edges: []models.Edge{{From: "src", To: "sink"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(3 * time.Second)
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		close(released)
	}()

	run, runErr := NewRunner(s, nil, pipe, nil, nil, nil, nil, "", nil).Execute()
	<-released
	time.Sleep(2 * time.Second)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM out`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("table has %d rows, want 3 (run %v, err %v)", n, run.Status, runErr)
	}
}
