package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// The pool-backed launchers (ADR-029). Same inputs, same outputs, same
// error text families as the legacy spawn path — different lifecycle:
// a warm worker from pkg/codeexec's pool runs the exec, so interpreter
// boot and library imports are paid once per worker, not per node run.

// scriptErrorToEngineError maps a pool execution failure onto the same
// error shapes the legacy path produces, so callers and tests see one
// contract regardless of lifecycle.
func scriptErrorToEngineError(err error, language string, limits codeexec.Limits, stderr string) error {
	var scriptErr *codeexec.ErrScript
	if errors.As(err, &scriptErr) {
		switch scriptErr.Kind {
		case codeexec.ErrKindResourceLimit:
			if limits.MemoryMB > 0 {
				mechanism := "address space"
				if language == "typescript" {
					mechanism = "V8 old-space heap"
				}
				return fmt.Errorf(
					"script exceeded the memory limit (%d MiB, enforced as %s): "+
						"raise max_memory_mb on the node or the server default, or stream with emit() "+
						"instead of materializing the dataset\nstderr: %s",
					limits.MemoryMB, mechanism, stderr)
			}
			return fmt.Errorf("script exceeded a resource limit: %s\nstderr: %s", scriptErr.Message, stderr)
		default:
			detail := scriptErr.Message
			if scriptErr.Traceback != "" {
				detail = scriptErr.Traceback
			}
			return fmt.Errorf("script failed: %s\nstderr: %s", scriptErr.Message, strings.TrimSpace(detail+"\n"+stderr))
		}
	}
	if strings.Contains(err.Error(), "timed out") || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// A worker that died mid-execution with ceilings configured was most
	// often killed BY one of them: RLIMIT_CPU's hard limit is SIGKILL,
	// and a C-level allocator giving up under RLIMIT_AS exits without a
	// Python traceback. The unframed detail died with the process, so
	// name the plausible ceilings rather than reporting a bare EOF.
	var workerDied *codeexec.ErrWorkerDied
	if errors.As(err, &workerDied) {
		var hints []string
		if limits.CPUSeconds > 0 {
			hints = append(hints, fmt.Sprintf("the CPU limit (%d s)", limits.CPUSeconds))
		}
		if limits.MemoryMB > 0 {
			hints = append(hints, fmt.Sprintf("the memory limit (%d MiB)", limits.MemoryMB))
		}
		if len(hints) > 0 {
			detail := strings.TrimSpace(strings.Join([]string{stderr, workerDied.Stderr}, "\n"))
			return fmt.Errorf("script was killed, most likely by %s\nstderr: %s", strings.Join(hints, " or "), detail)
		}
	}
	return fmt.Errorf("script failed: %w\nstderr: %s", err, stderr)
}

func executeCodeNodePooled(parent context.Context, script string, input *common.DataSet, nodeConfig map[string]interface{}, runParams map[string]string, timeoutSec int, progress func(int, string)) (*common.DataSet, string, error) {
	limits := codeexec.Resolve(nodeConfig)
	language, interpreter, err := resolveCodeRuntime(nodeConfig)
	if err != nil {
		return nil, "", err
	}
	tmpDir := os.TempDir()
	outputPath := filepath.Join(tmpDir, fmt.Sprintf("brokoli_out_%d.ndjson", time.Now().UnixNano()))
	defer os.Remove(outputPath)

	req := codeexec.Request{
		Language:     language,
		Script:       script,
		Config:       nodeConfig,
		Params:       runParams,
		Timeout:      time.Duration(timeoutSec) * time.Second,
		Interpreter:  interpreter,
		Limits:       limits,
		OutputNDJSON: outputPath,
	}

	// Same threshold, same staging as the legacy path: large inputs go
	// by file reference, small ones ride the exec frame inline.
	if input != nil && len(input.Rows) >= fileModeThreshold {
		inputPath := filepath.Join(tmpDir, fmt.Sprintf("brokoli_in_%d.ndjson", time.Now().UnixNano()))
		if err := WriteArrowJSON(inputPath, input); err != nil {
			return nil, "", fmt.Errorf("stage code input: %w", err)
		}
		defer os.Remove(inputPath)
		req.InputNDJSON = inputPath
		req.InputColumns = input.Columns
	} else if input != nil {
		rows := make([]map[string]interface{}, len(input.Rows))
		for i, row := range input.Rows {
			rows[i] = map[string]interface{}(row)
		}
		req.InlineRows = rows
		req.InputColumns = input.Columns
	}

	var stderrLines []string
	req.LogHandler = func(_, message string) { stderrLines = append(stderrLines, message) }
	req.ProgressHandler = progress

	res, err := codeexec.GlobalPool().Exec(parent, req)
	stderrStr := strings.Join(stderrLines, "\n")
	if err != nil {
		return nil, stderrStr, scriptErrorToEngineError(err, language, limits, stderrStr)
	}

	if res.Path != "" {
		ds, readErr := ReadArrowJSON(res.Path)
		if readErr != nil {
			return nil, stderrStr, fmt.Errorf("read code output: %w", readErr)
		}
		if len(res.Columns) > 0 {
			// The declared column order travels in the result frame now
			// (the sidecar file's successor).
			ds.Columns = res.Columns
		}
		return ds, stderrStr, nil
	}
	ds := &common.DataSet{Columns: res.Columns, Rows: make([]common.DataRow, len(res.Rows))}
	if ds.Columns == nil {
		ds.Columns = []string{}
	}
	for i, row := range res.Rows {
		ds.Rows[i] = common.DataRow(row)
	}
	return ds, stderrStr, nil
}

func executeCodeNodeStreamedPooled(parent context.Context, script string, inputNDJSONPath string, inputColumns []string, nodeConfig map[string]interface{}, runParams map[string]string, timeoutSec int, progress func(int, string)) (*codeStreamResult, error) {
	limits := codeexec.Resolve(nodeConfig)
	language, interpreter, err := resolveCodeRuntime(nodeConfig)
	if err != nil {
		return nil, err
	}
	outputPath := filepath.Join(os.TempDir(), fmt.Sprintf("brokoli_out_%d.ndjson", time.Now().UnixNano()))

	req := codeexec.Request{
		Language:     language,
		Script:       script,
		Config:       nodeConfig,
		Params:       runParams,
		Timeout:      time.Duration(timeoutSec) * time.Second,
		Interpreter:  interpreter,
		Limits:       limits,
		InputNDJSON:  inputNDJSONPath,
		InputColumns: inputColumns,
		OutputNDJSON: outputPath,
	}
	var stderrLines []string
	req.LogHandler = func(_, message string) { stderrLines = append(stderrLines, message) }
	req.ProgressHandler = progress

	res, err := codeexec.GlobalPool().Exec(parent, req)
	stderrStr := strings.Join(stderrLines, "\n")
	if err != nil {
		_ = os.Remove(outputPath)
		return nil, scriptErrorToEngineError(err, language, limits, stderrStr)
	}
	out := &codeStreamResult{stderr: stderrStr}
	if res.Path != "" {
		out.outputPath = res.Path
		out.columns = res.Columns
		return out, nil
	}
	_ = os.Remove(outputPath)
	// Inline (zero-emit) result: hand the caller the same JSON shape the
	// legacy stdout fallback produced.
	payload, marshalErr := json.Marshal(map[string]interface{}{
		"columns": res.Columns, "rows": res.Rows,
	})
	if marshalErr != nil {
		return nil, marshalErr
	}
	out.stdout = payload
	return out, nil
}
