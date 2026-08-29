package api

import (
	"net/http"

	"github.com/Tnsor-Labs/brokoli/models"
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

// CapabilitiesHandler returns the host's supported pipeline IR versions,
// plugin protocol versions, plugin packaging versions and runtime classes,
// and known node/connector capability tags.
// Unauthenticated and static/derived — SDK clients and the UI use it to
// discover what a given Brokoli deployment understands before deploying
// a pipeline (e.g. whether IR 2.1 conditional edges or decorator-based
// source nodes are supported).
func CapabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ir_version":                         models.CurrentIRVersion,
		"supported_ir_versions":              models.SupportedIRVersions,
		"supported_execution_features":       models.SupportedExecutionFeatures,
		"plugin_protocol_version":            plugins.ProtocolVersion,
		"supported_plugin_protocol_versions": plugins.SupportedProtocolVersions,
		"supported_packaging_versions":       plugins.SupportedPackagingVersions,
		"supported_runtime_classes":          plugins.SupportedRuntimeClasses,
		"node_capabilities":                  []string{models.CapabilitySource, models.CapabilitySink, models.CapabilityCompute, models.CapabilityDatasetOutput},
		"node_type_capabilities":             nodeTypeCapabilities,
	})
}
