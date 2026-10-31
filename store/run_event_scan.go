package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// RunEventScanner reads the run event log across runs, oldest first. It
// is what turns run events into agent trigger deliveries (ADR-047): the
// scheduler's leader reads from a cursor and matches what it finds.
//
// It is an optional capability (type-assert), not part of Store, so a
// store that does not keep the log in SQL does not have to provide it.
type RunEventScanner interface {
	// ScanRunEvents returns events of the given types whose id is above
	// afterID, or that were recorded at or after since, ordered by id, at
	// most limit of them. The second condition re-reads a recent margin:
	// on Postgres an id is drawn before its transaction commits, so a
	// slow transaction can commit an id below one already read.
	ScanRunEvents(afterID int64, since time.Time, types []models.RunEventType, limit int) ([]models.RunEvent, error)
}

func scanEventsQuery(dialect string, types int) string {
	placeholders := make([]string, types)
	for i := range placeholders {
		if dialect == "postgres" {
			placeholders[i] = fmt.Sprintf("$%d", i+3)
		} else {
			placeholders[i] = "?"
		}
	}
	p1, p2, pl := "?", "?", "?"
	if dialect == "postgres" {
		p1, p2, pl = "$1", "$2", fmt.Sprintf("$%d", types+3)
	}
	return `SELECT id, run_id, node_id, attempt, event_type, payload, created_at, schema_version
		FROM run_events WHERE (id > ` + p1 + ` OR created_at >= ` + p2 + `)
		AND event_type IN (` + strings.Join(placeholders, ", ") + `) ORDER BY id ASC LIMIT ` + pl
}

func scanEventsArgs(afterID int64, since any, types []models.RunEventType, limit int) []any {
	args := []any{afterID, since}
	for _, t := range types {
		args = append(args, string(t))
	}
	return append(args, limit)
}

// ScanRunEvents implements RunEventScanner.
func (s *SQLiteStore) ScanRunEvents(afterID int64, since time.Time, types []models.RunEventType, limit int) ([]models.RunEvent, error) {
	if len(types) == 0 {
		return nil, nil
	}
	// created_at is fixed-width UTC text (timefmt.go), so it orders as
	// text does.
	rows, err := s.db.Query(scanEventsQuery("sqlite", len(types)), scanEventsArgs(afterID, formatTime(since), types, limit)...)
	if err != nil {
		return nil, wrapStoreErr("ScanRunEvents", "", err)
	}
	defer rows.Close()
	var events []models.RunEvent
	for rows.Next() {
		ev, err := scanRunEvent(rows)
		if err != nil {
			return nil, wrapStoreErr("ScanRunEvents", "", err)
		}
		events = append(events, *ev)
	}
	return events, rows.Err()
}

// ScanRunEvents implements RunEventScanner.
func (s *PostgresStore) ScanRunEvents(afterID int64, since time.Time, types []models.RunEventType, limit int) ([]models.RunEvent, error) {
	if len(types) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(scanEventsQuery("postgres", len(types)), scanEventsArgs(afterID, since.UTC(), types, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []models.RunEvent
	for rows.Next() {
		var ev models.RunEvent
		var nodeID sql.NullString
		var attempt sql.NullInt64
		var eventType string
		var payloadJSON []byte
		if err := rows.Scan(&ev.ID, &ev.RunID, &nodeID, &attempt, &eventType, &payloadJSON, &ev.CreatedAt, &ev.SchemaVersion); err != nil {
			return nil, err
		}
		ev.NodeID = nodeID.String
		if attempt.Valid {
			a := int(attempt.Int64)
			ev.Attempt = &a
		}
		ev.EventType = models.RunEventType(eventType)
		if err := json.Unmarshal(payloadJSON, &ev.Payload); err != nil {
			return nil, fmt.Errorf("decode event payload: %w", err)
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

var (
	_ RunEventScanner = (*SQLiteStore)(nil)
	_ RunEventScanner = (*PostgresStore)(nil)
)
