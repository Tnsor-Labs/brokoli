package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// osEnviron wraps os.Environ to keep the single-line stub visible in
// runner source without importing "os" at the top of the file just
// for this one use. Tests can't stub this out today — if we need
// that later, promote it to a package-level variable.
func osEnviron() []string { return os.Environ() }

// pluginTerminationGrace bounds how long a plugin has to shut down
// cleanly after the host asks it to stop. On cancellation or timeout the
// runner sends SIGTERM; if the process has not exited within this
// window, exec.Cmd.WaitDelay kills it and closes the I/O pipes, which is
// what stops Run from blocking on a stdout that never reaches EOF.
//
// Carried on Runner as an unexported field defaulted from this
// constant: no plugin has needed a different value, so it stays off the
// package's API surface, while the cancellation tests can shrink it
// rather than paying the full period on every run of the suite.
const pluginTerminationGrace = 5 * time.Second

// Runner invokes a plugin binary and ferries messages between the
// host and the plugin process. One Runner handles one invocation —
// it's constructed per-call rather than reused so a cancelled run
// doesn't leak state into the next.
//
// The Runner is the only piece of the plugins package that knows how
// to spawn processes. Everything else (the manager, the node executor,
// the CLI commands) drives the Runner.
type Runner struct {
	manifest *Manifest
	timeout  time.Duration

	// terminationGrace is how long the plugin has between SIGTERM and
	// being killed. Defaults to pluginTerminationGrace. Unexported so it
	// stays off the package's API surface while the cancellation tests
	// can still shrink it rather than paying the full grace period on
	// every run of the suite.
	terminationGrace time.Duration

	// LogHandler is called for every MsgLog line the plugin emits
	// (via stdout) and for every non-empty stderr line. If nil, logs
	// are dropped. Runners in production wire this to the run log
	// infrastructure so plugin logs appear in the UI's run timeline.
	LogHandler func(level LogLevel, msg string)

	// ProgressHandler is called for every MsgProgress line the plugin
	// emits. If nil, progress messages are still recorded in
	// RunResult.LastProgress but otherwise dropped - the same no-op
	// behavior a host that predates MsgProgress already has.
	ProgressHandler func(Progress)
}

// NewRunner constructs a Runner for the given manifest. Timeout is
// an overall wall-clock cap on the plugin invocation; the runner sends
// SIGTERM on timeout and SIGKILL after a short grace period.
func NewRunner(m *Manifest, timeout time.Duration) *Runner {
	return &Runner{
		manifest:         m,
		timeout:          timeout,
		terminationGrace: pluginTerminationGrace,
	}
}

// RunResult is what a plugin invocation produces. Records is the
// collected data rows (for sources and transforms). State is the
// final state cursor (for incremental sources) — overwritten each
// time the plugin emits a state line, so the caller ends up with the
// last state the plugin declared. Streams is populated by discover.
// Status is populated by check/write.
type RunResult struct {
	Records []map[string]interface{}
	State   map[string]interface{}

	// LastProgress is the most recent MsgProgress the plugin emitted,
	// overwritten each time - the same last-one-wins semantics State
	// already uses. Nil if the plugin never reported progress.
	LastProgress *Progress

	// WorkUnits is populated by `plan` (ADR-013 M3) — the independent
	// pieces of a stream's work the plugin declared. Nil for every
	// other command, or for a plan call from a plugin that returns
	// none.
	WorkUnits []WorkUnit

	Streams []Stream
	Status  string // "ok" | "error" | ""
	Message string // human-readable status detail
}

// Run launches the plugin with the given subcommand and stdin payload,
// streams stdout as JSONL, and collects everything into a RunResult.
//
// Cancellation: ctx drives the child process group. If ctx is cancelled
// or its deadline fires before the plugin exits, the runner sends
// SIGTERM to the group; plugin has pluginTerminationGrace to clean up,
// after which it is killed and the I/O pipes are closed. Non-Unix
// platforms get a reduced version of this - see process_other.go
//
// Streaming: stdout is decoded line-by-line rather than buffered in
// full, so a source plugin yielding millions of rows doesn't blow up
// the host's memory. That said, we currently collect every record
// into RunResult.Records in memory — the caller (node executor)
// assembles them into a DataSet. For datasets too big to fit in RAM
// we'll add a streaming sink API later; out of scope for Phase 1.
//
// Additional stdin lines (for write streams) are provided via the
// writer parameter — the caller can push records after the header.
// For non-write commands, pass nil.
//
// Idempotency / durability contract (Tnsor-Labs/brokoli#7): Run has no
// durable intent record written before proc.Start() below. If the host
// process dies between spawning the plugin and this function returning, an
// external side effect the plugin already performed (e.g. a write-command
// plugin completing a sink write) can be committed with nothing durable on
// the host side to reconcile against on restart — the exact gap the new
// store.ExecutionAttemptStore contract (models.ExecutionAttempt,
// store.ExecutionAttemptStore.ClaimAttempt/AckAttempt/CompleteAttempt/
// FailAttempt) exists to close.
//
// Wiring this specific call site is deliberately deferred rather than done
// in this change:
//   - The caller of Run (the node executor) would need to claim/ack/settle
//     an ExecutionAttempt around this call — a real behavior change to the
//     plugin dispatch path, not just an error-handling fix, with its own
//     failure modes (e.g. what happens to the lease if AckAttempt itself
//     fails after proc.Start() already ran) that deserve their own
//     review and tests rather than being folded into this PR's blast
//     radius.
//   - Full crash-recovery semantics for an in-flight claim are issue #9's
//     job; landing half of that contract here without the reconciler that
//     consumes it would add complexity with no observable benefit yet.
//
// Until that follow-up lands, callers that need at-least-once safety for
// write-command plugins must supply their own idempotency at the
// destination (e.g. upsert semantics, a dedup key) — Run being retried
// after a partial/unknown outcome is expected, not a bug.
func (r *Runner) Run(ctx context.Context, cmd Command, stdinJSON []byte, extraStdin io.Reader) (*RunResult, error) {
	if r.manifest == nil {
		return nil, errors.New("runner: nil manifest")
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	args := append([]string{}, r.manifest.Args...)
	args = append(args, string(cmd))

	proc := exec.CommandContext(ctx, r.manifest.BinaryPath(), args...)
	// Preserve PATH etc. but don't leak the host's entire environment —
	// we'll need to revisit this once we add secret injection.
	proc.Env = minimalEnv()
	// Run from the plugin's own directory: interpreted payloads reference
	// their entrypoint and vendored modules relative to it, and nothing
	// in the protocol ever promised the host's working directory.
	proc.Dir = r.manifest.dir

	// Cancellation. exec.CommandContext's default is Process.Kill() - an
	// immediate SIGKILL the plugin cannot catch, leaving it no chance to
	// close connections or emit a final checkpoint. Three pieces replace
	// it:
	//
	//	- the child leads its own process group, so signals reach
	//		everything it spawned rather than just a wrapper script;
	//	- Cancel sends SIGTERM to that group;
	//	- WaitDelay bounds the wait, after which the standard library
	//		kills the process and closes the I/O pipes. That last part is
	//		what stops Run from blocking forever on a stdout that never
	//		reaches EOF because a surviving child still holds it open.
	//
	// Cancel is a closure because proc.Process is nil until Start
	// succeeds; the standard library documents that Cancel is never
	// called if Start returns an error.
	configureProcessGroup(proc)
	proc.Cancel = func() error { return terminateProcessTree(proc.Process) }
	proc.WaitDelay = r.terminationGrace

	stdin, err := proc.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin %s: stdin pipe: %w", r.manifest.Name, err)
	}
	stdout, err := proc.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin %s: stdout pipe: %w", r.manifest.Name, err)
	}
	stderr, err := proc.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin %s: stderr pipe: %w", r.manifest.Name, err)
	}

	if err := proc.Start(); err != nil {
		return nil, fmt.Errorf("plugin %s: start %s: %w",
			r.manifest.Name, r.manifest.BinaryPath(), err)
	}

	// Feed stdin: header line (stdinJSON), then optional extraStdin,
	// then close to signal EOF. All in a goroutine so stdout/stderr
	// can drain concurrently.
	stdinErrCh := make(chan error, 1)
	go func() {
		defer close(stdinErrCh)
		defer stdin.Close()
		if len(stdinJSON) > 0 {
			if _, err := stdin.Write(stdinJSON); err != nil {
				stdinErrCh <- fmt.Errorf("write stdin header: %w", err)
				return
			}
			if len(stdinJSON) == 0 || stdinJSON[len(stdinJSON)-1] != '\n' {
				if _, err := stdin.Write([]byte{'\n'}); err != nil {
					stdinErrCh <- fmt.Errorf("write stdin newline: %w", err)
					return
				}
			}
		}
		if extraStdin != nil {
			if _, err := io.Copy(stdin, extraStdin); err != nil {
				stdinErrCh <- fmt.Errorf("write stdin body: %w", err)
				return
			}
		}
	}()

	// Drain stderr into the log handler. Unstructured — every non-empty
	// line becomes an info-level log entry. Plugins that want levels
	// should emit MsgLog on stdout instead.
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 1<<16), 1<<20)
		for sc.Scan() {
			if r.LogHandler != nil {
				r.LogHandler(LogInfo, sc.Text())
			}
		}
	}()

	// Decode stdout into a RunResult.
	result := &RunResult{}
	var pluginErr error
	decodeErr := DecodeStream(stdout, func(m Message) error {
		switch m.Type {
		case MsgRecord:
			if m.Data != nil {
				result.Records = append(result.Records, m.Data)
			}
		case MsgState:
			if m.Value != nil {
				result.State = m.Value
			}
		case MsgProgress:
			if m.Progress != nil {
				result.LastProgress = m.Progress
				if r.ProgressHandler != nil {
					r.ProgressHandler(*m.Progress)
				}
			}
		case MsgStream:
			if m.Stream != nil {
				result.Streams = append(result.Streams, *m.Stream)
			}
		case MsgWorkUnit:
			if m.WorkUnit != nil {
				result.WorkUnits = append(result.WorkUnits, *m.WorkUnit)
			}
		case MsgLog:
			if r.LogHandler != nil {
				level := m.Level
				if level == "" {
					level = LogInfo
				}
				r.LogHandler(level, m.Message)
			}
		case MsgStatus:
			result.Status = m.StatusCode
			result.Message = m.Message
		case MsgError:
			// Remember the first error the plugin reports; combined with
			// the exit code it gives the caller a clean failure reason.
			if pluginErr == nil {
				pluginErr = errors.New(m.Message)
			}
			if r.LogHandler != nil {
				r.LogHandler(LogError, m.Message)
			}
		}
		return nil
	})

	stderrWG.Wait()
	waitErr := proc.Wait()

	// WaitDelay's own escalation calls Process.Kill, which reaches only
	// the direct child. Anything it spawned survives - and keeps the
	// process group alive, so a single SIGKILL to the group lands on
	// exactly those survivors. ESRCH (returned as os.ErrProcessDone)
	// means the group is already empty, which is the normal case.
	//
	// Cancellation is the only condition worth checking here. A plugin
	// whose own process exits while a child still holds stdout never
	// reaches this point with the context clean: DecodeStream above is
	// still blocked on that pipe, and WaitDelay cannot break the
	// deadlock because its "process exited, pipes still open" branch
	// runs inside Wait. Such a run blocks until the runner's own timeout
	// fires, which makes the context done and takes this branch anyway.
	if ctx.Err() != nil && proc.Process != nil {
		_ = killProcessTree(proc.Process)
	}

	// Error ordering: the plugin's own MsgError first — when it managed
	// to report one it explains the failure better than anything the
	// host can infer. Then the context, because once it is done every
	// rung below describes the host's own shutdown as a plugin fault.
	// Then stdin, decode, and exit status.
	if pluginErr != nil {
		return result, fmt.Errorf("plugin %s %s: %w", r.manifest.Name, cmd, pluginErr)
	}

	// Once the context is done the runner signals the process and closes
	// the pipes itself, so the rungs below would report a broken stdin
	// pipe, a "file already closed" decode failure, or a bare
	// "signal: killed" exit — all of them blaming the plugin for the
	// host's own shutdown.
	//
	// Gated on an actual failure so a read that happened to complete as
	// the context was cancelled still returns its result instead of
	// being relabelled a cancellation.
	//
	// The gate holds on every real cancellation path: exec.Cmd
	// propagates a non-ErrProcessDone error from Cancel into Wait's
	// result, so a signalled plugin yields a non-nil waitErr even when it
	// exits 0. The exception is deliberate - when Cancel finds the
	// process already gone it returns os.ErrProcessDone, Wait keeps the
	// plugin's own exit status, and a read that finished just as the
	// context was cancelled returns its result instead of being
	// relabelled a cancellation.
	if ctxErr := ctx.Err(); ctxErr != nil && (waitErr != nil || decodeErr != nil) {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return result, fmt.Errorf("plugin %s %s: timed out after %s",
				r.manifest.Name, cmd, r.timeout)
		}
		return result, fmt.Errorf("plugin %s %s: cancelled", r.manifest.Name, cmd)
	}

	if stdinErr := <-stdinErrCh; stdinErr != nil {
		return result, fmt.Errorf("plugin %s %s: %w", r.manifest.Name, cmd, stdinErr)
	}
	if decodeErr != nil {
		return result, fmt.Errorf("plugin %s %s: decode stdout: %w", r.manifest.Name, cmd, decodeErr)
	}
	if waitErr != nil {
		return result, fmt.Errorf("plugin %s %s: exit: %w", r.manifest.Name, cmd, waitErr)
	}
	return result, nil
}

// Check runs the `check` command and returns nil on success or a
// descriptive error on failure. Shortcut for the common case.
func (r *Runner) Check(ctx context.Context, cfg Config) error {
	payload, err := json.Marshal(CheckParams{Config: cfg})
	if err != nil {
		return fmt.Errorf("marshal check params: %w", err)
	}
	result, err := r.Run(ctx, CmdCheck, payload, nil)
	if err != nil {
		return err
	}
	if result.Status == "error" {
		return fmt.Errorf("plugin %s check: %s", r.manifest.Name, result.Message)
	}
	return nil
}

// Discover runs the `discover` command and returns the streams the
// plugin exposes for the given config.
func (r *Runner) Discover(ctx context.Context, cfg Config) ([]Stream, error) {
	payload, err := json.Marshal(DiscoverParams{Config: cfg})
	if err != nil {
		return nil, fmt.Errorf("marshal discover params: %w", err)
	}
	result, err := r.Run(ctx, CmdDiscover, payload, nil)
	if err != nil {
		return nil, err
	}
	return result.Streams, nil
}

// Plan runs the `plan` command for one already-discovered stream and
// returns the independent work units the plugin says it can be broken
// into (ADR-013 M3). Only meaningful for a node type whose manifest
// sets NodeTypeDecl.SupportsPlan — calling it against a plugin that
// doesn't implement `plan` fails the same way any other unrecognized
// subcommand would (the plugin's own "unknown command" error), not a
// special case handled here. The caller is expected to check
// SupportsPlan first, same as it already resolves Kind before deciding
// whether to Read or Write.
func (r *Runner) Plan(ctx context.Context, cfg Config, stream string) ([]WorkUnit, error) {
	payload, err := json.Marshal(PlanParams{Config: cfg, Stream: stream})
	if err != nil {
		return nil, fmt.Errorf("marshal plan params: %w", err)
	}
	result, err := r.Run(ctx, CmdPlan, payload, nil)
	if err != nil {
		return nil, err
	}
	return result.WorkUnits, nil
}

// Read runs the `read` command for a stream and collects records.
// state is the incremental cursor from the previous run (may be nil
// for full refresh or first run). The returned RunResult's State field
// holds the advanced cursor the caller should persist.
func (r *Runner) Read(ctx context.Context, cfg Config, stream string, state map[string]interface{}) (*RunResult, error) {
	payload, err := json.Marshal(ReadParams{Config: cfg, Stream: stream, State: state})
	if err != nil {
		return nil, fmt.Errorf("marshal read params: %w", err)
	}
	return r.Run(ctx, CmdRead, payload, nil)
}

// ReadUnit runs the `read` command for one work unit a prior Plan call
// returned, threading unit through ReadParams.Unit so the plugin knows
// which piece of the stream to fetch. Identical to Read otherwise —
// kept as a separate method rather than adding a parameter to Read so
// every existing caller of Read (today, every source/sink plugin
// invocation in the codebase) is untouched by this addition.
func (r *Runner) ReadUnit(ctx context.Context, cfg Config, stream string, state map[string]interface{}, unit map[string]interface{}) (*RunResult, error) {
	payload, err := json.Marshal(ReadParams{Config: cfg, Stream: stream, State: state, Unit: unit})
	if err != nil {
		return nil, fmt.Errorf("marshal read params: %w", err)
	}
	return r.Run(ctx, CmdRead, payload, nil)
}

// Write runs the `write` command for a stream, streaming records from
// the given iterator into the plugin's stdin after the header line.
// The caller is responsible for converting their input DataSet to a
// Message stream; this is the raw path that lets the node executor
// push rows without loading them all into memory.
func (r *Runner) Write(ctx context.Context, cfg Config, stream string, records []map[string]interface{}) error {
	header, err := json.Marshal(WriteParams{Config: cfg, Stream: stream})
	if err != nil {
		return fmt.Errorf("marshal write params: %w", err)
	}
	// Serialize records into a pipe reader so the runner can stream them
	// in alongside the header without buffering the whole dataset.
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for _, rec := range records {
			msg := NewRecord(rec)
			if err := EncodeLine(pw, msg); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}()
	result, err := r.Run(ctx, CmdWrite, header, pr)
	if err != nil {
		return err
	}
	if result.Status == "error" {
		return fmt.Errorf("plugin %s write: %s", r.manifest.Name, result.Message)
	}
	return nil
}

// Spec runs the `spec` command — no stdin, plugin just prints its
// manifest JSON to stdout. Used at install time to snapshot what the
// plugin declares it can do. The host validates the result against
// the on-disk manifest to catch drift between a plugin's build-time
// capabilities and its declared manifest.
//
// Returns the raw JSON bytes so the caller can do their own comparison.
func (r *Runner) Spec(ctx context.Context) ([]byte, error) {
	// Bypass the JSONL decoder — `spec` emits a single JSON object, not
	// a line-delimited stream. Run exec directly.
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	args := append([]string{}, r.manifest.Args...)
	args = append(args, string(CmdSpec))
	proc := exec.CommandContext(ctx, r.manifest.BinaryPath(), args...)
	proc.Env = minimalEnv()
	// Run from the plugin's own directory: interpreted payloads reference
	// their entrypoint and vendored modules relative to it, and nothing
	// in the protocol ever promised the host's working directory.
	proc.Dir = r.manifest.dir
	// Same graceful stop as Run: on timeout/cancel, SIGTERM the process
	// group and give it terminationGrace before WaitDelay escalates to
	// SIGKILL. Without this, exec.CommandContext's default is an immediate
	// Process.Kill() — the SIGKILL-with-no-grace this fixes (#110). A spec
	// probe is usually quick, but a hung plugin still deserves the same
	// clean-shutdown window the protocol documents.
	configureProcessGroup(proc)
	proc.Cancel = func() error { return terminateProcessTree(proc.Process) }
	proc.WaitDelay = r.terminationGrace
	out, err := proc.Output()
	if err != nil {
		return nil, fmt.Errorf("plugin %s spec: %w", r.manifest.Name, err)
	}
	return out, nil
}

// minimalEnv returns the env vars we pass to plugins. Keeping this
// centralized so secret-injection changes (a planned EE feature) land
// in exactly one place.
//
// Phase 1: inherit the parent process's environment so plugins can
// find python3, node, tools in PATH, etc. This is the "trust the
// local box" mode — fine for OSS single-binary deployments.
//
// Phase 2 (EE): filter to an allowlist (PATH, HOME, LANG, TZ) plus
// plugin-specific injections from Vault / Secrets Manager. Plugins
// that need more will have to declare it in the manifest and go
// through the secret provider.
func minimalEnv() []string {
	return osEnviron()
}
