package codeexec

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/proctree"
)

// spawnTimeout bounds worker boot: interpreter start + hello. A worker
// that cannot say hello in this window is killed and reported.
const spawnTimeout = 15 * time.Second

// terminationGrace mirrors pkg/plugins: SIGTERM the group, then
// WaitDelay escalates.
const terminationGrace = 5 * time.Second

// Worker is one long-lived interpreter process plus its socket.
type Worker struct {
	cmd       *exec.Cmd
	conn      net.Conn
	rw        *bufio.ReadWriter
	hello     HelloMsg
	sockPath  string
	execs     int
	idleAt    time.Time
	oneShot   bool
	stderr    *stderrTail
	waitDone  chan struct{}
	waitErr   error
	killOnce  sync.Once
	cleanOnce sync.Once
}

// spawnWorker starts one worker process and waits for its hello.
func spawnWorker(ctx context.Context, language, interpreter string, limits Limits, sockDir string, oneShot bool) (*Worker, error) {
	var workerMain string
	var err error
	if language == "typescript" {
		workerMain, err = JSWrapperPath()
	} else {
		var wrapperPath string
		wrapperPath, err = WrapperPath()
		workerMain = filepath.Join(filepath.Dir(wrapperPath), "worker_main.py")
	}
	if err != nil {
		return nil, err
	}

	sockPath := filepath.Join(sockDir, fmt.Sprintf("w-%d.sock", time.Now().UnixNano()))
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}
	defer listener.Close()

	args := []string{workerMain, "--socket", sockPath}
	if language == "typescript" && limits.MemoryMB > 0 {
		args = append([]string{fmt.Sprintf("--max-old-space-size=%d", limits.MemoryMB)}, args...)
	}
	if oneShot {
		args = append(args, "--one-shot")
	}
	// #nosec G204 -- the interpreter is resolved server-side (or the
	// node's own python_path, the baseline-accepted case: a code node
	// exists to run pipeline-author code) and the script is our own
	// materialized worker_main.py.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), interpreter, args...)
	cmd.Env = append(workerEnv(), limits.Env()...)
	proctree.ConfigureProcessGroup(cmd)
	cmd.Cancel = func() error { return proctree.TerminateProcessTree(cmd.Process) }
	cmd.WaitDelay = terminationGrace
	tail := &stderrTail{}
	cmd.Stdout = nil
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn code worker (%s): %w", interpreter, err)
	}
	w := &Worker{
		cmd:      cmd,
		sockPath: sockPath,
		oneShot:  oneShot,
		stderr:   tail,
		waitDone: make(chan struct{}),
	}
	go func() {
		w.waitErr = cmd.Wait()
		close(w.waitDone)
	}()
	fileSizeBytes := uint64(0)
	if limits.FileSizeMB > 0 {
		fileSizeBytes = uint64(limits.FileSizeMB) * 1024 * 1024
	}
	if err := proctree.ApplyRlimits(cmd.Process.Pid, proctree.Rlimits{
		CPUSeconds: uint64(max(limits.CPUSeconds, 0)), FileSizeBytes: fileSizeBytes, OpenFiles: uint64(max(limits.OpenFiles, 0)),
	}); err != nil {
		w.kill()
		return nil, fmt.Errorf("apply code worker limits: %w\nstderr: %s", err, strings.TrimSpace(tail.String()))
	}

	type accepted struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan accepted, 1)
	go func() {
		conn, err := listener.Accept()
		acceptCh <- accepted{conn, err}
	}()

	var conn net.Conn
	select {
	case a := <-acceptCh:
		if a.err != nil {
			w.kill()
			return nil, fmt.Errorf("worker accept: %w", a.err)
		}
		conn = a.conn
	case <-w.waitDone:
		w.cleanup()
		return nil, fmt.Errorf("code worker exited before connecting: %v\nstderr: %s", w.waitErr, strings.TrimSpace(tail.String()))
	case <-time.After(spawnTimeout):
		w.kill()
		return nil, fmt.Errorf("code worker did not connect within %s", spawnTimeout)
	case <-ctx.Done():
		w.kill()
		return nil, ctx.Err()
	}

	w.conn = conn
	w.rw = bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	_ = conn.SetReadDeadline(time.Now().Add(spawnTimeout))
	frameType, payload, err := ReadFrame(w.rw.Reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || frameType != FrameHello {
		w.kill()
		return nil, fmt.Errorf("worker hello: %w (frame %#x)\nstderr: %s", err, frameType, strings.TrimSpace(tail.String()))
	}
	if err := unmarshalStrictEnough(payload, &w.hello); err != nil {
		w.kill()
		return nil, fmt.Errorf("worker hello decode: %w", err)
	}
	if !isCodeProtocolVersionSupported(w.hello.ProtocolVersion) {
		w.kill()
		return nil, fmt.Errorf("code worker speaks protocol %d; host supports %v",
			w.hello.ProtocolVersion, SupportedCodeProtocolVersions)
	}
	if w.hello.Language != language {
		w.kill()
		return nil, fmt.Errorf("code worker language %q does not match requested %q", w.hello.Language, language)
	}
	if want := wrapperVersionForLanguage(language); w.hello.WrapperVersion != want {
		w.kill()
		return nil, fmt.Errorf("%s code worker wrapper version %d does not match embedded contract %d", language, w.hello.WrapperVersion, want)
	}
	return w, nil
}

// kill terminates the worker's whole process group and releases its
// socket. Used on cancel, timeout, protocol corruption, and recycle —
// every path where the process's state is no longer trusted.
func (w *Worker) kill() {
	w.killOnce.Do(func() {
		if w.conn != nil {
			_ = w.conn.Close()
		}
		if w.cmd != nil && w.cmd.Process != nil {
			_ = proctree.KillProcessTree(w.cmd.Process)
		}
		w.awaitExit(terminationGrace)
		w.cleanup()
	})
}

// shutdown asks the worker to exit cleanly, then enforces it.
func (w *Worker) shutdown() {
	if w.rw != nil {
		_ = WriteFrame(w.rw.Writer, FrameShutdown, struct{}{})
		_ = w.rw.Flush()
	}
	if !w.awaitExit(terminationGrace) {
		w.kill()
		return
	}
	w.cleanup()
}

func (w *Worker) awaitExit(timeout time.Duration) bool {
	if w.waitDone == nil {
		return true
	}
	select {
	case <-w.waitDone:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (w *Worker) cleanup() {
	w.cleanOnce.Do(func() {
		if w.conn != nil {
			_ = w.conn.Close()
		}
		_ = os.Remove(w.sockPath)
	})
}
