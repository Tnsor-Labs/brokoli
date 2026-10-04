package engine

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func TestParseMigratePartitionConfig(t *testing.T) {
	config, ranges, err := parseMigratePartitionConfig(map[string]interface{}{
		"column":       "id",
		"strategy":     "numeric",
		"boundaries":   []interface{}{float64(0), float64(10), float64(20)},
		"max_parallel": float64(2),
	})
	if err != nil {
		t.Fatalf("parse partition: %v", err)
	}
	if config.MaxParallel != 2 || len(ranges) != 2 {
		t.Fatalf("config=%+v ranges=%d, want max_parallel=2 and 2 ranges", config, len(ranges))
	}
	if ranges[1].Lower != float64(10) || ranges[1].Upper != float64(20) {
		t.Fatalf("second range=%+v", ranges[1])
	}
}

func TestParseMigratePartitionConfigRejectsInvalidBoundaries(t *testing.T) {
	for name, boundaries := range map[string]interface{}{
		"not increasing": []interface{}{float64(2), float64(2)},
		"too few":        []interface{}{float64(2)},
		"wrong type":     []interface{}{float64(1), "2"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := parseMigratePartitionConfig(map[string]interface{}{
				"column": "id", "strategy": "numeric", "boundaries": boundaries,
			})
			if err == nil {
				t.Fatal("expected invalid partition boundaries to fail")
			}
		})
	}
}

func TestPartitionedMigrateRequiresPartitionColumnInKey(t *testing.T) {
	node := models.Node{Type: models.NodeTypeMigrate, Name: "partitioned", Config: map[string]interface{}{
		"source_uri": "postgres://source/db", "source_query": "SELECT id, payload FROM source",
		"dest_uri": "postgres://dest/db", "dest_table": "dest", "mode": "upsert",
		"key_columns": []interface{}{"id"},
		"partition": map[string]interface{}{
			"column": "created_at", "strategy": "date",
			"boundaries": []interface{}{"2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z"},
		},
	}}
	result := NodeValidationResult{}
	validateNodeConfigDetailed(node, &result)
	for _, issue := range result.Errors {
		if strings.Contains(issue, "must be included in key_columns") {
			return
		}
	}
	t.Fatalf("validation errors=%v, want partition key safety error", result.Errors)
}

func TestMigratePartitionQuery(t *testing.T) {
	query, args, err := migratePartitionQuery(
		"SELECT id, name FROM source",
		"postgres",
		migratePartitionConfig{Column: "id", Strategy: "numeric"},
		migratePartitionRange{Lower: float64(10), Upper: float64(20)},
	)
	if err != nil {
		t.Fatalf("partition query: %v", err)
	}
	want := `SELECT * FROM (SELECT id, name FROM source) AS brokoli_partition_source WHERE "id" >= $1 AND "id" < $2`
	if query != want {
		t.Fatalf("query=%q, want %q", query, want)
	}
	if len(args) != 2 || args[0] != float64(10) || args[1] != float64(20) {
		t.Fatalf("args=%#v", args)
	}
}

func TestMigratePartitionsPostgresEndToEnd(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	sourceTable := "brokoli_partition_src_" + tag
	destTable := "brokoli_partition_dst_" + tag
	if _, err := db.Exec(`CREATE TABLE ` + sourceTable + ` (id bigint PRIMARY KEY, payload text); CREATE TABLE ` + destTable + ` (id bigint PRIMARY KEY, payload text);`); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TABLE IF EXISTS ` + sourceTable + `; DROP TABLE IF EXISTS ` + destTable)
	})

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO `+sourceTable+` (id, payload) VALUES ($1, $2)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 1000; i++ {
		if _, err := stmt.Exec(i, fmt.Sprintf("row-%04d", i)); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			t.Fatalf("insert source row %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	meta, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer meta.Close()
	pipeline := &models.Pipeline{
		ID: "partition-e2e-" + tag, Name: "partitioned migration e2e",
		Nodes: []models.Node{{
			ID: "migrate", Type: models.NodeTypeMigrate, Name: "Partitioned migrate",
			Config: map[string]interface{}{
				"source_uri": uri, "source_query": "SELECT id, payload FROM " + sourceTable,
				"dest_uri": uri, "dest_table": destTable, "mode": "upsert",
				"key_columns": []interface{}{"id"}, "create_table": false, "chunk_size": 100,
				"partition": map[string]interface{}{
					"column": "id", "strategy": "numeric",
					"boundaries":   []interface{}{float64(0), float64(250), float64(500), float64(750), float64(1001), float64(2000)},
					"max_parallel": float64(4),
				},
			},
		}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := meta.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(meta)
	defer engine.Close(ctx)
	first, err := engine.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("first RunPipeline: %v", err)
	}
	if first.Status != models.RunStatusSuccess {
		t.Fatalf("first run status=%s error=%s", first.Status, first.Error)
	}
	assertPartitionDestination(t, db, destTable, 1000)
	assertPartitionMetadata(t, meta, first.ID, 5)

	second, err := engine.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("second RunPipeline: %v", err)
	}
	if second.Status != models.RunStatusSuccess {
		t.Fatalf("second run status=%s error=%s", second.Status, second.Error)
	}
	assertPartitionDestination(t, db, destTable, 1000)

	failedPipeline := *pipeline
	failedPipeline.ID = "partition-failure-e2e-" + tag
	failedPipeline.Nodes = []models.Node{pipeline.Nodes[0]}
	failedPipeline.Nodes[0].Config = cloneConfig(pipeline.Nodes[0].Config)
	failedPipeline.Nodes[0].Config["source_query"] = "SELECT id, payload FROM brokoli_partition_missing_table"
	if err := meta.CreatePipeline(&failedPipeline); err != nil {
		t.Fatal(err)
	}
	failedRun, err := engine.RunPipeline(failedPipeline.ID)
	if err != nil && failedRun == nil {
		t.Fatalf("failed RunPipeline returned no run: %v", err)
	}
	if failedRun == nil {
		t.Fatal("failed RunPipeline returned nil run")
	}
	if failedRun.Status != models.RunStatusFailed {
		t.Fatalf("failed run status=%s error=%s, want failed", failedRun.Status, failedRun.Error)
	}
	failedInstances, err := meta.ListExpansionInstancesByRun(failedRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(failedInstances) != 5 {
		t.Fatalf("failed run expansion instances=%d, want 5", len(failedInstances))
	}
	failedCount := 0
	for _, instance := range failedInstances {
		if instance.Status == models.RunStatusFailed {
			failedCount++
		}
	}
	if failedCount != 5 {
		t.Fatalf("failed run has %d failed partitions, want 5", failedCount)
	}
	assertPartitionDestination(t, db, destTable, 1000)
}

func TestMigratePartitionsPostgresBenchmark(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	const rowCount = 250000
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	sourceTable := "brokoli_partition_bench_src_" + tag
	if _, err := db.Exec(`CREATE TABLE ` + sourceTable + ` (id bigint PRIMARY KEY, payload text)`); err != nil {
		t.Fatalf("create benchmark source: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO `+sourceTable+` (id, payload) SELECT id, 'row-' || id::text FROM generate_series(1, $1) AS id`, rowCount); err != nil {
		t.Fatalf("populate benchmark source: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + sourceTable) })

	type result struct {
		name       string
		partitions int
		parallel   int
		elapsed    time.Duration
	}
	results := make([]result, 0, 5)
	for _, tc := range []struct {
		name       string
		partitions int
		parallel   int
	}{
		{name: "single", partitions: 1, parallel: 1},
		{name: "2 partitions", partitions: 2, parallel: 2},
		{name: "4 partitions", partitions: 4, parallel: 4},
		{name: "8 partitions", partitions: 8, parallel: 8},
		{name: "16 partitions", partitions: 16, parallel: 16},
	} {
		destTable := "brokoli_partition_bench_dst_" + tag + "_" + fmt.Sprint(tc.partitions)
		if _, err := db.Exec(`CREATE TABLE ` + destTable + ` (id bigint PRIMARY KEY, payload text)`); err != nil {
			t.Fatalf("create benchmark destination %s: %v", destTable, err)
		}
		t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + destTable) })

		config := map[string]interface{}{
			"source_uri": uri, "source_query": "SELECT id, payload FROM " + sourceTable,
			"dest_uri": uri, "dest_table": destTable, "mode": "upsert",
			"key_columns": []interface{}{"id"}, "create_table": false, "chunk_size": 1000,
		}
		if tc.partitions > 1 {
			boundaries := make([]interface{}, tc.partitions+1)
			for i := range boundaries {
				boundaries[i] = float64(i * (rowCount / tc.partitions))
			}
			boundaries[len(boundaries)-1] = float64(rowCount + 1)
			config["partition"] = map[string]interface{}{
				"column": "id", "strategy": "numeric", "boundaries": boundaries,
				"max_parallel": float64(tc.parallel),
			}
		}

		meta, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "meta.db"))
		if err != nil {
			t.Fatal(err)
		}
		pipeline := &models.Pipeline{
			ID: "partition-benchmark-" + tag + "-" + fmt.Sprint(tc.partitions), Name: "partition migration benchmark",
			Nodes:     []models.Node{{ID: "migrate", Type: models.NodeTypeMigrate, Name: "Migrate", Config: config}},
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := meta.CreatePipeline(pipeline); err != nil {
			meta.Close()
			t.Fatal(err)
		}
		engine := NewEngine(meta)
		started := time.Now()
		run, runErr := engine.RunPipeline(pipeline.ID)
		elapsed := time.Since(started)
		engine.Close(context.Background())
		meta.Close()
		if runErr != nil || run == nil || run.Status != models.RunStatusSuccess {
			t.Fatalf("benchmark %s failed: run=%+v err=%v", tc.name, run, runErr)
		}
		assertPartitionDestination(t, db, destTable, rowCount)
		results = append(results, result{name: tc.name, partitions: tc.partitions, parallel: tc.parallel, elapsed: elapsed})
	}

	t.Log("benchmark: end-to-end PostgreSQL migration")
	t.Log("case | partitions | max_parallel | elapsed | rows/sec | speedup")
	base := results[0].elapsed.Seconds()
	for _, got := range results {
		seconds := got.elapsed.Seconds()
		t.Logf("%s | %d | %d | %s | %.0f | %.2fx", got.name, got.partitions, got.parallel, got.elapsed.Round(time.Millisecond), float64(rowCount)/seconds, base/seconds)
	}
}

func TestMigratePartitionsPostgresCrossDatabaseBenchmark(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	const rowCount = 100000
	admin, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	sourceDBName := "brokoli_partition_src_" + tag
	destDBName := "brokoli_partition_dst_" + tag
	for _, name := range []string{sourceDBName, destDBName} {
		if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
			t.Fatalf("create database %s: %v", name, err)
		}
	}
	t.Cleanup(func() {
		for _, name := range []string{sourceDBName, destDBName} {
			_, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`)
		}
	})
	sourceURI := postgresURIForDatabase(t, uri, sourceDBName)
	destURI := postgresURIForDatabase(t, uri, destDBName)
	if canMigratePostgresSameServer(sourceURI, destURI, "postgres", ModeUpsert, false, "SELECT 1") {
		t.Fatal("cross-database benchmark must use the portable migration path")
	}
	sourceDB, err := sql.Open("pgx", sourceURI)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceDB.Close()
	destDB, err := sql.Open("pgx", destURI)
	if err != nil {
		t.Fatal(err)
	}
	defer destDB.Close()
	if _, err := sourceDB.Exec(`CREATE TABLE source_rows (id bigint PRIMARY KEY, payload text)`); err != nil {
		t.Fatalf("create cross-database source: %v", err)
	}
	if _, err := sourceDB.Exec(`INSERT INTO source_rows (id, payload) SELECT id, 'row-' || id::text FROM generate_series(1, $1) AS id`, rowCount); err != nil {
		t.Fatalf("populate cross-database source: %v", err)
	}

	type result struct {
		partitions int
		elapsed    time.Duration
	}
	var results []result
	for _, partitions := range []int{1, 2, 4, 8} {
		table := "destination_" + fmt.Sprint(partitions)
		if _, err := destDB.Exec(`CREATE TABLE ` + table + ` (id bigint PRIMARY KEY, payload text)`); err != nil {
			t.Fatal(err)
		}
		config := map[string]interface{}{
			"source_uri": sourceURI, "source_query": "SELECT id, payload FROM source_rows",
			"dest_uri": destURI, "dest_table": table, "mode": "upsert",
			"key_columns": []interface{}{"id"}, "create_table": false, "chunk_size": 1000,
		}
		if partitions > 1 {
			boundaries := make([]interface{}, partitions+1)
			for i := range boundaries {
				boundaries[i] = float64(i * (rowCount / partitions))
			}
			boundaries[len(boundaries)-1] = float64(rowCount + 1)
			config["partition"] = map[string]interface{}{
				"column": "id", "strategy": "numeric", "boundaries": boundaries,
				"max_parallel": float64(partitions),
			}
		}
		meta, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "meta.db"))
		if err != nil {
			t.Fatal(err)
		}
		pipeline := &models.Pipeline{
			ID: "cross-db-benchmark-" + tag + "-" + fmt.Sprint(partitions), Name: "cross database partition benchmark",
			Nodes:     []models.Node{{ID: "migrate", Type: models.NodeTypeMigrate, Name: "Migrate", Config: config}},
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := meta.CreatePipeline(pipeline); err != nil {
			meta.Close()
			t.Fatal(err)
		}
		engine := NewEngine(meta)
		started := time.Now()
		run, runErr := engine.RunPipeline(pipeline.ID)
		elapsed := time.Since(started)
		engine.Close(context.Background())
		meta.Close()
		if runErr != nil || run == nil || run.Status != models.RunStatusSuccess {
			t.Fatalf("cross-database benchmark %d partitions failed: run=%+v err=%v", partitions, run, runErr)
		}
		assertPartitionDestination(t, destDB, table, rowCount)
		results = append(results, result{partitions: partitions, elapsed: elapsed})
	}
	t.Log("benchmark: cross-database PostgreSQL migration using portable Go row transport")
	t.Log("partitions | elapsed | rows/sec | speedup")
	base := results[0].elapsed.Seconds()
	for _, got := range results {
		seconds := got.elapsed.Seconds()
		t.Logf("%d | %s | %.0f | %.2fx", got.partitions, got.elapsed.Round(time.Millisecond), float64(rowCount)/seconds, base/seconds)
	}
}

func postgresURIForDatabase(t *testing.T, raw, database string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + database
	parsed.RawPath = ""
	return parsed.String()
}

func assertPartitionDestination(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var count, distinct, minID, maxID int
	if err := db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT id), MIN(id), MAX(id) FROM `+table).Scan(&count, &distinct, &minID, &maxID); err != nil {
		t.Fatal(err)
	}
	if count != want || distinct != want || minID != 1 || maxID != want {
		t.Fatalf("destination count=%d distinct=%d range=%d..%d, want %d distinct rows 1..%d", count, distinct, minID, maxID, want, want)
	}
}

func assertPartitionMetadata(t *testing.T, meta *store.SQLiteStore, runID string, want int) {
	t.Helper()
	instances, err := meta.ListExpansionInstancesByRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != want {
		t.Fatalf("expansion instances=%d, want %d", len(instances), want)
	}
	totalRows := 0
	emptyPartitions := 0
	for _, instance := range instances {
		if instance.Status != models.RunStatusSuccess {
			t.Fatalf("partition metadata=%+v, want successful partition", instance)
		}
		totalRows += instance.RowCount
		if instance.RowCount == 0 {
			emptyPartitions++
		}
	}
	if totalRows != 1000 || emptyPartitions != 1 {
		t.Fatalf("partition row counts total=%d empty=%d, want total=1000 and one empty partition", totalRows, emptyPartitions)
	}
	attempts, err := meta.ListExecutionAttemptsByRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != want+1 {
		t.Fatalf("execution attempts=%d, want %d partition attempts plus node attempt", len(attempts), want)
	}
	for _, attempt := range attempts {
		if attempt.Status != models.AttemptStatusCompleted {
			t.Fatalf("execution attempt=%+v, want completed", attempt)
		}
	}
}
