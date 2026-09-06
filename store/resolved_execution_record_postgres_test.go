package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// Live-Postgres coverage of ResolvedExecutionRecordStore. Same
// skip-if-unreachable discipline as taskbundlev2_postgres_test.go.
func openResolvedExecutionRecordTestPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres@localhost:5432/brokoli_leader_test?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := NewPostgresStore(dsn)
	if err != nil {
		t.Skipf("skipping live-Postgres resolved execution record test: %v", err)
	}
	if err := s.db.PingContext(ctx); err != nil {
		s.Close()
		t.Skipf("skipping live-Postgres resolved execution record test: no reachable Postgres at %s", dsn)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestResolvedExecutionRecordStore_PostgresSemantics(t *testing.T) {
	s := openResolvedExecutionRecordTestPostgresStore(t)
	runID := fmt.Sprintf("pg-rer-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = s.db.ExecContext(ctx, `DELETE FROM resolved_execution_records WHERE run_id = $1`, runID)
	})

	record := sampleRecord("standard@1")
	created, err := s.PutResolvedExecutionRecord(runID, "task", record)
	if err != nil || !created {
		t.Fatalf("first postgres pin: created=%v err=%v", created, err)
	}
	created, err = s.PutResolvedExecutionRecord(runID, "task", sampleRecord("standard@1"))
	if err != nil || created {
		t.Fatalf("identical re-pin: created=%v err=%v (want false,nil)", created, err)
	}
	if _, err := s.PutResolvedExecutionRecord(runID, "task", sampleRecord("standard@2")); err != ErrResolvedExecutionRecordConflict {
		t.Fatalf("different re-pin: got %v, want ErrResolvedExecutionRecordConflict", err)
	}
	got, err := s.GetResolvedExecutionRecord(runID, "task")
	if err != nil || *got != *record {
		t.Fatalf("GetResolvedExecutionRecord = %+v, %v", got, err)
	}
}
