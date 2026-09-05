package api

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
	"github.com/Tnsor-Labs/brokoli/pkg/plugins"
)

// nodeTypeCapabilities maps each built-in node type to the capability
// tags it implies. Used by CapabilitiesHandler as a reference for SDK/UI
// clients; the engine itself only consults Node.Capabilities at runtime
// (falling back to type-based inference per node, not this table).
var nodeTypeCapabilities = map[models.NodeType][]string{
	models.NodeTypeSourceFile:    {models.CapabilitySource},
	models.NodeTypeSourceAPI:     {models.CapabilitySource},
	models.NodeTypeSourceDB:      {models.CapabilitySource},
	models.NodeTypeDBT:           {models.CapabilitySource, models.CapabilityCompute},
	models.NodeTypeMigrate:       {models.CapabilitySource},
	models.NodeTypeTransform:     {models.CapabilityCompute},
	models.NodeTypeQualityCheck:  {models.CapabilityCompute},
	models.NodeTypeSQLGenerate:   {models.CapabilityCompute},
	models.NodeTypeCode:          {models.CapabilityCompute},
	models.NodeTypeJoin:          {models.CapabilityCompute},
	models.NodeTypeCondition:     {models.CapabilityCompute},
	models.NodeTypeSinkFile:      {models.CapabilitySink},
	models.NodeTypeSinkDB:        {models.CapabilitySink},
	models.NodeTypeSinkAPI:       {models.CapabilitySink},
	models.NodeTypeNotify:        {models.CapabilitySink},
	models.NodeTypeUnion:         {models.CapabilityCompute, models.CapabilityDatasetOutput},
	models.NodeTypeWait:          {models.CapabilityCompute, models.CapabilityDatasetOutput},
	models.NodeTypeDatasetMap:    {models.CapabilityCompute, models.CapabilityDatasetOutput},
	models.NodeTypeDatasetFilter: {models.CapabilityCompute, models.CapabilityDatasetOutput},
}

// mustParseInterface decodes a literal ADR-032 task-interface JSON object
// at package init. A JSON literal, not a nested Go map literal, because
// the nesting depth here (port -> value -> record -> fields -> type) makes
// a hand-built map[string]interface{} error-prone in ways a typo in JSON
// is not: models/pipeline_ir_2_2_schema_test.go validates every entry
// below against docs/schema/task-interface-v1.json, so a mistake here
// fails a test rather than shipping silently.
func mustParseInterface(doc string) map[string]interface{} {
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		panic("api: invalid task-interface literal: " + err.Error())
	}
	return v
}

// nodeTypeInterfaces maps a built-in node type to its ADR-032 task
// interface, for the built-in node types whose contract is genuinely
// already known -- never a guess (ADR-032 section 6). This is a
// reference/discovery table only, exactly like nodeTypeCapabilities above:
// no deploy or validate path reads it yet (ADR-032 rollout step 2, issue
// #439), and no node instance carries a persisted 'interface' of its own
// as a result of this table existing.
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
var nodeTypeInterfaces = map[models.NodeType]map[string]interface{}{
	models.NodeTypeSourceFile: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	models.NodeTypeSourceDB: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	models.NodeTypeTransform: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	models.NodeTypeQualityCheck: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	models.NodeTypeCondition: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	models.NodeTypeUnion: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}, "cardinality": "many"}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	models.NodeTypeDatasetMap: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	models.NodeTypeDatasetFilter: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {"result": {"value": {"kind": "dataset"}}}
	}`),
	models.NodeTypeSinkFile: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {}
	}`),
	models.NodeTypeSinkDB: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {}
	}`),
	models.NodeTypeSinkAPI: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}}},
		"outputs": {}
	}`),
	models.NodeTypeNotify: mustParseInterface(`{
		"contract": "brokoli.task-interface/v1",
		"inputs": {"input": {"value": {"kind": "dataset"}, "cardinality": "optional"}},
		"outputs": {}
	}`),
	// migrate reads directly from source_uri/dest_uri (no dataset input;
	// engine/node_handlers.go runMigrate(node models.Node) takes no input
	// parameter) and its output is a small, precisely known summary row
	// (engine/node_handlers.go:migrateSummary), not the migrated rows
	// themselves -- this is the one entry precise enough to declare a
	// real record shape instead of an unknown row.
	models.NodeTypeMigrate: mustParseInterface(`{
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

var codeRuntimeState = struct {
	sync.RWMutex
	nodePath string
}{}

// SetCodeRuntime records the runtime resolution performed by the server at
// startup. Capabilities must describe the process that will execute a node,
// not merely the runtime classes the binary knows about.
func SetCodeRuntime(nodePath string) {
	codeRuntimeState.Lock()
	codeRuntimeState.nodePath = nodePath
	codeRuntimeState.Unlock()
}

func codeRuntimeCapabilities() (languages []string, features []string, nodePath string) {
	codeRuntimeState.RLock()
	nodePath = codeRuntimeState.nodePath
	codeRuntimeState.RUnlock()
	languages = []string{"python"}
	features = append([]string(nil), models.SupportedExecutionFeatures...)
	if nodePath != "" {
		languages = append(languages, "typescript")
		features = append(features, "code-typescript")
	}
	return languages, features, nodePath
}

// CapabilitiesHandler returns the host's supported pipeline IR versions,
// plugin protocol versions, plugin packaging versions and runtime classes,
// and known node/connector capability tags.
// Unauthenticated and static/derived — SDK clients and the UI use it to
// discover what a given Brokoli deployment understands before deploying
// a pipeline (e.g. whether IR 2.1 conditional edges or decorator-based
// source nodes are supported).
func CapabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	languages, features, _ := codeRuntimeCapabilities()
	response := map[string]interface{}{
		"ir_version":                         models.CurrentIRVersion,
		"supported_ir_versions":              models.SupportedIRVersions,
		"supported_execution_features":       features,
		"plugin_protocol_version":            plugins.ProtocolVersion,
		"code_protocol_version":              codeexec.CodeProtocolVersion,
		"supported_code_protocol_versions":   codeexec.SupportedCodeProtocolVersions,
		"code_wrapper_version":               codeexec.WrapperVersion(),
		"supported_plugin_protocol_versions": plugins.SupportedProtocolVersions,
		"supported_packaging_versions":       plugins.SupportedPackagingVersions,
		"supported_runtime_classes":          plugins.SupportedRuntimeClasses,
		"code_languages":                     languages,
		"node_capabilities":                  []string{models.CapabilitySource, models.CapabilitySink, models.CapabilityCompute, models.CapabilityDatasetOutput},
		"node_type_capabilities":             nodeTypeCapabilities,
		"node_type_interfaces":               nodeTypeInterfaces,
	}
	// The wrapper contract is embedded even when Node is unavailable; this
	// version identifies what the binary would run if the runtime resolves.
	response["code_js_wrapper_version"] = codeexec.JSWrapperVersion()
	writeJSON(w, http.StatusOK, response)
}
