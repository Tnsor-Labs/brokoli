package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hc12r/broked/store"
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate data between databases (SQLite → PostgreSQL or vice versa)",
	Long: `Migrate all data from one database to another.

Examples:
  # SQLite to PostgreSQL
  broked migrate --from ./broked.db --to postgres://user:pass@host:5432/brokoli

  # PostgreSQL to SQLite
  broked migrate --from postgres://user:pass@host:5432/brokoli --to ./backup.db

  # Dry run (show counts, don't migrate)
  broked migrate --from ./broked.db --to postgres://... --dry-run`,
	RunE: runMigrate,
}

var (
	migrateFrom string
	migrateTo   string
	dryRun      bool
)

func init() {
	migrateCmd.Flags().StringVar(&migrateFrom, "from", "", "Source database URI (required)")
	migrateCmd.Flags().StringVar(&migrateTo, "to", "", "Target database URI (required)")
	migrateCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be migrated without executing")
	migrateCmd.MarkFlagRequired("from")
	migrateCmd.MarkFlagRequired("to")
	rootCmd.AddCommand(migrateCmd)
}

type tableSpec struct {
	name    string
	columns string
	fkDeps  []string // tables that must be migrated first
}

func migrationOrder() []tableSpec {
	return []tableSpec{
		{"users", "id, username, password_hash, role, created_at", nil},
		{"workspaces", "id, name, slug, description, created_at, updated_at", nil},
		{"roles", "id, name, description, permissions, is_system, created_at", nil},
		{"organizations", "id, name, slug, plan, max_pipelines, max_runs_per_day, max_storage_mb, max_members, contact_email, billing_email, company_size, industry, country, timezone, logo_url, phone, notes, account_status, trial_starts_at, trial_ends_at, plan_started_at, suspended_at, suspended_reason, created_at, updated_at", nil},
		{"org_members", "org_id, user_id, username, role, joined_at", []string{"organizations"}},
		{"workspace_members", "workspace_id, user_id, username, role, joined_at", []string{"workspaces"}},
		{"settings", "key, value", nil},
		{"connections", "id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, created_at, updated_at, workspace_id, org_id", nil},
		{"variables", "key, value, type, description, created_at, updated_at, workspace_id", nil},
		{"pipelines", "id, name, description, nodes, edges, schedule, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id", nil},
		{"runs", "id, pipeline_id, status, started_at, finished_at, org_id", []string{"pipelines"}},
		{"node_runs", "id, run_id, node_id, status, row_count, started_at, duration_ms, error", []string{"runs"}},
		{"logs", "run_id, node_id, level, message, timestamp", []string{"runs"}},
		{"pipeline_versions", "pipeline_id, version, snapshot, message, created_at", []string{"pipelines"}},
		{"node_previews", "run_id, node_id, columns, rows", []string{"runs"}},
		{"node_profiles", "run_id, node_id, profile, schema_snapshot, drift_alerts, created_at", []string{"runs"}},
		{"api_tokens", "id, name, token_hash, workspace_id, user_id, role, expires_at, created_at, last_used_at", []string{"workspaces"}},
		{"dead_letter_queue", "id, pipeline_id, run_id, error, node_id, node_name, payload, created_at, resolved, resolved_at", []string{"pipelines"}},
		{"support_tickets", "id, org_id, user_id, username, subject, body, status, priority, assigned_to, created_at, updated_at, resolved_at, resolved_by", []string{"organizations"}},
		{"ticket_replies", "id, ticket_id, user_id, username, is_staff, body, created_at, attachments", []string{"support_tickets"}},
		{"announcements", "id, title, body, type, target_org, created_by, created_at, expires_at", nil},
		{"ops_audit_log", "id, admin_id, admin_name, action, target_org, target_user, details, ip, created_at", nil},
		{"ops_team", "user_id, username, ops_role, added_at, added_by", nil},
		{"login_attempts", "username, ip, success, attempted_at", nil},
		{"permissions", "id, workspace_id, user_id, resource, resource_id, action", []string{"workspaces"}},
		{"oidc_group_mappings", "oidc_group, workspace_id, role", []string{"workspaces"}},
	}
}

func runMigrate(cmd *cobra.Command, args []string) error {
	srcDialect := store.DriverName(migrateFrom)
	dstDialect := store.DriverName(migrateTo)

	log.Printf("Migration: %s → %s", srcDialect, dstDialect)
	log.Printf("Source: %s", store.Describe(migrateFrom))
	log.Printf("Target: %s", store.Describe(migrateTo))

	// Open source
	srcStore, err := store.NewStore(migrateFrom)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer srcStore.Close()
	srcDB := srcStore.RawDB().(*sql.DB)

	// Open target (runs migrations to create tables)
	dstStore, err := store.NewStore(migrateTo)
	if err != nil {
		return fmt.Errorf("open target: %w", err)
	}
	defer dstStore.Close()
	dstDB := dstStore.RawDB().(*sql.DB)

	tables := migrationOrder()

	// Phase 1: Count
	log.Println("")
	log.Println("=== Source Data ===")
	totalRows := 0
	for _, t := range tables {
		count := tableCount(srcDB, t.name)
		if count > 0 {
			log.Printf("  %-25s %d rows", t.name, count)
			totalRows += count
		}
	}
	log.Printf("  %-25s %d rows", "TOTAL", totalRows)

	if dryRun {
		log.Println("")
		log.Println("Dry run — no data was migrated.")
		return nil
	}

	// Phase 2: Clear target
	log.Println("")
	log.Println("Clearing target tables...")
	for i := len(tables) - 1; i >= 0; i-- {
		dstDB.Exec(fmt.Sprintf("DELETE FROM %s", tables[i].name))
	}

	// Phase 3: Migrate
	log.Println("")
	log.Println("=== Migrating ===")
	start := time.Now()
	migrated := 0
	failed := 0

	isDestPG := dstDialect == "postgres"

	for _, t := range tables {
		count := tableCount(srcDB, t.name)
		if count == 0 {
			continue
		}

		n, err := migrateTable(srcDB, dstDB, t.name, t.columns, isDestPG)
		if err != nil {
			log.Printf("  %-25s FAILED: %v", t.name, err)
			failed++
		} else {
			log.Printf("  %-25s %d rows", t.name, n)
			migrated += n
		}
	}

	elapsed := time.Since(start)
	log.Println("")
	log.Printf("=== Complete === %d rows migrated in %s", migrated, elapsed.Round(time.Millisecond))
	if failed > 0 {
		log.Printf("WARNING: %d table(s) failed", failed)
	}

	// Phase 4: Verify
	log.Println("")
	log.Println("=== Verification ===")
	allMatch := true
	for _, t := range tables {
		srcCount := tableCount(srcDB, t.name)
		dstCount := tableCount(dstDB, t.name)
		status := "OK"
		if srcCount != dstCount {
			status = "MISMATCH"
			allMatch = false
		}
		if srcCount > 0 || dstCount > 0 {
			log.Printf("  %-25s src=%-6d dst=%-6d %s", t.name, srcCount, dstCount, status)
		}
	}
	if allMatch {
		log.Println("")
		log.Println("All tables match. Migration successful.")
	}

	return nil
}

func tableCount(db *sql.DB, table string) int {
	var count int
	db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count)
	return count
}

func migrateTable(src, dst *sql.DB, table, columns string, destIsPG bool) (int, error) {
	cols := strings.Split(columns, ", ")
	selectSQL := fmt.Sprintf("SELECT %s FROM %s", columns, table)

	rows, err := src.Query(selectSQL)
	if err != nil {
		return 0, fmt.Errorf("select: %w", err)
	}
	defer rows.Close()

	// Build insert with proper placeholders
	placeholders := make([]string, len(cols))
	for i := range cols {
		if destIsPG {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
		} else {
			placeholders[i] = "?"
		}
	}
	insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table, columns, strings.Join(placeholders, ", "))

	// Use a transaction for batch performance
	tx, err := dst.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}

	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	count := 0
	for rows.Next() {
		// Scan all columns as interface{}
		values := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			tx.Rollback()
			return count, fmt.Errorf("scan row %d: %w", count, err)
		}

		// Fix type mismatches for Postgres
		if destIsPG {
			for i, v := range values {
				col := strings.TrimSpace(cols[i])
				switch val := v.(type) {
				case []byte:
					s := string(val)
					// Fix empty strings for timestamp columns
					if s == "" && isTimestampCol(col) {
						values[i] = nil
					} else if s == "" && isBoolCol(col) {
						values[i] = false
					} else {
						// Check if it's JSON — keep as string
						values[i] = s
					}
				case string:
					if val == "" && isTimestampCol(col) {
						values[i] = nil
					} else if isBoolCol(col) {
						values[i] = val == "1" || val == "true"
					}
				case int64:
					if isBoolCol(col) {
						values[i] = val != 0
					}
				}
			}
		}

		if _, err := stmt.Exec(values...); err != nil {
			// Try to continue on individual row failures
			log.Printf("    WARNING: %s row %d: %v", table, count, truncateErr(err))
			continue
		}
		count++
	}

	if err := tx.Commit(); err != nil {
		return count, fmt.Errorf("commit: %w", err)
	}
	return count, rows.Err()
}

func isTimestampCol(col string) bool {
	ts := []string{
		"created_at", "updated_at", "started_at", "finished_at",
		"joined_at", "added_at", "attempted_at", "expires_at",
		"last_used_at", "resolved_at", "trial_starts_at", "trial_ends_at",
		"plan_started_at", "suspended_at", "timestamp",
	}
	for _, t := range ts {
		if col == t {
			return true
		}
	}
	return false
}

func isBoolCol(col string) bool {
	return col == "enabled" || col == "resolved" || col == "is_staff" ||
		col == "is_system" || col == "success" || col == "auto_sync"
}

func truncateErr(err error) string {
	s := err.Error()
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

// Ensure json import is used (for potential future use)
var _ = json.Marshal
