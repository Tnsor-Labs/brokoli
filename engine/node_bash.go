package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/proctree"
)

// Output limits for one bash node. The command's output goes to the run
// log, a row per line, so it is bounded: a command that prints a
// generated file or loops forever must not fill the metadata store.
// Beyond the limits the output is still read -- a command blocked on a
// full pipe would never finish -- but no longer logged.
const (
	bashMaxLineBytes = 16 << 10
	bashStderrTail   = 20
	bashTermGrace    = 5 * time.Second
)

// bashMaxLogLines is a variable so tests can lower it.
var bashMaxLogLines = 10000

// runBash executes a trusted worker command and preserves the input dataset.
// This is intentionally a shell operator, not a sandbox: only trusted
// pipeline authors should be allowed to create bash nodes.
//
// The command runs the way code nodes and task harnesses do: in its own
// process group, so cancellation and timeouts stop everything it
// started, not only bash; under the code-node rlimits; with the filtered
// worker environment.
func (r *Runner) runBash(ctx context.Context, node models.Node, input *common.DataSet) (*common.DataSet, error) {
	command, _ := node.Config["command"].(string)
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("bash node requires a non-empty 'command'")
	}

	workingDir, _ := node.Config["working_dir"].(string)
	if workingDir == "" {
		// A private directory per attempt, removed afterwards. The shared
		// temp directory is neither private nor, when BROKOLI_DATA_DIRS
		// excludes it, allowed.
		scratch, err := os.MkdirTemp("", "brokoli-bash-")
		if err != nil {
			return nil, fmt.Errorf("bash working directory: %w", err)
		}
		defer func() { _ = os.RemoveAll(scratch) }()
		workingDir = scratch
	} else {
		if err := common.PathAllowed(workingDir); err != nil {
			return nil, fmt.Errorf("bash working_dir: %w", err)
		}
		if info, err := os.Stat(workingDir); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("bash working_dir %q is not a directory", workingDir)
		}
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		return nil, fmt.Errorf("bash executable is not available: %w", err)
	}
	limits := codeexec.Resolve(node.Config)
	env := bashEnvironment(node.Config["env"])
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	logged := &bashLogBudget{remaining: bashMaxLogLines}
	stdout := &bashLineWriter{emit: func(line string) { r.logBashLine(node.ID, models.LogLevelInfo, "bash", line, logged) }}
	stderr := &bashLineWriter{tail: bashStderrTail, emit: func(line string) {
		r.logBashLine(node.ID, models.LogLevelInfo, "bash stderr", line, logged)
	}}

	memory := r.planBashMemory(node, limits.MemoryMB)
	defer memory.close()

	// The limits are set by the shell on itself before the command runs
	// (#818). Set from outside after Start, a command that forks at once
	// -- any pipeline -- ran its children without them. Built per command:
	// a refused cgroup placement switches memory to address space.
	newCmd := func() (*exec.Cmd, error) {
		addressSpace := uint64(0)
		if memory.addressSpace {
			addressSpace = uint64(max(memory.limitMB, 0)) * 1024 * 1024
		}
		prelude, err := proctree.ShellLimitPrelude(proctree.Rlimits{
			CPUSeconds:    uint64(max(limits.CPUSeconds, 0)),
			FileSizeBytes: uint64(max(limits.FileSizeMB, 0)) * 1024 * 1024,
			OpenFiles:     uint64(max(limits.OpenFiles, 0)),
		}, addressSpace)
		if err != nil {
			return nil, fmt.Errorf("apply bash limits: %w", err)
		}
		args := []string{"-o", "pipefail", "-c", command}
		if prelude != "" {
			// exec keeps the PID, so the process group and the cgroup
			// placement are the command's own.
			args = []string{"-c", prelude + `exec "$0" -o pipefail -c "$1"`, bash, command}
		}
		// #nosec G204 -- running the pipeline author's command is this node's
		// purpose; it is documented as trusted-worker execution.
		cmd := exec.CommandContext(ctx, bash, args...)
		cmd.Dir = workingDir
		cmd.Env = append(codeexec.WorkerEnv(), "BROKOLI_NODE_ID="+node.ID)
		if r.run != nil {
			cmd.Env = append(cmd.Env, "BROKOLI_RUN_ID="+r.run.ID)
		}
		for _, key := range keys {
			cmd.Env = append(cmd.Env, key+"="+env[key])
		}
		proctree.ConfigureProcessGroup(cmd)
		cmd.Cancel = func() error { return proctree.TerminateProcessTree(cmd.Process) }
		// Bounds Wait both after a cancel and when the command exits but
		// something it started in the background still holds its output.
		cmd.WaitDelay = bashTermGrace
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		return cmd, nil
	}

	cmd, err := newCmd()
	if err != nil {
		return nil, err
	}
	memory.place(cmd)
	if err := cmd.Start(); err != nil {
		if memory.cgroup == nil {
			return nil, fmt.Errorf("start bash: %w", err)
		}
		// Starting inside the cgroup can be refused (moving a process
		// needs write access up to the common ancestor of its old and new
		// cgroups); fall back as if no cgroup were available.
		memory.fallBack(r, node, fmt.Errorf("start inside %s: %w", memory.parent, err))
		if cmd, err = newCmd(); err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start bash: %w", err)
		}
	}
	waitErr := cmd.Wait()
	// Whatever the command left running in the background is part of
	// this node and does not outlive it, on success as on failure.
	_ = proctree.KillProcessTree(cmd.Process)
	stdout.flush()
	stderr.flush()
	if logged.dropped > 0 {
		r.logBashLine(node.ID, models.LogLevelWarning, "bash", fmt.Sprintf(
			"%d further output lines were not logged (the limit is %d lines per node)",
			logged.dropped, bashMaxLogLines), &bashLogBudget{remaining: 1})
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if memory.cgroup != nil && memory.cgroup.OOMKilled() {
		return nil, fmt.Errorf("bash command exceeded the memory limit (%d MiB): raise max_memory_mb on the node or the server default", memory.limitMB)
	}
	if waitErr != nil {
		// The prelude could not set a limit, so the command never ran.
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) && exitErr.ExitCode() == 125 && strings.Contains(stderr.tailText(), "brokoli: could not apply the") {
			return nil, fmt.Errorf("apply bash limits: %s", stderr.tailText())
		}
		if memory.addressSpace && bashOutOfMemory(stderr.tailText()) {
			return nil, fmt.Errorf("bash command exceeded the memory limit (%d MiB, enforced as address space): "+
				"raise max_memory_mb on the node\nstderr: %s", memory.limitMB, stderr.tailText())
		}
		if lerr := signalBreachError(waitErr, limits); lerr != nil {
			return nil, fmt.Errorf("bash command: %w", lerr)
		}
		if tail := stderr.tailText(); tail != "" {
			return nil, fmt.Errorf("bash command failed: %w\nstderr: %s", waitErr, tail)
		}
		return nil, fmt.Errorf("bash command failed: %w", waitErr)
	}
	return input, nil
}

// logBashLine writes one output line to the run log, within the node's
// budget.
func (r *Runner) logBashLine(nodeID string, level models.LogLevel, prefix, line string, budget *bashLogBudget) {
	if line == "" || r.store == nil || r.run == nil {
		return
	}
	if !budget.take() {
		return
	}
	r.log(nodeID, level, "[%s] %s", prefix, line)
}

type bashLogBudget struct {
	mu        sync.Mutex
	remaining int
	dropped   int
}

func (b *bashLogBudget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.remaining <= 0 {
		b.dropped++
		return false
	}
	b.remaining--
	return true
}

// bashLineWriter splits a stream into lines for emit. It never refuses a
// write: a writer that errors stops exec's copy, and a command writing
// into a pipe nobody reads blocks until the node times out. A line longer
// than bashMaxLineBytes is cut there and the rest of it discarded.
type bashLineWriter struct {
	emit      func(string)
	tail      int
	buf       []byte
	truncated bool
	last      []string
}

func (w *bashLineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		if room := bashMaxLineBytes - len(w.buf); room > 0 {
			if len(chunk) > room {
				w.buf = append(w.buf, chunk[:room]...)
				w.truncated = true
			} else {
				w.buf = append(w.buf, chunk...)
			}
		} else if len(chunk) > 0 {
			w.truncated = true
		}
		if i < 0 {
			break
		}
		w.line()
		p = p[i+1:]
	}
	return n, nil
}

func (w *bashLineWriter) line() {
	text := strings.TrimRight(string(w.buf), "\r")
	if w.truncated {
		text += " ... [line truncated]"
	}
	w.buf, w.truncated = w.buf[:0], false
	if w.tail > 0 && text != "" {
		w.last = append(w.last, text)
		if len(w.last) > w.tail {
			w.last = w.last[1:]
		}
	}
	w.emit(text)
}

// flush emits a final line that had no newline.
func (w *bashLineWriter) flush() {
	if len(w.buf) > 0 || w.truncated {
		w.line()
	}
}

func (w *bashLineWriter) tailText() string {
	return strings.Join(w.last, "\n")
}

func bashEnvironment(raw interface{}) map[string]string {
	out := map[string]string{}
	values, ok := raw.(map[string]interface{})
	if !ok {
		return out
	}
	for key, rawValue := range values {
		if !validBashEnvName(key) {
			continue
		}
		if value, ok := rawValue.(string); ok && !strings.ContainsRune(value, '\x00') {
			out[key] = value
		}
	}
	return out
}

// validBashEnvName is a POSIX shell variable name: bash cannot reference
// any other, so a key like "MY-VAR" would be silently unusable.
func validBashEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func bashConfigErrors(config map[string]interface{}) []string {
	var errors []string
	command, _ := config["command"].(string)
	if strings.TrimSpace(command) == "" {
		errors = append(errors, "'command' is required")
	}
	if ref := platformReference.FindString(command); ref != "" {
		errors = append(errors, fmt.Sprintf(
			"'command' contains %s, which is not substituted in a bash command: "+
				"a substituted value would be run as shell code. Set it in 'env' "+
				"(for example {\"NAME\": \"${param.name}\"}) and use \"$NAME\" in the command", ref))
	}
	if value, ok := config["working_dir"]; ok {
		// Only the type: the allowed directories are the worker's, which
		// the server validating this config may not share.
		if _, ok := value.(string); !ok {
			errors = append(errors, "'working_dir' must be a string")
		}
	}
	if value, ok := config["env"]; ok {
		values, ok := value.(map[string]interface{})
		if !ok {
			errors = append(errors, "'env' must be an object of string values")
		} else {
			for key, rawValue := range values {
				if !validBashEnvName(key) {
					errors = append(errors, fmt.Sprintf("'env' variable name %q is not a valid shell variable name", key))
					break
				}
				value, ok := rawValue.(string)
				if !ok || strings.ContainsRune(value, '\x00') {
					errors = append(errors, "'env' values must be strings without NUL bytes")
					break
				}
			}
		}
	}
	return errors
}
