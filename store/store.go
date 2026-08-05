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

// ExecutionAttemptStore provides durable claim/lease operations over
// models.ExecutionAttempt rows — the outbox/intent record and
// compare-and-swap claim contract described by Tnsor-Labs/brokoli#7,
// extending PendingRunClaimer's run-level CAS pattern to
// (run_id, node_id, attempt) granularity. Implemented by both SQLiteStore
// and PostgresStore; callers type-assert against it the same way
// engine.Engine.ExecuteQueuedRun already does for PendingRunClaimer, since
// it is a capability of the concrete store rather than a requirement every
// Store implementation must satisfy.
type ExecutionAttemptStore interface {
	// CreateExecutionAttemptTx inserts the durable outbox/intent record for
	// an attempt inside an existing transaction (via WithTx), so it commits
	// atomically alongside CreateRunTx/AppendEventTx. Must be idempotent:
	// re-creating a row that already exists at (run_id, node_id, attempt) is
	// a no-op, not an error — duplicate outbox writes from a redelivered
	// dispatch must be safe.
	CreateExecutionAttemptTx(tx *sql.Tx, a *models.ExecutionAttempt) error

	// ClaimAttempt atomically transitions an attempt into claimed by
	// claimedBy, incrementing FencingGeneration, and returns the new
	// generation. It only succeeds (ok=true) if the attempt is currently
	// queued or its previous lease has expired; claiming an already
	// in-flight (live-leased) or already-terminal (completed/failed)
	// attempt returns ok=false with no error and no state change — the
	// documented no-op duplicate-delivery contract.
	ClaimAttempt(runID, nodeID string, attempt int, claimedBy string, leaseDuration time.Duration) (fencingGeneration int64, ok bool, err error)

	// RenewLease extends the lease of an attempt the caller currently holds,
	// verified by fencingGeneration. Returns ok=false (no error) if the
	// caller's fencing generation is stale, meaning another worker already
	// reclaimed the attempt after the lease lapsed.
	RenewLease(runID, nodeID string, attempt int, claimedBy string, fencingGeneration int64, leaseDuration time.Duration) (ok bool, err error)

	// AckAttempt marks a claimed attempt as started, fencing-checked.
	// Re-acknowledging an attempt already in the started state under the
	// same fencing generation is a no-op success.
	AckAttempt(runID, nodeID string, attempt int, claimedBy string, fencingGeneration int64) error

	// CompleteAttempt marks an attempt completed, fencing-checked. Settling
	// an attempt that is already completed is a no-op success (safe
	// duplicate delivery); a fencing-generation mismatch against a
	// non-terminal attempt returns an error.
	CompleteAttempt(runID, nodeID string, attempt int, fencingGeneration int64) error

	// FailAttempt marks an attempt failed, fencing-checked, with the same
	// no-op-on-duplicate and error-on-fencing-mismatch semantics as
	// CompleteAttempt.
	FailAttempt(runID, nodeID string, attempt int, fencingGeneration int64, errMsg string) error

	// GetExecutionAttempt returns the current durable state of one attempt.
	GetExecutionAttempt(runID, nodeID string, attempt int) (*models.ExecutionAttempt, error)

	// ListExecutionAttemptsByRun returns every execution_attempts row for a
	// run — across every node_id (including the pipeline-level row, where
	// node_id is empty) and every status — ordered by (node_id, attempt).
	// Unlike GetExecutionAttempt, which requires already knowing the exact
	// key to look up, this is the enumeration startup recovery
	// (Tnsor-Labs/brokoli#9) needs to find every attempt belonging to a run
	// so it can check each one's lease state without guessing which node
	// IDs and attempt numbers exist.
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
	ID         string `json:"id"`
	PipelineID string `json:"pipeline_id"`
	RunID      string `json:"run_id"`
	Error      string `json:"error"`
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name"`
	Payload    string `json:"payload"`
	CreatedAt  string `json:"created_at"`
	Resolved   bool   `json:"resolved"`
	ResolvedAt string `json:"resolved_at,omitempty"`
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
type Store interface {
	// Pipelines
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

	// GetLatestRunsByPipelineIDs returns the most recent run per pipeline ID in a single query.
	// Used by the dependency gate check to fold an O(N) loop of ListRunsByPipeline calls into O(1).
	GetLatestRunsByPipelineIDs(ids []string) (map[string]*models.Run, error)

	// Runs
	CreateRun(r *models.Run) error
	// CreateRunTx runs inside an existing transaction; used by
	// RunPipelineAsync's dispatch outbox (Tnsor-Labs/brokoli#7) to commit the
	// run row, its run.created event, and its execution-attempt outbox
	// record atomically. See store.ExecutionAttemptStore.
	CreateRunTx(tx *sql.Tx, r *models.Run) error
	GetRun(id string) (*models.Run, error)
	ListRunsByPipeline(pipelineID string, limit int) ([]models.Run, error)
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

	// Node Runs
	CreateNodeRun(nr *models.NodeRun) error
	UpdateNodeRun(nr *models.NodeRun) error
	ListNodeRunsByRun(runID string) ([]models.NodeRun, error)

	// Run Events — immutable, append-only log of run/node-attempt lifecycle
	// transitions (Tnsor-Labs/brokoli#6). Dual-written today alongside the
	// CreateRun/UpdateRun/CreateNodeRun/UpdateNodeRun calls above; see
	// engine/projection.go for how the event stream is folded back into the
	// equivalent runs/node_runs row shape.
	AppendEvent(e *models.RunEvent) error
	// AppendEventTx runs inside an existing transaction, so an event can be
	// appended atomically alongside other writes via WithTx.
	AppendEventTx(tx *sql.Tx, e *models.RunEvent) error
	ListEventsByRun(runID string) ([]models.RunEvent, error)

	// Logs
	AppendLog(entry *models.LogEntry) error
	GetLogs(runID string) ([]models.LogEntry, error)

	// Data Preview
	SaveNodePreview(runID, nodeID string, columns []string, rows []common.DataRow) error
	GetNodePreview(runID, nodeID string) (columns []string, rows []common.DataRow, err error)

	// Versioning
	SavePipelineVersion(pipelineID string, snapshot string, message string) (int, error)
	ListPipelineVersions(pipelineID string) ([]PipelineVersion, error)
	GetPipelineVersion(pipelineID string, version int) (string, error) // returns snapshot JSON

	// Connections
	CreateConnection(c *models.Connection) error
	GetConnection(connID string) (*models.Connection, error)
	ListConnections() ([]models.Connection, error)
	ListConnectionsByWorkspace(workspaceID string) ([]models.Connection, error)
	ListConnectionsByWorkspacePaged(workspaceID string, limit, offset int) ([]models.Connection, int, error)
	UpdateConnection(c *models.Connection) error
	DeleteConnection(connID string) error

	// Variables
	SetVariable(v *models.Variable) error
	GetVariable(key string) (*models.Variable, error)
	ListVariables() ([]models.Variable, error)
	ListVariablesByWorkspace(workspaceID string) ([]models.Variable, error)
	ListVariablesByWorkspacePaged(workspaceID string, limit, offset int) ([]models.Variable, int, error)
	DeleteVariable(key string) error

	// Workspaces
	CreateWorkspace(w *models.Workspace) error
	GetWorkspace(id string) (*models.Workspace, error)
	ListWorkspaces() ([]models.Workspace, error)
	DeleteWorkspace(id string) error
	AddWorkspaceMember(m *models.WorkspaceMember) error
	RemoveWorkspaceMember(workspaceID, userID string) error
	ListWorkspaceMembers(workspaceID string) ([]models.WorkspaceMember, error)
	GetUserWorkspaces(userID string) ([]models.Workspace, error)

	// API Tokens
	CreateAPIToken(t *models.APIToken) error
	GetAPITokenByHash(hash string) (*models.APIToken, error)
	ListAPITokens(workspaceID string) ([]models.APIToken, error)
	DeleteAPIToken(id string) error

	// Node Profiles
	SaveNodeProfile(runID, nodeID, profileJSON, schemaJSON, driftJSON string) error
	GetNodeProfile(runID, nodeID string) (profileJSON, schemaJSON, driftJSON string, err error)
	GetLatestNodeProfile(pipelineID, nodeID string) (profileJSON, schemaJSON string, err error)

	// Calendar / Aggregation
	GetRunCalendar(days int) ([]CalendarDay, error)
	GetRunCalendarByOrg(days int, orgID string) ([]CalendarDay, error)

	// Settings (key-value)
	GetSetting(key string) (string, error)
	SetSetting(key, value string) error

	// Roles
	CreateRole(r *models.Role) error
	GetRole(id string) (*models.Role, error)
	ListRoles() ([]models.Role, error)
	UpdateRole(r *models.Role) error
	DeleteRole(id string) error

	// Login attempt tracking
	RecordLoginAttempt(username, ip string, success bool) error
	GetRecentFailedAttempts(username string, since time.Time) (int, error)
	ClearLoginAttempts(username string) error

	// Transactions
	WithTx(fn func(*sql.Tx) error) error

	// Dead Letter Queue
	AddToDLQ(pipelineID, runID, nodeID, nodeName, errMsg, payload string) error
	ListDLQ(pipelineID string, includeResolved bool, limit int) ([]DLQEntry, error)
	ResolveDLQ(id string) error

	// Pagination counts
	CountPipelines(workspaceID string) (int, error)
	CountConnections(workspaceID string) (int, error)
	CountVariables(workspaceID string) (int, error)
	CountRunsByPipeline(pipelineID string) (int, error)

	// Maintenance
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

	// Lifecycle
	Close() error
	RawDB() interface{} // returns *sql.DB for extensions
}
