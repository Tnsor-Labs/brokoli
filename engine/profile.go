package engine

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ColumnProfile holds statistics for a single column.
type ColumnProfile struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	NullCount    int      `json:"null_count"`
	NullPct      float64  `json:"null_pct"`
	UniqueCount  int      `json:"unique_count"`
	UniquePct    float64  `json:"unique_pct"`
	MinVal       string   `json:"min_val,omitempty"`
	MaxVal       string   `json:"max_val,omitempty"`
	MeanVal      float64  `json:"mean_val,omitempty"`
	IsNumeric    bool     `json:"is_numeric"`
	SampleValues []string `json:"sample_values,omitempty"`
}

// DataProfile holds statistics for a complete dataset.
type DataProfile struct {
	RowCount    int             `json:"row_count"`
	ColumnCount int             `json:"column_count"`
	Columns     []ColumnProfile `json:"columns"`
	ProfilingMs int64           `json:"profiling_ms"`
}

// SchemaSnapshot captures the schema at a point in time.
type SchemaSnapshot struct {
	Columns []SchemaColumn `json:"columns"`
}

// SchemaColumn describes a single column in a schema snapshot.
type SchemaColumn struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	NullPct float64 `json:"null_pct"`
}

// ProfileDataSet computes a DataProfile from a DataSet.
func ProfileDataSet(ds *common.DataSet) *DataProfile {
	start := time.Now()

	if ds == nil || len(ds.Columns) == 0 {
		return &DataProfile{
			Columns:     []ColumnProfile{},
			ProfilingMs: time.Since(start).Milliseconds(),
		}
	}

	rowCount := len(ds.Rows)
	profiles := make([]ColumnProfile, len(ds.Columns))

	for i, col := range ds.Columns {
		profiles[i] = profileColumn(col, ds.Rows)
	}

	return &DataProfile{
		RowCount:    rowCount,
		ColumnCount: len(ds.Columns),
		Columns:     profiles,
		ProfilingMs: time.Since(start).Milliseconds(),
	}
}

func profileColumn(name string, rows []common.DataRow) ColumnProfile {
	p := ColumnProfile{Name: name}

	total := len(rows)
	if total == 0 {
		p.Type = "null"
		return p
	}

	uniqueSet := make(map[string]struct{})
	var samples []string

	var numericCount int
	var numSum, numMin, numMax float64
	numMinSet := false
	allNumeric := true

	for _, row := range rows {
		val, exists := row[name]
		if !exists || val == nil {
			p.NullCount++
			continue
		}

		str := fmt.Sprintf("%v", val)
		uniqueSet[str] = struct{}{}

		if len(samples) < 3 {
			// Only add distinct sample values.
			isDup := false
			for _, s := range samples {
				if s == str {
					isDup = true
					break
				}
			}
			if !isDup {
				samples = append(samples, str)
			}
		}

		f, err := toFloat64(val)
		if err != nil {
			allNumeric = false
		} else {
			numericCount++
			numSum += f
			if !numMinSet {
				numMin = f
				numMax = f
				numMinSet = true
			} else {
				if f < numMin {
					numMin = f
				}
				if f > numMax {
					numMax = f
				}
			}
		}
	}

	nonNull := total - p.NullCount
	p.NullPct = roundPct(float64(p.NullCount) / float64(total) * 100)
	p.UniqueCount = len(uniqueSet)
	if nonNull > 0 {
		p.UniquePct = roundPct(float64(p.UniqueCount) / float64(nonNull) * 100)
	}
	p.SampleValues = samples

	// Determine type: numeric if all non-null values parse as numbers.
	if nonNull == 0 {
		p.Type = "null"
	} else if allNumeric && numericCount > 0 {
		p.Type = "number"
		p.IsNumeric = true
		p.MinVal = strconv.FormatFloat(numMin, 'f', -1, 64)
		p.MaxVal = strconv.FormatFloat(numMax, 'f', -1, 64)
		p.MeanVal = roundPct(numSum / float64(numericCount))
	} else if isBooleanColumn(rows, name) {
		p.Type = "boolean"
	} else {
		p.Type = "string"
		// Still set min/max lexicographically for strings.
		if len(uniqueSet) > 0 {
			var minS, maxS string
			first := true
			for s := range uniqueSet {
				if first {
					minS = s
					maxS = s
					first = false
				} else {
					if s < minS {
						minS = s
					}
					if s > maxS {
						maxS = s
					}
				}
			}
			p.MinVal = minS
			p.MaxVal = maxS
		}
	}

	return p
}

func toFloat64(v interface{}) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case string:
		return strconv.ParseFloat(n, 64)
	default:
		return 0, fmt.Errorf("not numeric")
	}
}

func isBooleanColumn(rows []common.DataRow, col string) bool {
	for _, row := range rows {
		val, ok := row[col]
		if !ok || val == nil {
			continue
		}
		switch v := val.(type) {
		case bool:
			continue
		case string:
			lower := v
			if lower == "true" || lower == "false" {
				continue
			}
			return false
		default:
			return false
		}
	}
	return true
}

func roundPct(v float64) float64 {
	return math.Round(v*100) / 100
}

// ExtractSchema extracts a SchemaSnapshot from a DataProfile.
func ExtractSchema(p *DataProfile) *SchemaSnapshot {
	if p == nil {
		return &SchemaSnapshot{Columns: []SchemaColumn{}}
	}
	cols := make([]SchemaColumn, len(p.Columns))
	for i, c := range p.Columns {
		cols[i] = SchemaColumn{
			Name:    c.Name,
			Type:    c.Type,
			NullPct: c.NullPct,
		}
	}
	return &SchemaSnapshot{Columns: cols}
}
