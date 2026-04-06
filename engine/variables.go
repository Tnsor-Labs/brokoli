package engine

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

var varPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// VariableStore is the interface for resolving stored variables.
// This avoids importing the store package (which would create a cycle).
type VariableStore interface {
	GetVariableValue(key string) (value string, encrypted bool, err error)
}

// VariableContext holds the runtime context for variable resolution.
type VariableContext struct {
	Env       map[string]string // from os.Environ
	Params    map[string]string // from pipeline run params
	Vars      VariableStore     // stored variables (${var.key})
	RunID     string
	StartedAt time.Time
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
	case "env":
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
			if val, _, err := vc.Vars.GetVariableValue(name); err == nil {
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
