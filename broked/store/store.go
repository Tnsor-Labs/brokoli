package store

import (
	"github.com/hc12r/broked/models"
	"github.com/hc12r/brokolisql-go/pkg/common"
)

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
	UpdatePipeline(p *models.Pipeline) error
	DeletePipeline(id string) error

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
	UpdateConnection(c *models.Connection) error
	DeleteConnection(connID string) error

	// Variables
	SetVariable(v *models.Variable) error
	GetVariable(key string) (*models.Variable, error)
	ListVariables() ([]models.Variable, error)
	DeleteVariable(key string) error

	// Calendar / Aggregation
	GetRunCalendar(days int) ([]CalendarDay, error)

	// Maintenance
	PurgeRunsOlderThan(days int) (int64, error)
	GetDBSize() (int64, error)

	// Lifecycle
	Close() error
	RawDB() interface{} // returns *sql.DB for extensions
}
