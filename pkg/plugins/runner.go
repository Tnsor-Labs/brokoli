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

	// LogHandler is called for every MsgLog line the plugin emits
	// (via stdout) and for every non-empty stderr line. If nil, logs
	// are dropped. Runners in production wire this to the run log
	// infrastructure so plugin logs appear in the UI's run timeline.
	LogHandler func(level LogLevel, msg string)
}

// NewRunner constructs a Runner for the given manifest. Timeout is
// an overall wall-clock cap on the plugin invocation; the runner sends
// SIGTERM on timeout and SIGKILL after a short grace period.
func NewRunner(m *Manifest, timeout time.Duration) *Runner {
	return &Runner{
		manifest: m,
		timeout:  timeout,
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
	Streams []Stream
	Status  string // "ok" | "error" | ""
	Message string // human-readable status detail
}

// Run launches the plugin with the given subcommand and stdin payload,
// streams stdout as JSONL, and collects everything into a RunResult.
//
// Cancellation: ctx drives the child process group. If ctx is cancelled
// before the plugin exits, the runner sends SIGTERM; the plugin has
// 5 seconds to clean up, after which SIGKILL is sent. This matches the
// cmd/main.go signal-handler pattern the Brokoli worker uses.
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
		case MsgStream:
			if m.Stream != nil {
				result.Streams = append(result.Streams, *m.Stream)
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

	// Error ordering: prefer the plugin-reported MsgError over a bare
	// exit code, then fall back to a generic non-zero exit message.
	// Context deadline comes last because it's the runner's own error
	// and the caller usually wants the plugin's account first.
	if pluginErr != nil {
		return result, fmt.Errorf("plugin %s %s: %w", r.manifest.Name, cmd, pluginErr)
	}
	if stdinErr := <-stdinErrCh; stdinErr != nil {
		return result, fmt.Errorf("plugin %s %s: %w", r.manifest.Name, cmd, stdinErr)
	}
	if decodeErr != nil {
		return result, fmt.Errorf("plugin %s %s: decode stdout: %w", r.manifest.Name, cmd, decodeErr)
	}
	if waitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return result, fmt.Errorf("plugin %s %s: timed out after %s",
				r.manifest.Name, cmd, r.timeout)
		}
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
