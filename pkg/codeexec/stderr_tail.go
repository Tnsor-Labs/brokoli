package codeexec

import (
	"strings"
	"sync"
)

const workerStderrTailBytes = 8 * 1024

type stderrTail struct {
	mu        sync.Mutex
	buf       []byte
	truncated bool
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(p) >= workerStderrTailBytes {
		t.buf = append(t.buf[:0], p[len(p)-workerStderrTailBytes:]...)
		t.truncated = true
		return n, nil
	}
	if excess := len(t.buf) + len(p) - workerStderrTailBytes; excess > 0 {
		copy(t.buf, t.buf[excess:])
		t.buf = t.buf[:len(t.buf)-excess]
		t.truncated = true
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

func (t *stderrTail) Reset() {
	t.mu.Lock()
	t.buf = t.buf[:0]
	t.truncated = false
	t.mu.Unlock()
}

func (t *stderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	value := strings.ToValidUTF8(string(t.buf), "�")
	if t.truncated {
		return "[worker stderr truncated to last 8 KiB]\n" + value
	}
	return value
}
