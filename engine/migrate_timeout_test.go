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

// migrate wrote under the run's context, so a node timeout never reached
// the database: a write blocked on a lock outlived its attempt, and the
// retry migrated the rows again. Another session holds the destination
// for 3s; the node times out after 2s and retries once. The rows now land
// exactly once.
func TestMigrateTimeoutCancelsTheWrite(t *testing.T) {
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
	src, dst := fmt.Sprintf("brokoli_mig_src_%d", tag), fmt.Sprintf("brokoli_mig_dst_%d", tag)
	if _, err := db.Exec(`CREATE TABLE ` + src + ` (id bigint, name text); CREATE TABLE ` + dst + ` (id bigint, name text);
		INSERT INTO ` + src + ` VALUES (1,'a'),(2,'b'),(3,'c')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + src + `; DROP TABLE IF EXISTS ` + dst) })

	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	pipe := &models.Pipeline{ID: "migrate-timeout", Name: "migrate timeout", CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{{ID: "migrate", Type: models.NodeTypeMigrate, Name: "migrate", Config: map[string]interface{}{
			"source_uri": uri, "source_query": "SELECT id, name FROM " + src,
			"dest_uri": uri, "dest_table": dst, "mode": "append", "create_table": false,
			"timeout": float64(2), "max_retries": float64(1), "retry_delay": float64(10),
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
	if _, err := tx.Exec(`LOCK TABLE ` + dst + ` IN ACCESS EXCLUSIVE MODE`); err != nil {
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
	time.Sleep(2 * time.Second)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + dst).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("destination has %d rows, want 3 (run %v, err %v)", n, run.Status, runErr)
	}
	if runErr != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %v, %v; want success on the retry", run.Status, runErr)
	}
}
