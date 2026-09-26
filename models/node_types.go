package models

// The complete set of node types, in one place.
//
// This exists because several gates need to iterate every node type, and
// until now each one carried its own hand-written list. A hand-written
// list is a gate that rots: a node type added without touching the list
// is exactly the case the gate was built to catch, and it passes.
//
// AllNodeTypes is checked against the constant declarations themselves by
// TestAllNodeTypesIsComplete, which parses this package's source. So the
// list cannot silently fall behind the constants it mirrors.

// AllNodeTypes is every node type the engine recognises.
//
// Order is the declaration order in pipeline.go, which groups them by
// what they are for. Nothing depends on the order; keeping it means a
// diff of this list reads sensibly.
var AllNodeTypes = []NodeType{
	NodeTypeSourceFile,
	NodeTypeSourceAPI,
	NodeTypeSourceDB,
	NodeTypeTransform,
	NodeTypeProject,
	NodeTypeAggregate,
	NodeTypeFilter,
	NodeTypeQualityCheck,
	NodeTypeContractGate,
	NodeTypeSQLGenerate,
	NodeTypeCode,
	NodeTypeJoin,
	NodeTypeSinkFile,
	NodeTypeSinkDB,
	NodeTypeSinkAPI,
	NodeTypeMigrate,
	NodeTypeCondition,
	NodeTypeWait,
	NodeTypeDBT,
	NodeTypeNotify,
	NodeTypeUnion,
	NodeTypeDatasetMap,
	NodeTypeDatasetFilter,
	NodeTypeTask,
}

// IsKnownNodeType reports whether the engine recognises this type.
func IsKnownNodeType(t NodeType) bool {
	for _, known := range AllNodeTypes {
		if known == t {
			return true
		}
	}
	return false
}
