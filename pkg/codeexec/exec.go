package codeexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Request is one code-node execution against the pool. Exactly one
// input mode: InlineRows (small data) or InputNDJSON (a staged file by
// reference — the ADR-029 v1 data plane). Exactly one script source:
// Script (a bare code string) or TaskBundle* (a materialized ADR-031
// bundle — when set, Script is ignored).
type Request struct {
	Language    string // "python" (default) | "typescript"
	Script      string
	Config      map[string]interface{}
	Params      map[string]string
	Timeout     time.Duration
	Interpreter string // resolved python; required
	Limits      Limits
	InlineRows  []map[string]interface{}
	InputColumns    []string
	InputNDJSON     string
	OutputNDJSON    string // where file-mode output should land; required
	LogHandler      func(level, message string)
	ProgressHandler func(percent int, message string)

	// TaskBundle mounts a materialized task bundle for this execution
	// (ADR-031). TaskBundleDir is an already-extracted bundle root,
	// TaskBundleEntry its entry module relative to it, TaskBundleFiles
	// the manifest's authoritative file list. BundleDigest joins the
	// pool's sub-key: a warm python worker's sys.modules is
	// process-global, so a different bundle must never share a process
	// with one that already imported from its own tree.
	TaskBundleDir   string
	TaskBundleEntry string
	TaskBundleFiles []string
	BundleDigest    string
}

// ExecMeta is the per-run audit record (ADR-029): which contract this
// execution actually ran under.
type ExecMeta struct {
	Language        string
	WrapperVersion  int
	ProtocolVersion int
	Interpreter     string
	Warm            bool
	Limits          Limits
}

// Result is one execution's outcome.
type Result struct {
	// Inline output (small results, zero-emit) …
	Columns []string
	Rows    []map[string]interface{}
	// … or a written NDJSON file.
	Path        string
	RowsWritten int64
	Meta        ExecMeta
}

// ErrScript wraps a failure the user's script caused, with the worker's
// classification preserved.
type ErrScript struct {
	Kind      string
	Message   string
	Traceback string
}

// ErrWorkerDied preserves process-level diagnostics when a worker
// cannot send a typed frame (V8 OOM and native-library aborts).
type ErrWorkerDied struct {
	Cause  error
	Exit   error
	Stderr string
}

func (e *ErrWorkerDied) Error() string {
	detail := fmt.Sprintf("code worker died mid-execution: %v", e.Cause)
	if e.Exit != nil {
		detail += fmt.Sprintf(" (process: %v)", e.Exit)
	}
	if strings.TrimSpace(e.Stderr) != "" {
		detail += "\nworker stderr: " + strings.TrimSpace(e.Stderr)
	}
	return detail
}

func (e *ErrWorkerDied) Unwrap() error { return e.Cause }

func (e *ErrScript) Error() string {
	if e.Traceback != "" {
		return fmt.Sprintf("%s\nstderr: %s", e.Message, e.Traceback)
	}
	return e.Message
}

// Exec runs one request on a pooled worker. Cancellation and timeout
// kill that worker's process group (interrupted user-script state is
// untrusted — ADR-029) and leave every other worker untouched; the
// pool respawns lazily on the next demand.
func (p *Pool) Exec(ctx context.Context, req Request) (*Result, error) {
	if req.Language == "" {
		req.Language = "python"
	}
	if req.Language != "python" && req.Language != "typescript" {
		return nil, fmt.Errorf("codeexec: unsupported language %q", req.Language)
	}
	if req.Interpreter == "" {
		return nil, errors.New("codeexec: request needs a resolved interpreter")
	}
	if req.Timeout <= 0 {
		req.Timeout = 30 * time.Second
	}
	worker, warm, err := p.acquire(ctx, req.Language, req.Interpreter, req.Limits, req.BundleDigest)
	if err != nil {
		return nil, err
	}
	meta := ExecMeta{
		Language:        req.Language,
		WrapperVersion:  wrapperVersionForLanguage(req.Language),
		ProtocolVersion: CodeProtocolVersion,
		Interpreter:     req.Interpreter,
		Warm:            warm,
		Limits:          req.Limits,
	}

	input := ExecInput{Mode: "none"}
	switch {
	case req.InputNDJSON != "":
		input = ExecInput{Mode: "ndjson", Path: req.InputNDJSON, Columns: req.InputColumns}
	case req.InlineRows != nil:
		input = ExecInput{Mode: "inline", Rows: req.InlineRows, Columns: req.InputColumns}
	}
	msg := ExecMsg{
		ExecID:    fmt.Sprintf("x-%d", time.Now().UnixNano()),
		Script:    req.Script,
		Config:    req.Config,
		Params:    req.Params,
		Input:     input,
		Output:    ExecOutput{Mode: "ndjson", Path: req.OutputNDJSON},
		TimeoutMs: req.Timeout.Milliseconds(),
	}
	if req.TaskBundleDir != "" {
		// A bundle is the script source; never send a script alongside
		// it. The digest already identifies the archive the dir was
		// extracted from; the dir carries the actual bytes.
		msg.Script = ""
		msg.Bundle = &ExecBundle{Dir: req.TaskBundleDir, Entry: req.TaskBundleEntry, Files: req.TaskBundleFiles}
	}

	type outcome struct {
		result *Result
		err    error
		fatal  bool // transport-level: the worker is dead or untrusted
	}
	done := make(chan outcome, 1)
	go func() {
		done <- p.exchange(worker, msg, req, meta)
	}()

	timer := time.NewTimer(req.Timeout)
	defer timer.Stop()
	select {
	case out := <-done:
		healthy := !out.fatal
		if out.err != nil {
			var scriptErr *ErrScript
			if errors.As(out.err, &scriptErr) && (scriptErr.Kind == ErrKindResourceLimit || scriptErr.Kind == ErrKindInternal) {
				healthy = false // allocator/internal state is untrusted after a breach
			}
		}
		p.release(worker, req.Language, req.Interpreter, req.Limits, req.BundleDigest, healthy)
		return out.result, out.err
	case <-timer.C:
		worker.kill()
		p.release(worker, req.Language, req.Interpreter, req.Limits, req.BundleDigest, false)
		return nil, fmt.Errorf("script timed out after %ds", int(req.Timeout.Seconds()))
	case <-ctx.Done():
		worker.kill()
		p.release(worker, req.Language, req.Interpreter, req.Limits, req.BundleDigest, false)
		return nil, ctx.Err()
	}
}

func wrapperVersionForLanguage(language string) int {
	if language == "typescript" {
		return JSWrapperVersion()
	}
	return WrapperVersion()
}

// exchange drives one exec frame to its result on a single worker.
func (p *Pool) exchange(w *Worker, msg ExecMsg, req Request, meta ExecMeta) (out struct {
	result *Result
	err    error
	fatal  bool
}) {
	w.stderr.Reset()
	if err := WriteFrame(w.rw.Writer, FrameExec, msg); err != nil {
		return failFatal(fmt.Errorf("send exec to code worker: %w", err))
	}
	if err := w.rw.Flush(); err != nil {
		return failFatal(fmt.Errorf("send exec to code worker: %w", err))
	}
	for {
		frameType, payload, err := ReadFrame(w.rw.Reader)
		if err != nil {
			exited := w.awaitExit(250 * time.Millisecond)
			var exitErr error
			if exited {
				exitErr = w.waitErr
			}
			return failFatal(&ErrWorkerDied{Cause: err, Exit: exitErr, Stderr: w.stderr.String()})
		}
		switch frameType {
		case FrameLog:
			var log LogMsg
			if json.Unmarshal(payload, &log) == nil && req.LogHandler != nil {
				req.LogHandler(log.Level, log.Message)
			}
		case FrameProgress:
			var progress ProgressMsg
			if json.Unmarshal(payload, &progress) == nil && req.ProgressHandler != nil {
				req.ProgressHandler(progress.Percent, progress.Message)
			}
		case FrameResult:
			var result ResultMsg
			if err := json.Unmarshal(payload, &result); err != nil {
				return failFatal(fmt.Errorf("malformed result frame: %w", err))
			}
			out.result = &Result{
				Columns:     result.Output.Columns,
				Rows:        result.Output.Rows,
				Path:        result.Output.Path,
				RowsWritten: result.Output.RowsWritten,
				Meta:        meta,
			}
			return out
		case FrameError:
			var errMsg ErrorMsg
			if err := json.Unmarshal(payload, &errMsg); err != nil {
				return failFatal(fmt.Errorf("malformed error frame: %w", err))
			}
			out.err = &ErrScript{Kind: errMsg.Kind, Message: errMsg.Message, Traceback: errMsg.Traceback}
			return out
		default:
			return failFatal(fmt.Errorf("unexpected frame %#x from code worker", frameType))
		}
	}
}

func failFatal(err error) (out struct {
	result *Result
	err    error
	fatal  bool
}) {
	out.err = err
	out.fatal = true
	return out
}
