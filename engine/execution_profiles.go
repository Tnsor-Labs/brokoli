package engine

import "fmt"

const executionProfileVersion = 1

// executionProfileErrors validates the language-neutral marker carried beside
// the already-expanded execution values. It deliberately does not prescribe
// profile names: SDKs may define organization-specific profiles as long as
// they emit the same versioned shape and explicit values.
func executionProfileErrors(config map[string]interface{}) []string {
	raw, present := config["execution"]
	if !present {
		return nil
	}
	execution, ok := raw.(map[string]interface{})
	if !ok {
		return []string{"'execution' must be an object"}
	}
	markerRaw, present := execution["profile"]
	if !present {
		return nil
	}
	marker, ok := markerRaw.(map[string]interface{})
	if !ok {
		return []string{"'execution.profile' must be an object"}
	}
	var errors []string
	if version, ok := numericInt(marker["version"]); !ok || version != executionProfileVersion {
		errors = append(errors, fmt.Sprintf("'execution.profile.version' must be %d", executionProfileVersion))
	}
	if name, ok := marker["name"].(string); !ok || name == "" {
		errors = append(errors, "'execution.profile.name' is required")
	}
	if strict, present := marker["strict"]; present {
		if _, ok := strict.(bool); !ok {
			errors = append(errors, "'execution.profile.strict' must be boolean")
		}
		if strictValue, ok := strict.(bool); ok && strictValue {
			pagination, _ := config["pagination"].(map[string]interface{})
			strategy, _ := pagination["strategy"].(string)
			if maxConcurrency, ok := numericInt(execution["max_concurrency"]); ok && maxConcurrency > 1 &&
				(strategy == "cursor" || strategy == "next_link" || strategy == "link_header") {
				errors = append(errors, fmt.Sprintf("strict execution profile cannot request concurrency for sequential pagination strategy %q", strategy))
			}
		}
	}
	// A profile is only portable when it carries the complete effective policy.
	for _, key := range []string{"max_concurrency", "requests_per_second", "retry_scope", "checkpoint_every", "page_max_retries", "page_retry_backoff"} {
		if _, present := execution[key]; !present {
			errors = append(errors, fmt.Sprintf("profile must expand execution.%s explicitly", key))
		}
	}
	for _, key := range []string{"timeout", "max_retries", "retry_backoff", "retry_delay"} {
		if _, present := config[key]; !present {
			errors = append(errors, fmt.Sprintf("profile must expand %s explicitly", key))
		}
	}
	return errors
}

func numericInt(value interface{}) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), v == float64(int(v))
	default:
		return 0, false
	}
}

func strictExecution(config map[string]interface{}) bool {
	execution, _ := config["execution"].(map[string]interface{})
	marker, _ := execution["profile"].(map[string]interface{})
	strict, _ := marker["strict"].(bool)
	return strict
}
