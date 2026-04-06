package store

import (
	"database/sql"
	"math"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

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
	DeletePipeline(id string) error
	GetPipelineByPipelineID(pipelineID string) (*models.Pipeline, error)

	// Runs
	CreateRun(r *models.Run) error
	GetRun(id string) (*models.Run, error)
	ListRunsByPipeline(pipelineID string, limit int) ([]models.Run, error)
	UpdateRun(r *models.Run) error

	// Node Runs
	CreateNodeRun(nr *models.NodeRun) error
	UpdateNodeRun(nr *models.NodeRun) error
	ListNodeRunsByRun(runID string) ([]models.NodeRun, error)

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
	GetDBSize() (int64, error)

	// Lifecycle
	Close() error
	RawDB() interface{} // returns *sql.DB for extensions
}
