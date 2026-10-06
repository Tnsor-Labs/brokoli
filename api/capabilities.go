package api

import (
	"net/http"
	"sync"

	"github.com/Tnsor-Labs/brokoli/engine"
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
	models.NodeTypeProject:       {models.CapabilityCompute, models.CapabilityDatasetOutput},
	models.NodeTypeAggregate:     {models.CapabilityCompute, models.CapabilityDatasetOutput},
	models.NodeTypeFilter:        {models.CapabilityCompute, models.CapabilityDatasetOutput},
	models.NodeTypeQualityCheck:  {models.CapabilityCompute},
	models.NodeTypeContractGate:  {models.CapabilityCompute},
	models.NodeTypeSQLGenerate:   {models.CapabilityCompute},
	models.NodeTypeCode:          {models.CapabilityCompute},
	models.NodeTypeJoin:          {models.CapabilityCompute},
	models.NodeTypeCondition:     {models.CapabilityCompute},
	models.NodeTypeSinkFile:      {models.CapabilitySink},
	models.NodeTypeSinkDB:        {models.CapabilitySink},
	models.NodeTypeSinkAPI:       {models.CapabilitySink},
	models.NodeTypeNotify:        {models.CapabilitySink},
	models.NodeTypeBash:          {models.CapabilityCompute},
	models.NodeTypeUnion:         {models.CapabilityCompute, models.CapabilityDatasetOutput},
	models.NodeTypeWait:          {models.CapabilityCompute, models.CapabilityDatasetOutput},
	models.NodeTypeDatasetMap:    {models.CapabilityCompute, models.CapabilityDatasetOutput},
	models.NodeTypeDatasetFilter: {models.CapabilityCompute, models.CapabilityDatasetOutput},
}

var codeRuntimeState = struct {
	sync.RWMutex
	nodePath string
}{}

// nativeADBCDriverStatus says whether this server's worker can run native
// ADBC drivers at all. The installed builds are deliberately not listed:
// this document is public, and the exact versions of the native code a
// server loads are not for anyone who asks. GET /api/drivers lists them to
// signed-in users.
type nativeADBCDriverStatus struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func nativeADBCDrivers() nativeADBCDriverStatus {
	if engine.NativeADBCWorkerEnabled() {
		return nativeADBCDriverStatus{Status: "native_worker_enabled",
			Reason: "Connections pinned to an installed native ADBC driver are read in an isolated worker process."}
	}
	return nativeADBCDriverStatus{Status: "native_worker_unavailable", Reason: engine.ErrNativeADBCUnavailable.Error()}
}

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
		"node_type_interfaces":               models.NodeTypeInterfaces,
		"native_adbc_drivers":                nativeADBCDrivers(),
		// Node types this deployment refuses (engine.DisabledNodeTypesEnv),
		// so a client can leave them out rather than fail validation.
		"disabled_node_types": engine.DisabledNodeTypes(),
	}
	// The wrapper contract is embedded even when Node is unavailable; this
	// version identifies what the binary would run if the runtime resolves.
	response["code_js_wrapper_version"] = codeexec.JSWrapperVersion()
	writeJSON(w, http.StatusOK, response)
}
