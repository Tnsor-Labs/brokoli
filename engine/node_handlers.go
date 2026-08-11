package engine

// Node handler methods for the Runner.
// Extracted from runner.go for maintainability.

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/fetchers"
	"github.com/Tnsor-Labs/brokoli/pkg/loaders"
	"github.com/Tnsor-Labs/brokoli/quality"
)

// allowedDataDirs defines directories where source/sink file nodes may read/write.
// Configurable via BROKOLI_DATA_DIRS environment variable (colon-separated).
var allowedDataDirs = func() []string {
	dirs := os.Getenv("BROKOLI_DATA_DIRS")
	if dirs == "" {
		return []string{"/data", "/tmp", "."}
	}
	return strings.Split(dirs, ":")
}()

// validateFilePath ensures the path is within allowed directories and has no traversal.
func validateFilePath(path string) error {
	if strings.Contains(path, "..") {
		return fmt.Errorf("path traversal not allowed")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path")
	}
	for _, dir := range allowedDataDirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absPath, absDir+string(filepath.Separator)) || absPath == absDir {
			return nil
		}
	}
	return fmt.Errorf("file path %q outside allowed directories (%s); set BROKOLI_DATA_DIRS to allow additional paths", path, strings.Join(allowedDataDirs, ", "))
}

// runCondition evaluates a condition and returns its decision separately from
// the unchanged data payload. IR 2.0 compatibility is applied by runNodeLogic.
func (r *Runner) runCondition(node models.Node, input *common.DataSet) (*common.DataSet, bool, error) {
	expr, _ := node.Config["expression"].(string)
	if expr == "" {
		r.log(node.ID, models.LogLevelWarning, "Condition node has no expression — passing through")
		return input, true, nil
	}

	result := EvaluateCondition(expr, input)
	r.log(node.ID, models.LogLevelInfo, "Condition '%s' → %v (%s)", expr, result.Passed, result.Reason)
	return input, result.Passed, nil
}

func (r *Runner) runSourceFile(node models.Node) (*common.DataSet, error) {
	path, _ := node.Config["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("source_file node requires 'path' config")
	}

	if err := validateFilePath(path); err != nil {
		return nil, fmt.Errorf("source_file: %w", err)
	}

	loader, err := loaders.GetLoader(path)
	if err != nil {
		return nil, fmt.Errorf("get loader: %w", err)
	}

	ds, err := loader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}

	// Detailed source logging
	fi, _ := os.Stat(path)
	var sizeStr string
	if fi != nil {
		mb := float64(fi.Size()) / 1024 / 1024
		if mb >= 1 {
			sizeStr = fmt.Sprintf("%.1f MB", mb)
		} else {
			sizeStr = fmt.Sprintf("%.0f KB", float64(fi.Size())/1024)
		}
	}
	ext := filepath.Ext(path)
	r.log(node.ID, models.LogLevelInfo, "Loaded %d rows, %d columns from %s (%s, %s)", len(ds.Rows), len(ds.Columns), filepath.Base(path), ext, sizeStr)
	r.log(node.ID, models.LogLevelInfo, "Columns: %s", strings.Join(ds.Columns, ", "))
	if len(ds.Rows) > 0 {
		// Log first row as sample
		sample := make([]string, 0, len(ds.Columns))
		for _, col := range ds.Columns {
			v := fmt.Sprintf("%v", ds.Rows[0][col])
			if len(v) > 30 {
				v = v[:27] + "..."
			}
			sample = append(sample, col+"="+v)
		}
		r.log(node.ID, models.LogLevelInfo, "Sample row: %s", strings.Join(sample, " | "))
	}
	throughput := float64(len(ds.Rows))
	if fi != nil {
		mbps := float64(fi.Size()) / 1024 / 1024
		r.log(node.ID, models.LogLevelInfo, "Throughput: %.0f rows loaded, %.1f MB read", throughput, mbps)
	}
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
	r.attachArtifactSink(fetcher)

	paginationCfg, hasPagination := node.Config["pagination"].(map[string]interface{})
	execCfg, _ := node.Config["execution"].(map[string]interface{})

	// max_concurrency is implemented for the "offset" and "numbered"
	// pagination strategies (fetchPaginated dispatches a bounded concurrent
	// batch of pages for those two) but not for "cursor"/"next_link"/
	// "link_header", which can only compute their next request from the
	// current response and so stay sequential regardless of this setting.
	// Warn rather than silently ignoring a setting that has no effect.
	if v, ok := execCfg["max_concurrency"]; ok {
		if !hasPagination {
			r.log(node.ID, models.LogLevelWarning, "source_api execution.max_concurrency=%v has no effect without a pagination config — ignoring", v)
		} else {
			switch strategy, _ := paginationCfg["strategy"].(string); strategy {
			case "cursor", "next_link", "link_header":
				r.log(node.ID, models.LogLevelWarning, "source_api execution.max_concurrency=%v is not applicable to pagination strategy %q — each page's request depends on the previous page's response, so pages run sequentially regardless of this setting", v, strategy)
			}
		}
	}

	// retry_scope="page" (the only value documented for pagination — RFC
	// §14.4) describes what fetchPaginated's page-level retry loop already
	// does by default, so it needs no behavior change here — just
	// acknowledgment rather than silent disregard. Anything else is
	// unrecognized for this node type; warn instead of pretending it did
	// something.
	if v, ok := execCfg["retry_scope"].(string); ok && v != "" && v != "page" {
		r.log(node.ID, models.LogLevelWarning, "source_api execution.retry_scope=%q is not a recognized value for source_api pagination (only \"page\" applies here) — ignoring", v)
	}

	ds, err := r.fetchSourceAPI(node, fetcher, source, hasPagination, execCfg)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", source, err)
	}
	r.log(node.ID, models.LogLevelInfo, "Fetched %d rows from %s", len(ds.Rows), source)
	return ds, nil
}

// fetchSourceAPI performs the actual fetch for a source_api node, adding
// mid-pagination checkpoint save/resume when the node sets
// execution().checkpoint_every and the fetcher implements
// fetchers.CheckpointingFetcher (issue #41 M2). Falls back to a plain
// fetcher.Fetch — identical to this codebase's behavior before M2 —
// whenever checkpointing isn't applicable: no checkpoint_every set, no
// pagination config, no checkpoint store configured, or a fetcher type that
// doesn't support it. That fallback keeps checkpointing strictly opt-in and
// zero-overhead for every pipeline that doesn't set checkpoint_every.
func (r *Runner) fetchSourceAPI(node models.Node, fetcher fetchers.Fetcher, source string, hasPagination bool, execCfg map[string]interface{}) (*common.DataSet, error) {
	checkpointEvery, hasCheckpointEvery := execConfigInt(execCfg, "checkpoint_every")
	cf, supportsCheckpointing := fetcher.(fetchers.CheckpointingFetcher)

	if !hasCheckpointEvery || checkpointEvery <= 0 || !hasPagination || !supportsCheckpointing || r.checkpointStore == nil {
		if hasCheckpointEvery && checkpointEvery > 0 {
			switch {
			case !hasPagination:
				r.log(node.ID, models.LogLevelWarning, "source_api execution.checkpoint_every=%v has no effect without a pagination config — ignoring", execCfg["checkpoint_every"])
			case !supportsCheckpointing:
				r.log(node.ID, models.LogLevelWarning, "source_api execution.checkpoint_every=%v is not supported by source_type %q — ignoring", execCfg["checkpoint_every"], node.Config["source_type"])
			case r.checkpointStore == nil:
				r.log(node.ID, models.LogLevelWarning, "source_api execution.checkpoint_every=%v set but no checkpoint store is configured — ignoring", execCfg["checkpoint_every"])
			}
		}
		return fetcher.Fetch(source, node.Config)
	}

	// Look for a checkpoint under this run first (a node-level retry within
	// the same run), falling back to the original run's checkpoint on a
	// resume — mirroring restoreSkippedNodeOutput's resumedFromRunID
	// lineage lookup for durable artifacts.
	var resume *fetchers.PaginationCheckpoint
	var resumeRecords []map[string]interface{}
	sourceRunID := r.run.ID

	if cp, ds, loadErr := r.checkpointStore.LoadCheckpoint(r.run.ID, node.ID); loadErr == nil {
		resume, resumeRecords = cp, dataSetToRecords(ds)
	} else if !errors.Is(loadErr, ErrCheckpointNotFound) {
		r.log(node.ID, models.LogLevelWarning, "Failed to load pagination checkpoint for node %s (starting fresh): %v", node.Name, loadErr)
	}
	if resume == nil && r.resumedFromRunID != "" {
		if cp, ds, loadErr := r.checkpointStore.LoadCheckpoint(r.resumedFromRunID, node.ID); loadErr == nil {
			resume, resumeRecords = cp, dataSetToRecords(ds)
			sourceRunID = r.resumedFromRunID
		} else if !errors.Is(loadErr, ErrCheckpointNotFound) {
			r.log(node.ID, models.LogLevelWarning, "Failed to load pagination checkpoint for node %s from original run %s (starting fresh): %v", node.Name, r.resumedFromRunID, loadErr)
		}
	}
	if resume != nil {
		r.log(node.ID, models.LogLevelInfo, "Resuming pagination for node %s from checkpoint: %d pages already fetched (run %s)", node.Name, resume.PagesFetched, sourceRunID)
	}

	// Saves target sourceRunID, not always r.run.ID: for a same-run retry
	// that's identical (sourceRunID == r.run.ID), but for a cross-run
	// resume it means checkpoint saves keep appending to the *original*
	// run's existing records file instead of starting a fresh one under
	// the new run's key — no need to copy resumeRecords forward first
	// (issue #52).
	onCheckpoint := func(cp fetchers.PaginationCheckpoint, newRecords []map[string]interface{}) error {
		if err := r.checkpointStore.SaveCheckpoint(sourceRunID, node.ID, cp, newRecords); err != nil {
			r.log(node.ID, models.LogLevelWarning, "Failed to persist pagination checkpoint for node %s (a retry will restart this fetch from the beginning instead of resuming): %v", node.Name, err)
			return err
		}
		return nil
	}

	ds, err := cf.FetchPaginatedResumable(source, node.Config, resume, resumeRecords, onCheckpoint)
	if err != nil {
		return nil, err
	}

	// The fetch succeeded — the checkpoint is now superseded by the node's
	// normal durable artifact (written by executeNode after this returns).
	// Clear it so a later run doesn't see stale interim state.
	if delErr := r.checkpointStore.DeleteCheckpoint(sourceRunID, node.ID); delErr != nil {
		r.log(node.ID, models.LogLevelWarning, "Failed to clear pagination checkpoint for node %s after successful fetch: %v", node.Name, delErr)
	}
	return ds, nil
}

// execConfigInt reads an integer-valued execution() policy field out of a
// node's execution config, which arrives from JSON as float64 (or
// json.Number, depending on decode path) rather than int.
func execConfigInt(execCfg map[string]interface{}, key string) (int, bool) {
	switch v := execCfg[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return int(f), err == nil
	}
	return 0, false
}

// dataSetToRecords converts a DataSet's rows back into the plain
// []map[string]interface{} shape PaginationCheckpoint's accumulated
// records are carried as. nil-safe: a nil ds (no records checkpointed yet)
// yields a nil slice, not a panic.
func dataSetToRecords(ds *common.DataSet) []map[string]interface{} {
	if ds == nil {
		return nil
	}
	records := make([]map[string]interface{}, len(ds.Rows))
	for i, row := range ds.Rows {
		records[i] = map[string]interface{}(row)
	}
	return records
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

	inRows := 0
	inCols := ""
	if input != nil {
		inRows = len(input.Rows)
		inCols = truncateList(input.Columns, 6)
	}
	outRows := 0
	outCols := ""
	if result != nil {
		outRows = len(result.Rows)
		outCols = truncateList(result.Columns, 6)
	}
	r.log(node.ID, models.LogLevelInfo, "Python: %d rows in [%s] → %d rows out [%s]", inRows, inCols, outRows, outCols)
	return result, nil
}

func truncateList(items []string, max int) string {
	if len(items) == 0 {
		return "(empty)"
	}
	result := ""
	for i, item := range items {
		if i >= max {
			result += fmt.Sprintf(", ...+%d more", len(items)-max)
			break
		}
		if i > 0 {
			result += ", "
		}
		result += item
	}
	return result
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

// runUnion concatenates all of a union node's upstream datasets into one.
// See UnionDatasets (engine/union.go) for the schema-mismatch and
// output-shape design decisions.
//
// Identity-union exception (#31): a single input is passed through
// unchanged instead of erroring. This is the compiled shape of
// `CollectionRef.collect(mode="union")` on a `.expand()` node — brokoli-sdk
// always emits exactly one static edge into the union node in that case
// (see engine/expansion.go's top-of-file doc comment for the full trace),
// and by the time execution reaches here that shape has already been
// vetted by validate.go's validateEdgeSemantics (which still requires >=2
// edges into a union node UNLESS the sole upstream is a dynamic-expansion
// node) — so a single input at runtime is a legitimate "identity union",
// not a validation gap slipping through.
func (r *Runner) runUnion(node models.Node, inputs []*common.DataSet) (*common.DataSet, error) {
	if len(inputs) == 1 {
		result := inputs[0]
		if result == nil {
			result = &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}}
		}
		r.log(node.ID, models.LogLevelInfo, "union: single input (identity union — see #31, e.g. .collect(mode=\"union\") over a single .expand() node) — passing through %d rows (%d columns) unchanged",
			len(result.Rows), len(result.Columns))
		return result, nil
	}

	result, err := UnionDatasets(inputs)
	if err != nil {
		return nil, fmt.Errorf("union node: %w", err)
	}
	r.log(node.ID, models.LogLevelInfo, "union: combined %d input dataset(s) into %d rows (%d columns)",
		len(inputs), len(result.Rows), len(result.Columns))
	return result, nil
}

// runDatasetMap applies a dataset_map node's referenced function to every
// row of the WHOLE upstream dataset. See ExecutePartitionTransform
// (engine/partition_transform.go) for the exact config shape, the current
// brokoli-sdk gap it works around, and why this is whole-dataset (not
// per-partition) execution.
func (r *Runner) runDatasetMap(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	return r.runPartitionTransform(node, input, "dataset_map")
}

// runDatasetFilter applies a dataset_filter node's referenced predicate to
// the WHOLE upstream dataset. See ExecutePartitionTransform
// (engine/partition_transform.go) for the exact config shape, the current
// brokoli-sdk gap it works around, and why this is whole-dataset (not
// per-partition) execution.
func (r *Runner) runDatasetFilter(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	return r.runPartitionTransform(node, input, "dataset_filter")
}

func (r *Runner) runPartitionTransform(node models.Node, input *common.DataSet, kind string) (*common.DataSet, error) {
	var runParams map[string]string
	if r.varCtx != nil {
		runParams = r.varCtx.Params
	}

	result, fnName, stderr, err := ExecutePartitionTransform(node.Config, input, runParams, kind)
	if stderr != "" {
		for _, line := range splitLines(stderr) {
			if line != "" {
				r.log(node.ID, models.LogLevelWarning, "python: %s", line)
			}
		}
	}
	if err != nil {
		return nil, err
	}

	inRows := 0
	if input != nil {
		inRows = len(input.Rows)
	}
	outRows := 0
	if result != nil {
		outRows = len(result.Rows)
	}
	r.log(node.ID, models.LogLevelInfo, "%s: applied function %q to whole dataset (no partitioning yet): %d rows in -> %d rows out",
		kind, fnName, inRows, outRows)
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

	// Apply each rule individually with logging
	for i, rule := range rules {
		beforeRows := len(clone.Rows)
		beforeCols := len(clone.Columns)
		if err := ApplyTransforms([]TransformRule{rule}, clone); err != nil {
			return nil, fmt.Errorf("rule %d (%s): %w", i+1, rule.Type, err)
		}
		afterRows := len(clone.Rows)
		afterCols := len(clone.Columns)

		detail := rule.Type
		switch rule.Type {
		case "filter_rows":
			dropped := beforeRows - afterRows
			r.log(node.ID, models.LogLevelInfo, "Rule %d/%d [filter_rows] condition=%q: %d → %d rows (%d dropped, %.1f%% kept)",
				i+1, len(rules), rule.Condition, beforeRows, afterRows, dropped,
				float64(afterRows)/float64(beforeRows)*100)
		case "drop_columns":
			r.log(node.ID, models.LogLevelInfo, "Rule %d/%d [drop_columns]: %d → %d columns (dropped: %s)",
				i+1, len(rules), beforeCols, afterCols, strings.Join(rule.Columns, ", "))
		case "add_column":
			r.log(node.ID, models.LogLevelInfo, "Rule %d/%d [add_column] %q = %s: %d → %d columns",
				i+1, len(rules), rule.Name, rule.Expression, beforeCols, afterCols)
		case "apply_function":
			r.log(node.ID, models.LogLevelInfo, "Rule %d/%d [apply_function] %s(%s) on %d rows",
				i+1, len(rules), rule.Function, rule.Column, afterRows)
		case "sort":
			dir := "ASC"
			if !rule.Ascending {
				dir = "DESC"
			}
			r.log(node.ID, models.LogLevelInfo, "Rule %d/%d [sort] by %s %s on %d rows",
				i+1, len(rules), strings.Join(rule.Columns, ", "), dir, afterRows)
		case "rename_columns":
			r.log(node.ID, models.LogLevelInfo, "Rule %d/%d [rename] %d columns renamed",
				i+1, len(rules), len(rule.Mapping))
		case "deduplicate":
			r.log(node.ID, models.LogLevelInfo, "Rule %d/%d [deduplicate] by %s: %d → %d rows (%d duplicates removed)",
				i+1, len(rules), strings.Join(rule.Columns, ", "), beforeRows, afterRows, beforeRows-afterRows)
		case "aggregate":
			r.log(node.ID, models.LogLevelInfo, "Rule %d/%d [aggregate] group by %s: %d → %d groups",
				i+1, len(rules), strings.Join(rule.GroupBy, ", "), beforeRows, afterRows)
		default:
			r.log(node.ID, models.LogLevelInfo, "Rule %d/%d [%s]: %d rows, %d columns",
				i+1, len(rules), detail, afterRows, afterCols)
		}
	}

	r.log(node.ID, models.LogLevelInfo, "Transform summary: %d rules applied, %d → %d rows, %d → %d columns",
		len(rules), len(input.Rows), len(clone.Rows), len(input.Columns), len(clone.Columns))
	return clone, nil
}

func (r *Runner) runQualityCheck(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	if input == nil {
		return nil, fmt.Errorf("quality_check node requires input data")
	}

	// Parse checks from node config
	checksRaw, _ := node.Config["rules"]
	if checksRaw == nil {
		checksRaw, _ = node.Config["checks"]
	}
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

	// Log input data context first — helps debugging when checks fail
	r.log(node.ID, models.LogLevelInfo, "Input: %d rows, %d columns: [%s]",
		len(input.Rows), len(input.Columns), truncateList(input.Columns, 10))
	if len(input.Rows) > 0 && len(input.Rows) <= 3 {
		// Show full rows if very small dataset (likely a parsing issue)
		for i, row := range input.Rows {
			keys := make([]string, 0, len(row))
			for k := range row {
				keys = append(keys, k)
			}
			r.log(node.ID, models.LogLevelInfo, "  Row %d keys: [%s]", i+1, truncateList(keys, 8))
		}
	} else if len(input.Rows) > 0 {
		// Show sample of first row
		row := input.Rows[0]
		sample := ""
		count := 0
		for k, v := range row {
			if count >= 4 {
				sample += ", ..."
				break
			}
			if count > 0 {
				sample += ", "
			}
			vs := fmt.Sprintf("%v", v)
			if len(vs) > 40 {
				vs = vs[:40] + "..."
			}
			sample += fmt.Sprintf("%s=%s", k, vs)
			count++
		}
		r.log(node.ID, models.LogLevelInfo, "  Sample row: {%s}", sample)
	}

	checker := quality.NewChecker()
	result, err := checker.Run(checks, input)
	if err != nil {
		return nil, err
	}

	// Log each check result with actionable details
	passCount := 0
	failCount := 0
	for i, cr := range result.Results {
		check := cr.Check
		if cr.Passed {
			passCount++
			r.log(node.ID, models.LogLevelInfo, "Check %d/%d PASS: %s(%s)",
				i+1, len(result.Results), check.Rule, check.Column)
		} else {
			failCount++
			r.log(node.ID, models.LogLevelWarning, "Check %d/%d FAIL: %s(%s) [%s] — %s",
				i+1, len(result.Results), check.Rule, check.Column, check.OnFailure, cr.Message)
			// Show which column is missing if not_null fails
			if check.Rule == "not_null" || check.Rule == "unique" {
				found := false
				for _, col := range input.Columns {
					if col == check.Column {
						found = true
						break
					}
				}
				if !found {
					r.log(node.ID, models.LogLevelWarning, "  Column %q not found in data. Available columns: [%s]",
						check.Column, truncateList(input.Columns, 15))
				}
			}
			if check.Rule == "row_count" {
				r.log(node.ID, models.LogLevelWarning, "  Actual row count: %d", len(input.Rows))
			}
		}
	}

	r.log(node.ID, models.LogLevelInfo, "Quality summary: %d passed, %d failed out of %d checks on %d rows",
		passCount, failCount, len(result.Results), len(input.Rows))

	if result.ShouldBlock() {
		errMsg := fmt.Sprintf("quality check failed: %d/%d checks failed on %d rows", failCount, len(result.Results), len(input.Rows))
		for _, cr := range result.Results {
			if !cr.Passed {
				errMsg += fmt.Sprintf("\n  FAIL: %s(%s) — %s", cr.Check.Rule, cr.Check.Column, cr.Message)
			}
		}
		return nil, fmt.Errorf("%s", errMsg)
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

	numBatches := (len(input.Rows) + batchSize - 1) / batchSize
	sqlMB := float64(len(sql)) / 1024 / 1024
	r.log(node.ID, models.LogLevelInfo, "Generated %s SQL for table %q", cfg.Dialect, cfg.Table)
	r.log(node.ID, models.LogLevelInfo, "  Rows: %d, Batches: %d (batch size: %d)", len(input.Rows), numBatches, batchSize)
	r.log(node.ID, models.LogLevelInfo, "  Columns: %d, CREATE TABLE: %v", len(input.Columns), createTable)
	if sqlMB >= 1 {
		r.log(node.ID, models.LogLevelInfo, "  Output: %.1f MB SQL (%d bytes)", sqlMB, len(sql))
	} else {
		r.log(node.ID, models.LogLevelInfo, "  Output: %.0f KB SQL (%d bytes)", float64(len(sql))/1024, len(sql))
	}

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

	if err := validateFilePath(path); err != nil {
		return nil, fmt.Errorf("sink_file: %w", err)
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

	mb := float64(len(content)) / 1024 / 1024
	if mb >= 1 {
		r.log(node.ID, models.LogLevelInfo, "Wrote %s to %s (%.1f MB, %d rows)", format, filepath.Base(path), mb, len(input.Rows))
	} else {
		r.log(node.ID, models.LogLevelInfo, "Wrote %s to %s (%.0f KB, %d rows)", format, filepath.Base(path), float64(len(content))/1024, len(input.Rows))
	}
	r.log(node.ID, models.LogLevelInfo, "  Full path: %s", path)
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
				switch val := v.(type) {
				case float64:
					// Remove floating point noise
					if val == float64(int64(val)) {
						record[i] = fmt.Sprintf("%d", int64(val))
					} else {
						record[i] = strconv.FormatFloat(val, 'f', -1, 64)
					}
				default:
					record[i] = fmt.Sprintf("%v", v)
				}
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

	// Path 1 (kept for back-compat): an upstream sql_generate node already
	// produced ready-to-run SQL in a single sql_output row.
	if len(input.Rows) == 1 {
		if s, ok := input.Rows[0]["sql_output"].(string); ok && s != "" {
			return r.execSinkSQL(node, uri, s)
		}
	}

	// Path 2: generate INSERT/UPSERT/overwrite SQL from table + mode + the
	// input rows, so sink_db writes a dataset on its own — no sql_generate
	// node required (brokoli-sdk#12).
	table, _ := node.Config["table"].(string)
	if table == "" {
		return nil, fmt.Errorf("sink_db requires a 'table' (or input from a sql_generate node providing sql_output)")
	}
	if len(input.Rows) == 0 {
		r.log(node.ID, models.LogLevelInfo, "sink_db: no rows to write to %q", table)
		return nil, nil
	}
	mode, _ := node.Config["mode"].(string)
	cfg := SQLGenConfig{
		Dialect:     dialectForURI(uri),
		Table:       table,
		Mode:        mode,
		KeyColumns:  configStringSlice(node.Config["key_columns"]),
		CreateTable: configBool(node.Config["create_table"]),
	}
	sql, err := GenerateSQL(cfg, input)
	if err != nil {
		return nil, fmt.Errorf("sink_db: %w", err)
	}
	return r.execSinkSQL(node, uri, sql)
}

func (r *Runner) execSinkSQL(node models.Node, uri, sql string) (*common.DataSet, error) {
	affected, err := ExecuteSQL(uri, sql)
	if err != nil {
		return nil, fmt.Errorf("execute SQL: %w", err)
	}
	r.log(node.ID, models.LogLevelInfo, "Executed SQL against database: %d rows affected", affected)
	return nil, nil
}

// configStringSlice reads a config value that may be []string or the
// []interface{} JSON decoding produces, into []string.
func configStringSlice(v interface{}) []string {
	switch xs := v.(type) {
	case []string:
		return xs
	case []interface{}:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func configBool(v interface{}) bool {
	b, _ := v.(bool)
	return b
}

// ── Sink API ────────────────────────────────────────────────

func (r *Runner) runSinkAPI(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	if input == nil {
		return nil, fmt.Errorf("sink_api node requires input data")
	}

	url, _ := node.Config["url"].(string)
	if url == "" {
		return nil, fmt.Errorf("sink_api node requires 'url' config")
	}

	method, _ := node.Config["method"].(string)
	if method == "" {
		method = "POST"
	}

	batchSize := 100
	if bs, ok := node.Config["batch_size"].(float64); ok && bs > 0 {
		batchSize = int(bs)
	}

	// Build headers
	headers := make(map[string]string)
	headers["Content-Type"] = "application/json"
	if h, ok := node.Config["headers"].(map[string]interface{}); ok {
		for k, v := range h {
			if sv, ok := v.(string); ok {
				headers[k] = sv
			}
		}
	}

	// Send in batches
	client := &http.Client{Timeout: 30 * time.Second}
	totalSent := 0
	totalBatches := (len(input.Rows) + batchSize - 1) / batchSize

	for i := 0; i < len(input.Rows); i += batchSize {
		if r.ctx.Err() != nil {
			return nil, fmt.Errorf("cancelled")
		}

		end := i + batchSize
		if end > len(input.Rows) {
			end = len(input.Rows)
		}
		batch := input.Rows[i:end]
		batchNum := (i / batchSize) + 1

		payload, err := json.Marshal(batch)
		if err != nil {
			return nil, fmt.Errorf("marshal batch %d: %w", batchNum, err)
		}

		req, err := http.NewRequestWithContext(r.ctx, method, url, strings.NewReader(string(payload)))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		// Basic auth
		if user, _ := node.Config["auth_user"].(string); user != "" {
			pass, _ := node.Config["auth_password"].(string)
			req.SetBasicAuth(user, pass)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("batch %d/%d failed: %w", batchNum, totalBatches, err)
		}
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("batch %d/%d: HTTP %d", batchNum, totalBatches, resp.StatusCode)
		}

		totalSent += len(batch)
		r.log(node.ID, models.LogLevelInfo, "Batch %d/%d sent: %d rows (HTTP %d)", batchNum, totalBatches, len(batch), resp.StatusCode)
	}

	r.log(node.ID, models.LogLevelInfo, "API sink complete: %d rows sent in %d batches to %s", totalSent, totalBatches, url)
	return nil, nil
}

// ── DB-to-DB Migration ──────────────────────────────────────

func (r *Runner) runMigrate(node models.Node) (*common.DataSet, error) {
	// Resolve source connection
	sourceURI, _ := node.Config["source_uri"].(string)
	if sourceConnID, _ := node.Config["source_conn_id"].(string); sourceConnID != "" && r.connResolver != nil {
		resolved := r.connResolver.Resolve(map[string]interface{}{"conn_id": sourceConnID}, models.NodeTypeSourceDB)
		if u, ok := resolved["uri"].(string); ok {
			sourceURI = u
		}
	}

	// Resolve dest connection
	destURI, _ := node.Config["dest_uri"].(string)
	if destConnID, _ := node.Config["dest_conn_id"].(string); destConnID != "" && r.connResolver != nil {
		resolved := r.connResolver.Resolve(map[string]interface{}{"conn_id": destConnID}, models.NodeTypeSinkDB)
		if u, ok := resolved["uri"].(string); ok {
			destURI = u
		}
	}

	sourceQuery, _ := node.Config["source_query"].(string)
	destTable, _ := node.Config["dest_table"].(string)
	dialect, _ := node.Config["dialect"].(string)

	if sourceURI == "" || sourceQuery == "" {
		return nil, fmt.Errorf("migrate node requires source connection (source_conn_id or source_uri) and 'source_query'")
	}
	if destURI == "" || destTable == "" {
		return nil, fmt.Errorf("migrate node requires dest connection (dest_conn_id or dest_uri) and 'dest_table'")
	}
	if dialect == "" {
		dialect = "generic"
	}

	chunkSize := 5000
	if cs, ok := node.Config["chunk_size"].(float64); ok && cs > 0 {
		chunkSize = int(cs)
	}
	createTable, _ := node.Config["create_table"].(bool)

	r.log(node.ID, models.LogLevelInfo, "Migration: %s → %s.%s (chunk size: %d)", sourceURI[:min(40, len(sourceURI))], destTable, dialect, chunkSize)

	// Read all from source (for now — chunked read requires LIMIT/OFFSET rewriting)
	r.log(node.ID, models.LogLevelInfo, "Reading from source...")
	sourceDS, err := QueryDatabase(sourceURI, sourceQuery)
	if err != nil {
		return nil, fmt.Errorf("source query: %w", err)
	}
	r.log(node.ID, models.LogLevelInfo, "Source: %d rows, %d columns", len(sourceDS.Rows), len(sourceDS.Columns))

	// Process in chunks
	totalMigrated := 0
	totalChunks := (len(sourceDS.Rows) + chunkSize - 1) / chunkSize
	tableCreated := false

	for i := 0; i < len(sourceDS.Rows); i += chunkSize {
		if r.ctx.Err() != nil {
			return nil, fmt.Errorf("cancelled at chunk %d", (i/chunkSize)+1)
		}

		end := i + chunkSize
		if end > len(sourceDS.Rows) {
			end = len(sourceDS.Rows)
		}

		chunk := &common.DataSet{
			Columns: sourceDS.Columns,
			Rows:    sourceDS.Rows[i:end],
		}

		chunkNum := (i / chunkSize) + 1
		cfg := SQLGenConfig{
			Dialect:     dialect,
			Table:       destTable,
			BatchSize:   chunkSize,
			CreateTable: createTable && !tableCreated,
		}

		sql, err := GenerateSQL(cfg, chunk)
		if err != nil {
			return nil, fmt.Errorf("generate SQL chunk %d: %w", chunkNum, err)
		}

		affected, err := ExecuteSQL(destURI, sql)
		if err != nil {
			return nil, fmt.Errorf("execute chunk %d: %w", chunkNum, err)
		}

		tableCreated = true
		totalMigrated += int(affected)
		r.log(node.ID, models.LogLevelInfo, "Chunk %d/%d: %d rows written (%d total)", chunkNum, totalChunks, affected, totalMigrated)
	}

	r.log(node.ID, models.LogLevelInfo, "Migration complete: %d rows migrated to %s in %d chunks", totalMigrated, destTable, totalChunks)

	return &common.DataSet{
		Columns: []string{"migrated_rows", "table", "chunks"},
		Rows: []common.DataRow{{
			"migrated_rows": totalMigrated,
			"table":         destTable,
			"chunks":        totalChunks,
		}},
	}, nil
}

func (r *Runner) nodeByID(id string) *models.Node {
	for i := range r.pipe.Nodes {
		if r.pipe.Nodes[i].ID == id {
			return &r.pipe.Nodes[i]
		}
	}
	return nil
}

// runArtifactSink stores bytes in one run's namespace, so a fetcher can hold
// a response by reference without being handed the whole store or the
// ability to reach another run's data.
type runArtifactSink struct {
	store artifact.Store
	runID string
}

func (s *runArtifactSink) PutArtifact(ctx context.Context, r io.Reader, mediaType string) (*artifact.ArtifactRef, error) {
	return s.store.Put(ctx, s.runID, r, artifact.PutOptions{MediaType: mediaType})
}

// attachArtifactSink gives a fetcher somewhere to store response="artifact"
// bodies, when this runner has both a blob store and a run to scope them to.
//
// Silent when either is missing: a dry run has no run ID, and an
// ArtifactStore that is not a BlobStoreProvider has nowhere to put bytes.
// The fetcher then keeps its previous behaviour of inlining the body, which
// is the correct result when there is nowhere to store it — see
// RESTFetcher.artifactResult.
func (r *Runner) attachArtifactSink(fetcher fetchers.Fetcher) {
	aware, ok := fetcher.(fetchers.ArtifactAwareFetcher)
	if !ok {
		return
	}
	provider, ok := r.artifactStore.(BlobStoreProvider)
	if !ok || r.run == nil || r.run.ID == "" {
		return
	}
	blobs := provider.Blobs()
	if blobs == nil {
		return
	}
	aware.SetArtifactSink(&runArtifactSink{store: blobs, runID: r.run.ID})
}
