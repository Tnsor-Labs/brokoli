package engine

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
)

var varPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// VariableStore is the interface for resolving stored variables.
// This avoids importing the store package (which would create a cycle).
type VariableStore interface {
	GetVariableValue(workspaceID, key string) (value string, encrypted bool, err error)
}

// VariableContext holds the runtime context for variable resolution.
type VariableContext struct {
	Env    map[string]string // from os.Environ
	Params map[string]string // from pipeline run params
	Vars   VariableStore     // stored variables (${var.key})
	// WorkspaceID scopes ${var.*} to the pipeline's own workspace.
	// Empty means the default workspace, matching the store.
	WorkspaceID string
	RunID       string
	StartedAt   time.Time

	// IntervalStart/End are the run's data interval (ADR-028), resolvable
	// as ${interval.start} and ${interval.end} in RFC3339 UTC. Nil in a
	// run with no interval, in which case the references stay unresolved
	// -- the same visible-in-output behaviour every unknown variable has,
	// and validation warns about the combination up front.
	IntervalStart *time.Time
	IntervalEnd   *time.Time
}

// NewVariableContext creates a context from the current environment and params.
func NewVariableContext(params map[string]string, runID string, startedAt time.Time) *VariableContext {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}
	if params == nil {
		params = make(map[string]string)
	}
	return &VariableContext{
		Env:       env,
		Params:    params,
		RunID:     runID,
		StartedAt: startedAt,
	}
}

// Resolve replaces all ${...} variables in a string.
func (vc *VariableContext) Resolve(s string) string {
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Extract the key between ${ and }
		key := match[2 : len(match)-1]
		return vc.resolveKey(key)
	})
}

func (vc *VariableContext) resolveKey(key string) string {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 {
		return "${" + key + "}" // unresolvedreturn as-is
	}

	prefix, name := parts[0], parts[1]
	switch prefix {
	case "interval":
		// ADR-028: the run's data interval, RFC3339 UTC. Unresolved (the
		// reference stays visible) when the run carries no interval --
		// an empty string here would quietly produce WHERE ts >= '',
		// which is the silent kind of wrong; the visible kind gets fixed.
		switch name {
		case "start":
			if vc.IntervalStart != nil {
				return vc.IntervalStart.UTC().Format(time.RFC3339)
			}
		case "end":
			if vc.IntervalEnd != nil {
				return vc.IntervalEnd.UTC().Format(time.RFC3339)
			}
		}
		return "${" + key + "}"
	case "env":
		// Deny by default. NewVariableContext copies the WHOLE server
		// environment into vc.Env, so before this gate ${env.NAME}
		// returned any of it -- including BROKOLI_ENCRYPTION_KEY, which
		// decrypts every stored credential, and BROKOLI_JWT_SECRET, which
		// mints any session. Authoring a pipeline is not supposed to be a
		// way to read the control plane's own secrets, and it was.
		//
		// A refused reference stays VISIBLE as ${env.NAME} rather than
		// resolving to empty, for the reason the interval case above
		// gives: an empty string is the silent kind of wrong, and the
		// visible kind gets fixed. A name that is allowed but genuinely
		// unset still resolves to empty, which is what it always did.
		if !pipelineEnvAllowed(name) {
			log.Printf("[variables] refusing ${env.%s}: not in %s. "+
				"Pipeline templating reads only environment variables an operator has "+
				"listed there; the server's own environment holds its database URL, "+
				"signing secret and encryption key.", name, pipelineEnvAllowEnv)
			return "${" + key + "}"
		}
		if v, ok := vc.Env[name]; ok {
			return v
		}
		return ""
	case "param":
		if v, ok := vc.Params[name]; ok {
			return v
		}
		return ""
	case "secret":
		// Secrets resolve from env vars with BROKED_SECRET_ prefix
		if v, ok := vc.Env["BROKED_SECRET_"+strings.ToUpper(name)]; ok {
			return v
		}
		return ""
	case "var":
		// Resolve from stored variables
		if vc.Vars != nil {
			// Scoped to the run's workspace. Reading by key alone
			// returned whichever workspace had written that name last,
			// and variables hold secrets.
			if val, _, err := vc.Vars.GetVariableValue(vc.WorkspaceID, name); err == nil {
				return val
			}
		}
		return ""
	case "run":
		switch name {
		case "id":
			return vc.RunID
		case "started_at":
			return vc.StartedAt.Format(time.RFC3339)
		case "date":
			return vc.StartedAt.Format("2006-01-02")
		case "timestamp":
			return fmt.Sprintf("%d", vc.StartedAt.Unix())
		}
	}
	return "${" + key + "}"
}

// ResolveConfig deep-resolves all string values in a config map.
func (vc *VariableContext) ResolveConfig(config map[string]interface{}) map[string]interface{} {
	resolved := make(map[string]interface{}, len(config))
	for k, v := range config {
		resolved[k] = vc.resolveValue(v)
	}
	return resolved
}

func (vc *VariableContext) resolveValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return vc.Resolve(val)
	case map[string]interface{}:
		return vc.ResolveConfig(val)
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = vc.resolveValue(item)
		}
		return result
	default:
		return v
	}
}

// pipelineEnvAllowEnv names the operator's allowlist for ${env.*}.
//
// An allowlist rather than a denylist, because a denylist has to
// enumerate every secret anyone will ever put in the environment and is
// wrong the first time someone adds one. This is wrong only in the
// direction of refusing something harmless, which is visible and takes
// one setting to fix.
const pipelineEnvAllowEnv = "BROKOLI_PIPELINE_ENV_ALLOW"

// pipelineEnvAllowed reports whether a pipeline may read this environment
// variable through ${env.*}.
func pipelineEnvAllowed(name string) bool {
	// The never-readable floor lives in pkg/secrets so the three
	// mechanisms that can reach the server's environment -- this,
	// env:// references and a code node's inherited environment -- cannot
	// drift apart on which names are fatal.
	if name == "" || secrets.AlwaysDeniedEnvName(name) {
		return false
	}
	for _, allowed := range strings.Split(os.Getenv(pipelineEnvAllowEnv), ",") {
		if allowed = strings.TrimSpace(allowed); allowed != "" && allowed == name {
			return true
		}
	}
	return false
}
