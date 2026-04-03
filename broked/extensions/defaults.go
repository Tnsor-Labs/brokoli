package extensions

import "net/http"

// ── Default implementations (open source / community edition) ──

// CommunityAuth is the default (no external SSO).
type CommunityAuth struct{}

func (c *CommunityAuth) Name() string                            { return "builtin" }
func (c *CommunityAuth) Enabled() bool                           { return false }
func (c *CommunityAuth) Middleware() func(http.Handler) http.Handler { return nil }
func (c *CommunityAuth) CallbackHandler() http.HandlerFunc       { return nil }

// CommunityAudit is the default (no audit logging).
type CommunityAudit struct{}

func (c *CommunityAudit) Log(entry AuditEntry) error                  { return nil }
func (c *CommunityAudit) Query(filter AuditFilter) ([]AuditEntry, error) { return nil, nil }

// CommunityGitSync is the default (no git sync).
type CommunityGitSync struct{}

func (c *CommunityGitSync) Enabled() bool                    { return false }
func (c *CommunityGitSync) Push(pipelineID string) error     { return nil }
func (c *CommunityGitSync) Pull() (int, error)               { return 0, nil }
func (c *CommunityGitSync) WebhookHandler() http.HandlerFunc { return nil }
func (c *CommunityGitSync) Config() GitSyncConfig             { return GitSyncConfig{} }
func (c *CommunityGitSync) Status() GitSyncStatus             { return GitSyncStatus{} }

// CommunityNotifier is the default (no notifications).
type CommunityNotifier struct{}

func (c *CommunityNotifier) Name() string              { return "none" }
func (c *CommunityNotifier) Enabled() bool             { return false }
func (c *CommunityNotifier) Send(n Notification) error { return nil }

// CommunityLicense always returns community edition.
type CommunityLicense struct{}

func (c *CommunityLicense) Validate() (*LicenseInfo, error) {
	return &LicenseInfo{
		Edition:  "community",
		Features: []string{},
	}, nil
}
func (c *CommunityLicense) HasFeature(feature string) bool { return false }
func (c *CommunityLicense) Edition() string                { return "community" }

// CommunityContracts is the default (no contract validation).
type CommunityContracts struct{}

func (c *CommunityContracts) Validate(contract DataContract, columns []string, rows []map[string]interface{}) []ContractViolation {
	return nil
}

// CommunityPII is the default (no PII detection).
type CommunityPII struct{}

func (c *CommunityPII) Scan(columns []string, rows []map[string]interface{}, sampleSize int) []PIIDetection {
	return nil
}

// CommunityOpenLineage is the default (no OpenLineage emission).
type CommunityOpenLineage struct{}

func (c *CommunityOpenLineage) EmitRunStart(pipelineID, pipelineName, runID string) error {
	return nil
}
func (c *CommunityOpenLineage) EmitRunComplete(pipelineID, pipelineName, runID string, durationMs int64) error {
	return nil
}
func (c *CommunityOpenLineage) EmitRunFail(pipelineID, pipelineName, runID string, err string) error {
	return nil
}

// noopPlatform is the default (no platform features).
type noopPlatform struct{}

func (n *noopPlatform) Enabled() bool                         { return false }
func (n *noopPlatform) RegisterRoutes(r, s, us interface{})   {}
func (n *noopPlatform) StartServices(s interface{})            {}
func (n *noopPlatform) StopServices()                          {}
func (n *noopPlatform) MigrateDB(db interface{})               {}

// noopTeam is the default (no team features).
type noopTeam struct{}

func (n *noopTeam) Enabled() bool                                    { return false }
func (n *noopTeam) RegisterRoutes(r, s interface{})                  {}
func (n *noopTeam) PermissionMiddleware(permission string) interface{} { return nil }
func (n *noopTeam) MigrateDB(db interface{})                         {}

// DefaultRegistry returns the community defaults.
func DefaultRegistry() *Registry {
	return &Registry{
		Auth:        &CommunityAuth{},
		Audit:       &CommunityAudit{},
		GitSync:     &CommunityGitSync{},
		License:     &CommunityLicense{},
		Notifier:    &CommunityNotifier{},
		Contracts:   &CommunityContracts{},
		PII:         &CommunityPII{},
		OpenLineage: &CommunityOpenLineage{},
		Platform:    &noopPlatform{},
		Team:        &noopTeam{},
	}
}
