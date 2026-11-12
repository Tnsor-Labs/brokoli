package store

import (
	"context"
	"testing"
)

// holdConnections takes n connections from s at once, then returns them
// all, as n concurrent callers would.
func holdConnections(t *testing.T, s *PostgresStore, n int) {
	t.Helper()
	ctx := context.Background()
	conns := make([]interface{ Close() error }, 0, n)
	for i := 0; i < n; i++ {
		c, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// Connections returned by concurrent callers stay open for the next ones,
// instead of all but two being closed and reopened (#967 benchmark: 453
// sessions for a 1,000-item run).
func TestPostgresStoreKeepsConcurrentConnectionsIdle(t *testing.T) {
	s := openTestPostgresStoreOnly(t)
	holdConnections(t, s, 16)
	stats := s.db.Stats()
	if stats.MaxIdleClosed != 0 {
		t.Fatalf("%d of 16 connections were closed when returned, want 0", stats.MaxIdleClosed)
	}
	if stats.Idle < 16 {
		t.Fatalf("%d connections idle after 16 callers returned theirs, want at least 16", stats.Idle)
	}
	opened := stats.OpenConnections
	holdConnections(t, s, 16)
	if again := s.db.Stats().OpenConnections; again != opened {
		t.Fatalf("16 more callers left %d connections open, want the same %d reused", again, opened)
	}
}

func TestPostgresStoreIdleConnectionsFromEnvironment(t *testing.T) {
	t.Setenv("BROKOLI_POSTGRES_MAX_IDLE_CONNS", "3")
	s := openTestPostgresStoreOnly(t)
	holdConnections(t, s, 8)
	if idle := s.db.Stats().Idle; idle != 3 {
		t.Fatalf("%d connections idle with BROKOLI_POSTGRES_MAX_IDLE_CONNS=3, want 3", idle)
	}
}
