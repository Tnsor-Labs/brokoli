package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// runBash executes a trusted worker command and preserves the input dataset.
// This is intentionally a shell operator, not a sandbox: only trusted
// pipeline authors should be allowed to create bash nodes.
func (r *Runner) runBash(ctx context.Context, node models.Node, input *common.DataSet) (*common.DataSet, error) {
	command, _ := node.Config["command"].(string)
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("bash node requires a non-empty 'command'")
	}

	workingDir, _ := node.Config["working_dir"].(string)
	if workingDir == "" {
		workingDir = os.TempDir()
	}
	if err := common.PathAllowed(workingDir); err != nil {
		return nil, fmt.Errorf("bash working_dir: %w", err)
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		return nil, fmt.Errorf("bash executable is not available: %w", err)
	}
	cmd := exec.CommandContext(ctx, bash, "-o", "pipefail", "-c", command)
	cmd.Dir = workingDir
	cmd.Env = append(codeexec.WorkerEnv(), "BROKOLI_NODE_ID="+node.ID)
	for key, value := range bashEnvironment(node.Config["env"]) {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("bash stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("bash stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start bash: %w", err)
	}

	logLines := func(prefix string, stream io.Reader) error {
		scanner := bufio.NewScanner(stream)
		// Commands can emit generated lines larger than Scanner's default.
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line != "" && r.store != nil && r.run != nil {
				r.log(node.ID, models.LogLevelInfo, "[%s] %s", prefix, line)
			}
		}
		return scanner.Err()
	}

	stdoutErr := make(chan error, 1)
	stderrErr := make(chan error, 1)
	go func() { stdoutErr <- logLines("bash", stdout) }()
	go func() { stderrErr <- logLines("bash stderr", stderr) }()
	waitErr := cmd.Wait()
	if err := <-stdoutErr; err != nil && !errors.Is(err, os.ErrClosed) {
		return nil, fmt.Errorf("read bash stdout: %w", err)
	}
	if err := <-stderrErr; err != nil && !errors.Is(err, os.ErrClosed) {
		return nil, fmt.Errorf("read bash stderr: %w", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if waitErr != nil {
		return nil, fmt.Errorf("bash command failed: %w", waitErr)
	}
	return input, nil
}

func bashEnvironment(raw interface{}) map[string]string {
	out := map[string]string{}
	values, ok := raw.(map[string]interface{})
	if !ok {
		return out
	}
	for key, rawValue := range values {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			continue
		}
		if value, ok := rawValue.(string); ok && !strings.ContainsRune(value, '\x00') {
			out[key] = value
		}
	}
	return out
}

func bashConfigErrors(config map[string]interface{}) []string {
	var errors []string
	command, _ := config["command"].(string)
	if strings.TrimSpace(command) == "" {
		errors = append(errors, "'command' is required")
	}
	if value, ok := config["working_dir"]; ok {
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
				if key == "" || strings.ContainsAny(key, "=\x00") {
					errors = append(errors, "'env' contains an invalid variable name")
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
