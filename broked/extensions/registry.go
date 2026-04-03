package extensions

// Registry holds all extension implementations.
// The open source binary uses DefaultRegistry().
// The enterprise binary creates a Registry with real implementations.
type Registry struct {
	Auth        AuthProvider
	Audit       AuditLogger
	GitSync     GitSyncProvider
	License     LicenseProvider
	Executors   []NodeExecutor
	Secrets     SecretProvider
	Notifier    NotificationProvider
	Contracts   DataContractProvider
	PII         PIIDetector
	OpenLineage OpenLineageEmitter
	Platform    PlatformProvider
	Team        TeamProvider
}
