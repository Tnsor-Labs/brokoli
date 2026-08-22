// Package extensions defines interfaces for enterprise feature plugins.
// The open source binary uses default (no-op) implementations.
// The enterprise binary provides real implementations via a private repo.
package extensions

import (
	"context"
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

	// Metadata carries contextual fields that do not warrant their own
	// column — today, the tenant the action happened in.
	//
	// This matters because the enterprise audit query filters by org:
	// an entry recorded without one is stored and then unreachable
	// through the API. Everything core records (pipelines, connections,
	// variables, runs) had nowhere to put a tenant, so it all
	// disappeared from the audit view — on a live instance, 66 of 67
	// stored entries could not be read back.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
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

// NodeKindDeclarer is an optional interface a NodeExecutor may also
// implement to declare a dynamically-registered node type's structural
// role — the same tags a pipeline's own Node.Capabilities carries (see
// models.CapabilitySource etc.) — so validation-time structural checks
// (e.g. "pipeline must have a source") recognize executor-provided node
// types without requiring the pipeline author to hand-set Capabilities.
//
// Only pkg/plugins.Manager implements this today, mapping a plugin
// manifest's declared Kind ("source"/"sink"/"transform") to capability
// tags. A NodeExecutor that doesn't implement this interface is simply
// skipped by callers that consult it — e.g. the enterprise Kubernetes
// executor, which dispatches existing built-in node types rather than
// registering new ones, has no need to implement it.
type NodeKindDeclarer interface {
	// DeclaredCapabilities returns the capability tags for nodeType, and
	// whether this declarer recognizes the type at all (ok=false means
	// "not mine," not "no capabilities").
	DeclaredCapabilities(nodeType string) (caps []string, ok bool)
}

// ExecutionContext passed to a NodeExecutor.
//
// Attempt/IdempotencyKey/FencingGeneration mirror the identical fields on
// RunJob and models.ExecutionAttempt (Tnsor-Labs/brokoli#7) — see RunJob's
// doc comment for the shared (run, node, attempt) identity these describe.
// They were added, along with Context, to close a gap found while building
// support for external Kubernetes-based execution: an external
// executor (a K8s Job, a Docker container, a remote worker) had no attempt
// number to name its dispatch deterministically, no idempotency key to
// recognize a redispatch of the same attempt, and — despite this struct's
// Execute doc comment claiming otherwise — no actual context.Context to
// observe cancellation or a deadline through.
type ExecutionContext struct {
	RunID      string
	NodeID     string
	NodeType   string
	NodeName   string
	Config     map[string]interface{}
	InputData  interface{} // *common.DataSet
	PipelineID string

	// Attempt mirrors models.NodeRun.Attempt / models.ExecutionAttempt.Attempt:
	// 0 for the first try, 1+ for retries of this node within the run.
	Attempt int

	// IdempotencyKey lets an external executor (e.g. a Kubernetes Job name,
	// which must be deterministic and stable across redispatch to avoid
	// creating a duplicate Job) recognize a redispatch of the same logical
	// (run, node, attempt) as the same unit of work rather than starting a
	// new one. See engine/runner.go's nodeAttemptIdempotencyKey.
	IdempotencyKey string

	// FencingGeneration is the generation this attempt's claim was issued
	// under (models.ExecutionAttempt.FencingGeneration), used to detect a
	// stale dispatch after a lease was reassigned to another worker. It is
	// zero until engine/runner.go's per-node execution loop is wired
	// through claim/lease (Tnsor-Labs/brokoli#7 follow-up work, same as
	// RunJob.FencingGeneration, which is zero for the same reason today).
	FencingGeneration int64

	// Context carries cancellation and a deadline for this attempt. It is
	// derived from the pipeline run's own cancellable context combined
	// with the node's configured (or default) timeout, so Done() closes
	// when the run is cancelled OR the attempt's timeout elapses,
	// whichever happens first. Always non-nil when populated by
	// engine/runner.go; a NodeExecutor should still guard against a nil
	// Context defensively (e.g. via context.Background() fallback) since
	// nothing in this package enforces it at construction time.
	Context context.Context
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
	// Enqueue adds a pipeline run job to the queue. It is idempotent by job ID;
	// re-enqueuing an identical job must not create another pending delivery.
	Enqueue(job RunJob) error

	// Dequeue atomically claims a job and blocks until one is available.
	// Returns ErrQueueClosed when the queue is shut down.
	Dequeue() (RunJob, error)

	// Ack settles exactly the claimed job ID. It must never settle another job.
	Ack(jobID string) error

	// Fail atomically releases exactly the claimed job back to the pending queue.
	// Callers must Ack jobs whose execution reached a durably terminal run state.
	Fail(jobID string, err error) error

	// Len returns the current queue length.
	Len() int

	// Close shuts down the queue.
	Close() error
}

// JobQueueRenewer is an optional JobQueue capability (check via type
// assertion, same pattern as store's optional capability interfaces):
// keeping a claimed job's transport-level claim from being treated as
// abandoned while it is genuinely still being processed.
//
// Found live: a transport whose claim has its own idle/visibility
// timeout (RedisJobQueue's default is 30s) will hand a still-in-flight
// job's claim to a different, idle consumer the instant that timeout
// elapses — Redis Streams' XAUTOCLAIM has no way to know the original
// consumer is still legitimately working, only that it has gone quiet
// for that long. The stolen-from consumer's own eventual Ack or Fail
// then fails ("job is not claimed"), since ownership already moved.
// RunPipeline and a single dynamic-expansion instance can both
// legitimately run past 30s, so this isn't a corner case.
//
// RenewClaim is a heartbeat, not a new delivery: implementations must
// not increment whatever delivery-count/attempt bookkeeping they use for
// max-deliveries enforcement, only reset the claim's own idle timer. A
// JobQueue with no such timeout concept (e.g. an in-memory one) simply
// doesn't need to implement this — nothing requires it, and a caller
// must type-assert before calling.
type JobQueueRenewer interface {
	// RenewClaim resets the currently-claimed job's idle/visibility timer.
	// A no-op, not an error, if jobID is not currently claimed by anyone
	// (it may have already been settled).
	RenewClaim(jobID string) error
}

// RunCancelBroadcaster broadcasts run-cancellation requests across engine
// instances. Engine.CancelRun can only cancel a run whose Runner lives in
// its own process; in a distributed deployment (API pods and worker pods
// sharing one store and JobQueue) the instance that receives
// POST /runs/{id}/cancel is almost never the instance executing the run.
// Without a broadcaster such cancels fail with "not found or already
// completed" while the run keeps executing on its worker.
//
// The transport is enterprise-provided, mirroring JobQueue. Wiring: every
// engine instance sets Engine.CancelBroadcaster and subscribes its transport
// to deliver received run IDs to Engine.CancelRelayedRun, which cancels
// locally-executing runs and quietly ignores the rest. Delivery is
// best-effort fan-out — publishing to every instance and letting the owner
// react avoids tracking run ownership in the transport, and a lost message
// degrades to today's behavior (the run completes), never to a wrong
// terminal status.
type RunCancelBroadcaster interface {
	// BroadcastCancel publishes a cancellation request for runID to all
	// engine instances, including the caller's own.
	BroadcastCancel(runID string) error

	// SubscribeCancels registers the handler invoked for every broadcast
	// this instance receives (typically Engine.CancelRelayedRun). Called
	// once at engine wiring time, before any broadcasts are expected;
	// implementations deliver each received run ID on a background
	// goroutine until Close.
	SubscribeCancels(handler func(runID string)) error

	// Close shuts down the broadcaster and its subscription.
	Close() error
}

// RunJob represents a pipeline execution request in the job queue.
//
// NodeID/Attempt/IdempotencyKey/FencingGeneration (Tnsor-Labs/brokoli#7)
// address the node/attempt-level durable claim record — a
// models.ExecutionAttempt, store.ExecutionAttemptStore — that a JobQueue
// delivery should be claimed against once dispatch moves from
// pipeline-level (today's RunPipelineAsync, one job per run) to node-level.
// Today's only producer, Engine.RunPipelineAsync, populates IdempotencyKey
// (set to RunID: a queued run's own ID is already a globally unique,
// stable dispatch identity) and leaves NodeID/Attempt/FencingGeneration at
// their zero values, since the outbox record it writes atomically
// alongside this job is itself pipeline-level — see
// models.ExecutionAttempt's doc comment. A future node-level dispatch path
// populates all four from the models.ExecutionAttempt it claims.
type RunJob struct {
	ID         string            `json:"id"`
	PipelineID string            `json:"pipeline_id"`
	RunID      string            `json:"run_id"`
	OrgID      string            `json:"org_id"`
	Params     map[string]string `json:"params,omitempty"`
	// RequiredCapabilities restricts delivery to workers advertising every
	// listed capability. Empty means any eligible worker may claim the job.
	RequiredCapabilities []string  `json:"required_capabilities,omitempty"`
	Priority             int       `json:"priority"`
	EnqueuedAt           time.Time `json:"enqueued_at"`

	// NodeID is empty for pipeline-level jobs (today's only kind).
	NodeID string `json:"node_id,omitempty"`
	// InstanceKey identifies which physical instance of NodeID this job is
	// for (ADR-017) — a dynamic-expansion item or, in the future, a
	// pagination page — mirroring models.ExecutionAttempt.InstanceKey.
	// Empty for a whole-node or whole-pipeline job.
	InstanceKey string `json:"instance_key,omitempty"`
	// Attempt mirrors models.NodeRun.Attempt / models.ExecutionAttempt.Attempt
	// — which node/instance-level execution attempt generation this job
	// belongs to. This is a durable identity used to look up and settle a
	// specific store.ExecutionAttemptStore row (ADR-017); it has nothing to
	// do with how many times the queue transport itself has (re)delivered
	// this job message — see DeliveryCount for that. A JobQueue
	// implementation must never overwrite this field on dequeue: doing so
	// once silently broke ADR-017 remote instance dispatch, since
	// WorkOrder-bearing jobs rely on Attempt surviving redelivery unchanged
	// to keep settling the correct execution_attempts row.
	Attempt int `json:"attempt,omitempty"`
	// DeliveryCount is a transport-level concept a JobQueue implementation
	// may set on Dequeue: how many times this job message has been
	// delivered (1 on first delivery, 2 on first redelivery, etc.), used
	// for poison-pill/max-deliveries bookkeeping. Deliberately a separate
	// field from Attempt (see its doc comment) — a queue is free to leave
	// this at zero if it doesn't track deliveries.
	DeliveryCount int `json:"delivery_count,omitempty"`
	// IdempotencyKey lets a redelivered job recognize it is re-processing
	// the same logical attempt rather than starting a new one.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// FencingGeneration is the generation this job's claim was issued
	// under (models.ExecutionAttempt.FencingGeneration), used to detect a
	// stale claim after a lease was reassigned.
	FencingGeneration int64 `json:"fencing_generation,omitempty"`

	// WorkOrder (ADR-017) is the work-description envelope a remote
	// claimant needs to actually execute this instance, when NodeID/
	// InstanceKey identify one — nil for every job today, including
	// whole-pipeline jobs, which carry their work implicitly (the claiming
	// process already has the full pipeline definition via PipelineID).
	// See InstanceWorkOrder's own doc comment for scope.
	WorkOrder *InstanceWorkOrder `json:"work_order,omitempty"`
}

// InstanceWorkOrder (ADR-017, worker protocol v2 — proposed) is the
// work-description envelope a remote claimant needs to execute one
// physical instance without already having the full pipeline definition
// in hand — RFC §18.5's "worker lease" sketch (connector/runtime, input
// references, config, checkpoint, resource policy, progress endpoint,
// cancellation token), scoped to the physical work units currently dispatched
// by the engine: dynamic-expansion items and eligible source_api pagination
// pages.
//
// ItemColumns/ItemRow inline common.DataSet's shape as plain JSON-safe
// types rather than importing pkg/common here: this package deliberately
// stays import-light (see ExecutionContext.InputData's own
// interface{} // *common.DataSet convention above) since it's a low-level
// interface package many others import.
type InstanceWorkOrder struct {
	// NodeType is the work unit's node type (e.g. "code") — a remote
	// claimant needs this to know how to interpret Script/Config, mirroring
	// models.Node.Type.
	NodeType string `json:"node_type"`
	// Script is the node's executable script body, mirroring
	// models.Node.Config["script"].
	Script string `json:"script,omitempty"`
	// Config is the node-specific execution config. Code expansion dispatch
	// strips script/expansion metadata before populating it; source_api page
	// dispatch carries the request/response config needed to parse one page.
	Config map[string]interface{} `json:"config,omitempty"`
	// ItemColumns/ItemRow is this instance's one-row input dataset (a
	// dynamic-expansion item).
	ItemColumns []string               `json:"item_columns,omitempty"`
	ItemRow     map[string]interface{} `json:"item_row,omitempty"`
	// RunParams carries the run's own params (${param.x} interpolation),
	// mirroring RunJob.Params.
	RunParams map[string]string `json:"run_params,omitempty"`
	// TimeoutSeconds bounds this instance's execution — mirrors
	// executeExpansionInstance's timeoutSec parameter, and is what a
	// remote claimant's own lease duration should be derived from (see
	// that function's claim-duration comment).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	// SourceURL/SourceType/PageURL/PageParams describe one source_api page.
	// They are populated only when NodeType is "source_api"; code expansion
	// WorkOrders continue to use ItemColumns/ItemRow above. PageParams are
	// merged with the source node's configured params by the worker.
	SourceURL  string            `json:"source_url,omitempty"`
	SourceType string            `json:"source_type,omitempty"`
	PageURL    string            `json:"page_url,omitempty"`
	PageParams map[string]string `json:"page_params,omitempty"`
}

// ErrQueueClosed is returned by Dequeue when the queue is shut down.
var ErrQueueClosed = fmt.Errorf("queue closed")

// ErrJobNotClaimed is returned when settling a job that this queue has not claimed.
var ErrJobNotClaimed = fmt.Errorf("job not claimed")

// ErrJobConflict is returned when a job ID is reused with different content.
var ErrJobConflict = fmt.Errorf("job ID already exists with different content")

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
