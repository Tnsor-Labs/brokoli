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
	"github.com/Tnsor-Labs/brokoli/pkg/runas"
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
	home      string // the worker's own folder when it runs as its tenant's user; removed with it
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
	id, err := runas.ForContext(ctx)
	if err != nil {
		return nil, err
	}
	if id.Enabled() {
		return spawnWorkerAs(ctx, id, language, interpreter, workerMain, limits, oneShot)
	}

	// A unique path per worker, from the OS rather than the clock.
	// time.Now().UnixNano() looks unique and is not: two workers spawned
	// in the same nanosecond tick -- which the pool does routinely when
	// it fills, and which CI reproduces -- get the same path, and the
	// second fails with "bind: address already in use" (#449).
	//
	// os.MkdirTemp is the uniqueness primitive: the kernel guarantees the
	// directory name is unused, so the socket inside it cannot collide
	// however many workers start at once.
	sockHome, err := os.MkdirTemp(sockDir, "w-")
	if err != nil {
		return nil, fmt.Errorf("create worker socket dir: %w", err)
	}
	sockPath := filepath.Join(sockHome, "w.sock")
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}
	// Safe to remove on return, including every error path: this
	// function blocks on listener.Accept() below, so the child has
	// already connected by the time it returns, and a Unix socket keeps
	// working once its path is unlinked. Without this the directory
	// would leak per worker -- Worker.cleanup only knows the socket
	// file, whose removal is now redundant but harmless.
	defer func() { _ = os.RemoveAll(sockHome) }()
	defer listener.Close()
	cmd := workerCommand(ctx, language, interpreter, workerMain, sockPath, limits, oneShot)
	cmd.Env = append(WorkerEnv(), limits.Env()...)
	return startWorker(ctx, cmd, listener, language, sockPath, "", limits, oneShot)
}

// spawnWorkerAs is spawnWorker for a worker that runs as its tenant's own
// user. Everything the worker touches lives in one folder that user owns:
// its socket, its HOME and TMPDIR, and its own copy of the wrapper (the
// shared materialized copy is the server's, and a folder every tenant
// could write would let one tenant's code replace another's wrapper).
func spawnWorkerAs(ctx context.Context, id runas.Identity, language, interpreter, workerMain string, limits Limits, oneShot bool) (*Worker, error) {
	home, err := id.MkdirTemp("w-")
	if err != nil {
		return nil, fmt.Errorf("create worker folder: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(home)
		}
	}()
	src := filepath.Dir(workerMain)
	dst := filepath.Join(home, filepath.Base(src))
	if err := copyFlatDir(src, dst); err != nil {
		return nil, fmt.Errorf("copy code wrapper for %s: %w", id, err)
	}
	workerMain = filepath.Join(dst, filepath.Base(workerMain))
	sockPath := filepath.Join(home, "w.sock")
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}
	defer listener.Close()
	// After the socket exists: connecting needs write permission on it.
	if err := id.Own(home); err != nil {
		return nil, err
	}
	cmd := workerCommand(ctx, language, interpreter, workerMain, sockPath, limits, oneShot)
	cmd.Env = id.Env(append(WorkerEnv(), limits.Env()...), home)
	id.Apply(cmd)
	w, err := startWorker(ctx, cmd, listener, language, sockPath, home, limits, oneShot)
	ok = err == nil
	return w, err
}

// copyFlatDir copies the regular files directly in src into a new dst.
func copyFlatDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dst, 0o700); err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(src, e.Name())) // #nosec G304 -- src is the server's own materialized wrapper folder
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), raw, 0o600); err != nil { // #nosec G703 -- dst is a folder this process just made, and the name is one of the wrapper's own files
			return err
		}
	}
	return nil
}

func workerCommand(ctx context.Context, language, interpreter, workerMain, sockPath string, limits Limits, oneShot bool) *exec.Cmd {
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
	proctree.ConfigureProcessGroup(cmd)
	cmd.Cancel = func() error { return proctree.TerminateProcessTree(cmd.Process) }
	cmd.WaitDelay = terminationGrace
	tail := &stderrTail{}
	cmd.Stdout = nil
	cmd.Stderr = tail
	return cmd
}

// startWorker starts cmd, applies its limits, and waits for it to connect
// to listener and say hello.
func startWorker(ctx context.Context, cmd *exec.Cmd, listener net.Listener, language, sockPath, home string, limits Limits, oneShot bool) (*Worker, error) {
	tail := cmd.Stderr.(*stderrTail)
	interpreter := cmd.Path
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn code worker (%s): %w", interpreter, err)
	}
	w := &Worker{
		cmd:      cmd,
		sockPath: sockPath,
		home:     home,
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
		if w.home != "" {
			_ = os.RemoveAll(w.home)
		}
	})
}

// WrapperPathIn copies the Python wrapper into a new folder inside dir and
// returns its wrapper.py, for a one-shot child that runs as its tenant's
// user and so cannot read the server's own materialized copy.
func WrapperPathIn(dir string) (string, error) {
	shared, err := WrapperPath()
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "wrapper")
	if err := copyFlatDir(filepath.Dir(shared), dst); err != nil {
		return "", err
	}
	return filepath.Join(dst, filepath.Base(shared)), nil
}
