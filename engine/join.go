package engine

import (
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// JoinType defines the kind of join operation.
type JoinType string

const (
	JoinInner JoinType = "inner"
	JoinLeft  JoinType = "left"
	JoinRight JoinType = "right"
	JoinFull  JoinType = "full"
)

// JoinCollisionPolicy controls how right-side columns that overlap the left
// side are represented in the output schema.
type JoinCollisionPolicy string

const (
	// JoinCollisionPrefix preserves the legacy right_ naming behavior.
	JoinCollisionPrefix JoinCollisionPolicy = "prefix"
	// JoinCollisionError refuses a join with overlapping non-key columns.
	JoinCollisionError JoinCollisionPolicy = "error"
	// JoinCollisionAlias prefixes every retained right-side column with the
	// configured right alias, making the output name independent of collisions.
	JoinCollisionAlias JoinCollisionPolicy = "alias"
)

// JoinOptions describes output-schema behavior for a join.
type JoinOptions struct {
	CollisionPolicy JoinCollisionPolicy
	RightAlias      string
}

// planJoinColumns computes the output schema and the right-column mapping in
// one place. Execution and lineage both consume this plan so a collision
// policy cannot be implemented differently by the two paths.
func planJoinColumns(leftColumns, rightColumns []string, leftKey, rightKey string, options JoinOptions) ([]string, map[string]string, error) {
	policy := options.CollisionPolicy
	if policy == "" {
		policy = JoinCollisionPrefix
	}
	if policy != JoinCollisionPrefix && policy != JoinCollisionError && policy != JoinCollisionAlias {
		return nil, nil, fmt.Errorf("join collision_policy %q is not supported; expected error, prefix, or alias", policy)
	}
	if policy == JoinCollisionAlias && strings.TrimSpace(options.RightAlias) == "" {
		return nil, nil, fmt.Errorf("join collision_policy %q requires right_alias", policy)
	}
	if policy != JoinCollisionAlias && options.RightAlias != "" {
		return nil, nil, fmt.Errorf("join right_alias is only valid with collision_policy %q", JoinCollisionAlias)
	}

	leftCols := make(map[string]bool, len(leftColumns))
	for _, c := range leftColumns {
		if leftCols[c] {
			return nil, nil, fmt.Errorf("join left input has duplicate column %q", c)
		}
		leftCols[c] = true
	}
	rightCols := make(map[string]bool, len(rightColumns))
	for _, c := range rightColumns {
		if rightCols[c] {
			return nil, nil, fmt.Errorf("join right input has duplicate column %q", c)
		}
		rightCols[c] = true
	}
	if !leftCols[leftKey] {
		return nil, nil, fmt.Errorf("join left key %q is not present in the left input schema", leftKey)
	}
	if !rightCols[rightKey] {
		return nil, nil, fmt.Errorf("join right key %q is not present in the right input schema", rightKey)
	}

	var collisions []string
	for _, c := range rightColumns {
		if leftCols[c] && !(c == rightKey && leftKey == rightKey) {
			collisions = append(collisions, c)
		}
	}
	if policy == JoinCollisionError && len(collisions) > 0 {
		return nil, nil, fmt.Errorf("join collision_policy %q rejected colliding right columns: %s", policy, strings.Join(collisions, ", "))
	}

	outColumns := append([]string(nil), leftColumns...)
	rightOutputNames := make(map[string]string, len(rightColumns))
	used := make(map[string]bool, len(leftColumns)+len(rightColumns))
	for _, c := range leftColumns {
		used[c] = true
	}
	for _, c := range rightColumns {
		if c == rightKey && leftKey == rightKey {
			continue
		}
		output := c
		switch policy {
		case JoinCollisionAlias:
			output = options.RightAlias + "_" + c
		case JoinCollisionPrefix:
			if len(collisions) > 0 {
				output = "right_" + c
				for used[output] {
					output = "right_" + output
				}
			}
		}
		if used[output] {
			return nil, nil, fmt.Errorf("join collision_policy %q cannot produce unique output column %q", policy, output)
		}
		used[output] = true
		rightOutputNames[c] = output
		outColumns = append(outColumns, output)
	}
	return outColumns, rightOutputNames, nil
}

// JoinDatasets merges two datasets on a key column.
func JoinDatasets(left, right *common.DataSet, leftKey, rightKey string, joinType JoinType) (*common.DataSet, error) {
	return JoinDatasetsWithOptions(left, right, leftKey, rightKey, joinType, JoinOptions{
		CollisionPolicy: JoinCollisionPrefix,
	})
}

// JoinDatasetsWithOptions merges two datasets and applies an explicit output
// collision policy. JoinDatasets remains the legacy entry point and selects
// prefix behavior so persisted pipelines keep their existing schema.
func JoinDatasetsWithOptions(left, right *common.DataSet, leftKey, rightKey string, joinType JoinType, options JoinOptions) (*common.DataSet, error) {
	if left == nil || right == nil {
		return nil, fmt.Errorf("join requires two input datasets")
	}
	if leftKey == "" || rightKey == "" {
		return nil, fmt.Errorf("join requires key columns")
	}
	outCols, rightOutputNames, err := planJoinColumns(left.Columns, right.Columns, leftKey, rightKey, options)
	if err != nil {
		return nil, err
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
				merged := mergeRows(leftRow, rightRow, right.Columns, rightKey, leftKey, rightOutputNames)
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
				merged[rightOutputNames[c]] = nil
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
						merged[rightOutputNames[k]] = v
					}
				}
				outRows = append(outRows, merged)
			}
		}
	}

	return &common.DataSet{Columns: outCols, Rows: outRows}, nil
}

func mergeRows(leftRow, rightRow common.DataRow, rightCols []string, rightKey, leftKey string, rightOutputNames map[string]string) common.DataRow {
	merged := make(common.DataRow)
	for k, v := range leftRow {
		merged[k] = v
	}
	for _, c := range rightCols {
		if c == rightKey && leftKey == rightKey {
			continue
		}
		merged[rightOutputNames[c]] = rightRow[c]
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
