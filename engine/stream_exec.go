package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// This file is docs/adr/019-execution-segments-and-streaming.md Milestone
// 1: reference-passing dataflow. Nothing here changes what a node
// computes — only what form its data takes on the way through. A
// stream-capable node whose input is already a blob reference (spilled,
// or itself produced by one of these paths) processes it in bounded
// memory and hands its output back as a reference; everything else takes
// exactly the batch path it always took. The engagement rule is the spill
// threshold: data small enough to live in memory cheaply still does,
// because batch is faster and its memory doesn't matter.

// DefaultStreamThresholdBytes is the encoded size at or above which
// reference-passing engages when no explicit threshold is configured.
// Sized from the live measurement that exposed the need for a knob
// separate from spilling: the benchmark's 100k-row datasets encode to
// ~15-20 MiB of NDJSON but cost ~95 MiB each as in-memory map rows (a
// 5-6x multiplier), so an 8 MiB encoded floor means streaming starts
// where roughly 40-50 MiB of real memory per dataset copy is at stake —
// well below the spill threshold's deliberately high 64 MiB, which was
// sized for a mechanism that only parks memory already paid for.
const DefaultStreamThresholdBytes int64 = 8 << 20 // 8 MiB

// rowLocalTransformRules reports whether every rule in the list is
// row-local — computable on one row without seeing any other — which is
// the streamability condition for a transform node. The blocking rules
// (sort, dedup, aggregate) need the whole dataset and keep the batch
// path; per ADR-019 they are barriers, not failures.
func rowLocalTransformRules(rules []TransformRule) bool {
	for _, rule := range rules {
		switch rule.Type {
		case "rename_columns", "rename",
			"add_column",
			"filter_rows", "filter",
			"apply_function", "function",
			"replace_values", "replace",
			"drop_columns", "drop":
			// row-local
		default:
			return false
		}
	}
	return true
}

// streamTransformToRef applies row-local rules to a referenced input,
// batch by batch, writing the result straight into the blob store —
// neither the input nor the output dataset ever materializes. The
// returned ref's RowCount and Columns reflect the transformed output.
//
// The per-rule logging runTransform does (rows dropped by filters, etc.)
// is summarized rather than replicated per batch — one line for the
// whole streamed pass, so a 100-batch input doesn't produce 100x the log
// volume of its batch equivalent.
func streamTransformToRef(outputs *nodeOutputs, inputRef *artifact.DatasetRef, plan transformStreamPlan) (*artifact.DatasetRef, error) {
	var aggState *streamAggState
	if plan.agg != nil {
		var err error
		if aggState, err = newStreamAggState(*plan.agg); err != nil {
			return nil, err
		}
	}
	batches, closer, err := outputs.OpenBatches(inputRef)
	if err != nil {
		return nil, fmt.Errorf("open streamed input: %w", err)
	}
	defer closer.Close()

	pr, pw := io.Pipe()
	type putResult struct {
		ref *artifact.ArtifactRef
		err error
	}
	putDone := make(chan putResult, 1)
	go func() {
		ref, err := outputs.blobs.Put(context.Background(), outputs.namespace, pr, artifact.PutOptions{
			MediaType: artifact.MediaTypeNDJSON,
		})
		if err != nil {
			// Unblock the encoder side if Put failed mid-stream.
			_ = pr.CloseWithError(err)
		}
		putDone <- putResult{ref, err}
	}()

	enc := json.NewEncoder(pw)
	enc.SetEscapeHTML(false) // match EncodeArrowJSON byte-for-byte
	var outCols []string
	rowCount := int64(0)
	wroteAny := false
	var streamErr error
	for {
		batch, err := batches.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			streamErr = fmt.Errorf("read streamed input: %w", err)
			break
		}
		if err := ApplyTransforms(plan.prefix, batch); err != nil {
			streamErr = err
			break
		}
		if aggState != nil {
			// Milestone 2 streaming aggregation: the batch folds into
			// small per-group state instead of flowing through; the
			// grouped result is emitted once, after EOF below.
			aggState.fold(batch)
			continue
		}
		if outCols == nil && len(batch.Columns) > 0 {
			outCols = batch.Columns
		}
		for _, row := range batch.Rows {
			if err := enc.Encode(row); err != nil {
				streamErr = fmt.Errorf("encode streamed output: %w", err)
				break
			}
			rowCount++
			wroteAny = true
		}
		if streamErr != nil {
			break
		}
	}
	if streamErr == nil && aggState != nil {
		final := aggState.finalize()
		// Suffix rules — anything, including sort — run on the grouped
		// output, which is small by construction (one row per group).
		if err := ApplyTransforms(plan.suffix, final); err != nil {
			streamErr = err
		} else {
			outCols = final.Columns
			for _, row := range final.Rows {
				if err := enc.Encode(row); err != nil {
					streamErr = fmt.Errorf("encode aggregated output: %w", err)
					break
				}
				rowCount++
				wroteAny = true
			}
		}
	}
	if streamErr == nil && !wroteAny {
		// Preserve EncodeArrowJSON's empty-dataset sentinel so the blob
		// decodes identically to a batch-written empty output.
		if _, err := pw.Write([]byte("[]")); err != nil {
			streamErr = err
		}
	}
	_ = pw.CloseWithError(streamErr)
	put := <-putDone
	// Error priority: a genuine producer error (transform failure, bad
	// input) is the root cause even though it also surfaces as a Put
	// error via the closed pipe. But a producer error that IS a pipe
	// error means the consumer died first — Put's own error is the root
	// cause there, and reporting "closed pipe" would bury it.
	if streamErr != nil && !errors.Is(streamErr, io.ErrClosedPipe) {
		return nil, streamErr
	}
	if put.err != nil {
		return nil, fmt.Errorf("store streamed output: %w", put.err)
	}
	if streamErr != nil {
		return nil, streamErr
	}
	if outCols == nil {
		outCols = inputRef.Columns
	}
	if outCols == nil {
		outCols = []string{}
	}
	return &artifact.DatasetRef{
		ArtifactRef: *put.ref,
		Format:      artifact.FormatNDJSON,
		Columns:     outCols,
		RowCount:    rowCount,
	}, nil
}

// codeStreamResult is what executeCodeNodeStreamed hands back: the output
// as a temp NDJSON file plus the column order the Python wrapper declared
// via its sidecar. The caller decides the output's final form (blob ref
// vs. materialized DataSet) from the file's actual size — the honest
// signal, available only after the script ran.
type codeStreamResult struct {
	outputPath string   // empty when the script produced no NDJSON file (stdout fallback below)
	columns    []string // from the sidecar; nil if the sidecar wasn't written
	stdout     []byte   // for the JSON fallback when outputPath is empty
	stderr     string
}

// executeCodeNodeStreamed runs a code node's script with file-based
// NDJSON I/O on both sides, without ever holding the dataset in Go:
// the input, when present, is an existing NDJSON file (stream-copied from
// a blob by the caller); the output is left as the temp file this returns.
//
// Deliberately a separate, narrower function from ExecuteCodeNode rather
// than a refactor of it: that function's CSV and stdin/stdout fallback
// lattice is battle-tested and this path never needs it — a streamed
// invocation is by definition the large-NDJSON case. The subprocess
// mechanics (env, timeout, progress-line filtering) mirror it exactly.
func executeCodeNodeStreamed(script string, inputNDJSONPath string, inputColumns []string, nodeConfig map[string]interface{}, runParams map[string]string, timeoutSec int) (*codeStreamResult, error) {
	if script == "" {
		return nil, fmt.Errorf("code node requires a 'script' in config")
	}
	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	fullScript := fmt.Sprintf(pythonWrapper, script)
	tmpDir := os.TempDir()
	nano := time.Now().UnixNano()
	scriptFile := filepath.Join(tmpDir, fmt.Sprintf("brokoli_code_%d.py", nano))
	if err := os.WriteFile(scriptFile, []byte(fullScript), 0o600); err != nil {
		return nil, fmt.Errorf("write script file: %w", err)
	}
	defer os.Remove(scriptFile)

	outputNDJSON := filepath.Join(tmpDir, fmt.Sprintf("brokoli_out_%d.ndjson", nano))
	outputCols := filepath.Join(tmpDir, fmt.Sprintf("brokoli_cols_%d.json", nano))
	// The output file is the caller's to consume and remove; only the
	// sidecar is cleaned up here.
	defer os.Remove(outputCols)

	configJSON, _ := json.Marshal(nodeConfig)
	paramsJSON, _ := json.Marshal(runParams)
	pythonPath := "python3"
	if pp, ok := nodeConfig["python_path"].(string); ok && pp != "" {
		pythonPath = pp
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, pythonPath, scriptFile) // #nosec G204 -- identical to ExecuteCodeNode's baseline-accepted launch: a code node exists to run pipeline-author code, and an author-set python_path grants nothing the script itself doesn't already have.
	cmd.Env = append(os.Environ(),
		"BROKED_CONFIG="+string(configJSON),
		"BROKED_PARAMS="+string(paramsJSON),
		"BROKED_OUTPUT_NDJSON="+outputNDJSON,
		"BROKED_OUTPUT_COLUMNS="+outputCols,
	)
	if inputNDJSONPath != "" {
		cmd.Env = append(cmd.Env, "BROKED_INPUT_NDJSON="+inputNDJSONPath)
		if len(inputColumns) > 0 {
			// Milestone 1.5: hand the wrapper the column order from the
			// input ref so its lazy rows never need a first-row peek —
			// and column ORDER survives, which map keys never preserved.
			if colsJSON, err := json.Marshal(inputColumns); err == nil {
				cmd.Env = append(cmd.Env, "BROKED_INPUT_COLUMNS="+string(colsJSON))
			}
		}
	}
	cmd.Stdin = bytes.NewReader([]byte(""))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	stderrStr := stderr.String()
	var cleanStderr []string
	for _, line := range strings.Split(stderrStr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#PROGRESS:") {
			continue
		}
		cleanStderr = append(cleanStderr, line)
	}
	stderrStr = strings.Join(cleanStderr, "\n")

	if ctx.Err() == context.DeadlineExceeded {
		_ = os.Remove(outputNDJSON)
		return nil, fmt.Errorf("script timed out after %ds", timeoutSec)
	}
	if err != nil {
		_ = os.Remove(outputNDJSON)
		return nil, fmt.Errorf("script failed: %w\nstderr: %s", err, stderrStr)
	}

	res := &codeStreamResult{stderr: stderrStr, stdout: stdout.Bytes()}
	if _, statErr := os.Stat(outputNDJSON); statErr == nil {
		res.outputPath = outputNDJSON
		if colsData, readErr := os.ReadFile(outputCols); readErr == nil { // #nosec G304 -- both paths are os.TempDir joined with a process-generated nanosecond name, never caller input.
			var cols []string
			if json.Unmarshal(colsData, &cols) == nil {
				res.columns = cols
			}
		}
	}
	// No output file: the wrapper's empty-rows (or non-NDJSON) case fell
	// through to printing JSON on stdout — the caller parses that exactly
	// like ExecuteCodeNode's own JSON fallback.
	return res, nil
}

// stageRefToNDJSONFile stream-copies a blob-referenced dataset into a temp
// NDJSON file for a code node's Python subprocess — disk to disk through
// an I/O buffer, never a Go DataSet. Caller removes the returned file.
func stageRefToNDJSONFile(outputs *nodeOutputs, ref *artifact.DatasetRef) (string, error) {
	rc, err := outputs.blobs.Open(context.Background(), &ref.ArtifactRef)
	if err != nil {
		return "", fmt.Errorf("open input blob: %w", err)
	}
	defer rc.Close()
	path := filepath.Join(os.TempDir(), fmt.Sprintf("brokoli_in_%d.ndjson", time.Now().UnixNano()))
	f, err := os.Create(path) // #nosec G304 -- os.TempDir joined with a process-generated nanosecond name.
	if err != nil {
		return "", fmt.Errorf("create staged input: %w", err)
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("stage input: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("finalize staged input: %w", err)
	}
	return path, nil
}

// storeNDJSONFileAsRef moves a finished NDJSON output file into the blob
// store as a dataset reference, counting rows on the way through — again
// disk to disk, no DataSet. columns, when non-nil, is the authoritative
// order (the wrapper's sidecar); otherwise it is recovered from the first
// row later, at first decode, exactly like ReadArrowJSON would have.
func storeNDJSONFileAsRef(outputs *nodeOutputs, path string, columns []string) (*artifact.DatasetRef, error) {
	f, err := os.Open(path) // #nosec G304 -- temp path produced by executeCodeNodeStreamed, never caller input.
	if err != nil {
		return nil, fmt.Errorf("open output file: %w", err)
	}
	defer f.Close()

	counter := &ndjsonRowCounter{}
	tee := io.TeeReader(f, counter)
	ref, err := outputs.blobs.Put(context.Background(), outputs.namespace, tee, artifact.PutOptions{
		MediaType: artifact.MediaTypeNDJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("store output: %w", err)
	}
	if columns == nil {
		columns = []string{}
	}
	return &artifact.DatasetRef{
		ArtifactRef: *ref,
		Format:      artifact.FormatNDJSON,
		Columns:     columns,
		RowCount:    counter.finalCount(),
	}, nil
}

// ndjsonRowCounter counts newline-terminated rows flowing through a
// TeeReader — one line per row is EncodeArrowJSON's (and the Python
// wrapper's) invariant. A final line without a trailing newline is still
// a row.
type ndjsonRowCounter struct {
	rows        int64
	sawAnyByte  bool
	lastNewline bool
}

func (c *ndjsonRowCounter) Write(p []byte) (int, error) {
	for _, ch := range p {
		c.sawAnyByte = true
		if ch == '\n' {
			c.rows++
			c.lastNewline = true
		} else {
			c.lastNewline = false
		}
	}
	return len(p), nil
}

func (c *ndjsonRowCounter) finalCount() int64 {
	if c.sawAnyByte && !c.lastNewline {
		return c.rows + 1
	}
	return c.rows
}

// previewFromRef materializes only the first previewRows rows of a
// referenced output — what SaveNodePreview actually keeps — instead of
// the whole dataset.
func previewFromRef(outputs *nodeOutputs, ref *artifact.DatasetRef, previewRows int) (*common.DataSet, error) {
	batches, closer, err := outputs.OpenBatches(ref)
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	out := &common.DataSet{Columns: ref.Columns}
	for len(out.Rows) < previewRows {
		batch, err := batches.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(out.Columns) == 0 {
			out.Columns = batch.Columns
		}
		need := previewRows - len(out.Rows)
		if need > len(batch.Rows) {
			need = len(batch.Rows)
		}
		out.Rows = append(out.Rows, batch.Rows[:need]...)
	}
	if out.Columns == nil {
		out.Columns = []string{}
	}
	return out, nil
}

// streamEligible reports whether node's TYPE can take the reference-
// passing path at all — the static half of the decision executeNode makes
// (the dynamic half is whether the input actually arrives as a ref).
// Deliberately conservative: dry runs never stream (they truncate and
// preview in-memory), disabled spilling means there is no blob store to
// reference, expansion code nodes resolve upstream data by node ID, and
// every type not listed here simply keeps its batch path.
func (r *Runner) streamEligible(node models.Node, outputs *nodeOutputs) bool {
	if r.dryRun || !outputs.spillEnabled() || outputs.streamThreshold <= 0 {
		return false
	}
	switch node.Type {
	case models.NodeTypeCode:
		return !nodeHasExpansion(node)
	case models.NodeTypeTransform:
		rules, err := parseNodeTransformRules(node)
		if err != nil {
			return false
		}
		_, ok := planTransformRules(rules)
		return ok
	}
	return false
}

// runNodeStreamed is the ADR-019 Milestone 1 counterpart of runNodeLogic
// for the two stream-capable node types. executeNode only dispatches here
// after streamEligible plus the input-form checks passed, so the
// preconditions (transform: inputRef non-nil; code: inputRef or no input)
// hold by construction.
func (r *Runner) runNodeStreamed(node models.Node, inputRef *artifact.DatasetRef, outputs *nodeOutputs) (nodeExecutionResult, error) {
	switch node.Type {
	case models.NodeTypeTransform:
		rules, err := parseNodeTransformRules(node)
		if err != nil {
			return nodeExecutionResult{}, err
		}
		plan, ok := planTransformRules(rules)
		if !ok {
			return nodeExecutionResult{}, fmt.Errorf("transform rules are not streamable (dispatch bug: eligibility should have caught this)")
		}
		inRows := int64(0)
		if inputRef != nil {
			inRows = inputRef.RowCount
		}
		ref, err := streamTransformToRef(outputs, inputRef, plan)
		if err != nil {
			return nodeExecutionResult{}, err
		}
		r.log(node.ID, models.LogLevelInfo,
			"Streamed %d rule(s) over %d row(s) by reference: %d row(s) out (never materialized)",
			len(rules), inRows, ref.RowCount)
		return nodeExecutionResult{outputRef: ref}, nil
	case models.NodeTypeCode:
		return r.runCodeStreamed(node, inputRef, outputs)
	}
	return nodeExecutionResult{}, fmt.Errorf("node type %q is not stream-capable", node.Type)
}

// runCodeStreamed mirrors runCode's config handling and stderr logging
// exactly, around file-based I/O on both sides of the subprocess.
func (r *Runner) runCodeStreamed(node models.Node, inputRef *artifact.DatasetRef, outputs *nodeOutputs) (nodeExecutionResult, error) {
	script, _ := node.Config["script"].(string)
	if script == "" {
		return nodeExecutionResult{}, fmt.Errorf("code node requires 'script' in config")
	}
	timeoutSec := 30
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}
	configForScript := make(map[string]interface{})
	for k, v := range node.Config {
		if k != "script" {
			configForScript[k] = v
		}
	}
	var runParams map[string]string
	if r.varCtx != nil {
		runParams = r.varCtx.Params
	}

	stagedInput := ""
	if inputRef != nil {
		var err error
		stagedInput, err = stageRefToNDJSONFile(outputs, inputRef)
		if err != nil {
			return nodeExecutionResult{}, err
		}
		defer os.Remove(stagedInput)
		r.log(node.ID, models.LogLevelInfo, "Staged %d row(s) to the script by reference (never materialized in the engine)", inputRef.RowCount)
	}

	var inputCols []string
	if inputRef != nil {
		inputCols = inputRef.Columns
	}
	res, err := executeCodeNodeStreamed(script, stagedInput, inputCols, configForScript, runParams, timeoutSec)
	if res != nil && res.stderr != "" {
		for _, line := range splitLines(res.stderr) {
			if line != "" {
				r.log(node.ID, models.LogLevelWarning, "python: %s", line)
			}
		}
	}
	if err != nil {
		return nodeExecutionResult{}, err
	}

	if res.outputPath != "" {
		defer os.Remove(res.outputPath)
		info, statErr := os.Stat(res.outputPath)
		if statErr == nil && info.Size() >= outputs.streamThreshold {
			ref, err := storeNDJSONFileAsRef(outputs, res.outputPath, res.columns)
			if err != nil {
				return nodeExecutionResult{}, err
			}
			return nodeExecutionResult{outputRef: ref}, nil
		}
		// Small output: materialize, exactly as the batch path would have.
		ds, err := ReadArrowJSON(res.outputPath)
		if err != nil {
			return nodeExecutionResult{}, fmt.Errorf("read script output: %w", err)
		}
		if len(res.columns) > 0 {
			ds.Columns = res.columns
		}
		return nodeExecutionResult{output: ds}, nil
	}

	// No NDJSON file: the wrapper printed JSON on stdout (empty result, or
	// a script that bypassed output_data) — same fallback ExecuteCodeNode
	// implements.
	var out CodeNodeOutput
	if err := json.Unmarshal(res.stdout, &out); err != nil {
		return nodeExecutionResult{}, fmt.Errorf("script output is not valid JSON: %w", err)
	}
	ds := &common.DataSet{Columns: out.Columns, Rows: make([]common.DataRow, len(out.Rows))}
	for i, row := range out.Rows {
		ds.Rows[i] = common.DataRow(row)
	}
	return nodeExecutionResult{output: ds}, nil
}
