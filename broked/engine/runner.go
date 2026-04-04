package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hc12r/broked/extensions"
	"github.com/hc12r/broked/models"
	"github.com/hc12r/broked/store"
	"github.com/hc12r/brokolisql-go/pkg/common"
)

// Runner executes a single pipeline run.
type Runner struct {
	store         store.Store
	eventCh       chan<- models.Event
	varStore      VariableStore       // for ${var.key} resolution
	connResolver  *ConnectionResolver // for conn_id → URI
	ctx           context.Context
	cancel        context.CancelFunc
	run           *models.Run
	pipe          *models.Pipeline
	skipNodes     map[string]bool
	dryRun        bool
	dryRunMaxRows int
	dryRunResults map[string]*DryRunNodeResult
	params        map[string]string // runtime params
	varCtx        *VariableContext
	preRunID      string // pre-generated run ID (for registration before Execute)
	orgID         string // tenant isolation for WebSocket events
	executors     []extensions.NodeExecutor // enterprise: external executors (K8s, Docker)
	notifier      extensions.NotificationProvider // enterprise: Slack, PagerDuty, etc.
}

// NewRunner creates a runner for the given pipeline.
func NewRunner(s store.Store, eventCh chan<- models.Event, pipe *models.Pipeline, vs VariableStore, cr *ConnectionResolver, execs []extensions.NodeExecutor, notifier extensions.NotificationProvider) *Runner {
	return &Runner{
		varStore:     vs,
		connResolver: cr,
		executors:    execs,
		notifier:     notifier,
		store:        s,
		eventCh:      eventCh,
		pipe:         pipe,
	}
}

// Cancel stops a running pipeline.
func (r *Runner) Cancel() {
	if r.cancel != nil {
		r.cancel()
	}
}

// Execute runs the pipeline end-to-end.
func (r *Runner) Execute() (*models.Run, error) {
	r.ctx, r.cancel = context.WithCancel(context.Background())
	defer r.cancel()

	now := time.Now().UTC()
	runID := r.preRunID
	if runID == "" {
		runID = uuid.New().String()
	}
	r.run = &models.Run{
		ID:         runID,
		PipelineID: r.pipe.ID,
		Status:     models.RunStatusRunning,
		StartedAt:  &now,
	}
	if err := r.store.CreateRun(r.run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	r.emit(models.Event{Type: models.EventRunStarted, RunID: r.run.ID, PipelineID: r.pipe.ID})
	r.fireHook("on_start", nil)

	// Initialize variable context — merge pipeline default params with runtime params
	mergedParams := make(map[string]string)
	for k, v := range r.pipe.Params {
		mergedParams[k] = v
	}
	for k, v := range r.params {
		mergedParams[k] = v
	}
	r.varCtx = NewVariableContext(mergedParams, r.run.ID, now)
	r.varCtx.Vars = r.varStore // wire stored variables into resolver

	// Build dependency graph
	nodeMap := make(map[string]models.Node)
	inDegree := make(map[string]int)
	dependents := make(map[string][]string) // nodeID -> nodes that depend on it
	for _, n := range r.pipe.Nodes {
		nodeMap[n.ID] = n
		inDegree[n.ID] = 0
	}
	for _, e := range r.pipe.Edges {
		inDegree[e.To]++
		dependents[e.From] = append(dependents[e.From], e.To)
	}

	// Outputs map (thread-safe)
	outputs := make(map[string]*common.DataSet)
	var outputsMu sync.Mutex

	// Max parallelism semaphore (default 4, configurable later)
	maxParallel := 4
	sem := make(chan struct{}, maxParallel)

	// Execute in waves using Kahn's algorithm
	// Start with nodes that have no incoming edges
	remaining := make(map[string]int)
	for id, deg := range inDegree {
		remaining[id] = deg
	}

	var runErr error
	for {
		// Check if cancelled
		if r.ctx.Err() != nil {
			runErr = fmt.Errorf("pipeline cancelled")
			break
		}

		// Collect ready nodes (in-degree == 0 and not yet processed)
		var ready []models.Node
		for id, deg := range remaining {
			if deg == 0 {
				ready = append(ready, nodeMap[id])
			}
		}
		if len(ready) == 0 {
			break
		}

		// Remove ready nodes from remaining
		for _, n := range ready {
			delete(remaining, n.ID)
		}

		// Execute ready nodes in parallel
		var wg sync.WaitGroup
		errCh := make(chan error, len(ready))

		for _, node := range ready {
			wg.Add(1)
			sem <- struct{}{} // acquire semaphore
			go func(n models.Node) {
				defer wg.Done()
				defer func() { <-sem }() // release semaphore

				// Recover from panics — never let a node crash the server
				defer func() {
					if rec := recover(); rec != nil {
						r.log(n.ID, models.LogLevelError, "PANIC in node %s: %v", n.Name, rec)
						errCh <- fmt.Errorf("node %s panicked: %v", n.Name, rec)
					}
				}()

				// Check cancellation before starting node
				if r.ctx.Err() != nil {
					errCh <- fmt.Errorf("pipeline cancelled")
					return
				}

				if err := r.executeNode(n, outputs, &outputsMu); err != nil {
					errCh <- err
					return
				}
			}(node)
		}

		wg.Wait()
		close(errCh)

		// Check for errors
		for err := range errCh {
			if err != nil {
				runErr = err
				break
			}
		}
		if runErr != nil {
			return r.run, r.failRun(runErr)
		}

		// Decrement in-degree of dependents
		for _, n := range ready {
			for _, depID := range dependents[n.ID] {
				if _, ok := remaining[depID]; ok {
					remaining[depID]--
				}
			}
		}
	}

	finishTime := time.Now().UTC()

	// Check if already cancelled (by CancelRun)
	if r.ctx.Err() != nil {
		r.run.Status = models.RunStatusCancelled
		r.run.FinishedAt = &finishTime
		r.store.UpdateRun(r.run)
		r.emit(models.Event{Type: models.EventRunFailed, RunID: r.run.ID, PipelineID: r.pipe.ID, Status: models.RunStatusCancelled, Error: "cancelled"})
		r.fireHook("on_failure", map[string]string{"error": "cancelled by user"})
		return r.run, fmt.Errorf("pipeline cancelled")
	}

	if runErr != nil {
		r.run.Status = models.RunStatusFailed
		r.run.FinishedAt = &finishTime
		r.store.UpdateRun(r.run)
		r.emit(models.Event{Type: models.EventRunFailed, RunID: r.run.ID, PipelineID: r.pipe.ID, Status: models.RunStatusFailed, Error: runErr.Error()})
		r.fireHook("on_failure", map[string]string{"error": runErr.Error()})
		r.sendNotification("run.failed", "critical", fmt.Sprintf("Pipeline \"%s\" failed", r.pipe.Name), runErr.Error())
		NotifyPipelineEvent(r.pipe, r.run, "run.failed", runErr.Error())
		return r.run, runErr
	}

	r.run.Status = models.RunStatusSuccess
	r.run.FinishedAt = &finishTime
	r.store.UpdateRun(r.run)
	r.emit(models.Event{Type: models.EventRunCompleted, RunID: r.run.ID, PipelineID: r.pipe.ID, Status: models.RunStatusSuccess})
	r.fireHook("on_success", nil)
	r.sendNotification("run.completed", "info", fmt.Sprintf("Pipeline \"%s\" completed", r.pipe.Name), "Run finished successfully")
	NotifyPipelineEvent(r.pipe, r.run, "run.completed", "")
	return r.run, nil
}

func (r *Runner) executeNode(node models.Node, outputs map[string]*common.DataSet, outputsMu *sync.Mutex) error {
	// Skip nodes that already succeeded (resume mode)
	if r.skipNodes != nil && r.skipNodes[node.ID] {
		r.log(node.ID, models.LogLevelInfo, "Skipping node %s (already succeeded)", node.Name)
		return nil
	}

	startTime := time.Now().UTC()
	nr := &models.NodeRun{
		ID:        uuid.New().String(),
		RunID:     r.run.ID,
		NodeID:    node.ID,
		Status:    models.RunStatusRunning,
		StartedAt: &startTime,
	}
	r.store.CreateNodeRun(nr)
	r.emit(models.Event{Type: models.EventNodeStarted, RunID: r.run.ID, NodeID: node.ID})

	r.log(node.ID, models.LogLevelInfo, "Starting node: %s (%s)", node.Name, node.Type)

	// Resolve variables in node config
	if r.varCtx != nil && node.Config != nil {
		node.Config = r.varCtx.ResolveConfig(node.Config)
	}

	// Resolve connection (conn_id → URI/headers)
	if r.connResolver != nil && node.Config != nil {
		node.Config = r.connResolver.Resolve(node.Config, node.Type)
	}

	// Find input data from connected upstream nodes (thread-safe read)
	outputsMu.Lock()
	var input *common.DataSet
	var allInputs []*common.DataSet // for multi-input nodes like join
	for _, edge := range r.pipe.Edges {
		if edge.To == node.ID {
			if ds, ok := outputs[edge.From]; ok {
				if input == nil {
					input = ds
				}
				allInputs = append(allInputs, ds)
			}
		}
	}
	outputsMu.Unlock()

	// Ensure input is never nil for non-source nodes (prevents panics)
	if input == nil && node.Type != models.NodeTypeSourceFile &&
		node.Type != models.NodeTypeSourceAPI && node.Type != models.NodeTypeSourceDB {
		input = &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}}
	}

	// Retry logic with exponential backoff
	maxRetries := 0
	if mr, ok := node.Config["max_retries"].(float64); ok {
		maxRetries = int(mr)
	}
	baseDelay := time.Second
	if rd, ok := node.Config["retry_delay"].(float64); ok && rd > 0 {
		baseDelay = time.Duration(rd) * time.Millisecond
	}

	var output *common.DataSet
	var err error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: baseDelay * 2^(attempt-1), max 60s
			delay := baseDelay * time.Duration(1<<uint(attempt-1))
			if delay > 60*time.Second {
				delay = 60 * time.Second
			}
			r.log(node.ID, models.LogLevelWarning, "Retry %d/%d in %v (exponential backoff)", attempt, maxRetries, delay)
			r.emit(models.Event{
				Type:   models.EventNodeStarted,
				RunID:  r.run.ID,
				NodeID: node.ID,
				Status: "retrying",
				Error:  fmt.Sprintf("retry %d/%d", attempt, maxRetries),
			})
			// Context-aware sleep
			select {
			case <-time.After(delay):
			case <-r.ctx.Done():
				return fmt.Errorf("cancelled during retry wait")
			}
		}
		// Per-node timeout (default 30m for most, configurable via node config "timeout" in seconds)
		type nodeResult struct {
			output *common.DataSet
			err    error
		}

		nodeTimeout := 30 * time.Minute
		if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
			nodeTimeout = time.Duration(t) * time.Second
		}

		resultCh := make(chan nodeResult, 1)
		go func() {
			out, e := r.runNodeLogic(node, input, allInputs)
			resultCh <- nodeResult{out, e}
		}()

		select {
		case result := <-resultCh:
			output, err = result.output, result.err
		case <-time.After(nodeTimeout):
			err = fmt.Errorf("node timed out after %s", nodeTimeout)
		case <-r.ctx.Done():
			err = fmt.Errorf("pipeline cancelled")
		}

		if err == nil {
			if attempt > 0 {
				r.log(node.ID, models.LogLevelInfo, "Succeeded after %d retries", attempt)
			}
			break
		}
		if attempt < maxRetries {
			r.log(node.ID, models.LogLevelWarning, "Attempt %d/%d failed: %v", attempt+1, maxRetries+1, err)
		}
	}

	duration := time.Since(startTime).Milliseconds()

	if err != nil {
		nr.Status = models.RunStatusFailed
		nr.DurationMs = duration
		nr.Error = err.Error()
		r.store.UpdateNodeRun(nr)
		r.log(node.ID, models.LogLevelError, "Node failed after %d attempts: %v", maxRetries+1, err)
		r.emit(models.Event{Type: models.EventNodeFailed, RunID: r.run.ID, NodeID: node.ID, Error: err.Error()})
		return fmt.Errorf("node %s (%s) failed: %w", node.Name, node.ID, err)
	}

	rowCount := 0
	if output != nil {
		// In dry run mode, truncate rows and capture results
		if r.dryRun && r.dryRunMaxRows > 0 && len(output.Rows) > r.dryRunMaxRows {
			output.Rows = output.Rows[:r.dryRunMaxRows]
		}

		rowCount = len(output.Rows)
		outputsMu.Lock()
		outputs[node.ID] = output
		outputsMu.Unlock()

		if r.dryRun {
			// Capture preview for dry run results
			if r.dryRunResults == nil {
				r.dryRunResults = make(map[string]*DryRunNodeResult)
			}
			previewRows := make([]map[string]interface{}, len(output.Rows))
			for i, row := range output.Rows {
				previewRows[i] = map[string]interface{}(row)
			}
			r.dryRunResults[node.ID] = &DryRunNodeResult{
				NodeID:  node.ID,
				Name:    node.Name,
				Status:  "success",
				Columns: output.Columns,
				Rows:    previewRows,
			}
		} else {
			// Save data preview (first 50 rows) for the UI
			r.store.SaveNodePreview(r.run.ID, node.ID, output.Columns, output.Rows)
		}
	}

	nr.Status = models.RunStatusSuccess
	nr.DurationMs = duration
	nr.RowCount = rowCount
	if !r.dryRun {
		r.store.UpdateNodeRun(nr)
	}
	// Detailed completion log
	durStr := fmt.Sprintf("%dms", duration)
	if duration >= 1000 {
		durStr = fmt.Sprintf("%.1fs", float64(duration)/1000)
	}
	throughput := ""
	if duration > 0 && rowCount > 0 {
		rps := float64(rowCount) / (float64(duration) / 1000)
		if rps >= 1000 {
			throughput = fmt.Sprintf(" (%.0fK rows/sec)", rps/1000)
		} else {
			throughput = fmt.Sprintf(" (%.0f rows/sec)", rps)
		}
	}
	colInfo := ""
	if output != nil && len(output.Columns) > 0 {
		colInfo = fmt.Sprintf(", columns: [%s]", truncateList(output.Columns, 8))
	}
	r.log(node.ID, models.LogLevelInfo, "Node completed: %d rows in %s%s%s", rowCount, durStr, throughput, colInfo)
	r.emit(models.Event{Type: models.EventNodeCompleted, RunID: r.run.ID, NodeID: node.ID, RowCount: rowCount, DurationMs: duration})

	return nil
}

func (r *Runner) runNodeLogic(node models.Node, input *common.DataSet, allInputs []*common.DataSet) (*common.DataSet, error) {
	// Check if an external executor handles this node type (enterprise: K8s, Docker)
	for _, exec := range r.executors {
		if exec != nil && exec.CanHandle(string(node.Type)) {
			r.log(node.ID, models.LogLevelInfo, "Dispatching to %s executor", exec.Name())
			result, err := exec.Execute(extensions.ExecutionContext{
				RunID:      r.run.ID,
				NodeID:     node.ID,
				NodeType:   string(node.Type),
				NodeName:   node.Name,
				Config:     node.Config,
				InputData:  input,
				PipelineID: r.pipe.ID,
			})
			if err != nil {
				return nil, err
			}
			for _, logLine := range result.Logs {
				if logLine != "" {
					r.log(node.ID, models.LogLevelInfo, "[%s] %s", exec.Name(), logLine)
				}
			}
			if result.OutputData != nil {
				if ds, ok := result.OutputData.(*common.DataSet); ok {
					return ds, nil
				}
			}
			return nil, nil
		}
	}

	switch node.Type {
	case models.NodeTypeSourceFile:
		return r.runSourceFile(node)
	case models.NodeTypeSourceAPI:
		return r.runSourceAPI(node)
	case models.NodeTypeSourceDB:
		return r.runSourceDB(node)
	case models.NodeTypeTransform:
		return r.runTransform(node, input)
	case models.NodeTypeQualityCheck:
		return r.runQualityCheck(node, input)
	case models.NodeTypeCode:
		return r.runCode(node, input)
	case models.NodeTypeJoin:
		return r.runJoin(node, allInputs)
	case models.NodeTypeSQLGenerate:
		return r.runSQLGenerate(node, input)
	case models.NodeTypeSinkFile:
		return r.runSinkFile(node, input)
	case models.NodeTypeSinkDB:
		return r.runSinkDB(node, input)
	case models.NodeTypeSinkAPI:
		return r.runSinkAPI(node, input)
	case models.NodeTypeMigrate:
		return r.runMigrate(node)
	case models.NodeTypeCondition:
		return r.runCondition(node, input)
	default:
		return input, nil
	}
}


func (r *Runner) failRun(err error) error {
	finishTime := time.Now().UTC()
	r.run.Status = models.RunStatusFailed
	r.run.FinishedAt = &finishTime
	r.store.UpdateRun(r.run)
	r.emit(models.Event{Type: models.EventRunFailed, RunID: r.run.ID, PipelineID: r.pipe.ID, Error: err.Error()})
	r.sendNotification("run.failed", "critical", fmt.Sprintf("Pipeline \"%s\" failed", r.pipe.Name), err.Error())
	NotifyPipelineEvent(r.pipe, r.run, "run.failed", err.Error())
	// Add to dead letter queue
	if r.store != nil {
		r.store.AddToDLQ(r.pipe.ID, r.run.ID, "", "", err.Error(), "")
	}
	return err
}

func (r *Runner) emit(e models.Event) {
	e.Timestamp = time.Now().UTC()
	e.OrgID = r.orgID // tenant isolation
	select {
	case r.eventCh <- e:
	default:
	}
}

func (r *Runner) log(nodeID string, level models.LogLevel, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	r.store.AppendLog(&models.LogEntry{
		RunID:     r.run.ID,
		NodeID:    nodeID,
		Level:     level,
		Message:   msg,
		Timestamp: time.Now().UTC(),
	})
	r.emit(models.Event{
		Type:    models.EventLog,
		RunID:   r.run.ID,
		NodeID:  nodeID,
		Level:   level,
		Message: msg,
	})
}

// topoSort performs Kahn's algorithm for topological ordering.
func topoSort(nodes []models.Node, edges []models.Edge) ([]models.Node, error) {
	nodeMap := make(map[string]models.Node)
	inDegree := make(map[string]int)
	adj := make(map[string][]string)

	for _, n := range nodes {
		nodeMap[n.ID] = n
		inDegree[n.ID] = 0
	}

	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
		inDegree[e.To]++
	}

	var queue []string
	for _, n := range nodes {
		if inDegree[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}

	var sorted []models.Node
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sorted = append(sorted, nodeMap[id])

		for _, next := range adj[id] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if len(sorted) != len(nodes) {
		return nil, fmt.Errorf("cycle detected in pipeline graph")
	}

	return sorted, nil
}


// ── Lifecycle Hooks ─────────────────────────────────────────

func (r *Runner) fireHook(hookName string, extra map[string]string) {
	if r.pipe.Hooks == nil {
		return
	}
	hook, ok := r.pipe.Hooks[hookName]
	if !ok || !hook.Enabled || hook.URL == "" {
		return
	}

	payload := map[string]interface{}{
		"event":       hookName,
		"pipeline_id": r.pipe.ID,
		"pipeline":    r.pipe.Name,
		"run_id":      r.run.ID,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}
	for k, v := range extra {
		payload[k] = v
	}

	data, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", hook.URL, strings.NewReader(string(data)))
	if err != nil {
		r.log("", models.LogLevelWarning, "Hook %s: failed to create request: %v", hookName, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		r.log("", models.LogLevelWarning, "Hook %s: request failed: %v", hookName, err)
		return
	}
	resp.Body.Close()
	r.log("", models.LogLevelInfo, "Hook %s fired: HTTP %d", hookName, resp.StatusCode)
}

