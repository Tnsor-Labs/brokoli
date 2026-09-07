package engine

// Reading a task's collection output (ADR-032 section 6, ADR-033
// section 8), the last of the four value kinds to get a reader.
//
// A collection is "a finite collection of separately addressable
// portable values", and its items follow their own declared value
// contract -- which may be scalar, artifact, dataset, or another
// collection. That is why task-result-v1 carries them as a recursive
// `items` array of output port results rather than one inline blob:
// a collection of artifacts needs per-item path and checksum, and
// flattening it into a single JSON value would lose exactly the
// integrity metadata the artifact kind exists to carry.
//
// The engine's node output is a DataSet, so a collection becomes ONE ROW
// PER ITEM, each row in the same shape that item's kind already
// produces on its own. A collection of artifacts is N reference rows; a
// collection of scalars is N value rows. Downstream then reads a
// collection with the machinery it already has, rather than a new
// container type.
//
// Deliberately NOT here: deriving physical instance identity from
// item_key. ADR-032 says the key is "used by ADR-015 to derive physical
// instance identity", but expansion currently keys instances by index
// (deriveInstanceKey), and those keys are persisted and reused for
// resume. Adopting key-based identity changes fan-out behaviour rather
// than adding to it, so it belongs to its own arc. The key is read,
// validated for uniqueness, and surfaced as a column -- which is what
// makes that later change possible without a format change.

import (
	"context"
	"fmt"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/taskinterface"
)

// ItemKeyColumn names the column carrying each item's declared item_key.
// Exported because it is part of what a collection output looks like to
// everything downstream, not an engine-internal detail.
const ItemKeyColumn = "item_key"

// maxCollectionItems bounds how many items one collection output may
// carry, for the same reason pkg/taskbundlev2 bounds archive entries
// alongside archive bytes: a per-item cost (a blob store round trip, for
// artifact items) multiplied by an unbounded count is its own denial of
// service, independent of how small each item is.
const maxCollectionItems = 10000

// readTaskCollectionOutput turns a collection's items into one row each.
//
// Every item is read by the same reader its own kind uses standalone, so
// an artifact item gets the identical safe-open, size and checksum
// verification a top-level artifact output gets -- there is no weaker
// path into the store just because bytes arrived inside a collection.
func readTaskCollectionOutput(ctx context.Context, blobs artifact.Store, namespace, stagingDir string, items []taskOutputPort, port taskinterface.PortValue) (*common.DataSet, error) {
	if len(items) > maxCollectionItems {
		return nil, fmt.Errorf("task collection output has %d items, over the %d-item cap", len(items), maxCollectionItems)
	}

	// ADR-032 section 6: "Duplicate keys are invalid even when duplicate
	// values are allowed." Checked across the whole collection before any
	// row is trusted, because a duplicate key means the items are not
	// separately addressable, which is the one property the kind
	// promises.
	seen := make(map[string]int, len(items))

	var itemContract taskinterface.PortValue
	if port.Items != nil {
		itemContract = *port.Items
	}

	rows := make([]common.DataRow, 0, len(items))
	columns := []string{ItemKeyColumn}
	for i, item := range items {
		key, err := collectionItemKey(item, i, seen)
		if err != nil {
			return nil, err
		}

		itemDS, err := readCollectionItem(ctx, blobs, namespace, stagingDir, item, itemContract, i)
		if err != nil {
			return nil, err
		}
		// An item whose own kind yields several rows (a dataset item)
		// would make "one row per item" a lie, and with it the item_key
		// column's meaning. Named rather than silently flattened.
		if len(itemDS.Rows) != 1 {
			return nil, fmt.Errorf("task collection item %d (kind %q) produced %d rows; this server maps one row per item", i, item.Kind, len(itemDS.Rows))
		}
		row := common.DataRow{ItemKeyColumn: key}
		for k, v := range itemDS.Rows[0] {
			if k == ItemKeyColumn {
				return nil, fmt.Errorf("task collection item %d produces its own %q column, which would collide with the collection's item key", i, ItemKeyColumn)
			}
			row[k] = v
		}
		rows = append(rows, row)
		columns = mergeColumns(columns, itemDS.Columns)
	}
	return &common.DataSet{Columns: columns, Rows: rows}, nil
}

// collectionItemKey reads and uniqueness-checks one item's key.
//
// A key is required: without it the item is not separately addressable,
// so accepting a collection whose items have none would be accepting
// something that is not a collection.
func collectionItemKey(item taskOutputPort, i int, seen map[string]int) (string, error) {
	if item.ItemKey == nil {
		return "", fmt.Errorf("task collection item %d declares no item_key; a collection's items must be separately addressable (ADR-032 section 6)", i)
	}
	// Rendered rather than type-switched: item_key's declared descriptor
	// may be any BPTD scalar, and uniqueness is a property of the value,
	// not of the Go type JSON happened to decode it into.
	key := fmt.Sprintf("%v", item.ItemKey)
	if first, dup := seen[key]; dup {
		return "", fmt.Errorf("task collection items %d and %d share item_key %q; duplicate keys are invalid even when duplicate values are allowed (ADR-032 section 6)", first, i, key)
	}
	seen[key] = i
	return key, nil
}

// readCollectionItem dispatches one item to the reader for its kind.
//
// A nested collection is refused rather than recursed into: the row
// shape here is one row per item, and an item that is itself many items
// has no honest representation in it. The schema permits the nesting, so
// this is a limit of the mapping, not of the contract -- named as such.
func readCollectionItem(ctx context.Context, blobs artifact.Store, namespace, stagingDir string, item taskOutputPort, itemContract taskinterface.PortValue, i int) (*common.DataSet, error) {
	switch item.Kind {
	case "scalar":
		if itemContract.Kind == taskinterface.ValueScalar && itemContract.ScalarType != nil {
			if err := taskinterface.ValidateValue(item.Value, *itemContract.ScalarType, fmt.Sprintf("$.items[%d]", i)); err != nil {
				failure := taskinterface.NewValidationFailure(
					taskinterface.DirectionOutput, "result", *itemContract.ScalarType,
					item.Value, err, fmt.Sprintf("$.items[%d]", i), false,
				)
				return nil, fmt.Errorf("%w: %w", ErrTaskOutputContractViolation, failure)
			}
		}
		return &common.DataSet{Columns: []string{"value"}, Rows: []common.DataRow{{"value": item.Value}}}, nil
	case "artifact":
		return readTaskArtifactOutput(ctx, blobs, namespace, stagingDir, item.Path, item.Codec, item.SizeBytes, item.Checksum, itemContract)
	case "dataset":
		return nil, fmt.Errorf("task collection item %d is a dataset; this server maps one row per item, which a multi-row item cannot honour", i)
	case "collection":
		return nil, fmt.Errorf("task collection item %d is itself a collection; nested collections are permitted by the contract but this server has no row shape for them yet", i)
	default:
		return nil, fmt.Errorf("task collection item %d declares unknown kind %q", i, item.Kind)
	}
}

// mergeColumns appends any column not already present, preserving first
// -seen order so the item key stays leftmost and items with differing
// shapes still contribute their columns.
func mergeColumns(into []string, add []string) []string {
	for _, c := range add {
		found := false
		for _, existing := range into {
			if existing == c {
				found = true
				break
			}
		}
		if !found {
			into = append(into, c)
		}
	}
	return into
}
