package taskinterface

import (
	"fmt"
	"regexp"
)

// ParameterDeclaration is a parsed parameter_declaration
// (docs/schema/task-interface-v1.json#/$defs/parameter_declaration).
type ParameterDeclaration struct {
	Type        Type
	Required    bool
	Default     interface{} // nil means "no default declared" -- distinct from an explicit JSON null default
	HasDefault  bool
	Description string
	Sensitive   bool
}

// ParseParameterDeclaration decodes a parameter_declaration JSON object.
func ParseParameterDeclaration(raw interface{}) (ParameterDeclaration, error) {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return ParameterDeclaration{}, fmt.Errorf("taskinterface: parameter declaration is not an object: %T", raw)
	}
	typeRaw, ok := m["type"]
	if !ok {
		return ParameterDeclaration{}, fmt.Errorf("taskinterface: parameter declaration missing 'type'")
	}
	t, err := ParseType(typeRaw)
	if err != nil {
		return ParameterDeclaration{}, fmt.Errorf("taskinterface: parameter type: %w", err)
	}
	pd := ParameterDeclaration{Type: t}
	if required, ok := m["required"].(bool); ok {
		pd.Required = required
	}
	if def, ok := m["default"]; ok {
		pd.Default = def
		pd.HasDefault = true
	}
	pd.Description, _ = m["description"].(string)
	pd.Sensitive, _ = m["sensitive"].(bool)
	return pd, nil
}

var parameterNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// ResolveParameters implements ADR-032 section 3 rule 4/5: given a
// pipeline's raw 'parameters' declarations (as stored on
// models.Pipeline.Parameters or a task interface's local 'parameters')
// and a submitted run-request value object, it validates each submitted
// value against its declared type, applies declared defaults for
// parameters the caller omitted, and rejects both unknown submitted
// parameter names and missing required parameters -- all before a run is
// created, per that rule. The returned map contains exactly the
// declared parameter names, each with a value that has passed
// ValidateValue against its declared type (submitted or defaulted).
//
// declarations is a raw map[string]interface{} of parameter_declaration
// objects (the shape Pipeline.Parameters/Node.Interface already store);
// submitted is the run request's own JSON object of parameter values.
func ResolveParameters(declarations map[string]interface{}, submitted map[string]interface{}) (map[string]interface{}, error) {
	parsed := map[string]ParameterDeclaration{}
	for name, raw := range declarations {
		if !parameterNamePattern.MatchString(name) {
			return nil, fmt.Errorf("parameter %q: invalid name (ADR-032 section 3 rule 1)", name)
		}
		pd, err := ParseParameterDeclaration(raw)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		if pd.Required && pd.HasDefault {
			return nil, fmt.Errorf("parameter %q: required:true and default are mutually exclusive (ADR-032 section 3 rule 2)", name)
		}
		parsed[name] = pd
	}

	for name := range submitted {
		if _, ok := parsed[name]; !ok {
			return nil, fmt.Errorf("unknown parameter %q", name)
		}
	}

	resolved := map[string]interface{}{}
	for name, pd := range parsed {
		value, ok := submitted[name]
		if !ok {
			if pd.Required {
				return nil, fmt.Errorf("missing required parameter %q", name)
			}
			if pd.HasDefault {
				resolved[name] = pd.Default
			}
			continue
		}
		if err := ValidateValue(value, pd.Type, "$."+name); err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		resolved[name] = value
	}
	return resolved, nil
}
