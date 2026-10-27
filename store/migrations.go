package store

import (
	"database/sql"
	"fmt"
	"log"
	"time"
)

// Versioned schema migrations.
//
// A database records the migrations applied to it in schema_migrations,
// one row per version, and a start applies only the ones it lacks. Before
// this, the stores ran their whole schema setup on every start: every
// statement had to be safe to repeat, so failures were ignored wholesale
// (79 of 94 statements on SQLite, 94 of 110 on Postgres), and a one-time
// rewrite of existing rows had nowhere to live except a scan on every
// boot.
//
// Version 1 is the baseline: the schema setup as it stood when versioning
// began, still idempotent, so it brings any database to the same shape
// whether it is empty or was created by an earlier release. It runs once
// more on a database that predates versioning and is then recorded, which
// is how existing installations join without guessing their version from
// their tables.
//
// A schema change after the baseline is a new migration with the next
// version, appended to the dialect's list (sqliteMigrations,
// postgresMigrations). It runs once, in its own transaction together with
// the row that records it, and a failure stops the start with nothing
// recorded. Never edit the baseline or an applied migration: a database
// that has recorded it will not run it again, so the change would reach
// new installations only. TestBaselineIsFrozen holds the baseline still.
//
// Work that must happen on every start, because it follows the code rather
// than the schema (system roles, built-in templates), is not a migration;
// see the stores' reconcile methods.

// migration is one versioned step. The baseline sets baseline and runs on
// the database directly, since its helpers do; every later step sets apply
// and runs inside the transaction that records it.
type migration struct {
	version  int
	name     string
	baseline func() error
	apply    func(tx *sql.Tx) error
}

const baselineVersion = 1

// migrationDialect holds the SQL that differs between the stores.
type migrationDialect struct {
	name        string
	createTable string
	record      string
}

var (
	sqliteMigrationDialect = migrationDialect{
		name: "sqlite",
		createTable: `CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL)`,
		record: `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
	}
	postgresMigrationDialect = migrationDialect{
		name: "postgres",
		createTable: `CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL)`,
		record: `INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)`,
	}
)

func (d migrationDialect) appliedAt(t time.Time) interface{} {
	if d.name == "sqlite" {
		return t.UTC().Format(timeFormat)
	}
	return t.UTC()
}

// runMigrations applies the migrations db has not recorded, in version
// order. On Postgres the caller holds WithMigrationLock, so processes
// starting together apply each step once.
func runMigrations(db *sql.DB, d migrationDialect, migrations []migration) error {
	if err := checkMigrationList(migrations); err != nil {
		return err
	}
	if _, err := db.Exec(d.createTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied, err := appliedMigrations(db)
	if err != nil {
		return err
	}

	latest := migrations[len(migrations)-1].version
	for v := range applied {
		if v > latest {
			// A newer build migrated this database, and this one is older:
			// a rollback, or an old process restarting during a rolling
			// deploy. Migrations only add, so the older code still runs;
			// refusing here would make every rollback an outage.
			log.Printf("store: the database has schema version %d, newer than this build's %d; continuing, since migrations only add to the schema", v, latest)
			break
		}
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(db, d, m); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}
		log.Printf("store: applied schema migration %d (%s)", m.version, m.name)
	}
	return nil
}

func applyMigration(db *sql.DB, d migrationDialect, m migration) error {
	if m.baseline != nil {
		if err := m.baseline(); err != nil {
			return err
		}
		_, err := db.Exec(d.record, m.version, m.name, d.appliedAt(time.Now()))
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	if err := m.apply(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(d.record, m.version, m.name, d.appliedAt(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

func appliedMigrations(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("read schema_migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	return applied, nil
}

// checkMigrationList refuses a malformed list: versions must run 1, 2, 3...
// with the baseline first and only there, and every step must do something.
func checkMigrationList(migrations []migration) error {
	if len(migrations) == 0 {
		return fmt.Errorf("no migrations")
	}
	for i, m := range migrations {
		switch {
		case m.version != i+1:
			return fmt.Errorf("migration %q has version %d, want %d: versions run 1, 2, 3... in order", m.name, m.version, i+1)
		case m.name == "":
			return fmt.Errorf("migration %d has no name", m.version)
		case (m.version == baselineVersion) != (m.baseline != nil):
			return fmt.Errorf("migration %d (%s): only version %d is the baseline", m.version, m.name, baselineVersion)
		case m.baseline == nil && m.apply == nil:
			return fmt.Errorf("migration %d (%s) does nothing", m.version, m.name)
		}
	}
	return nil
}

// schemaVersion reports the highest migration version db has recorded.
func schemaVersion(db *sql.DB) (int, error) {
	applied, err := appliedMigrations(db)
	if err != nil {
		return 0, err
	}
	max := 0
	for v := range applied {
		if v > max {
			max = v
		}
	}
	return max, nil
}
