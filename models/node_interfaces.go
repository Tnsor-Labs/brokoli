package models

import "encoding/json"

// mustParseInterface decodes a literal ADR-032 task-interface JSON object
// at package init. A JSON literal, not a nested Go map literal, because
// the nesting depth here (port -> value -> record -> fields -> type) makes
// a hand-built map[string]interface{} error-prone in ways a typo in JSON
// is not: models/node_interfaces_test.go validates every entry below
// against docs/schema/task-interface-v1.json, so a mistake here fails a
// test rather than shipping silently.
func mustParseInterface(doc string) map[string]interface{} {
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		panic("models: invalid task-interface literal: " + err.Error())
	}
	return v
}

// NodeTypeInterfaces maps a built-in node type to its ADR-032 task
// interface, for the built-in node types whose contract is genuinely
// already known -- never a guess (ADR-032 section 6). This is a
// reference/discovery table, exactly like nodeTypeCapabilities in
// api/capabilities.go: api.CapabilitiesHandler exposes it for SDK/UI
// discovery, and engine/validate.go consults it (as the fallback when a
// node carries no explicit Interface of its own) to run the
// pkg/taskinterface assignability check across an edge -- both are
// legitimate consumers of the same table, which is why it lives in
// models rather than api (engine cannot import api; api already imports
// engine).
//
// Deliberately excluded, and why (do not add these without resolving the
// named gap first):
//   - source_api: its output kind (dataset/scalar/artifact) depends on
//     the node's own config.response, not on its type alone -- a static
//     per-type table cannot honestly express that; needs per-node
//     computation, not a table entry.
//   - join: takes exactly two inputs (engine/node_handlers.go), but
//     models.Edge carries no port/handle identifier -- edge list order,
//     not port identity, currently distinguishes left from right. A
//     named-port interface would claim a distinction the wire format
//     cannot express yet.
//   - dbt: its output shape varies (a TableRef, a results dataset, or a
//     generic command/output dataset) depending on config.command in ways
//     not yet cleanly modeled here.
//   - wait: the SDK builder never wires an input to it, and its
//     pass-through semantics when unconnected are murkier than the other
//     control-flow nodes; deferred rather than guessed.
//   - sql_generate, code: not yet investigated / genuinely not knowable
//     ahead of a schema-derivation mechanism (code) respectively.
var NodeTypeInterfaces = map[NodeType]map[string]interface{}{
	NodeTypeSourceFile: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	NodeTypeSourceDB: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	NodeTypeTransform: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	NodeTypeQualityCheck: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	NodeTypeCondition: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	NodeTypeUnion: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}, "cardinality": "many"}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	NodeTypeDatasetMap: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	NodeTypeDatasetFilter: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	NodeTypeSinkFile: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {}
	}`),
	NodeTypeSinkDB: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {}
	}`),
	NodeTypeSinkAPI: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {}
	}`),
	NodeTypeNotify: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}, "cardinality": "optional"}},
		"outputs": {}
	}`),
	// migrate reads directly from source_uri/dest_uri (no dataset input;
	// engine/node_handlers.go runMigrate(node models.Node) takes no input
	// parameter) and its output is a small, precisely known summary row
	// (engine/node_handlers.go:migrateSummary), not the migrated rows
	// themselves -- this is the one entry precise enough to declare a
	// real record shape instead of an unknown row. It poses no
	// assignability risk: migrate cannot have outgoing edges
	// (engine/validate.go), so this interface's output port is never
	// actually checked against a consumer today.
	NodeTypeMigrate: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {},
		"outputs": {"result": {"value": {"kind": "dataset", "row": {
			"kind": "record",
			"fields": [
				{"name": "migrated_rows", "type": {"kind": "int64"}, "required": true},
				{"name": "table", "type": {"kind": "string"}, "required": true},
				{"name": "chunks", "type": {"kind": "int64"}, "required": true}
			]
		}}}}
	}`),
}
