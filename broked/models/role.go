package models

// Permission represents a granular action permission.
type Permission string

const (
	// Pipelines
	PermPipelinesView   Permission = "pipelines.view"
	PermPipelinesCreate Permission = "pipelines.create"
	PermPipelinesEdit   Permission = "pipelines.edit"
	PermPipelinesDelete Permission = "pipelines.delete"
	PermPipelinesRun    Permission = "pipelines.run"
	PermPipelinesExport Permission = "pipelines.export"

	// Runs
	PermRunsView     Permission = "runs.view"
	PermRunsCancel   Permission = "runs.cancel"
	PermRunsResume   Permission = "runs.resume"
	PermRunsBackfill Permission = "runs.backfill"

	// Connections
	PermConnectionsView   Permission = "connections.view"
	PermConnectionsCreate Permission = "connections.create"
	PermConnectionsEdit   Permission = "connections.edit"
	PermConnectionsDelete Permission = "connections.delete"
	PermConnectionsTest   Permission = "connections.test"

	// Variables
	PermVariablesView       Permission = "variables.view"
	PermVariablesCreate     Permission = "variables.create"
	PermVariablesEdit       Permission = "variables.edit"
	PermVariablesDelete     Permission = "variables.delete"
	PermVariablesViewSecret Permission = "variables.view_secrets"

	// Workspaces
	PermWorkspacesView          Permission = "workspaces.view"
	PermWorkspacesCreate        Permission = "workspaces.create"
	PermWorkspacesManageMembers Permission = "workspaces.manage_members"
	PermWorkspacesManageTokens  Permission = "workspaces.manage_tokens"
	PermWorkspacesDelete        Permission = "workspaces.delete"

	// Settings
	PermSettingsView        Permission = "settings.view"
	PermSettingsEdit        Permission = "settings.edit"
	PermSettingsManageUsers Permission = "settings.manage_users"
	PermSettingsManageRoles Permission = "settings.manage_roles"

	// Audit
	PermAuditView   Permission = "audit.view"
	PermAuditExport Permission = "audit.export"

	// Git Sync
	PermGitSyncView Permission = "gitsync.view"
	PermGitSyncPull Permission = "gitsync.pull"
	PermGitSyncPush Permission = "gitsync.push"
)

// AllPermissions returns all available permissions.
func AllPermissions() []Permission {
	return []Permission{
		PermPipelinesView, PermPipelinesCreate, PermPipelinesEdit, PermPipelinesDelete, PermPipelinesRun, PermPipelinesExport,
		PermRunsView, PermRunsCancel, PermRunsResume, PermRunsBackfill,
		PermConnectionsView, PermConnectionsCreate, PermConnectionsEdit, PermConnectionsDelete, PermConnectionsTest,
		PermVariablesView, PermVariablesCreate, PermVariablesEdit, PermVariablesDelete, PermVariablesViewSecret,
		PermWorkspacesView, PermWorkspacesCreate, PermWorkspacesManageMembers, PermWorkspacesManageTokens, PermWorkspacesDelete,
		PermSettingsView, PermSettingsEdit, PermSettingsManageUsers, PermSettingsManageRoles,
		PermAuditView, PermAuditExport,
		PermGitSyncView, PermGitSyncPull, PermGitSyncPush,
	}
}

// PermissionInfo describes a permission for the API.
type PermissionInfo struct {
	Key         Permission `json:"key"`
	Category    string     `json:"category"`
	Description string     `json:"description"`
}

// AllPermissionInfos returns all permissions with descriptions.
func AllPermissionInfos() []PermissionInfo {
	return []PermissionInfo{
		{PermPipelinesView, "Pipelines", "View pipelines"},
		{PermPipelinesCreate, "Pipelines", "Create pipelines"},
		{PermPipelinesEdit, "Pipelines", "Edit pipelines"},
		{PermPipelinesDelete, "Pipelines", "Delete pipelines"},
		{PermPipelinesRun, "Pipelines", "Run pipelines"},
		{PermPipelinesExport, "Pipelines", "Export pipelines"},
		{PermRunsView, "Runs", "View runs and logs"},
		{PermRunsCancel, "Runs", "Cancel running pipelines"},
		{PermRunsResume, "Runs", "Resume failed runs"},
		{PermRunsBackfill, "Runs", "Trigger backfill runs"},
		{PermConnectionsView, "Connections", "View connections"},
		{PermConnectionsCreate, "Connections", "Create connections"},
		{PermConnectionsEdit, "Connections", "Edit connections"},
		{PermConnectionsDelete, "Connections", "Delete connections"},
		{PermConnectionsTest, "Connections", "Test connections"},
		{PermVariablesView, "Variables", "View variables"},
		{PermVariablesCreate, "Variables", "Create variables"},
		{PermVariablesEdit, "Variables", "Edit variables"},
		{PermVariablesDelete, "Variables", "Delete variables"},
		{PermVariablesViewSecret, "Variables", "View secret variable values"},
		{PermWorkspacesView, "Workspaces", "View workspaces"},
		{PermWorkspacesCreate, "Workspaces", "Create workspaces"},
		{PermWorkspacesManageMembers, "Workspaces", "Manage workspace members"},
		{PermWorkspacesManageTokens, "Workspaces", "Manage API tokens"},
		{PermWorkspacesDelete, "Workspaces", "Delete workspaces"},
		{PermSettingsView, "Settings", "View settings"},
		{PermSettingsEdit, "Settings", "Edit settings"},
		{PermSettingsManageUsers, "Settings", "Manage users"},
		{PermSettingsManageRoles, "Settings", "Manage roles"},
		{PermAuditView, "Audit", "View audit log"},
		{PermAuditExport, "Audit", "Export audit log"},
		{PermGitSyncView, "Git Sync", "View git sync status"},
		{PermGitSyncPull, "Git Sync", "Pull from git"},
		{PermGitSyncPush, "Git Sync", "Push to git"},
	}
}

// Role represents a role with permissions.
type Role struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions"`
	IsSystem    bool         `json:"is_system"`
	CreatedAt   string       `json:"created_at"`
}

// DefaultRoles returns the built-in system roles.
func DefaultRoles() []Role {
	all := AllPermissions()

	// Editor: everything except managing users, roles, workspaces (delete), secrets
	editorPerms := []Permission{
		PermPipelinesView, PermPipelinesCreate, PermPipelinesEdit, PermPipelinesDelete, PermPipelinesRun, PermPipelinesExport,
		PermRunsView, PermRunsCancel, PermRunsResume, PermRunsBackfill,
		PermConnectionsView, PermConnectionsCreate, PermConnectionsEdit, PermConnectionsDelete, PermConnectionsTest,
		PermVariablesView, PermVariablesCreate, PermVariablesEdit, PermVariablesDelete,
		PermWorkspacesView,
		PermSettingsView,
		PermAuditView,
		PermGitSyncView, PermGitSyncPull, PermGitSyncPush,
	}

	// Operator: run and monitor only
	operatorPerms := []Permission{
		PermPipelinesView, PermPipelinesRun,
		PermRunsView, PermRunsCancel, PermRunsResume,
		PermConnectionsView, PermConnectionsTest,
		PermVariablesView,
		PermWorkspacesView,
		PermSettingsView,
		PermAuditView,
	}

	// Viewer: read-only
	viewerPerms := []Permission{
		PermPipelinesView,
		PermRunsView,
		PermConnectionsView,
		PermVariablesView,
		PermWorkspacesView,
		PermSettingsView,
		PermAuditView,
		PermGitSyncView,
	}

	return []Role{
		{ID: "admin", Name: "Admin", Description: "Full access to all features", Permissions: all, IsSystem: true},
		{ID: "editor", Name: "Editor", Description: "Create, edit, and run pipelines", Permissions: editorPerms, IsSystem: true},
		{ID: "operator", Name: "Operator", Description: "Run and monitor pipelines", Permissions: operatorPerms, IsSystem: true},
		{ID: "viewer", Name: "Viewer", Description: "Read-only access", Permissions: viewerPerms, IsSystem: true},
	}
}

// HasPermission checks if a role has a specific permission.
func (r *Role) HasPermission(perm Permission) bool {
	for _, p := range r.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}
