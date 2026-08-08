package models

import "time"

// PipelineTemplate is a starter pipeline definition offered at
// pipeline-creation time (GET /api/templates). Global, not per-org or
// per-workspace — template curation (create/update/delete) is an
// admin-only, platform-wide action, not a per-tenant one.
//
// Nodes/Edges are copied as-is into a new Pipeline when a user picks a
// template — see api/handlers_pipeline.go's Create handler and
// pkg/templates.Builtin, which seeds the initial set of rows on first
// migrate.
type PipelineTemplate struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	Nodes       []Node    `json:"nodes"`
	Edges       []Edge    `json:"edges"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
