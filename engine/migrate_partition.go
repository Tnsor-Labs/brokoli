package engine

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

const (
	defaultMigratePartitionParallel = 4
	maxMigratePartitionParallel     = 64
)

var migratePartitionIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type migratePartitionConfig struct {
	Column      string
	Strategy    string
	Boundaries  []interface{}
	MaxParallel int
}

type migratePartitionRange struct {
	Index int
	Lower interface{}
	Upper interface{}
}

func parseMigratePartitionConfig(raw interface{}) (migratePartitionConfig, []migratePartitionRange, error) {
	config, ok := raw.(map[string]interface{})
	if !ok {
		return migratePartitionConfig{}, nil, fmt.Errorf("partition must be an object")
	}
	column, _ := config["column"].(string)
	if !migratePartitionIdentifier.MatchString(column) {
		return migratePartitionConfig{}, nil, fmt.Errorf("partition.column must be a simple SQL identifier")
	}
	strategy, _ := config["strategy"].(string)
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy != "numeric" && strategy != "date" {
		return migratePartitionConfig{}, nil, fmt.Errorf("partition.strategy must be numeric or date")
	}
	boundaries, ok := config["boundaries"].([]interface{})
	if !ok || len(boundaries) < 2 {
		return migratePartitionConfig{}, nil, fmt.Errorf("partition.boundaries must contain at least two values")
	}
	for i, boundary := range boundaries {
		if err := validateMigratePartitionBoundary(strategy, boundary); err != nil {
			return migratePartitionConfig{}, nil, fmt.Errorf("partition.boundaries[%d]: %w", i, err)
		}
		if i > 0 && compareMigratePartitionBoundaries(strategy, boundaries[i-1], boundary) >= 0 {
			return migratePartitionConfig{}, nil, fmt.Errorf("partition.boundaries must be strictly increasing")
		}
	}
	maxParallel := defaultMigratePartitionParallel
	if rawMax, ok := config["max_parallel"]; ok {
		maxParallel, ok = migratePartitionInt(rawMax)
		if !ok || maxParallel < 1 || maxParallel > maxMigratePartitionParallel {
			return migratePartitionConfig{}, nil, fmt.Errorf("partition.max_parallel must be between 1 and %d", maxMigratePartitionParallel)
		}
	}
	parsed := migratePartitionConfig{Column: column, Strategy: strategy, Boundaries: boundaries, MaxParallel: maxParallel}
	ranges := make([]migratePartitionRange, len(boundaries)-1)
	for i := range ranges {
		ranges[i] = migratePartitionRange{Index: i, Lower: boundaries[i], Upper: boundaries[i+1]}
	}
	return parsed, ranges, nil
}

func migratePartitionInt(raw interface{}) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		if value != math.Trunc(value) {
			return 0, false
		}
		return int(value), true
	default:
		return 0, false
	}
}

func validateMigratePartitionBoundary(strategy string, raw interface{}) error {
	switch strategy {
	case "numeric":
		value, ok := numericPartitionBoundary(raw)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("must be a finite number")
		}
	case "date":
		value, ok := raw.(string)
		if !ok {
			return fmt.Errorf("must be an RFC3339 timestamp string")
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return fmt.Errorf("must be an RFC3339 timestamp string: %w", err)
		}
	}
	return nil
}

func numericPartitionBoundary(raw interface{}) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case jsonNumberLike:
		result, err := value.Float64()
		return result, err == nil
	default:
		return 0, false
	}
}

// jsonNumberLike keeps this parser independent of encoding/json while still
// accepting json.Number in tests and callers that construct configs directly.
type jsonNumberLike interface {
	Float64() (float64, error)
}

func compareMigratePartitionBoundaries(strategy string, left, right interface{}) int {
	if strategy == "date" {
		l, _ := time.Parse(time.RFC3339, left.(string))
		r, _ := time.Parse(time.RFC3339, right.(string))
		if l.Before(r) {
			return -1
		}
		if l.After(r) {
			return 1
		}
		return 0
	}
	l, _ := numericPartitionBoundary(left)
	r, _ := numericPartitionBoundary(right)
	if l < r {
		return -1
	}
	if l > r {
		return 1
	}
	return 0
}

func migratePartitionErrors(config map[string]interface{}) []string {
	raw, ok := config["partition"]
	if !ok || raw == nil {
		return nil
	}
	_, _, err := parseMigratePartitionConfig(raw)
	if err == nil {
		return nil
	}
	return []string{err.Error()}
}

func migratePartitionQuery(sourceQuery, dialect string, partition migratePartitionConfig, bounds migratePartitionRange) (string, []interface{}, error) {
	if strings.Contains(sourceQuery, ";") {
		return "", nil, fmt.Errorf("partitioned source_query must not contain a semicolon")
	}
	column, err := quoteMigratePartitionIdentifier(dialect, partition.Column)
	if err != nil {
		return "", nil, err
	}
	left, right := "?", "?"
	switch strings.ToLower(dialect) {
	case "postgres":
		left, right = "$1", "$2"
	case "sqlserver", "mssql":
		left, right = "@p1", "@p2"
	}
	return fmt.Sprintf("SELECT * FROM (%s) AS brokoli_partition_source WHERE %s >= %s AND %s < %s", sourceQuery, column, left, column, right), []interface{}{bounds.Lower, bounds.Upper}, nil
}

func quoteMigratePartitionIdentifier(dialect, column string) (string, error) {
	if !migratePartitionIdentifier.MatchString(column) {
		return "", fmt.Errorf("partition column must be a simple SQL identifier")
	}
	switch strings.ToLower(dialect) {
	case "mysql":
		return "`" + column + "`", nil
	case "sqlserver", "mssql":
		return "[" + column + "]", nil
	default:
		return `"` + column + `"`, nil
	}
}
