package sodp

import "fmt"

// DeltaOpType identifies the kind of structural change.
type DeltaOpType string

const (
	OpAdd    DeltaOpType = "ADD"
	OpUpdate DeltaOpType = "UPDATE"
	OpRemove DeltaOpType = "REMOVE"
)

// DeltaOp represents a single field-level change within a state entry.
type DeltaOp struct {
	Op    DeltaOpType `msgpack:"op" json:"op"`
	Path  string      `msgpack:"path" json:"path"` // JSON Pointer (e.g. "/status")
	Value any         `msgpack:"value,omitempty" json:"value,omitempty"`
}

// DeltaEntry is a versioned set of operations for a single key, stored in the delta log
// and broadcast to watchers.
type DeltaEntry struct {
	Key     string    `msgpack:"key" json:"key"`
	Version uint64    `msgpack:"version" json:"version"`
	Ops     []DeltaOp `msgpack:"ops" json:"ops"`
}

// Diff computes the field-level delta operations between two map values.
// Both old and new must be map[string]any. For non-map types or nil old,
// a single "add" op at "/" replaces the entire value.
func Diff(old, new any) []DeltaOp {
	oldMap, oldOK := toStringMap(old)
	newMap, newOK := toStringMap(new)

	// Atomic replacement if either side isn't a map
	if !oldOK || !newOK {
		return []DeltaOp{{Op: OpUpdate, Path: "/", Value: new}}
	}

	var ops []DeltaOp

	// Removed or updated keys
	for k, oldVal := range oldMap {
		newVal, exists := newMap[k]
		if !exists {
			ops = append(ops, DeltaOp{Op: OpRemove, Path: "/" + k})
			continue
		}
		// Recurse into nested maps
		oldNested, oldNestOK := toStringMap(oldVal)
		newNested, newNestOK := toStringMap(newVal)
		if oldNestOK && newNestOK {
			for _, sub := range Diff(oldNested, newNested) {
				sub.Path = "/" + k + sub.Path
				ops = append(ops, sub)
			}
			continue
		}
		if !equal(oldVal, newVal) {
			ops = append(ops, DeltaOp{Op: OpUpdate, Path: "/" + k, Value: newVal})
		}
	}

	// Added keys
	for k, newVal := range newMap {
		if _, exists := oldMap[k]; !exists {
			ops = append(ops, DeltaOp{Op: OpAdd, Path: "/" + k, Value: newVal})
		}
	}

	return ops
}

// toStringMap attempts to convert a value to map[string]any.
func toStringMap(v any) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// equal does a shallow equality check suitable for JSON-like values.
func equal(a, b any) bool {
	// Fast path for common types
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case int64:
		bv, ok := b.(int64)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case nil:
		return b == nil
	}
	// Fall back to fmt comparison for complex types
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}
