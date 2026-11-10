package store

import (
	"fmt"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// decodePreviewRows reads a stored preview's rows with every integer kept
// exact. json.Unmarshal would make each number a float64, so an id above
// 2^53 that SaveNodePreview wrote exactly came back rounded (#965). An
// integer that fits becomes an int64, as a run's own rows have it; any
// other number is a float64.
func decodePreviewRows(raw []byte) ([]common.DataRow, error) {
	decoded, err := common.DecodeJSONValue(raw)
	if err != nil {
		return nil, fmt.Errorf("unmarshal rows: %w", err)
	}
	if decoded == nil {
		return nil, nil
	}
	list, ok := decoded.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unmarshal rows: stored preview rows are a %T, not a list", decoded)
	}
	rows := make([]common.DataRow, len(list))
	for i, item := range list {
		switch row := item.(type) {
		case map[string]interface{}:
			rows[i] = row
		case nil:
		default:
			return nil, fmt.Errorf("unmarshal rows: stored preview row %d is a %T, not an object", i, item)
		}
	}
	return rows, nil
}
