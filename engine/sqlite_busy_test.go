package engine

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A second writer to a SQLite file waits for the first one's lock instead of
// failing with SQLITE_BUSY (#834). Both ways of naming a SQLite database go
// through DetectDriver, so both are checked. Without a busy timeout the
// writer fails at once, well inside the hold below.
func TestSQLiteWriterWaitsForAnotherWritersLock(t *testing.T) {
	const hold = 300 * time.Millisecond
	for _, form := range []string{"scheme", "bare path"} {
		t.Run(form, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "busy.db")
			uri := "sqlite://" + path
			if form == "bare path" {
				uri = path
			}

			// The lock holder opens the file directly, with SQLite's
			// default timeout, so only the writer under test can wait.
			holder, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			if _, err := holder.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			conn, err := holder.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
				t.Fatal(err)
			}

			driver, dsn, err := DetectDriver(uri)
			if err != nil {
				t.Fatal(err)
			}
			writer, err := sql.Open(driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()

			done := make(chan error, 1)
			start := time.Now()
			go func() {
				_, err := writer.Exec(`INSERT INTO t (id) VALUES (1)`)
				done <- err
			}()
			time.Sleep(hold)
			if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("writer failed instead of waiting for the lock: %v", err)
			}
			if waited := time.Since(start); waited < hold {
				t.Fatalf("writer finished after %v, before the lock was released at %v", waited, hold)
			}
		})
	}
}

// The case #834 was reported from: a partitioned migrate whose ranges write
// to one SQLite file in parallel. Before the busy timeout, about half of
// these runs failed with "database is locked"; several runs make a
// regression all but certain to show.
func TestMigratePartitionsSQLiteTargetInParallel(t *testing.T) {
	const rows, runs = 3000, 5
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Exec(`CREATE TABLE events (id INTEGER PRIMARY KEY, payload TEXT)`); err != nil {
		t.Fatal(err)
	}
	tx, err := source.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= rows; i++ {
		if _, err := tx.Exec(`INSERT INTO events (id, payload) VALUES (?, ?)`, i, fmt.Sprintf("row-%04d", i)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	meta, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer meta.Close()
	ctx := context.Background()
	engine := NewEngine(meta)
	defer engine.Close(ctx)

	for run := 0; run < runs; run++ {
		destPath := filepath.Join(dir, fmt.Sprintf("dest-%d.db", run))
		dest, err := sql.Open("sqlite", destPath)
		if err != nil {
			t.Fatal(err)
		}
		defer dest.Close()
		if _, err := dest.Exec(`CREATE TABLE events (id INTEGER PRIMARY KEY, payload TEXT)`); err != nil {
			t.Fatal(err)
		}

		pipeline := &models.Pipeline{
			ID: fmt.Sprintf("sqlite-partition-%d", run), Name: "partitioned migrate into sqlite",
			Nodes: []models.Node{{
				ID: "migrate", Type: models.NodeTypeMigrate, Name: "Partitioned migrate",
				Config: map[string]interface{}{
					"source_uri": "sqlite://" + sourcePath, "source_query": "SELECT id, payload FROM events",
					"dest_uri": "sqlite://" + destPath, "dest_table": "events", "mode": "upsert",
					"key_columns": []interface{}{"id"}, "create_table": false, "chunk_size": 100,
					"partition": map[string]interface{}{
						"column": "id", "strategy": "numeric",
						"boundaries": []interface{}{float64(0), float64(501), float64(1001), float64(1501),
							float64(2001), float64(2501), float64(3001)},
						"max_parallel": float64(6),
					},
				},
			}},
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := meta.CreatePipeline(pipeline); err != nil {
			t.Fatal(err)
		}
		result, err := engine.RunPipeline(pipeline.ID)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if result.Status != models.RunStatusSuccess {
			t.Fatalf("run %d status=%s error=%s", run, result.Status, result.Error)
		}
		assertPartitionDestination(t, dest, "events", rows)
		instances, err := meta.ListExpansionInstancesByRun(result.ID)
		if err != nil {
			t.Fatal(err)
		}
		copied := 0
		for _, instance := range instances {
			if instance.Status != models.RunStatusSuccess {
				t.Fatalf("run %d partition %+v, want success", run, instance)
			}
			copied += instance.RowCount
		}
		if len(instances) != 6 || copied != rows {
			t.Fatalf("run %d: %d partitions copied %d rows, want 6 partitions and %d rows", run, len(instances), copied, rows)
		}
	}
}
