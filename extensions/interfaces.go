// Package extensions defines interfaces for enterprise feature plugins.
// The open source binary uses default (no-op) implementations.
// The enterprise binary provides real implementations via a private repo.
package extensions

import (
	"fmt"
	"net/http"
	"time"
)

// NodeTypeGateFunc checks if an org's plan allows a specific node type.
// Set by enterprise. Returns error message if blocked, "" if allowed.
var NodeTypeGateFunc func(orgID, nodeType string) string

// AuthProvider handles external authentication (SSO/OIDC).
// Open source: uses built-in JWT auth.
// Enterprise: implements OIDC with Okta, Azure AD, Google Workspace, etc.
type AuthProvider interface {
	// Name returns the provider name (e.g., "oidc", "saml").
	Name() string

	// Enabled returns true if external auth is configured.
	Enabled() bool

	// Middleware returns an HTTP middleware that handles the auth flow.
	// It should redirect unauthenticated users to the provider's login page.
	Middleware() func(http.Handler) http.Handler

	// CallbackHandler returns the HTTP handler for the auth callback URL.
	CallbackHandler() http.HandlerFunc
}

// AuditLogger records user actions for compliance.
// Open source: no-op.
// Enterprise: logs to audit_log table with before/after state.
type AuditLogger interface {
	// Log records an action.
	Log(entry AuditEntry) error

	// Query returns audit entries matching the filter.
	Query(filter AuditFilter) ([]AuditEntry, error)
}

// AuditEntry represents a single auditable action.
type AuditEntry struct {
	ID         string                 `json:"id"`
	Timestamp  time.Time              `json:"timestamp"`
	UserID     string                 `json:"user_id"`
	Username   string                 `json:"username"`
	Action     string                 `json:"action"`   // create, update, delete, run, cancel, login
	Resource   string                 `json:"resource"` // pipeline, connection, variable, user
	ResourceID string                 `json:"resource_id"`
	Before     map[string]interface{} `json:"before,omitempty"` // state before change
	After      map[string]interface{} `json:"after,omitempty"`  // state after change
	IP         string                 `json:"ip"`
}

// AuditFilter for querying audit logs.
type AuditFilter struct {
	UserID     string    `json:"user_id,omitempty"`
	Action     string    `json:"action,omitempty"`
	Resource   string    `json:"resource,omitempty"`
	ResourceID string    `json:"resource_id,omitempty"`
	Since      time.Time `json:"since,omitempty"`
	Limit      int       `json:"limit,omitempty"`
}

// NodeExecutor runs a pipeline node.
// Open source: runs in-process on the same machine.
// Enterprise: dispatches to Kubernetes Jobs, Docker containers, or remote workers.
type NodeExecutor interface {
	// Name returns the executor type (e.g., "local", "kubernetes", "docker").
	Name() string

	// Execute runs a node and returns the output dataset.
	// The context carries cancellation and timeout.
	Execute(ctx ExecutionContext) (*ExecutionResult, error)

	// CanHandle returns true if this executor handles the given node type.
	CanHandle(nodeType string) bool
}

// ExecutionContext passed to a NodeExecutor.
type ExecutionContext struct {
	RunID      string
	NodeID     string
	NodeType   string
	NodeName   string
	Config     map[string]interface{}
	InputData  interface{} // *common.DataSet
	PipelineID string
}

// ExecutionResult from a NodeExecutor.
type ExecutionResult struct {
	OutputData interface{} // *common.DataSet
	RowCount   int
	DurationMs int64
	Logs       []string
}

// GitSyncProvider manages pipeline-as-code with git.
// Open source: no-op.
// Enterprise: syncs pipelines to/from a git repo.
type GitSyncProvider interface {
	// Enabled returns true if git sync is configured.
	Enabled() bool

	// Push exports a pipeline to the git repo.
	Push(pipelineID string) error

	// Pull imports pipelines from the git repo.
	Pull() (int, error) // returns number of pipelines imported/updated

	// WebhookHandler handles git push webhooks (auto-deploy).
	WebhookHandler() http.HandlerFunc

	// Config returns the current configuration (safe for API — no secrets).
	Config() GitSyncConfig

	// Status returns the current sync status.
	Status() GitSyncStatus
}

// GitSyncConfig is the safe-for-API git sync configuration.
type GitSyncConfig struct {
	RepoURL  string `json:"repo_url"`
	Branch   string `json:"branch"`
	Path     string `json:"path"`
	AutoSync bool   `json:"auto_sync"`
	HasToken bool   `json:"has_token"`
}

// GitSyncStatus reports the current state of git sync.
type GitSyncStatus struct {
	Enabled       bool   `json:"enabled"`
	Cloned        bool   `json:"cloned"`
	LastSync      string `json:"last_sync,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	PipelineCount int    `json:"pipeline_count"`
}

// SecretProvider integrates with external secret managers.
// Open source: uses built-in AES-256-GCM encryption.
// Enterprise: delegates to HashiCorp Vault, AWS Secrets Manager, etc.
type SecretProvider interface {
	// Name returns the provider name.
	Name() string

	// GetSecret retrieves a secret by key.
	GetSecret(key string) (string, error)

	// SetSecret stores a secret.
	SetSecret(key, value string) error
}

// NotificationProvider sends alerts to external services (Slack, PagerDuty, email).
type NotificationProvider interface {
	// Name returns the provider name (e.g., "slack", "pagerduty").
	Name() string

	// Enabled returns true if notifications are configured.
	Enabled() bool

	// Send delivers a notification.
	Send(notification Notification) error
}

// Notification represents an alert to send.
type Notification struct {
	Event      string            `json:"event"`    // run.completed, run.failed, sla.breach
	Severity   string            `json:"severity"` // info, warning, critical
	Title      string            `json:"title"`
	Message    string            `json:"message"`
	PipelineID string            `json:"pipeline_id"`
	Pipeline   string            `json:"pipeline"`
	RunID      string            `json:"run_id,omitempty"`
	Extra      map[string]string `json:"extra,omitempty"`
}

// LicenseInfo describes the active license.
type LicenseInfo struct {
	Edition   string    `json:"edition"` // community, team, enterprise
	Company   string    `json:"company"`
	Users     int       `json:"users"` // max users (0 = unlimited)
	ExpiresAt time.Time `json:"expires_at"`
	Features  []string  `json:"features"` // enabled feature flags
}

// LicenseProvider validates and returns license info.
type LicenseProvider interface {
	// Validate checks the license key and returns info.
	Validate() (*LicenseInfo, error)

	// HasFeature returns true if the license includes the given feature.
	HasFeature(feature string) bool

	// Edition returns the current edition.
	Edition() string
}

// PlatformProvider handles multi-tenant SaaS platform features.
// Open source: no-op (single-tenant, no admin panel).
// Enterprise: full platform with orgs, admin, tickets, analytics.
type PlatformProvider interface {
	// Enabled returns true if platform features are available.
	Enabled() bool

	// RegisterRoutes adds platform-specific API routes (admin, signup, tickets, orgs).
	// engine is *engine.Engine for fallback pipeline execution.
	RegisterRoutes(r interface{}, s interface{}, userStore interface{}, engine ...interface{})

	// StartServices starts background services (trial checker, etc).
	StartServices(s interface{})

	// StopServices stops background services.
	StopServices()

	// MigrateDB runs platform-specific database migrations.
	MigrateDB(db interface{})
}

// TeamProvider handles team-tier features.
// Open source: no-op.
// Enterprise: RBAC, alerts config, SLA, profiling, workspaces.
type TeamProvider interface {
	// Enabled returns true if team features are available.
	Enabled() bool

	// RegisterRoutes adds team-specific API routes (workspaces, roles, alerts config, etc).
	RegisterRoutes(r interface{}, s interface{})

	// PermissionMiddleware returns middleware that checks permissions (no-op in free tier).
	PermissionMiddleware(permission string) interface{}

	// MigrateDB runs team-specific database migrations.
	MigrateDB(db interface{})
}

// ── Distributed Infrastructure ──

// EventBus distributes real-time events across service instances.
// Open source: in-memory channel (single process).
// Enterprise: Redis pub/sub (multi-process, multi-pod).
type EventBus interface {
	// Publish sends an event to all subscribers.
	Publish(channel string, data []byte) error

	// Subscribe listens for events on a channel pattern.
	// Returns a channel that receives messages and a close function.
	Subscribe(pattern string) (<-chan EventMessage, func(), error)

	// Close shuts down the event bus.
	Close() error
}

// EventMessage is a message received from the event bus.
type EventMessage struct {
	Channel string
	Data    []byte
}

// JobQueue manages pipeline execution jobs.
// Open source: in-memory (runs in goroutines in the same process).
// Enterprise: Redis queue (distributed workers).
type JobQueue interface {
	// Enqueue adds a pipeline run job to the queue.
	Enqueue(job RunJob) error

	// Dequeue blocks until a job is available, then returns it.
	// Returns ErrQueueClosed when the queue is shut down.
	Dequeue() (RunJob, error)

	// Ack marks a job as completed.
	Ack(jobID string) error

	// Fail marks a job as failed.
	Fail(jobID string, err error) error

	// Len returns the current queue length.
	Len() int

	// Close shuts down the queue.
	Close() error
}

// RunJob represents a pipeline execution request in the job queue.
type RunJob struct {
	ID         string            `json:"id"`
	PipelineID string            `json:"pipeline_id"`
	RunID      string            `json:"run_id"`
	OrgID      string            `json:"org_id"`
	Params     map[string]string `json:"params,omitempty"`
	Priority   int               `json:"priority"`
	EnqueuedAt time.Time         `json:"enqueued_at"`
}

// ErrQueueClosed is returned by Dequeue when the queue is shut down.
var ErrQueueClosed = fmt.Errorf("queue closed")

// ── Column Lineage, Data Contracts, PII Detection, OpenLineage ──

// ColumnLineage tracks column-level data flow through pipelines.
type ColumnLineage struct {
	SourcePipeline string `json:"source_pipeline"`
	SourceNode     string `json:"source_node"`
	SourceColumn   string `json:"source_column"`
	TargetPipeline string `json:"target_pipeline"`
	TargetNode     string `json:"target_node"`
	TargetColumn   string `json:"target_column"`
	Transform      string `json:"transform,omitempty"` // "passthrough", "derived", "aggregated"
}

// DataContract defines expected schema and constraints for a pipeline output.
type DataContract struct {
	PipelineID  string           `json:"pipeline_id"`
	NodeID      string           `json:"node_id,omitempty"` // empty = final output
	Columns     []ContractColumn `json:"columns"`
	MinRows     int              `json:"min_rows,omitempty"`
	MaxRows     int              `json:"max_rows,omitempty"`
	Owner       string           `json:"owner,omitempty"`
	Description string           `json:"description,omitempty"`
}

// ContractColumn defines a column constraint within a data contract.
type ContractColumn struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`                   // string, number, boolean
	Required   bool    `json:"required"`               // must exist in output
	NotNull    bool    `json:"not_null"`               // no null values allowed
	Unique     bool    `json:"unique,omitempty"`       // all values unique
	MaxNullPct float64 `json:"max_null_pct,omitempty"` // max null percentage (0-100)
}

// ContractViolation records a contract breach.
type ContractViolation struct {
	Column   string `json:"column"`
	Rule     string `json:"rule"` // required, not_null, unique, type, null_pct
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Severity string `json:"severity"` // warning, error
}

// PIIDetection represents a detected PII field.
type PIIDetection struct {
	Column      string  `json:"column"`
	PIIType     string  `json:"pii_type"`     // email, phone, ssn, ip_address, credit_card, name
	Confidence  float64 `json:"confidence"`   // 0.0-1.0
	SampleCount int     `json:"sample_count"` // how many samples matched
}

// DataContractProvider validates data contracts.
type DataContractProvider interface {
	Validate(contract DataContract, columns []string, rows []map[string]interface{}) []ContractViolation
}

// PIIDetector scans data for PII.
type PIIDetector interface {
	Scan(columns []string, rows []map[string]interface{}, sampleSize int) []PIIDetection
}

// OpenLineageEmitter sends lineage events to an OpenLineage-compatible endpoint.
type OpenLineageEmitter interface {
	EmitRunStart(pipelineID, pipelineName, runID string) error
	EmitRunComplete(pipelineID, pipelineName, runID string, durationMs int64) error
	EmitRunFail(pipelineID, pipelineName, runID string, err string) error
}
