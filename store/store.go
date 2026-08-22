package store

import (
	"database/sql"
	"math"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// nonTerminalRunStatusFilter is the SQL fragment (valid in both the SQLite
// and PostgreSQL dialects this package targets) selecting only non-terminal
// runs, shared verbatim by SQLiteStore.ListNonTerminalRuns and
// PostgresStore.ListNonTerminalRuns so the terminal-status set only needs
// keeping in sync with engine's private isTerminalRunStatus (which store
// cannot import) in one place.
const nonTerminalRunStatusFilter = `status NOT IN ('success','failed','cancelled','blocked')`

// PageParams holds pagination parameters.
type PageParams struct {
	Page     int // 1-based
	PageSize int // items per page
}

// PageResult holds paginated results with metadata.
type PageResult struct {
	Total    int         `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Pages    int         `json:"pages"`
	Items    interface{} `json:"items"`
}

// CursorResult holds cursor-based pagination results.
// No COUNT query needed — uses UUIDv7 for efficient keyset pagination.
type CursorResult struct {
	Items   interface{} `json:"items"`
	HasNext bool        `json:"has_next"`
	Cursor  string      `json:"cursor,omitempty"` // ID of last item — pass as ?after= for next page
	Limit   int         `json:"limit"`
}

// PendingRunClaimer atomically transitions an accepted run into execution.
// Implementations must return claimed=false when the run is missing, belongs
// to another pipeline, or is no longer pending.
type PendingRunClaimer interface {
	ClaimPendingRun(runID, pipelineID string, startedAt time.Time, traceID string) (claimed bool, err error)
}

// PendingRunCanceller is an optional store capability (check via type
// assertion, same pattern as PendingRunClaimer): atomically cancel a run
// only while it is still pending — the compare-and-swap mirror image of
// ClaimPendingRun. The conditional write is what makes cancel-vs-claim
// race-free: both sides are UPDATE ... WHERE status='pending', so exactly
// one of "worker claims the run" and "cancel marks it cancelled" wins, and
// the loser observes it lost.
type PendingRunCanceller interface {
	// CancelPendingRun transitions runID from pending to cancelled.
	// Returns false with a nil error when the run was not pending —
	// already claimed, already terminal, or missing.
	CancelPendingRun(runID string, finishedAt time.Time) (cancelled bool, err error)
}

// RunCancelRequester is an optional store capability (type-assert, same
// pattern as PendingRunClaimer): durably record that cancellation of a run
// was requested, before any of the acting halves of a cancel (local ctx
// cancel, pending compare-and-swap, relay broadcast) happen. The acting
// half can be lost — a broadcast is fire-and-forget, a process can die
// mid-cancel — but this row write cannot: the Runner re-checks the flag at
// every wave boundary and recovery honors it when closing out a run with
// no recoverable path, so the cancel still converges.
type RunCancelRequester interface {
	// RequestRunCancel sets the durable cancel flag on runID. Returns
	// false with a nil error when the run is already terminal or missing
	// (nothing left to cancel).
	RequestRunCancel(runID string) (requested bool, err error)
}

// ExecutionAttemptStore provides durable claim/lease operations over
// models.ExecutionAttempt rows — the outbox/intent record and
// compare-and-swap claim contract described by Tnsor-Labs/brokoli#7,
// extending PendingRunClaimer's run-level CAS pattern to
// (run_id, node_id, attempt) granularity. Implemented by both SQLiteStore
// and PostgresStore; callers type-assert against it the same way
// engine.Engine.ExecuteQueuedRun already does for PendingRunClaimer, since
// it is a capability of the concrete store rather than a requirement every
// Store implementation must satisfy.
// instanceKey (ADR-017) identifies which physical instance of nodeID a
// call targets — empty for a whole-node/whole-pipeline claim, matching
// models.ExecutionAttempt.InstanceKey. Every method below takes it as an
// explicit parameter (rather than overloading nodeID or attempt) so a
// caller that has never heard of instances — every caller before ADR-017 —
// keeps working unchanged by simply passing "".
type ExecutionAttemptStore interface {
	// CreateExecutionAttemptTx inserts the durable outbox/intent record for
	// an attempt inside an existing transaction (via WithTx), so it commits
	// atomically alongside CreateRunTx/AppendEventTx. Must be idempotent:
	// re-creating a row that already exists at
	// (run_id, node_id, instance_key, attempt) is a no-op, not an error —
	// duplicate outbox writes from a redelivered dispatch must be safe.
	CreateExecutionAttemptTx(tx *sql.Tx, a *models.ExecutionAttempt) error

	// ClaimAttempt atomically transitions an attempt into claimed by
	// claimedBy, incrementing FencingGeneration, and returns the new
	// generation. It only succeeds (ok=true) if the attempt is currently
	// queued or its previous lease has expired; claiming an already
	// in-flight (live-leased) or already-terminal (completed/failed)
	// attempt returns ok=false with no error and no state change — the
	// documented no-op duplicate-delivery contract.
	ClaimAttempt(runID, nodeID, instanceKey string, attempt int, claimedBy string, leaseDuration time.Duration) (fencingGeneration int64, ok bool, err error)

	// RenewLease extends the lease of an attempt the caller currently holds,
	// verified by fencingGeneration. Returns ok=false (no error) if the
	// caller's fencing generation is stale, meaning another worker already
	// reclaimed the attempt after the lease lapsed.
	RenewLease(runID, nodeID, instanceKey string, attempt int, claimedBy string, fencingGeneration int64, leaseDuration time.Duration) (ok bool, err error)

	// AckAttempt marks a claimed attempt as started, fencing-checked.
	// Re-acknowledging an attempt already in the started state under the
	// same fencing generation is a no-op success.
	AckAttempt(runID, nodeID, instanceKey string, attempt int, claimedBy string, fencingGeneration int64) error

	// CompleteAttempt marks an attempt completed, fencing-checked. Settling
	// an attempt that is already completed is a no-op success (safe
	// duplicate delivery); a fencing-generation mismatch against a
	// non-terminal attempt returns an error.
	CompleteAttempt(runID, nodeID, instanceKey string, attempt int, fencingGeneration int64) error

	// FailAttempt marks an attempt failed, fencing-checked, with the same
	// no-op-on-duplicate and error-on-fencing-mismatch semantics as
	// CompleteAttempt.
	FailAttempt(runID, nodeID, instanceKey string, attempt int, fencingGeneration int64, errMsg string) error

	// GetExecutionAttempt returns the current durable state of one attempt.
	GetExecutionAttempt(runID, nodeID, instanceKey string, attempt int) (*models.ExecutionAttempt, error)

	// ListExecutionAttemptsByRun returns every execution_attempts row for a
	// run — across every node_id (including the pipeline-level row, where
	// node_id is empty), every instance_key, and every status — ordered by
	// (node_id, instance_key, attempt). Unlike GetExecutionAttempt, which
	// requires already knowing the exact key to look up, this is the
	// enumeration startup recovery (Tnsor-Labs/brokoli#9) needs to find
	// every attempt belonging to a run so it can check each one's lease
	// state without guessing which node IDs, instance keys, and attempt
	// numbers exist.
	ListExecutionAttemptsByRun(runID string) ([]models.ExecutionAttempt, error)
}

// ExpansionInstanceStore provides durable per-item execution tracking for
// dynamic-expansion `code` nodes (Tnsor-Labs/brokoli#31 — the `expansion`
// config block compiled by brokoli-sdk's `_TaskWrapper.expand()`). See
// models.ExpansionInstance's doc comment for why this is a separate table
// rather than reusing NodeRun's or ExecutionAttempt's (node_id, attempt)
// keying: both already use "attempt" as the node-level retry counter, and
// overloading it with per-item identity would corrupt engine.ProjectRun's
// replay and engine/recovery.go's outcome determination for every OTHER
// node type. Implemented by both SQLiteStore and PostgresStore; callers
// type-assert against it (same pattern as ExecutionAttemptStore/
// PendingRunClaimer) since it's a capability of the concrete store, not a
// requirement every Store implementation must satisfy — a store that
// doesn't implement it simply doesn't get per-item durability, without
// blocking expansion execution itself.
type ExpansionInstanceStore interface {
	// CreateExpansionInstance inserts the row for one item's execution
	// attempt, before that item's script runs.
	CreateExpansionInstance(ei *models.ExpansionInstance) error

	// UpdateExpansionInstance persists the outcome (status/row count/
	// duration/error) of an item's execution attempt, keyed by ei.ID.
	UpdateExpansionInstance(ei *models.ExpansionInstance) error

	// ListExpansionInstancesByRun returns every expansion_instances row for
	// a run, across every expansion node and every item, ordered by
	// (node_id, node_attempt, instance_index).
	ListExpansionInstancesByRun(runID string) ([]models.ExpansionInstance, error)
}

// NewPageParams creates validated pagination parameters.
// Defaults: page=1, page_size=25. Max page_size=100.
func NewPageParams(page, pageSize int) PageParams {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return PageParams{Page: page, PageSize: pageSize}
}

// Offset returns the zero-based offset for SQL queries.
func (p PageParams) Offset() int {
	return (p.Page - 1) * p.PageSize
}

// Limit returns the page size (alias for clarity in SQL queries).
func (p PageParams) Limit() int {
	return p.PageSize
}

// NewPageResult creates a PageResult from a total count and items slice.
func NewPageResult(items interface{}, total int, params PageParams) PageResult {
	pages := int(math.Ceil(float64(total) / float64(params.PageSize)))
	return PageResult{
		Total:    total,
		Page:     params.Page,
		PageSize: params.PageSize,
		Pages:    pages,
		Items:    items,
	}
}

// DLQEntry represents a dead letter queue entry for a failed run.
type DLQEntry struct {
	ID           string `json:"id"`
	PipelineID   string `json:"pipeline_id"`
	PipelineName string `json:"pipeline_name,omitempty"` // resolved by ListDLQByOrg
	RunID        string `json:"run_id"`
	Error        string `json:"error"`
	NodeID       string `json:"node_id"`
	NodeName     string `json:"node_name"`
	Payload      string `json:"payload"`
	CreatedAt    string `json:"created_at"`
	Resolved     bool   `json:"resolved"`
	ResolvedAt   string `json:"resolved_at,omitempty"`
}

// PipelineVersion represents a saved version of a pipeline.
type PipelineVersion struct {
	Version   int    `json:"version"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

// CalendarDay aggregates run statuses for a single day.
type CalendarDay struct {
	Date    string `json:"date"` // YYYY-MM-DD
	Total   int    `json:"total"`
	Success int    `json:"success"`
	Failed  int    `json:"failed"`
	Running int    `json:"running"`
}

// Store defines the persistence interface for Broked.
// Implementations must be safe for concurrent use.
// Store is the full persistence contract. It is a composition of the
// focused capability interfaces below (ADR-015): a new capability — the
// physical execution plan next — is added as its own interface and
// embedded here, instead of appending to a single hundred-method
// declaration that force-breaks every hand-written implementation on
// each change. Callers that need only one capability should accept the
// narrow interface (e.g. take a store.AlertStore, not a store.Store).
//
// Every concrete implementation (SQLiteStore, PostgresStore, the EE
// APIStore) satisfies the whole set, so composing changes nothing for
// them; it only gives narrow consumers a smaller surface to depend on.
type Store interface {
	PipelineStore
	RunStore
	NodeRunStore
	RunEventStore
	LogStore
	PreviewStore
	VersionStore
	ConnectionStore
	VariableStore
	WorkspaceStore
	APITokenStore
	NodeProfileStore
	CalendarStore
	SettingsStore
	RoleStore
	LoginAttemptStore
	TxStore
	DLQStore
	CountStore
	MaintenanceStore
	AlertStore
	TemplateStore
	LifecycleStore
}

// PhysicalPlanStore persists the physical execution plan decided for a
// run (ADR-015, #90 M2): the planner's snapshot as-of-run-time, so
// recovery and audit read the plan that was actually used rather than
// recomputing one that could differ after code, connector, or policy
// changes.
//
// It is an OPTIONAL capability, deliberately not embedded in Store — the
// same shape as ExecutionAttemptStore. Core's SQLite/Postgres backends
// implement it; callers reach it by type-asserting the Store they hold
// (`if pp, ok := s.(store.PhysicalPlanStore); ok`). This lets a
// hand-written implementation that doesn't persist plans yet (the EE
// APIStore) keep satisfying Store, and opt into the capability when it's
// ready rather than being force-broken the moment the method lands.
// Rows cascade with their run, so retention is automatic via run-purge.
type PhysicalPlanStore interface {
	SaveRunPlan(runID, planJSON string) error
	GetRunPlan(runID string) (string, error)
}

// PhysicalInstanceStore persists the physical instances a run executed
// (ADR-015 point 3, #90 M3): the authoritative per-instance record, with
// its own durable identity keyed by (run_id, instance_key), rather than
// a projection recomputed from node_runs each read. This is the record
// dispatch will later lease and fence against.
//
// Optional capability, not embedded in Store (same shape as
// ExecutionAttemptStore / PhysicalPlanStore): core backends implement
// it, callers reach it by type assertion, and a hand-written store that
// hasn't adopted it keeps satisfying Store. Rows cascade with their run.
type PhysicalInstanceStore interface {
	// SavePhysicalInstances upserts a run's instances by (run_id,
	// instance_key), so re-saving a run (a resume) refreshes rather than
	// duplicates.
	SavePhysicalInstances(runID string, instances []models.PhysicalInstance) error
	ListPhysicalInstances(runID string) ([]models.PhysicalInstance, error)
}

// PipelineStore persists authored pipelines and answers dependency-graph
// queries over them.
type PipelineStore interface {
	CreatePipeline(p *models.Pipeline) error
	GetPipeline(id string) (*models.Pipeline, error)
	ListPipelines() ([]models.Pipeline, error)
	ListPipelinesByWorkspace(workspaceID string) ([]models.Pipeline, error)
	ListPipelinesByOrg(orgID string) ([]models.Pipeline, error)
	ListPipelinesByOrgPaged(orgID string, limit, offset int) ([]models.Pipeline, int, error)
	ListPipelinesByOrgCursor(orgID string, afterID string, limit int) ([]models.Pipeline, bool, error)
	UpdatePipeline(p *models.Pipeline) error
	// UpdatePipelineTx runs inside an existing transaction; for atomic cascades/decouples.
	UpdatePipelineTx(tx *sql.Tx, p *models.Pipeline) error
	DeletePipeline(id string) error
	// DeletePipelineTx runs inside an existing transaction; for atomic cascade deletes.
	DeletePipelineTx(tx *sql.Tx, id string) error
	GetPipelineByPipelineID(pipelineID string) (*models.Pipeline, error)
	// PipelinesDependingOn returns pipelines that list the given id in DependsOn or DependencyRules.
	// NOTE: returns raw cross-org matches; callers are responsible for org filtering when relevant.
	// Prefer ListPipelineDepsByOrg for graph walks — it avoids loading nodes/edges/params blobs.
	PipelinesDependingOn(pipelineID string) ([]models.Pipeline, error)

	// ListPipelineDepsByOrg returns a lightweight projection of every pipeline in the given org,
	// carrying only the fields needed for dependency-graph operations. Use this for cycle
	// detection, save-time validation, reverse-lookup, and the /dependency-graph endpoint —
	// it's a single query and skips loading multi-KB nodes/edges JSON blobs.
	ListPipelineDepsByOrg(orgID string) ([]models.PipelineDepSummary, error)
}

// RunStore persists pipeline runs and the queries recovery and the
// dependency gate need over them.
type RunStore interface {
	// GetLatestRunsByPipelineIDs returns the most recent run per pipeline ID in a single query.
	// Used by the dependency gate check to fold an O(N) loop of ListRunsByPipeline calls into O(1).
	GetLatestRunsByPipelineIDs(ids []string) (map[string]*models.Run, error)

	CreateRun(r *models.Run) error
	// CreateRunTx runs inside an existing transaction; used by
	// RunPipelineAsync's dispatch outbox (Tnsor-Labs/brokoli#7) to commit the
	// run row, its run.created event, and its execution-attempt outbox
	// record atomically. See store.ExecutionAttemptStore.
	CreateRunTx(tx *sql.Tx, r *models.Run) error
	GetRun(id string) (*models.Run, error)
	ListRunsByPipeline(pipelineID string, limit int) ([]models.Run, error)
	// ListRunsByPipelineCursor walks run history by keyset, newest first,
	// using the run ID as the cursor. Pass an empty afterID for the first
	// page and the last returned ID for each page after that. Returns
	// hasNext so callers can stop without a COUNT.
	//
	// Ordering is by ID alone, unlike ListRunsByPipeline, which orders by
	// started_at and cannot be walked by an ID cursor: a keyset is only
	// correct when the sort key and the cursor key are the same column.
	// Run IDs are UUIDv7, so ID order is creation order.
	ListRunsByPipelineCursor(pipelineID, afterID string, limit int) ([]models.Run, bool, error)
	// ListRunsByPipelinePaged returns one offset page plus the true total,
	// for callers that need page numbers rather than a cursor.
	ListRunsByPipelinePaged(pipelineID string, limit, offset int) ([]models.Run, int, error)
	UpdateRun(r *models.Run) error

	// ListNonTerminalRuns returns a keyset-paginated page of runs whose
	// status is not yet terminal (i.e. not success/failed/cancelled/blocked
	// — the same set engine's private isTerminalRunStatus treats as never
	// transitioning further; kept in sync manually since store cannot
	// import engine). This is the startup-recovery entry point
	// (Tnsor-Labs/brokoli#9): a process that died mid-run leaves exactly
	// these rows behind, and nothing before #9 ever queried for them.
	//
	// Pagination is keyset (afterID), not offset-based, deliberately: a
	// caller processing a page typically reconciles some of those runs to a
	// terminal status before requesting the next page, which would shift
	// an offset-based page boundary and skip or repeat rows. afterID="" or
	// "" starts from the beginning; pass the ID of the last run from the
	// previous page (ascending by id, which — like
	// ListPipelinesByOrgCursor's cursor — is a sortable UUIDv7, so this is
	// a plain indexed range scan) to continue. hasNext reports whether
	// another page remains.
	ListNonTerminalRuns(afterID string, limit int) (runs []models.Run, hasNext bool, err error)
}

// NodeRunStore persists per-node outcomes within a run.
type NodeRunStore interface {
	CreateNodeRun(nr *models.NodeRun) error
	// CreateNodeRunTx inserts a node outcome inside an existing transaction.
	CreateNodeRunTx(tx *sql.Tx, nr *models.NodeRun) error
	UpdateNodeRun(nr *models.NodeRun) error
	// UpdateNodeRunTx updates a node outcome inside an existing transaction.
	UpdateNodeRunTx(tx *sql.Tx, nr *models.NodeRun) error
	ListNodeRunsByRun(runID string) ([]models.NodeRun, error)
}

// RunEventStore is the immutable, append-only log of run/node-attempt
// lifecycle transitions (Tnsor-Labs/brokoli#6). Dual-written today
// alongside the CreateRun/UpdateRun/CreateNodeRun/UpdateNodeRun calls;
// see engine/projection.go for how the event stream is folded back into
// the equivalent runs/node_runs row shape.
type RunEventStore interface {
	AppendEvent(e *models.RunEvent) error
	// AppendEventTx runs inside an existing transaction, so an event can be
	// appended atomically alongside other writes via WithTx.
	AppendEventTx(tx *sql.Tx, e *models.RunEvent) error
	ListEventsByRun(runID string) ([]models.RunEvent, error)
}

// LogStore persists per-run log lines.
type LogStore interface {
	AppendLog(entry *models.LogEntry) error
	GetLogs(runID string) ([]models.LogEntry, error)
}

// PreviewStore persists per-node data previews for the editor.
type PreviewStore interface {
	SaveNodePreview(runID, nodeID string, columns []string, rows []common.DataRow) error
	GetNodePreview(runID, nodeID string) (columns []string, rows []common.DataRow, err error)
}

// VersionStore persists pipeline version snapshots.
type VersionStore interface {
	SavePipelineVersion(pipelineID string, snapshot string, message string) (int, error)
	ListPipelineVersions(pipelineID string) ([]PipelineVersion, error)
	GetPipelineVersion(pipelineID string, version int) (string, error) // returns snapshot JSON
}

// ConnectionStore persists saved connections.
type ConnectionStore interface {
	CreateConnection(c *models.Connection) error
	GetConnection(connID string) (*models.Connection, error)
	ListConnections() ([]models.Connection, error)
	ListConnectionsByWorkspace(workspaceID string) ([]models.Connection, error)
	ListConnectionsByWorkspacePaged(workspaceID string, limit, offset int) ([]models.Connection, int, error)
	UpdateConnection(c *models.Connection) error
	DeleteConnection(connID string) error
}

// VariableStore persists pipeline variables.
type VariableStore interface {
	SetVariable(v *models.Variable) error
	GetVariable(key string) (*models.Variable, error)
	ListVariables() ([]models.Variable, error)
	ListVariablesByWorkspace(workspaceID string) ([]models.Variable, error)
	ListVariablesByWorkspacePaged(workspaceID string, limit, offset int) ([]models.Variable, int, error)
	DeleteVariable(key string) error
}

// WorkspaceStore persists workspaces and their memberships.
type WorkspaceStore interface {
	CreateWorkspace(w *models.Workspace) error
	GetWorkspace(id string) (*models.Workspace, error)
	ListWorkspaces() ([]models.Workspace, error)
	DeleteWorkspace(id string) error
	AddWorkspaceMember(m *models.WorkspaceMember) error
	RemoveWorkspaceMember(workspaceID, userID string) error
	ListWorkspaceMembers(workspaceID string) ([]models.WorkspaceMember, error)
	GetUserWorkspaces(userID string) ([]models.Workspace, error)
}

// APITokenStore persists API tokens.
type APITokenStore interface {
	CreateAPIToken(t *models.APIToken) error
	GetAPITokenByHash(hash string) (*models.APIToken, error)
	ListAPITokens(workspaceID string) ([]models.APIToken, error)
	DeleteAPIToken(id string) error
}

// NodeProfileStore persists per-node data profiles and schema/drift.
type NodeProfileStore interface {
	SaveNodeProfile(runID, nodeID, profileJSON, schemaJSON, driftJSON string) error
	GetNodeProfile(runID, nodeID string) (profileJSON, schemaJSON, driftJSON string, err error)
	GetLatestNodeProfile(pipelineID, nodeID string) (profileJSON, schemaJSON string, err error)

	// GetLatestNodeProfilesForPipelines batch-fetches the latest profile for
	// every node of every given pipeline in one round trip, keyed by
	// "pipelineID:nodeID" — the batched counterpart to calling
	// GetLatestNodeProfile once per node, which turns lineage graph
	// construction into an N+1 query. ObservedAt lets a caller merging
	// profiles for the same shared asset across pipelines pick the
	// genuinely most recent one rather than guessing from data shape.
	GetLatestNodeProfilesForPipelines(pipelineIDs []string) (map[string]NodeProfileRecord, error)
}

// NodeProfileRecord is one row of GetLatestNodeProfilesForPipelines: the
// latest profile for one (pipeline, node) pair, plus the identity of the
// run that produced it.
type NodeProfileRecord struct {
	ProfileJSON string
	SchemaJSON  string
	RunID       string
	ObservedAt  time.Time
}

// CalendarStore answers run-activity aggregation queries.
type CalendarStore interface {
	GetRunCalendar(days int) ([]CalendarDay, error)
	GetRunCalendarByOrg(days int, orgID string) ([]CalendarDay, error)
}

// SettingsStore is a key-value settings table.
type SettingsStore interface {
	GetSetting(key string) (string, error)
	SetSetting(key, value string) error
}

// RoleStore persists RBAC roles.
type RoleStore interface {
	CreateRole(r *models.Role) error
	GetRole(id string) (*models.Role, error)
	ListRoles() ([]models.Role, error)
	UpdateRole(r *models.Role) error
	DeleteRole(id string) error
}

// LoginAttemptStore tracks failed-login lockout state.
type LoginAttemptStore interface {
	RecordLoginAttempt(username, ip string, success bool) error
	GetRecentFailedAttempts(username string, since time.Time) (int, error)
	ClearLoginAttempts(username string) error
}

// TxStore runs a function inside a single database transaction.
type TxStore interface {
	WithTx(fn func(*sql.Tx) error) error
}

// DLQStore persists dead-letter entries, per pipeline and across an org.
type DLQStore interface {
	AddToDLQ(pipelineID, runID, nodeID, nodeName, errMsg, payload string) error
	ListDLQ(pipelineID string, includeResolved bool, limit int) ([]DLQEntry, error)
	ResolveDLQ(id string) error
	// ListDLQByOrg lists dead-letter entries across every pipeline in an
	// org rather than one at a time — the question you actually have while
	// triaging.
	ListDLQByOrg(orgID string, includeResolved bool, limit int) ([]DLQEntry, error)
}

// CountStore answers the COUNT queries the paginated list endpoints need.
type CountStore interface {
	CountPipelines(workspaceID string) (int, error)
	CountConnections(workspaceID string) (int, error)
	CountVariables(workspaceID string) (int, error)
	CountRunsByPipeline(pipelineID string) (int, error)

	// CountRunsByStatus totals runs per status across the whole
	// deployment, for the metrics endpoint. In-process counters cannot
	// answer this: runs execute on workers, so the API — the stable
	// scrape target — reports zero for every run metric while the fleet
	// is busy, and a worker's own counters vanish when autoscaling
	// removes the pod.
	CountRunsByStatus() (map[string]int, error)
}

// MaintenanceStore owns retention and size introspection. ADR-015 makes
// this the home for instance retention when physical plans land, since
// instances multiply row counts by orders of magnitude.
type MaintenanceStore interface {
	PurgeRunsOlderThan(days int) (int64, error)
	PurgeRunsOlderThanByOrg(days int, orgID string) (int64, error)
	// ListRunIDsOlderThan/ListRunIDsOlderThanByOrg return exactly the run
	// IDs PurgeRunsOlderThan(By/Org) would delete, without deleting them —
	// call before the matching Purge call so a caller can clean up
	// per-run state the DB purge doesn't reach (local-disk artifacts and
	// pagination checkpoints — see api.systemPurge and
	// Tnsor-Labs/brokoli#49).
	ListRunIDsOlderThan(days int) ([]string, error)
	ListRunIDsOlderThanByOrg(days int, orgID string) ([]string, error)
	GetDBSize() (int64, error)
}

// AlertStore persists readable notifications. Every method is org-scoped;
// an alert must never be readable or mutable across tenants.
type AlertStore interface {
	CreateAlert(a *models.Alert) error
	ListAlerts(orgID string, unreadOnly bool, limit int) ([]models.Alert, error)
	CountUnreadAlerts(orgID string) (int, error)
	MarkAlertRead(orgID, id string) error
	MarkAllAlertsRead(orgID string) error
	DismissAlert(orgID, id string) error
}

// TemplateStore persists global, admin-curated starter pipelines offered
// at pipeline-creation time (GET /api/templates). Seeded from
// pkg/templates.Builtin on first migrate; editable afterward through
// these methods, not by redeploying.
type TemplateStore interface {
	ListPipelineTemplates() ([]models.PipelineTemplate, error)
	GetPipelineTemplate(id string) (*models.PipelineTemplate, error)
	CreatePipelineTemplate(t *models.PipelineTemplate) error
	UpdatePipelineTemplate(t *models.PipelineTemplate) error
	DeletePipelineTemplate(id string) error
}

// LifecycleStore is process-level store lifecycle.
type LifecycleStore interface {
	Close() error
	RawDB() interface{} // returns *sql.DB for extensions
}
