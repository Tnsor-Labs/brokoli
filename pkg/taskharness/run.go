package taskharness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/proctree"
)

const (
	defaultHandshakeTimeout = 10 * time.Second
	defaultTerminationGrace = 5 * time.Second
	stderrTailBytes         = 8 * 1024
)

// Options configures one harness invocation. Zero-value HandshakeTimeout
// and TerminationGrace fall back to their documented defaults.
type Options struct {
	// Command is the argv used to launch the harness process, e.g.
	// {"/usr/bin/python3", "/path/to/pyharness/harness.py"}.
	Command []string
	Dir     string
	Env     []string

	// Rlimits bounds the harness process the same way a code node is
	// bounded today (reusing pkg/proctree's rlimit primitive, ADR-029) —
	// the trusted-profile resource baseline this phase reuses rather
	// than reimplements.
	Rlimits proctree.Rlimits

	// HandshakeTimeout bounds how long the harness has to send ready
	// before Run gives up and reports a runtime_protocol failure.
	// Defaults to 10s.
	HandshakeTimeout time.Duration

	// TerminationGrace bounds both how long a cancelled harness has to
	// reply cancel_ack and exit, and how long any other terminating path
	// (handshake timeout, protocol violation) waits before Run escalates
	// from SIGTERM to SIGKILL. Defaults to 5s.
	TerminationGrace time.Duration
}

// Result is Run's outcome. Failure is nil exactly when the attempt
// completed successfully.
type Result struct {
	Failure *Failure
}

// stderrTail retains only the last stderrTailBytes of a harness's
// stderr, for inclusion in a synthesized failure's diagnostic message —
// stdout is reserved for protocol frames (framing doc), so stderr is
// the only place an interpreter crash's traceback would otherwise go.
type stderrTail struct {
	mu        sync.Mutex
	buf       []byte
	truncated bool
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(p) >= stderrTailBytes {
		t.buf = append(t.buf[:0], p[len(p)-stderrTailBytes:]...)
		t.truncated = true
		return len(p), nil
	}
	if excess := len(t.buf) + len(p) - stderrTailBytes; excess > 0 {
		copy(t.buf, t.buf[excess:])
		t.buf = t.buf[:len(t.buf)-excess]
		t.truncated = true
	}
	t.buf = append(t.buf, p...)
	return len(p), nil
}

func (t *stderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.ToValidUTF8(string(t.buf), "�")
	if t.truncated {
		return "[harness stderr truncated to last 8 KiB]\n" + s
	}
	return s
}

type frameEvent struct {
	typ    string
	fields map[string]json.RawMessage
	err    error
}

func protocolFailure(code, format string, args ...interface{}) *Failure {
	return &Failure{Category: FailureRuntimeProtocol, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Run spawns the harness, performs the full start->ready->events->
// terminal handshake over the task-runtime/v1 protocol, waits for
// process exit, and returns the outcome. It never returns a non-nil
// error for a task or protocol failure -- those become Result.Failure.
// A non-nil error means the harness could not be run at all (spawn
// failure, bad Options).
//
// Cancellation: if ctx is done after ready has been observed, Run sends
// a protocol cancel frame and waits up to Options.TerminationGrace for
// cancel_ack (or a racing terminal frame — framing doc: "resolved by
// whichever frame the worker reads first") before escalating to
// SIGTERM/SIGKILL. If ctx is done before ready, Run terminates the
// process tree directly (the protocol has nothing to say about
// cancelling a harness that hasn't finished its handshake).
func Run(ctx context.Context, start StartFrame, opts Options, handlers Handlers) (Result, error) {
	if len(opts.Command) == 0 {
		return Result{}, fmt.Errorf("taskharness: Options.Command must not be empty")
	}
	handshakeTimeout := opts.HandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = defaultHandshakeTimeout
	}
	grace := opts.TerminationGrace
	if grace <= 0 {
		grace = defaultTerminationGrace
	}

	cmd := exec.Command(opts.Command[0], opts.Command[1:]...) // #nosec G204 -- Command is caller-constructed (the trusted worker resolves the harness binary itself, matching pkg/plugins.Runner's own idiom)
	cmd.Dir = opts.Dir
	if opts.Env != nil {
		cmd.Env = opts.Env
	}
	proctree.ConfigureProcessGroup(cmd)

	var tail stderrTail
	cmd.Stderr = &tail

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("taskharness: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("taskharness: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("taskharness: start %s: %w", opts.Command[0], err)
	}
	if err := proctree.ApplyRlimits(cmd.Process.Pid, opts.Rlimits); err != nil {
		_ = proctree.KillProcessTree(cmd.Process)
		_ = cmd.Wait()
		return Result{}, fmt.Errorf("taskharness: apply rlimits: %w", err)
	}

	startBytes, err := encodeFrame(start)
	if err != nil {
		_ = proctree.KillProcessTree(cmd.Process)
		_ = cmd.Wait()
		return Result{}, fmt.Errorf("taskharness: encode start frame: %w", err)
	}
	if _, err := stdin.Write(startBytes); err != nil {
		_ = proctree.KillProcessTree(cmd.Process)
		_ = cmd.Wait()
		return Result{}, fmt.Errorf("taskharness: write start frame: %w", err)
	}

	frameCh := make(chan frameEvent)
	go func() {
		defer close(frameCh)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), MaxFrameLine+1)
		for scanner.Scan() {
			fields, err := decodeFrameLine(scanner.Bytes())
			if err != nil {
				frameCh <- frameEvent{err: err}
				return
			}
			typ, err := frameType(fields)
			if err != nil {
				frameCh <- frameEvent{err: err}
				return
			}
			frameCh <- frameEvent{typ: typ, fields: fields}
		}
		if err := scanner.Err(); err != nil {
			frameCh <- frameEvent{err: fmt.Errorf("read harness stdout: %w", err)}
		}
	}()

	handshakeTimer := time.NewTimer(handshakeTimeout)
	defer handshakeTimer.Stop()
	var cancelTimer *time.Timer
	defer func() {
		if cancelTimer != nil {
			cancelTimer.Stop()
		}
	}()
	var cancelTimerC <-chan time.Time

	var (
		terminal      *Failure
		completedSeen bool
		readyObserved bool
		// reachedCleanTerminal is true only when the loop ended because
		// the harness itself sent a valid completed/failed/cancel_ack
		// frame -- as opposed to Run terminating the process for a
		// protocol violation it already detected. Only in the former
		// case does further stdout output during the drain phase below
		// mean anything (a harness lying about being done); in the
		// latter, it's just a process we already killed still finishing
		// a write it queued before the signal landed.
		reachedCleanTerminal bool
	)
	doneCh := ctx.Done()

mainLoop:
	for {
		select {
		case <-doneCh:
			doneCh = nil // handle a cancellation request at most once
			if readyObserved {
				if b, encErr := encodeFrame(CancelFrame{Type: "cancel", Reason: "context cancelled"}); encErr == nil {
					_, _ = stdin.Write(b)
				}
			} else {
				_ = proctree.TerminateProcessTree(cmd.Process)
			}
			cancelTimer = time.NewTimer(grace)
			cancelTimerC = cancelTimer.C

		case <-cancelTimerC:
			_ = proctree.TerminateProcessTree(cmd.Process)
			_ = proctree.KillProcessTree(cmd.Process)
			terminal = &Failure{Category: FailureCancelled, Code: "cancel_grace_expired", Message: "harness did not exit within the cancellation grace period"}
			break mainLoop

		case <-handshakeTimer.C:
			if !readyObserved {
				_ = proctree.TerminateProcessTree(cmd.Process)
				terminal = protocolFailure("handshake_timeout", "harness did not send ready within %s", handshakeTimeout)
				break mainLoop
			}

		case ev, ok := <-frameCh:
			if !ok {
				terminal = protocolFailure("exited_before_terminal", "harness closed its output before sending a terminal frame")
				break mainLoop
			}
			if ev.err != nil {
				_ = proctree.TerminateProcessTree(cmd.Process)
				terminal = protocolFailure("frame_decode_error", "%v", ev.err)
				break mainLoop
			}
			if !readyObserved {
				if ev.typ != "ready" {
					_ = proctree.TerminateProcessTree(cmd.Process)
					terminal = protocolFailure("output_before_ready", "harness emitted %q before ready", ev.typ)
					break mainLoop
				}
				var r Ready
				if err := decodeInto(ev.fields, &r); err != nil {
					_ = proctree.TerminateProcessTree(cmd.Process)
					terminal = protocolFailure("malformed_ready", "%v", err)
					break mainLoop
				}
				readyObserved = true
				handshakeTimer.Stop()
				if handlers.OnReady != nil {
					if err := handlers.OnReady(r); err != nil {
						_ = proctree.TerminateProcessTree(cmd.Process)
						terminal = protocolFailure("ready_rejected", "%v", err)
						break mainLoop
					}
				}
				continue mainLoop
			}
			switch ev.typ {
			case "log":
				if handlers.OnLog != nil {
					var l Log
					if err := decodeInto(ev.fields, &l); err == nil {
						handlers.OnLog(l)
					}
				}
			case "progress":
				if handlers.OnProgress != nil {
					var p Progress
					if err := decodeInto(ev.fields, &p); err == nil {
						handlers.OnProgress(p)
					}
				}
			case "warning":
				if handlers.OnWarning != nil {
					var w Warning
					if err := decodeInto(ev.fields, &w); err == nil {
						handlers.OnWarning(w)
					}
				}
			case "metric":
				if handlers.OnMetric != nil {
					var m Metric
					if err := decodeInto(ev.fields, &m); err == nil {
						handlers.OnMetric(m)
					}
				}
			case "completed":
				completedSeen = true
				reachedCleanTerminal = true
				break mainLoop
			case "failed":
				var f failedFrame
				if err := decodeInto(ev.fields, &f); err != nil {
					terminal = protocolFailure("malformed_failed", "%v", err)
				} else {
					terminal = &f.Failure
					reachedCleanTerminal = true
				}
				break mainLoop
			case "cancel_ack":
				terminal = &Failure{Category: FailureCancelled, Code: "cancelled", Message: "harness acknowledged cancellation"}
				reachedCleanTerminal = true
				break mainLoop
			default:
				terminal = protocolFailure("unexpected_frame_type", "unexpected frame type %q", ev.typ)
				break mainLoop
			}
		}
	}

	// Drain any remaining frames before Wait: the stdout pipe must be
	// fully read before calling cmd.Wait (exec.Cmd's own documented
	// contract). When the loop above ended on a legitimate terminal
	// frame, this also catches a frame sent after it (framing doc: a
	// protocol failure), overriding whatever terminal was otherwise
	// decided -- but only then: when Run itself terminated the process
	// for a violation it already found, further output here is just
	// that shutting-down process finishing a write it had already
	// queued, not a second violation. Bounded by the same grace period
	// so a harness that never closes stdout after completing doesn't
	// hang Run forever.
	drainDeadline := time.NewTimer(grace)
	defer drainDeadline.Stop()
drainLoop:
	for {
		select {
		case ev, ok := <-frameCh:
			if !ok {
				break drainLoop
			}
			if ev.err == nil && reachedCleanTerminal {
				terminal = protocolFailure("frame_after_terminal", "harness emitted %q after its terminal frame", ev.typ)
			}
		case <-drainDeadline.C:
			_ = proctree.TerminateProcessTree(cmd.Process)
			_ = proctree.KillProcessTree(cmd.Process)
			break drainLoop
		}
	}

	exitErr := cmd.Wait()
	if terminal != nil && terminal.Category == FailureRuntimeProtocol && exitErr != nil {
		// A generic protocol failure (most often "exited_before_terminal")
		// may really be a resource ceiling: rlimits deliver SIGXCPU/SIGKILL/
		// SIGXFSZ, which look identical to a crash unless the exit signal
		// is inspected -- mirrors engine.signalBreachError's approach for
		// code nodes, giving the same diagnostic honesty to task nodes.
		if rf := resourceLimitFailure(exitErr, opts.Rlimits); rf != nil {
			terminal = rf
		}
	}
	if completedSeen && terminal == nil && exitErr != nil {
		terminal = protocolFailure("nonzero_exit_after_completed", "harness reported completed but exited with an error: %v", exitErr)
	}
	if terminal != nil && terminal.Category == FailureRuntimeProtocol && tail.String() != "" {
		terminal.Message = terminal.Message + "\nharness stderr: " + tail.String()
	}

	return Result{Failure: terminal}, nil
}
