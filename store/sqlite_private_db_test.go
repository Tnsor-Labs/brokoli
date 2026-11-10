package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// A database SQLite makes per connection cannot back a pooled store: a
// test on one passed 4 runs in 10 (#636). NewSQLiteStore refuses it.
func TestNewSQLiteStoreRefusesAPerConnectionDatabase(t *testing.T) {
	for _, path := range []string{":memory:", "", " ", "file::memory:", "file::memory:?cache=shared", "file:x?mode=memory", "file:x?mode=memory&cache=shared"} {
		s, err := NewSQLiteStore(path)
		if err == nil {
			s.Close()
			t.Errorf("NewSQLiteStore(%q) opened a store", path)
			continue
		}
		if !strings.Contains(err.Error(), "t.TempDir()") {
			t.Errorf("NewSQLiteStore(%q) error %q does not say what to use instead", path, err)
		}
	}
}

func TestNewSQLiteStoreOpensAFileWithMemoryInItsName(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	s.Close()
}
