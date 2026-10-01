package engine

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/Tnsor-Labs/brokoli/models"
)

// DisabledNodeTypesEnv names the node types a deployment refuses to run,
// as a comma-separated list: BROKOLI_DISABLED_NODE_TYPES=bash,code.
//
// bash and code nodes run pipeline-author code on the worker with the
// worker's own access. That is the right default for a single team, and
// the wrong one for a server whose pipeline authors should not have a
// shell on it. This is the operator's switch: a disabled type fails
// validation, so it cannot be saved or run, and is refused again where
// it would execute, so a worker configured with it never runs one even
// when the server that dispatched the run was configured without it.
const DisabledNodeTypesEnv = "BROKOLI_DISABLED_NODE_TYPES"

var unknownDisabledTypeWarned sync.Map

// DisabledNodeTypes returns the node types this process refuses, sorted.
// Read from the environment on every call, so it costs a lookup and a
// split, and a test can set it per case.
func DisabledNodeTypes() []string {
	out := []string{}
	seen := map[string]bool{}
	for _, name := range strings.Split(os.Getenv(DisabledNodeTypesEnv), ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if !IsBuiltInNodeType(models.NodeType(name)) {
			// Plugin node types are disabled by name too, so an unknown
			// name is kept; but a typo would disable nothing, so say so.
			if _, warned := unknownDisabledTypeWarned.LoadOrStore(name, true); !warned {
				// #nosec G706 -- the operator's own setting, printed with %q so it cannot forge a line.
				log.Printf("[node-policy] %s names %q, which is not a built-in node type; it disables only a plugin node type of that name", DisabledNodeTypesEnv, name)
			}
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// nodeTypeDisabledError is the refusal for a disabled node type, or nil.
func nodeTypeDisabledError(t models.NodeType) error {
	for _, name := range DisabledNodeTypes() {
		if name == string(t) {
			return fmt.Errorf("%s nodes are disabled on this deployment (%s)", t, DisabledNodeTypesEnv)
		}
	}
	return nil
}

// platformReference matches a Brokoli ${...} reference: the forms
// VariableContext substitutes into node configs before a node runs.
var platformReference = regexp.MustCompile(`\$\{(interval|env|param|secret|var|run)\.[^}]*\}`)

// paramReference matches a ${param...} reference, filtered or not.
var paramReference = regexp.MustCompile(`\$\{param\.[^}]*\}`)

// resolveNodeConfig substitutes ${...} references in a node's config,
// except where the value would become source code:
//
//   - a bash node's command is not substituted at all. Every value would
//     be spliced into shell source; they reach the command through env,
//     which is substituted and arrives as environment variables.
//
//   - a code node's script keeps its ${param...} references. A run
//     parameter is chosen by whoever starts the run, which pipelines.run
//     allows without pipelines.edit, so substituting it into the script
//     would let someone who may only run a pipeline change its code. The
//     script reads it from params instead.
//
//   - SQL a source runs (source_db query, migrate source_query) keeps its
//     ${param...} references for the same reason; they are bound as driver
//     parameters when the query runs (bindSQLParams), never written into
//     the SQL text.
//
// Validation refuses both forms with a message saying what to use, so
// the unsubstituted text is never run silently; this is the runner's own
// guarantee for a node that reached it some other way.
func resolveNodeConfig(vc *VariableContext, node models.Node) map[string]interface{} {
	resolved := vc.ResolveConfig(node.Config)
	switch node.Type {
	case models.NodeTypeBash:
		if command, ok := node.Config["command"]; ok {
			resolved["command"] = command
		}
	case models.NodeTypeCode:
		if script, ok := node.Config["script"].(string); ok {
			resolved["script"] = vc.resolveExceptParams(script)
		}
	case models.NodeTypeSourceDB:
		// Bound at execution instead (bindSQLParams, #774).
		if query, ok := node.Config["query"].(string); ok {
			resolved["query"] = vc.resolveExceptParams(query)
		}
	case models.NodeTypeMigrate:
		if query, ok := node.Config["source_query"].(string); ok {
			resolved["source_query"] = vc.resolveExceptParams(query)
		}
	}
	return resolved
}

// codeScriptParamError refuses a ${param...} reference in a code node's
// script, or returns "".
func codeScriptParamError(config map[string]interface{}) string {
	script, _ := config["script"].(string)
	ref := paramReference.FindString(script)
	if ref == "" {
		return ""
	}
	return fmt.Sprintf("'script' contains %s, which is not substituted in code: a run parameter "+
		"is chosen by whoever starts the run, and substituting it would let them change the "+
		"code. Read it from params instead (params[\"name\"] in Python, params.name in TypeScript)", ref)
}
