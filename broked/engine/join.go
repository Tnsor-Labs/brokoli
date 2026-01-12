package engine

import (
	"fmt"
	"strings"

	"github.com/hc12r/brokolisql-go/pkg/common"
)

// JoinType defines the kind of join operation.
type JoinType string

const (
	JoinInner JoinType = "inner"
	JoinLeft  JoinType = "left"
	JoinRight JoinType = "right"
	JoinFull  JoinType = "full"
)

// JoinDatasets merges two datasets on a key column.
func JoinDatasets(left, right *common.DataSet, leftKey, rightKey string, joinType JoinType) (*common.DataSet, error) {
	if left == nil || right == nil {
		return nil, fmt.Errorf("join requires two input datasets")
	}
	if leftKey == "" || rightKey == "" {
		return nil, fmt.Errorf("join requires key columns")
	}

	// Build output columns: all left columns + right columns (prefixed if duplicate)
	rightPrefix := ""
	leftCols := make(map[string]bool)
	for _, c := range left.Columns {
		leftCols[c] = true
	}
	for _, c := range right.Columns {
		if leftCols[c] && c != rightKey {
			rightPrefix = "right_"
			break
		}
	}

	var outCols []string
	outCols = append(outCols, left.Columns...)
	for _, c := range right.Columns {
		if c == rightKey && leftKey == rightKey {
			continue // skip duplicate key column
		}
		outCols = append(outCols, rightPrefix+c)
	}

	// Index right dataset by key
	rightIndex := make(map[string][]common.DataRow)
	for _, row := range right.Rows {
		key := fmt.Sprintf("%v", row[rightKey])
		rightIndex[key] = append(rightIndex[key], row)
	}

	var outRows []common.DataRow
	rightMatched := make(map[string]bool)

	// Process left rows
	for _, leftRow := range left.Rows {
		leftVal := fmt.Sprintf("%v", leftRow[leftKey])
		rightRows, found := rightIndex[leftVal]

		if found {
			rightMatched[leftVal] = true
			for _, rightRow := range rightRows {
				merged := mergeRows(leftRow, rightRow, left.Columns, right.Columns, rightKey, leftKey, rightPrefix)
				outRows = append(outRows, merged)
			}
		} else if joinType == JoinLeft || joinType == JoinFull {
			// Left row with nulls for right columns
			merged := make(common.DataRow)
			for k, v := range leftRow {
				merged[k] = v
			}
			for _, c := range right.Columns {
				if c == rightKey && leftKey == rightKey {
					continue
				}
				merged[rightPrefix+c] = nil
			}
			outRows = append(outRows, merged)
		}
	}

	// For right/full join, add unmatched right rows
	if joinType == JoinRight || joinType == JoinFull {
		for _, rightRow := range right.Rows {
			rightVal := fmt.Sprintf("%v", rightRow[rightKey])
			if !rightMatched[rightVal] {
				merged := make(common.DataRow)
				for _, c := range left.Columns {
					merged[c] = nil
				}
				for k, v := range rightRow {
					if k == rightKey && leftKey == rightKey {
						merged[k] = v
					} else {
						merged[rightPrefix+k] = v
					}
				}
				outRows = append(outRows, merged)
			}
		}
	}

	return &common.DataSet{Columns: outCols, Rows: outRows}, nil
}

func mergeRows(leftRow, rightRow common.DataRow, leftCols, rightCols []string, rightKey, leftKey, rightPrefix string) common.DataRow {
	merged := make(common.DataRow)
	for k, v := range leftRow {
		merged[k] = v
	}
	for _, c := range rightCols {
		if c == rightKey && leftKey == rightKey {
			continue
		}
		merged[rightPrefix+c] = rightRow[c]
	}
	return merged
}

// ParseJoinType converts a string to JoinType, defaulting to inner.
func ParseJoinType(s string) JoinType {
	switch strings.ToLower(s) {
	case "left":
		return JoinLeft
	case "right":
		return JoinRight
	case "full", "outer", "full_outer":
		return JoinFull
	default:
		return JoinInner
	}
}
