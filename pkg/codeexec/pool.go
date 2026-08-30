package codeexec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Pool keeps warm code-node workers, one sub-pool per
// (interpreter, limits) pair so a node's python_path override or
// custom ceiling never shares a process with differently-configured
// nodes. Concurrency never regresses relative to spawn-per-invocation:
// when every pooled slot is busy at the cap, Exec spawns a one-shot
// ephemeral worker instead of queueing — today N concurrent code nodes
// get N interpreters, and that stays exactly true, so pool exhaustion
// cannot deadlock anything (queue-at-cap is opt-in for
// memory-constrained hosts via BROKOLI_CODE_POOL_QUEUE=1).
type Pool struct {
	mu         sync.Mutex
	subs       map[string]*subPool
	total      int // pooled workers alive (busy + idle), excludes one-shots
	maxWorkers int
	maxExecs   int
	idleAfter  time.Duration
	queueAtCap bool
	sockDir    string
	boots      atomic.Int64
	janitor    sync.Once
	closed     bool
	waiters    []chan struct{}
}

type subPool struct {
	interpreter string
	limits      Limits
	idle        []*Worker
}

// PoolEnabled reports whether the warm pool runs code nodes. Default ON
// for unix (ADR-029, benchmarked in the wiring PR); BROKOLI_CODE_POOL=0
// keeps the legacy spawn-per-invocation path for one release. Windows
// always takes the legacy path — no process groups, no AF_UNIX story,
// no rlimits.
func PoolEnabled() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return os.Getenv("BROKOLI_CODE_POOL") != "0"
}

var (
	globalPool     *Pool
	globalPoolOnce sync.Once
)

// GlobalPool is the process singleton (engine runner and remote
// instance workers are separate processes; each gets its own).
func GlobalPool() *Pool {
	globalPoolOnce.Do(func() { globalPool = NewPool() })
	return globalPool
}

// NewPool builds a pool from the environment knobs.
func NewPool() *Pool {
	maxWorkers := envInt("BROKOLI_CODE_POOL_MAX_WORKERS")
	if maxWorkers <= 0 {
		maxWorkers = runtime.NumCPU() / 2
		if maxWorkers < 2 {
			maxWorkers = 2
		}
	}
	maxExecs := envInt("BROKOLI_CODE_POOL_MAX_EXECS")
	if maxExecs <= 0 {
		maxExecs = 500
	}
	idle := time.Duration(envInt("BROKOLI_CODE_POOL_IDLE_SECONDS")) * time.Second
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	dir, err := os.MkdirTemp(os.TempDir(), "brokoli-codeexec-")
	if err != nil {
		dir = os.TempDir()
	}
	_ = os.Chmod(dir, 0o700) // #nosec G302 -- a directory (Unix sockets live here): the execute bit is required to enter it, and 0700 is the minimal mode that works.
	return &Pool{
		subs:       map[string]*subPool{},
		maxWorkers: maxWorkers,
		maxExecs:   maxExecs,
		idleAfter:  idle,
		queueAtCap: os.Getenv("BROKOLI_CODE_POOL_QUEUE") == "1",
		sockDir:    dir,
	}
}

// WorkerBoots is the pool-lifetime count of processes spawned —
// pooled and one-shot — for tests and the audit trail.
func (p *Pool) WorkerBoots() int64 { return p.boots.Load() }

func subKey(interpreter string, limits Limits) string {
	raw, _ := json.Marshal(limits)
	return interpreter + "|" + string(raw)
}

// acquire returns a warm worker for the key, or spawns one. The bool
// reports warmth (true = reused). At the cap it either spawns a
// one-shot (default) or blocks until a slot frees (opt-in queue).
func (p *Pool) acquire(ctx context.Context, interpreter string, limits Limits) (*Worker, bool, error) {
	p.janitor.Do(func() { go p.reapLoop() })
	key := subKey(interpreter, limits)
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, false, fmt.Errorf("code worker pool is shut down")
		}
		sub := p.subs[key]
		if sub == nil {
			sub = &subPool{interpreter: interpreter, limits: limits}
			p.subs[key] = sub
		}
		if n := len(sub.idle); n > 0 {
			w := sub.idle[n-1]
			sub.idle = sub.idle[:n-1]
			p.mu.Unlock()
			return w, true, nil
		}
		if p.total < p.maxWorkers {
			p.total++
			p.mu.Unlock()
			w, err := spawnWorker(ctx, interpreter, limits, p.sockDir, false)
			if err != nil {
				p.mu.Lock()
				p.total--
				p.notifyWaiterLocked()
				p.mu.Unlock()
				return nil, false, err
			}
			p.boots.Add(1)
			return w, false, nil
		}
		if !p.queueAtCap {
			p.mu.Unlock()
			// Overflow: same protocol, one execution, exits by itself.
			w, err := spawnWorker(ctx, interpreter, limits, p.sockDir, true)
			if err != nil {
				return nil, false, err
			}
			p.boots.Add(1)
			return w, false, nil
		}
		waiter := make(chan struct{}, 1)
		p.waiters = append(p.waiters, waiter)
		p.mu.Unlock()
		select {
		case <-waiter:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

// release returns a healthy worker to its sub-pool, or retires it.
func (p *Pool) release(w *Worker, interpreter string, limits Limits, healthy bool) {
	if w.oneShot {
		w.kill()
		return
	}
	w.execs++
	if !healthy || w.execs >= p.maxExecs {
		w.kill()
		p.mu.Lock()
		p.total--
		p.notifyWaiterLocked()
		p.mu.Unlock()
		return
	}
	w.idleAt = time.Now()
	key := subKey(interpreter, limits)
	p.mu.Lock()
	if p.closed || p.subs[key] == nil {
		p.mu.Unlock()
		w.kill()
		return
	}
	p.subs[key].idle = append(p.subs[key].idle, w)
	p.notifyWaiterLocked()
	p.mu.Unlock()
}

func (p *Pool) notifyWaiterLocked() {
	if len(p.waiters) > 0 {
		close(p.waiters[0])
		p.waiters = p.waiters[1:]
	}
}

// reapLoop retires workers idle past the deadline.
func (p *Pool) reapLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		cutoff := time.Now().Add(-p.idleAfter)
		for _, sub := range p.subs {
			kept := sub.idle[:0]
			for _, w := range sub.idle {
				if w.idleAt.Before(cutoff) {
					go w.shutdown()
					p.total--
				} else {
					kept = append(kept, w)
				}
			}
			sub.idle = kept
		}
		p.mu.Unlock()
	}
}

// Close shuts every worker down. For tests and engine shutdown.
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	var all []*Worker
	for _, sub := range p.subs {
		all = append(all, sub.idle...)
		sub.idle = nil
	}
	for _, waiter := range p.waiters {
		close(waiter)
	}
	p.waiters = nil
	p.mu.Unlock()
	for _, w := range all {
		w.shutdown()
	}
	_ = os.RemoveAll(p.sockDir)
}

func unmarshalStrictEnough(payload []byte, v interface{}) error {
	return json.Unmarshal(payload, v)
}
