package engine

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hc12r/broked/models"
	"github.com/hc12r/broked/quality"
	"github.com/hc12r/broked/store"
	"github.com/hc12r/brokolisql-go/pkg/common"
	"github.com/hc12r/brokolisql-go/pkg/fetchers"
	"github.com/hc12r/brokolisql-go/pkg/loaders"
)

// Runner executes a single pipeline run.
type Runner struct {
	store        store.Store
	eventCh      chan<- models.Event
	varStore     VariableStore      // for ${var.key} resolution
	connResolver *ConnectionResolver // for conn_id → URI
	run            *models.Run
	pipe           *models.Pipeline
	skipNodes      map[string]bool
	dryRun         bool
	dryRunMaxRows  int
	dryRunResults  map[string]*DryRunNodeResult
	params         map[string]string // runtime params
	varCtx         *VariableContext
}

// NewRunner creates a runner for the given pipeline.
func NewRunner(s store.Store, eventCh chan<- models.Event, pipe *models.Pipeline, vs VariableStore, cr *ConnectionResolver) *Runner {
	return &Runner{
		varStore:     vs,
		connResolver: cr,
		store:   s,
		eventCh: eventCh,
		pipe:    pipe,
	}
}

// Execute runs the pipeline end-to-end.
func (r *Runner) Execute() (*models.Run, error) {
	now := time.Now()
	r.run = &models.Run{
		ID:         uuid.New().String(),
		PipelineID: r.pipe.ID,
		Status:     models.RunStatusRunning,
		StartedAt:  &now,
	}
	if err := r.store.CreateRun(r.run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	r.emit(models.Event{Type: models.EventRunStarted, RunID: r.run.ID, PipelineID: r.pipe.ID})

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

	finishTime := time.Now()
	r.run.Status = models.RunStatusSuccess
	r.run.FinishedAt = &finishTime
	r.store.UpdateRun(r.run)
	r.emit(models.Event{Type: models.EventRunCompleted, RunID: r.run.ID, PipelineID: r.pipe.ID, Status: models.RunStatusSuccess})
	NotifyPipelineEvent(r.pipe, r.run, "run.completed", "")
	return r.run, nil
}

func (r *Runner) executeNode(node models.Node, outputs map[string]*common.DataSet, outputsMu *sync.Mutex) error {
	// Skip nodes that already succeeded (resume mode)
	if r.skipNodes != nil && r.skipNodes[node.ID] {
		r.log(node.ID, models.LogLevelInfo, "Skipping node %s (already succeeded)", node.Name)
		return nil
	}

	startTime := time.Now()
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

	// Retry logic
	maxRetries := 0
	if mr, ok := node.Config["max_retries"].(float64); ok {
		maxRetries = int(mr)
	}
	retryDelay := time.Second
	if rd, ok := node.Config["retry_delay"].(float64); ok && rd > 0 {
		retryDelay = time.Duration(rd) * time.Millisecond
	}

	var output *common.DataSet
	var err error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			r.log(node.ID, models.LogLevelWarning, "Retry %d/%d after %v", attempt, maxRetries, retryDelay)
			time.Sleep(retryDelay)
		}
		output, err = r.runNodeLogic(node, input, allInputs)
		if err == nil {
			break
		}
		if attempt < maxRetries {
			r.log(node.ID, models.LogLevelWarning, "Attempt %d failed: %v", attempt+1, err)
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
	r.log(node.ID, models.LogLevelInfo, "Node completed: %d rows in %dms", rowCount, duration)
	r.emit(models.Event{Type: models.EventNodeCompleted, RunID: r.run.ID, NodeID: node.ID, RowCount: rowCount, DurationMs: duration})

	return nil
}

func (r *Runner) runNodeLogic(node models.Node, input *common.DataSet, allInputs []*common.DataSet) (*common.DataSet, error) {
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
	default:
		return input, nil
	}
}

func (r *Runner) runSourceFile(node models.Node) (*common.DataSet, error) {
	path, _ := node.Config["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("source_file node requires 'path' config")
	}

	loader, err := loaders.GetLoader(path)
	if err != nil {
		return nil, fmt.Errorf("get loader: %w", err)
	}

	ds, err := loader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	r.log(node.ID, models.LogLevelInfo, "Loaded %d rows from %s", len(ds.Rows), path)
	return ds, nil
}

func (r *Runner) runSourceAPI(node models.Node) (*common.DataSet, error) {
	source, _ := node.Config["url"].(string)
	sourceType, _ := node.Config["source_type"].(string)
	if source == "" {
		return nil, fmt.Errorf("source_api node requires 'url' config")
	}
	if sourceType == "" {
		sourceType = "rest"
	}

	fetcher, err := fetchers.GetFetcher(sourceType)
	if err != nil {
		return nil, fmt.Errorf("get fetcher: %w", err)
	}

	ds, err := fetcher.Fetch(source, node.Config)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", source, err)
	}
	r.log(node.ID, models.LogLevelInfo, "Fetched %d rows from %s", len(ds.Rows), source)
	return ds, nil
}

func (r *Runner) runSourceDB(node models.Node) (*common.DataSet, error) {
	uri, _ := node.Config["uri"].(string)
	query, _ := node.Config["query"].(string)
	if uri == "" {
		return nil, fmt.Errorf("source_db node requires 'uri' config")
	}
	if query == "" {
		return nil, fmt.Errorf("source_db node requires 'query' config")
	}

	ds, err := QueryDatabase(uri, query)
	if err != nil {
		return nil, fmt.Errorf("query database: %w", err)
	}
	r.log(node.ID, models.LogLevelInfo, "Queried %d rows, %d columns from database", len(ds.Rows), len(ds.Columns))
	return ds, nil
}

func (r *Runner) runCode(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	script, _ := node.Config["script"].(string)
	if script == "" {
		return nil, fmt.Errorf("code node requires 'script' in config")
	}

	timeoutSec := 30
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}

	// Remove script from config before passing to the script (avoid circular ref)
	configForScript := make(map[string]interface{})
	for k, v := range node.Config {
		if k != "script" {
			configForScript[k] = v
		}
	}

	// Get run params
	var runParams map[string]string
	if r.varCtx != nil {
		runParams = r.varCtx.Params
	}

	result, stderr, err := ExecuteCodeNode(script, input, configForScript, runParams, timeoutSec)
	if stderr != "" {
		// Log stderr as warnings (user print statements, warnings, etc.)
		for _, line := range splitLines(stderr) {
			if line != "" {
				r.log(node.ID, models.LogLevelWarning, "python: %s", line)
			}
		}
	}
	if err != nil {
		return nil, err
	}

	r.log(node.ID, models.LogLevelInfo, "Python script executed: %d rows in, %d rows out", len(input.Rows), len(result.Rows))
	return result, nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func (r *Runner) runJoin(node models.Node, inputs []*common.DataSet) (*common.DataSet, error) {
	if len(inputs) < 2 {
		return nil, fmt.Errorf("join node requires exactly 2 inputs, got %d", len(inputs))
	}

	leftKey, _ := node.Config["left_key"].(string)
	rightKey, _ := node.Config["right_key"].(string)
	joinTypeStr, _ := node.Config["join_type"].(string)

	if leftKey == "" {
		return nil, fmt.Errorf("join node requires 'left_key' config")
	}
	if rightKey == "" {
		rightKey = leftKey
	}

	jt := ParseJoinType(joinTypeStr)
	result, err := JoinDatasets(inputs[0], inputs[1], leftKey, rightKey, jt)
	if err != nil {
		return nil, err
	}

	r.log(node.ID, models.LogLevelInfo, "%s join on %s=%s: %d + %d -> %d rows",
		jt, leftKey, rightKey, len(inputs[0].Rows), len(inputs[1].Rows), len(result.Rows))
	return result, nil
}

func (r *Runner) runTransform(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	if input == nil {
		return nil, fmt.Errorf("transform node requires input data")
	}

	// Parse rules from node config
	rulesRaw, _ := node.Config["rules"]
	rulesJSON, err := json.Marshal(rulesRaw)
	if err != nil {
		return nil, fmt.Errorf("marshal transform rules: %w", err)
	}

	var rules []TransformRule
	if err := json.Unmarshal(rulesJSON, &rules); err != nil {
		return nil, fmt.Errorf("parse transform rules: %w", err)
	}

	// Clone dataset to avoid mutating upstream
	clone := &common.DataSet{
		Columns: make([]string, len(input.Columns)),
		Rows:    make([]common.DataRow, len(input.Rows)),
	}
	copy(clone.Columns, input.Columns)
	for i, row := range input.Rows {
		newRow := make(common.DataRow, len(row))
		for k, v := range row {
			newRow[k] = v
		}
		clone.Rows[i] = newRow
	}

	if err := ApplyTransforms(rules, clone); err != nil {
		return nil, err
	}

	r.log(node.ID, models.LogLevelInfo, "Applied %d transforms: %d rows in, %d rows out",
		len(rules), len(input.Rows), len(clone.Rows))
	return clone, nil
}

func (r *Runner) runQualityCheck(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	if input == nil {
		return nil, fmt.Errorf("quality_check node requires input data")
	}

	// Parse checks from node config
	checksRaw, _ := node.Config["checks"]
	checksJSON, err := json.Marshal(checksRaw)
	if err != nil {
		return nil, fmt.Errorf("marshal checks config: %w", err)
	}

	var checks []quality.Check
	if err := json.Unmarshal(checksJSON, &checks); err != nil {
		return nil, fmt.Errorf("parse checks config: %w", err)
	}

	// Apply default on_failure from node config
	defaultOnFailure, _ := node.Config["on_failure"].(string)
	if defaultOnFailure == "" {
		defaultOnFailure = "warn"
	}
	for i := range checks {
		if checks[i].OnFailure == "" {
			checks[i].OnFailure = defaultOnFailure
		}
	}

	checker := quality.NewChecker()
	result, err := checker.Run(checks, input)
	if err != nil {
		return nil, err
	}

	// Log each check result
	for _, cr := range result.Results {
		if cr.Passed {
			r.log(node.ID, models.LogLevelInfo, "PASS: %s", cr.Message)
		} else {
			r.log(node.ID, models.LogLevelWarning, "FAIL: %s", cr.Message)
		}
	}
	r.log(node.ID, models.LogLevelInfo, "Quality check: %s", result.Summary)

	if result.ShouldBlock() {
		return nil, fmt.Errorf("quality check failed (blocking): %s", result.Summary)
	}

	// Pass data through
	return input, nil
}

func (r *Runner) runSQLGenerate(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	if input == nil {
		return nil, fmt.Errorf("sql_generate node requires input data")
	}

	dialectName, _ := node.Config["dialect"].(string)
	tableName, _ := node.Config["table"].(string)
	batchSize := 100
	if bs, ok := node.Config["batch_size"].(float64); ok {
		batchSize = int(bs)
	}
	createTable, _ := node.Config["create_table"].(bool)

	cfg := SQLGenConfig{
		Dialect:     dialectName,
		Table:       tableName,
		BatchSize:   batchSize,
		CreateTable: createTable,
	}

	sql, err := GenerateSQL(cfg, input)
	if err != nil {
		return nil, fmt.Errorf("generate SQL: %w", err)
	}

	r.log(node.ID, models.LogLevelInfo, "Generated %s SQL for table %q: %d rows, %d bytes",
		cfg.Dialect, cfg.Table, len(input.Rows), len(sql))

	// Pass SQL downstream as a single-row dataset
	return &common.DataSet{
		Columns: []string{"sql_output"},
		Rows:    []common.DataRow{{"sql_output": sql}},
	}, nil
}

func (r *Runner) runSinkFile(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	if input == nil {
		return nil, fmt.Errorf("sink_file node requires input data")
	}

	path, _ := node.Config["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("sink_file node requires 'path' config")
	}

	// Determine format from config or file extension
	format, _ := node.Config["format"].(string)
	if format == "" {
		// Auto-detect from extension
		switch {
		case strings.HasSuffix(path, ".csv") || strings.HasSuffix(path, ".tsv"):
			format = "csv"
		case strings.HasSuffix(path, ".sql"):
			format = "sql"
		default:
			format = "json"
		}
	}

	// Ensure output directory exists
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create output directory: %w", err)
		}
	}

	var content []byte
	var err error

	switch format {
	case "csv":
		content, err = r.marshalCSV(input)
	case "sql":
		// If input has sql_output column, write it directly
		if len(input.Rows) > 0 {
			if sql, ok := input.Rows[0]["sql_output"].(string); ok {
				content = []byte(sql)
				break
			}
		}
		content, err = json.MarshalIndent(input.Rows, "", "  ")
	default: // json
		content, err = json.MarshalIndent(input.Rows, "", "  ")
	}

	if err != nil {
		return nil, fmt.Errorf("marshal output as %s: %w", format, err)
	}

	if err := os.WriteFile(path, content, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}

	r.log(node.ID, models.LogLevelInfo, "Wrote %s output to %s (%d bytes, %d rows)", format, path, len(content), len(input.Rows))
	return nil, nil
}

func (r *Runner) marshalCSV(ds *common.DataSet) ([]byte, error) {
	var buf strings.Builder
	w := csv.NewWriter(&buf)

	// Header
	if err := w.Write(ds.Columns); err != nil {
		return nil, err
	}

	// Rows
	for _, row := range ds.Rows {
		record := make([]string, len(ds.Columns))
		for i, col := range ds.Columns {
			if v, ok := row[col]; ok && v != nil {
				record[i] = fmt.Sprintf("%v", v)
			}
		}
		if err := w.Write(record); err != nil {
			return nil, err
		}
	}

	w.Flush()
	return []byte(buf.String()), w.Error()
}

func (r *Runner) runSinkDB(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	if input == nil {
		return nil, fmt.Errorf("sink_db node requires input data")
	}

	uri, _ := node.Config["uri"].(string)
	if uri == "" {
		return nil, fmt.Errorf("sink_db node requires 'uri' config")
	}

	// Input should have sql_output from a sql_generate node
	var sqlContent string
	if len(input.Rows) == 1 {
		if s, ok := input.Rows[0]["sql_output"].(string); ok {
			sqlContent = s
		}
	}
	if sqlContent == "" {
		return nil, fmt.Errorf("sink_db expects input from sql_generate node (sql_output column)")
	}

	affected, err := ExecuteSQL(uri, sqlContent)
	if err != nil {
		return nil, fmt.Errorf("execute SQL: %w", err)
	}

	r.log(node.ID, models.LogLevelInfo, "Executed SQL against database: %d rows affected", affected)
	return nil, nil
}

func (r *Runner) failRun(err error) error {
	finishTime := time.Now()
	r.run.Status = models.RunStatusFailed
	r.run.FinishedAt = &finishTime
	r.store.UpdateRun(r.run)
	r.emit(models.Event{Type: models.EventRunFailed, RunID: r.run.ID, PipelineID: r.pipe.ID, Error: err.Error()})
	NotifyPipelineEvent(r.pipe, r.run, "run.failed", err.Error())
	return err
}

func (r *Runner) emit(e models.Event) {
	e.Timestamp = time.Now()
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
		Timestamp: time.Now(),
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
