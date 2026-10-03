package extensions

import (
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/secretstore"
)

// Registry holds all extension implementations.
// The open source binary uses DefaultRegistry().
// The enterprise binary creates a Registry with real implementations.
type Registry struct {
	Auth              AuthProvider
	Audit             AuditLogger
	GitSync           GitSyncProvider
	License           LicenseProvider
	Executors         []NodeExecutor
	Secrets           SecretProvider
	Notifier          NotificationProvider
	Contracts         DataContractProvider
	PII               PIIDetector
	OpenLineage       OpenLineageEmitter
	Platform          PlatformProvider
	Team              TeamProvider
	EventBus          EventBus
	JobQueue          JobQueue
	CancelBroadcaster RunCancelBroadcaster
	// TokenSource issues OIDC tokens for backends that authenticate by
	// workload identity federation (ADR-041, ADR-042). A distribution that
	// issues a token per workspace or run sets it; without one, the
	// deployment falls back to identity.FileTokenSourceFromEnv.
	TokenSource identity.TokenSource

	// SecretStoreProviders are secret-manager providers a distribution adds
	// to core's own (ADR-041 section 3). Appended after core's, so one with
	// the same name replaces core's.
	SecretStoreProviders []secretstore.Provider
}
