package codeexec

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	cmd      *exec.Cmd
	conn     net.Conn
	rw       *bufio.ReadWriter
	hello    HelloMsg
	sockPath string
	execs    int
	idleAt   time.Time
	oneShot  bool
}

// spawnWorker starts one worker process and waits for its hello.
func spawnWorker(ctx context.Context, interpreter string, limits Limits, sockDir string, oneShot bool) (*Worker, error) {
	wrapperPath, err := WrapperPath()
	if err != nil {
		return nil, err
	}
	workerMain := filepath.Join(filepath.Dir(wrapperPath), "worker_main.py")

	sockPath := filepath.Join(sockDir, fmt.Sprintf("w-%d.sock", time.Now().UnixNano()))
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}
	defer listener.Close()

	args := []string{workerMain, "--socket", sockPath}
	if oneShot {
		args = append(args, "--one-shot")
	}
	// #nosec G204 -- the interpreter is resolved server-side (or the
	// node's own python_path, the baseline-accepted case: a code node
	// exists to run pipeline-author code) and the script is our own
	// materialized worker_main.py.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), interpreter, args...)
	cmd.Env = append(os.Environ(), limits.Env()...)
	proctree.ConfigureProcessGroup(cmd)
	cmd.Cancel = func() error { return proctree.TerminateProcessTree(cmd.Process) }
	cmd.WaitDelay = terminationGrace
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn code worker (%s): %w", interpreter, err)
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
			killWorkerProcess(cmd)
			return nil, fmt.Errorf("worker accept: %w", a.err)
		}
		conn = a.conn
	case <-time.After(spawnTimeout):
		killWorkerProcess(cmd)
		return nil, fmt.Errorf("code worker did not connect within %s", spawnTimeout)
	case <-ctx.Done():
		killWorkerProcess(cmd)
		return nil, ctx.Err()
	}

	w := &Worker{
		cmd:      cmd,
		conn:     conn,
		rw:       bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)),
		sockPath: sockPath,
		oneShot:  oneShot,
	}
	_ = conn.SetReadDeadline(time.Now().Add(spawnTimeout))
	frameType, payload, err := ReadFrame(w.rw.Reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil || frameType != FrameHello {
		w.kill()
		return nil, fmt.Errorf("worker hello: %w (frame %#x)", err, frameType)
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
	return w, nil
}

// kill terminates the worker's whole process group and releases its
// socket. Used on cancel, timeout, protocol corruption, and recycle —
// every path where the process's state is no longer trusted.
func (w *Worker) kill() {
	if w.conn != nil {
		_ = w.conn.Close()
	}
	killWorkerProcess(w.cmd)
	_ = os.Remove(w.sockPath)
}

// shutdown asks the worker to exit cleanly, then enforces it.
func (w *Worker) shutdown() {
	if w.rw != nil {
		_ = WriteFrame(w.rw.Writer, FrameShutdown, struct{}{})
		_ = w.rw.Flush()
	}
	if w.cmd != nil && w.cmd.Process != nil {
		done := make(chan struct{})
		go func() { _ = w.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(terminationGrace):
		}
	}
	w.kill()
}

func killWorkerProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = proctree.KillProcessTree(cmd.Process)
	// Reap; bounded because the group just got SIGKILL.
	waitDone := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(terminationGrace):
	}
}
