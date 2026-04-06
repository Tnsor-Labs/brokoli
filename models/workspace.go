package models

import "time"

// Workspace represents a team/project workspace for pipeline isolation.
type Workspace struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"` // URL-friendly identifier
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// WorkspaceMember links a user to a workspace with a specific role.
type WorkspaceMember struct {
	WorkspaceID string        `json:"workspace_id"`
	UserID      string        `json:"user_id"`
	Username    string        `json:"username"`
	Role        WorkspaceRole `json:"role"`
	JoinedAt    time.Time     `json:"joined_at"`
}

// WorkspaceRole defines granular permissions within a workspace.
type WorkspaceRole string

const (
	WsRoleOwner  WorkspaceRole = "owner"  // full control, can delete workspace
	WsRoleAdmin  WorkspaceRole = "admin"  // manage members, all pipeline ops
	WsRoleEditor WorkspaceRole = "editor" // create/edit/run pipelines
	WsRoleViewer WorkspaceRole = "viewer" // read-only access
)

// PermissionGrant defines a granular permission grant on a resource.
type PermissionGrant struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Resource    string `json:"resource"`    // pipeline, connection, variable, *
	ResourceID  string `json:"resource_id"` // specific ID or * for all
	Action      string `json:"action"`      // read, write, run, delete, admin, *
}

// APIToken is a long-lived token for service accounts / CI/CD.
type APIToken struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Token       string    `json:"token,omitempty"` // only shown on creation
	TokenHash   string    `json:"-"`               // stored hash
	WorkspaceID string    `json:"workspace_id"`
	UserID      string    `json:"user_id"`
	Role        string    `json:"role"` // role this token acts as
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
	LastUsedAt  time.Time `json:"last_used_at,omitempty"`
}

// OIDCGroupMapping maps OIDC group claims to workspace roles.
type OIDCGroupMapping struct {
	OIDCGroup   string        `json:"oidc_group"` // e.g., "engineering"
	WorkspaceID string        `json:"workspace_id"`
	Role        WorkspaceRole `json:"role"`
}

// DefaultWorkspaceID is used for community edition (single workspace).
const DefaultWorkspaceID = "default"
