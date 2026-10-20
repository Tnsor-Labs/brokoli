package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A preview runs a pipeline on a few rows to show what each node would
// produce, and leaves nothing behind: no run in the pipeline's history, no
// event, log, alert, hook or notification, and nothing written outside
// Brokoli. It backs the editor's Preview and the test of a change request.
//
// It is a real execution of the nodes it runs, in a scratch store that is
// thrown away. What it holds back:
//   - every node that may write outside Brokoli (sinks, notify, migrate,
//     bash, dbt, tasks, waits, and any node declaring the sink capability),
//     and everything downstream of one, which would have read its output;
//   - the pipeline's hooks;
//   - dispatch to workers: the nodes run in this process.
//
// Sources and code nodes do run. A source reads its whole input before the
// preview keeps the first rows, so a large source takes as long as it
// takes to read, within the preview's time limit.

// PreviewHeld is the status of a node a preview did not run because it may
// write outside Brokoli.
const PreviewHeld = "held"

// PreviewNotRun is the status of a node a preview did not run because it
// comes after a held node.
const PreviewNotRun = "not_run"

// PreviewTimeout bounds one preview. Under the 100 seconds a proxy in
// front of the server commonly allows a request.
var PreviewTimeout = 90 * time.Second

// previewRunnableTypes are the node types a preview runs: those that read,
// and those that only compute. Everything else is held.
var previewRunnableTypes = func() map[models.NodeType]bool {
	m := map[models.NodeType]bool{
		models.NodeTypeSourceFile: true,
		models.NodeTypeSourceAPI:  true,
		models.NodeTypeSourceDB:   true,
		models.NodeTypeCode:       true,
	}
	for t := range requeueSafeNodeTypes {
		m[t] = true
	}
	return m
}()

// previewHeldWhy says, by node type, why a preview does not run a node.
var previewHeldWhy = map[models.NodeType]string{
	models.NodeTypeSinkFile: "it writes a file",
	models.NodeTypeSinkDB:   "it writes to a database",
	models.NodeTypeSinkAPI:  "it sends data to an API",
	models.NodeTypeNotify:   "it sends a notification",
	models.NodeTypeMigrate:  "it changes a database",
	models.NodeTypeBash:     "it runs a shell command",
	models.NodeTypeDBT:      "it runs dbt, which builds tables",
	models.NodeTypeTask:     "it runs a task on a worker",
	models.NodeTypeWait:     "it waits on something outside the pipeline",
}

// previewRuns reports whether a preview runs n, and when not, why.
func previewRuns(n models.Node) (bool, string) {
	for _, c := range n.Capabilities {
		if c == models.CapabilitySink {
			return false, "Not run in a preview: it is declared a sink, so it writes outside Brokoli"
		}
	}
	if previewRunnableTypes[n.Type] {
		return true, ""
	}
	if why, ok := previewHeldWhy[n.Type]; ok {
		return false, "Not run in a preview: " + why
	}
	return false, fmt.Sprintf("Not run in a preview: a %s node may act outside Brokoli", n.Type)
}

// PreviewNode is what a preview shows of one node.
type PreviewNode struct {
	NodeID  string                   `json:"node_id"`
	Name    string                   `json:"name"`
	Type    string                   `json:"type"`
	Status  string                   `json:"status"`
	Columns []string                 `json:"columns,omitempty"`
	Rows    []map[string]interface{} `json:"rows,omitempty"`
	Error   string                   `json:"error,omitempty"`
	// Reason says why a held or not-run node did not run.
	Reason string `json:"reason,omitempty"`
}

// Preview is the outcome of a preview: every node of the pipeline, in the
// pipeline's order, and the error that stopped it, if one did.
type Preview struct {
	Nodes []PreviewNode `json:"nodes"`
	Error string        `json:"error,omitempty"`
}

// PreviewPipeline runs p as a preview, keeping at most maxRows rows of each
// node's output.
func (e *Engine) PreviewPipeline(ctx context.Context, p *models.Pipeline, maxRows int) (*Preview, error) {
	if maxRows <= 0 {
		maxRows = 10
	}
	run, held, after := previewPlan(p)

	dir, err := os.MkdirTemp("", "brokoli-preview-")
	if err != nil {
		return nil, fmt.Errorf("preview scratch space: %w", err)
	}
	defer os.RemoveAll(dir)
	scratch, err := store.NewSQLiteStore(filepath.Join(dir, "preview.db"))
	if err != nil {
		return nil, fmt.Errorf("preview scratch store: %w", err)
	}
	defer scratch.Close()
	if err := scratch.CreatePipeline(run); err != nil {
		return nil, fmt.Errorf("preview scratch store: %w", err)
	}

	// No event channel, no notifier, no job queue: nothing leaves the
	// process. Connections, variables and secrets are read from the real
	// server, as a run reads them.
	runner := e.configureRunner(NewRunner(previewStore{SQLiteStore: scratch, real: e.store}, nil, run, e.VarStore, e.ConnResolver, e.Executors, nil, e.InstanceID, nil))
	runner.orgID = p.OrgID
	runner.dryRun = true
	runner.dryRunMaxRows = maxRows
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, PreviewTimeout)
	defer cancel()
	runner.parentCtx = ctx

	_, runErr := runner.Execute()
	out := &Preview{}
	if runErr != nil {
		out.Error = runErr.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			out.Error = fmt.Sprintf("the preview stopped after %s: %v", PreviewTimeout, runErr)
		}
	}
	for _, n := range p.Nodes {
		pn := PreviewNode{NodeID: n.ID, Name: n.Name, Type: string(n.Type)}
		switch {
		case held[n.ID] != "":
			pn.Status, pn.Reason = PreviewHeld, held[n.ID]
		case after[n.ID]:
			pn.Status, pn.Reason = PreviewNotRun, "Not run in a preview: it comes after a step the preview holds back"
		default:
			if res := runner.dryRunResults[n.ID]; res != nil {
				pn.Status, pn.Columns, pn.Rows, pn.Error = res.Status, res.Columns, res.Rows, res.Error
			} else {
				pn.Status = PreviewNotRun
				pn.Reason = "The preview stopped before this step"
			}
		}
		out.Nodes = append(out.Nodes, pn)
	}
	return out, nil
}

// previewPlan is the pipeline a preview runs (p without its held nodes,
// what follows them, and its hooks), the held nodes with why, and the
// nodes not run because they follow one.
func previewPlan(p *models.Pipeline) (*models.Pipeline, map[string]string, map[string]bool) {
	held := map[string]string{}
	for _, n := range p.Nodes {
		if ok, why := previewRuns(n); !ok {
			held[n.ID] = why
		}
	}
	next := map[string][]string{}
	for _, e := range p.Edges {
		next[e.From] = append(next[e.From], e.To)
	}
	after := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		for _, to := range next[id] {
			if held[to] == "" && !after[to] {
				after[to] = true
				walk(to)
			}
		}
	}
	ids := make([]string, 0, len(held))
	for id := range held {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		walk(id)
	}

	cp := *p
	cp.Hooks = nil
	cp.Nodes = nil
	for _, n := range p.Nodes {
		if held[n.ID] == "" && !after[n.ID] {
			cp.Nodes = append(cp.Nodes, n)
		}
	}
	cp.Edges = nil
	for _, e := range p.Edges {
		if held[e.From] == "" && !after[e.From] && held[e.To] == "" && !after[e.To] {
			cp.Edges = append(cp.Edges, e)
		}
	}
	return &cp, held, after
}

// previewStore is the scratch store a preview runs in, reading the
// pipeline's packaged code from the real server, where it is stored.
type previewStore struct {
	*store.SQLiteStore
	real store.Store
}

func (s previewStore) GetTaskBundle(orgID, digest string) ([]byte, error) {
	if b, ok := s.real.(store.TaskBundleStore); ok {
		return b.GetTaskBundle(orgID, digest)
	}
	return nil, store.ErrTaskBundleNotFound
}
