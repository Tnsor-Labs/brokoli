package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/loaders"
)

// A preview keeps the first rows of each step, so its sources stop reading
// once they have them. Reading a source whole to keep ten rows made a
// preview of a large pipeline as long as a run, and held the whole source
// in the memory of the server running the preview.
//
//   - source_file: a CSV is read until it has the rows; other formats,
//     whose columns are only known once the whole file is read, are read
//     whole as before.
//   - source_db: the query streams and is cancelled once it has the rows
//     (a native driver already stopped at them; BigQuery runs its query).
//   - source_api: a paginated source stops fetching pages once it has the
//     rows (its pagination's max_records); one request is one request.
//   - code: a script that emit()s rows stops once it has emitted them;
//     one that builds output_data is run as it is.

// previewRowsKey is the config key a code node's wrapper reads its preview
// row limit from.
const previewRowsKey = "_brokoli_preview_rows"

// errPreviewHasRows stops a source that has the rows a preview keeps.
var errPreviewHasRows = errors.New("the preview has its rows")

// previewNode is n as a preview runs it: the same node, told how many rows
// the preview keeps where its type can stop at them.
func previewNode(n models.Node, rows int) models.Node {
	if rows <= 0 {
		return n
	}
	switch n.Type {
	case models.NodeTypeSourceAPI:
		pag, ok := n.Config["pagination"].(map[string]interface{})
		if !ok {
			return n
		}
		limited := make(map[string]interface{}, len(pag)+1)
		for k, v := range pag {
			limited[k] = v
		}
		if current, ok := previewInt(pag["max_records"]); !ok || current <= 0 || current > rows {
			limited["max_records"] = rows
		}
		n.Config = withConfig(n.Config, "pagination", limited)
	case models.NodeTypeCode:
		n.Config = withConfig(n.Config, previewRowsKey, rows)
	}
	return n
}

// withConfig is a copy of config with key set, leaving config as it was:
// the node's config is the pipeline's, shared with whatever else holds it.
func withConfig(config map[string]interface{}, key string, value interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(config)+1)
	for k, v := range config {
		out[k] = v
	}
	out[key] = value
	return out
}

func previewInt(v interface{}) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	}
	return 0, false
}

// keepFirst collects batches until it has rows rows, then stops the reader.
type keepFirst struct {
	rows int
	ds   *common.DataSet
}

func (k *keepFirst) add(b *common.DataSet) error {
	if k.ds == nil {
		k.ds = &common.DataSet{Columns: b.Columns, Rows: make([]common.DataRow, 0, k.rows)}
	}
	for _, row := range b.Rows {
		if len(k.ds.Rows) >= k.rows {
			break
		}
		k.ds.Rows = append(k.ds.Rows, row)
	}
	if len(k.ds.Rows) >= k.rows {
		return errPreviewHasRows
	}
	return nil
}

func (k *keepFirst) result(columns []string) *common.DataSet {
	if k.ds == nil {
		k.ds = &common.DataSet{Rows: []common.DataRow{}}
	}
	if len(columns) > 0 {
		k.ds.Columns = columns
	}
	return k.ds
}

// previewSourceFile reads the first rows of a CSV source. ok is false for a
// format that cannot stop early; the caller reads it whole.
func (r *Runner) previewSourceFile(ctx context.Context, node models.Node) (ds *common.DataSet, ok bool, err error) {
	path, _ := node.Config["path"].(string)
	if path == "" || !loaders.SupportsStreaming(path) {
		return nil, false, nil
	}
	local, cleanup, remote, err := r.sourceFileLocal(ctx, node, path)
	if err != nil {
		return nil, true, err
	}
	defer cleanup()
	keep := &keepFirst{rows: r.dryRunMaxRows}
	columns, _, err := loaders.StreamBatches(ctx, local, r.dryRunMaxRows, keep.add)
	if err != nil && !errors.Is(err, errPreviewHasRows) {
		if remote {
			return nil, true, fmt.Errorf("load %s: %w", remoteFileAssetID(fileConnID(node), path), err)
		}
		return nil, true, describeMissingFile(path, fmt.Errorf("load %s: %w", path, err))
	}
	ds = keep.result(columns)
	r.log(node.ID, models.LogLevelInfo, "Preview: read the first %d rows of %s", len(ds.Rows), path)
	return ds, true, nil
}

// previewSourceDB runs a source's query until it has the first rows, then
// cancels it. ok is false where the query cannot stream (BigQuery, or a
// database read through a native driver, which stops at the rows itself);
// the caller runs it as a run does.
func (r *Runner) previewSourceDB(ctx context.Context, node models.Node, attempt int) (res nodeExecutionResult, ok bool, err error) {
	if native, nerr := r.nativeSourceRequest(ctx, node); nerr != nil || native != nil {
		return nodeExecutionResult{}, false, nil
	}
	uri, _ := node.Config["uri"].(string)
	query, _ := node.Config["query"].(string)
	if uri == "" || query == "" || isBigQueryURI(uri) {
		return nodeExecutionResult{}, false, nil
	}
	query, args, err := r.bindNodeSQL(uri, query)
	if err != nil {
		return nodeExecutionResult{}, true, fmt.Errorf("source_db query: %w", err)
	}
	r.recordExecutedSQL(node.ID, attempt, query)
	qctx, cancel := context.WithCancel(ctx)
	defer cancel()
	keep := &keepFirst{rows: r.dryRunMaxRows}
	columns, _, err := streamQueryDatabase(qctx, uri, query, args, r.dryRunMaxRows, func(b *common.DataSet) error {
		if e := keep.add(b); e != nil {
			cancel()
			return e
		}
		return nil
	})
	if err != nil && !errors.Is(err, errPreviewHasRows) && !(keep.ds != nil && len(keep.ds.Rows) >= r.dryRunMaxRows) {
		return nodeExecutionResult{}, true, fmt.Errorf("query database: %w", err)
	}
	ds := keep.result(columns)
	r.log(node.ID, models.LogLevelInfo, "Preview: read the first %d rows of the query", len(ds.Rows))
	return nodeExecutionResult{output: ds, outputSchema: r.sourceSchema(node, uri, query, args...)}, true, nil
}

// previewCode runs a Python code node the way a streamed run does, with
// file-based output, so a script that emit()s works in a preview (the
// in-memory path refuses emit()) and stops at the preview's rows; only the
// first rows of its output are read. ok is false for another language,
// which runs as before.
func (r *Runner) previewCode(ctx context.Context, node models.Node, input *common.DataSet) (res nodeExecutionResult, ok bool, err error) {
	configForScript := make(map[string]interface{}, len(node.Config))
	for k, v := range node.Config {
		if k != "script" && k != "task_bundle" {
			configForScript[k] = v
		}
	}
	if language, lerr := codeLanguage(configForScript); lerr != nil || language != "python" {
		return nodeExecutionResult{}, false, nil
	}
	script := ""
	var bundle *codeBundleSpec
	digest, isBundle, err := taskBundleReference(node.Config)
	if err != nil {
		return nodeExecutionResult{}, true, err
	}
	if isBundle {
		b, err := r.materializeTaskBundle(digest)
		if err != nil {
			return nodeExecutionResult{}, true, err
		}
		bundle = b
		defer os.RemoveAll(bundle.dir)
	} else if script, _ = node.Config["script"].(string); script == "" {
		return nodeExecutionResult{}, true, fmt.Errorf("code node requires 'script' in config")
	}
	timeoutSec := 30
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}
	var runParams map[string]string
	if r.varCtx != nil {
		runParams = r.varCtx.Params
	}

	inputPath := ""
	var inputCols []string
	if input != nil && len(input.Rows) > 0 {
		f, err := os.CreateTemp("", "brokoli_preview_in_*.ndjson")
		if err != nil {
			return nodeExecutionResult{}, true, err
		}
		inputPath = f.Name()
		f.Close()
		defer os.Remove(inputPath)
		if err := WriteNDJSON(inputPath, input); err != nil {
			return nodeExecutionResult{}, true, fmt.Errorf("stage preview input: %w", err)
		}
		inputCols = input.Columns
	}

	out, err := executeCodeNodeStreamed(ctx, script, bundle, inputPath, inputCols, configForScript, runParams, r.parameters, timeoutSec, nil)
	if out != nil && out.stderr != "" {
		for _, line := range splitLines(out.stderr) {
			if line != "" {
				r.log(node.ID, models.LogLevelWarning, "python: %s", line)
			}
		}
	}
	if err != nil {
		return nodeExecutionResult{}, true, err
	}
	schema, err := declaredOutputSchema(node.Config)
	if err != nil {
		return nodeExecutionResult{}, true, err
	}
	if out.outputPath == "" {
		var parsed CodeNodeOutput
		if err := json.Unmarshal(out.stdout, &parsed); err != nil {
			return nodeExecutionResult{}, true, fmt.Errorf("script output is not valid JSON: %w", err)
		}
		ds := &common.DataSet{Columns: parsed.Columns, Rows: make([]common.DataRow, 0, len(parsed.Rows))}
		for i, row := range parsed.Rows {
			if i >= r.dryRunMaxRows {
				break
			}
			ds.Rows = append(ds.Rows, common.DataRow(row))
		}
		return nodeExecutionResult{output: ds, outputSchema: schema}, true, nil
	}
	defer os.Remove(out.outputPath)
	ds, err := readNDJSONHead(out.outputPath, r.dryRunMaxRows)
	if err != nil {
		return nodeExecutionResult{}, true, fmt.Errorf("read script output: %w", err)
	}
	if len(out.columns) > 0 {
		ds.Columns = out.columns
	}
	return nodeExecutionResult{output: ds, outputSchema: schema}, true, nil
}

// readNDJSONHead reads the first n rows of an NDJSON file.
func readNDJSONHead(path string, n int) (*common.DataSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	ds := &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}}
	for len(ds.Rows) < n && dec.More() {
		var row common.DataRow
		if err := decodeRow(dec, &row); err != nil {
			return nil, fmt.Errorf("row %d: %w", len(ds.Rows)+1, err)
		}
		ds.Rows = append(ds.Rows, row)
	}
	if len(ds.Rows) > 0 {
		for k := range ds.Rows[0] {
			ds.Columns = append(ds.Columns, k)
		}
	}
	return ds, nil
}
