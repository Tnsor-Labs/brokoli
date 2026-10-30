package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

type logBatchStore interface {
	Store
	LogBatchAppender
}

func testAppendLogs(t *testing.T, s logBatchStore) {
	runID := seedRunWithNodes(t, s, 0)
	at := time.Date(2026, 10, 30, 4, 0, 0, 0, time.UTC)
	var entries []*models.LogEntry
	for i := 0; i < 5; i++ {
		entries = append(entries, &models.LogEntry{
			RunID: runID, NodeID: "n", Level: models.LogLevelInfo, Message: fmt.Sprintf("line %d", i),
			Timestamp: at.Add(time.Duration(i) * time.Millisecond), Metadata: map[string]string{"i": fmt.Sprint(i)},
		})
	}
	if err := s.AppendLogs(entries); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLogs(nil); err != nil {
		t.Fatalf("an empty batch: %v", err)
	}
	got, err := s.GetLogs(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("%d lines stored, want 5", len(got))
	}
	for i, e := range got {
		if e.Message != fmt.Sprintf("line %d", i) || e.Metadata["i"] != fmt.Sprint(i) {
			t.Errorf("line %d read back as %q %v", i, e.Message, e.Metadata)
		}
	}

	// All or nothing: a batch with one line that cannot be stored stores
	// none of them.
	bad := []*models.LogEntry{
		{RunID: runID, NodeID: "n", Level: models.LogLevelInfo, Message: "kept?", Timestamp: at},
		{RunID: "no-such-run", NodeID: "n", Level: models.LogLevelInfo, Message: "orphan", Timestamp: at},
	}
	if err := s.AppendLogs(bad); err == nil || !strings.Contains(err.Error(), "log entry 1") {
		t.Fatalf("a batch with a line for a missing run: %v, want an error naming entry 1", err)
	}
	if got, _ := s.GetLogs(runID); len(got) != 5 {
		t.Errorf("a failed batch left %d lines, want the 5 from before", len(got))
	}
}

func TestSQLiteAppendLogs(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	testAppendLogs(t, s)
}

func TestPostgresAppendLogs(t *testing.T) {
	testAppendLogs(t, openTestPostgresStore(t))
}
