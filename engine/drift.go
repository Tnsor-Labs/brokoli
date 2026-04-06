package engine

import "fmt"

// DriftType classifies a schema change.
type DriftType string

const (
	DriftColumnAdded   DriftType = "column_added"
	DriftColumnRemoved DriftType = "column_removed"
	DriftTypeChanged   DriftType = "type_changed"
	DriftNullSpike     DriftType = "null_spike"
)

// DriftAlert represents a single schema change.
type DriftAlert struct {
	Column   string    `json:"column"`
	Type     DriftType `json:"type"`
	Previous string    `json:"previous"`
	Current  string    `json:"current"`
	Severity string    `json:"severity"`
}

// DetectDrift compares two schema snapshots and returns drift alerts.
// A nil snapshot is treated as having zero columns.
func DetectDrift(previous, current *SchemaSnapshot) []DriftAlert {
	if previous == nil {
		previous = &SchemaSnapshot{}
	}
	if current == nil {
		current = &SchemaSnapshot{}
	}

	prevMap := make(map[string]SchemaColumn, len(previous.Columns))
	for _, c := range previous.Columns {
		prevMap[c.Name] = c
	}
	curMap := make(map[string]SchemaColumn, len(current.Columns))
	for _, c := range current.Columns {
		curMap[c.Name] = c
	}

	var alerts []DriftAlert

	// Check for removed columns (in previous but not current).
	for _, pc := range previous.Columns {
		if _, exists := curMap[pc.Name]; !exists {
			alerts = append(alerts, DriftAlert{
				Column:   pc.Name,
				Type:     DriftColumnRemoved,
				Previous: fmt.Sprintf("present (type=%s)", pc.Type),
				Current:  "absent",
				Severity: "critical",
			})
		}
	}

	// Check for added columns and changes.
	for _, cc := range current.Columns {
		pc, existed := prevMap[cc.Name]
		if !existed {
			alerts = append(alerts, DriftAlert{
				Column:   cc.Name,
				Type:     DriftColumnAdded,
				Previous: "absent",
				Current:  fmt.Sprintf("present (type=%s)", cc.Type),
				Severity: "warning",
			})
			continue
		}

		// Type change.
		if pc.Type != cc.Type {
			alerts = append(alerts, DriftAlert{
				Column:   cc.Name,
				Type:     DriftTypeChanged,
				Previous: pc.Type,
				Current:  cc.Type,
				Severity: "critical",
			})
		}

		// Null spike: increase of more than 20 percentage points.
		if cc.NullPct-pc.NullPct > 20 {
			alerts = append(alerts, DriftAlert{
				Column:   cc.Name,
				Type:     DriftNullSpike,
				Previous: fmt.Sprintf("%.1f%%", pc.NullPct),
				Current:  fmt.Sprintf("%.1f%%", cc.NullPct),
				Severity: "warning",
			})
		}
	}

	return alerts
}
