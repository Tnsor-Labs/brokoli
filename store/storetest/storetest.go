// Package storetest opens SQLite stores for tests without migrating each
// one from nothing.
//
// A fresh store.NewSQLiteStore runs every migration, which costs about
// 0.25s, and about 0.7s under -race on CI. The engine package opens a new
// store in most of its 1500 tests, so migrations were close to half of its
// test time (#329). Here the first call migrates one store into a
// template, and every later call writes the template's bytes to the new
// path and opens that, which runs no migration: about 90ms under -race.
//
// It is for tests. Production opens stores with store.NewSQLiteStore.
package storetest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/Tnsor-Labs/brokoli/store"
)

var (
	templateOnce  sync.Once
	templateBytes []byte
	templateErr   error
)

// NewSQLiteStore is store.NewSQLiteStore for a test. A path that does not
// exist yet starts as a copy of an already migrated, empty store; a path
// that exists is opened as it is, so a test that closes and reopens its
// store keeps its data.
func NewSQLiteStore(path string) (*store.SQLiteStore, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		templateOnce.Do(buildTemplate)
		if templateErr != nil {
			return nil, templateErr
		}
		if err := os.WriteFile(path, templateBytes, 0o600); err != nil {
			return nil, fmt.Errorf("storetest: write %s: %w", path, err)
		}
	}
	return store.NewSQLiteStore(path)
}

// buildTemplate migrates one store in a directory of its own, keeps the
// database file's bytes, and removes the directory. Closing the store
// checkpoints its write-ahead log into the file, so the bytes are the
// whole database.
func buildTemplate() {
	dir, err := os.MkdirTemp("", "brokoli-storetest-")
	if err != nil {
		templateErr = fmt.Errorf("storetest: template dir: %w", err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "template.db")
	s, err := store.NewSQLiteStore(path)
	if err != nil {
		templateErr = fmt.Errorf("storetest: migrate template: %w", err)
		return
	}
	if err := s.Close(); err != nil {
		templateErr = fmt.Errorf("storetest: close template: %w", err)
		return
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil && info.Size() > 0 && suffix == "-wal" {
			templateErr = fmt.Errorf("storetest: template left a non-empty write-ahead log")
			return
		}
	}
	templateBytes, templateErr = os.ReadFile(path)
}
