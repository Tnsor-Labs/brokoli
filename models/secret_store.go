package models

import "time"

// SecretStore is a named, workspace-scoped connection to a customer's
// secret manager (ADR-041 section 1). It holds how to reach the secret
// manager, never a value fetched from it.
type SecretStore struct {
	// ID is immutable, assigned on create. It is what an identity token
	// names, so renaming a store never changes who it trusts.
	ID string `json:"id"`
	// Name is unique within the workspace and is what references use:
	// secret://<name>/<path>.
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	OrgID       string `json:"org_id,omitempty"`
	// Provider is the secret manager: "vault", "aws_secrets_manager", ...
	Provider string `json:"provider"`
	// Settings are the provider's non-secret settings.
	Settings map[string]string `json:"settings"`
	// AuthMethod is "ambient", "oidc" or "token".
	AuthMethod string `json:"auth_method"`
	// AuthSettings are the method's non-secret settings.
	AuthSettings map[string]string `json:"auth_settings"`
	// Credential is the static token for auth_method "token": plaintext in
	// memory only. The API never returns it.
	Credential string `json:"credential,omitempty"`
	// CredentialRef is where the credential is stored: an encrypted://
	// reference, as a connection password is.
	CredentialRef string    `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
