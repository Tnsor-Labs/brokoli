package models

import "time"

// VariableType defines the kind of variable value.
type VariableType string

const (
	VarTypeString VariableType = "string"
	VarTypeNumber VariableType = "number"
	VarTypeJSON   VariableType = "json"
	VarTypeSecret VariableType = "secret" // encrypted at rest, masked in UI
)

// Variable is a named configuration value that pipelines can reference via ${var.key}.
type Variable struct {
	Key         string       `json:"key"`
	Value       string       `json:"value"`
	Type        VariableType `json:"type"`
	Description string       `json:"description"`
	WorkspaceID string       `json:"workspace_id,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}
