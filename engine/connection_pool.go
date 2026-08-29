package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Connection pools (#398): a named concurrency budget nodes draw from.
//
// The engine's one pre-existing knob (SetMaxConcurrentRuns) protects the
// worker; it protects nothing behind it -- forty pipelines pointed at one
// database each hold a run slot happily while the database saturates.
// The budget lives where the contention lives: Connection.MaxConcurrent
// bounds how many node executions hold that connection at once, and a
// node waits -- visibly, in its run log -- rather than failing when the
// pool is full.
//
// PER ENGINE INSTANCE: this is an in-process semaphore. A multi-instance
// deployment multiplies every budget by the instance count; the
// store-backed cluster-wide claim (the execution-attempt lease shape) is
// the follow-on, and this version is deliberately not described as
// global anywhere it surfaces.

// connectionPools tracks in-use slot counts per pool name. Limits are
// not stored here: the caller passes the limit on every Acquire, read
// fresh from the connection record, so an operator raising or lowering
// max_concurrent applies to every acquire after the edit with no resize
// machinery.
type connectionPools struct {
	mu    sync.Mutex
	inUse map[string]int
	conds map[string]*sync.Cond
}

func newConnectionPools() *connectionPools {
	return &connectionPools{inUse: map[string]int{}, conds: map[string]*sync.Cond{}}
}

// Acquire takes one slot in the named pool, blocking while inUse >= limit.
// onWait is called once, outside the lock, the first time this acquire
// actually has to wait -- the visibility hook, because a silent wait is
// how operators conclude the system is hung. A limit <= 0 acquires
// nothing and always succeeds (unlimited, the 0-default's meaning).
// ctx ending while waiting abandons the acquire with ctx's error.
func (p *connectionPools) Acquire(ctx context.Context, pool string, limit int, onWait func(inUse, limit int)) error {
	if limit <= 0 || pool == "" {
		return nil
	}
	p.mu.Lock()
	c := p.conds[pool]
	if c == nil {
		c = sync.NewCond(&p.mu)
		p.conds[pool] = c
	}
	// A cond can't select on a context; wake the herd when ctx dies and
	// let the loop below observe ctx.Err.
	stop := context.AfterFunc(ctx, func() {
		p.mu.Lock()
		c.Broadcast()
		p.mu.Unlock()
	})
	defer stop()

	waited := false
	for p.inUse[pool] >= limit {
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return err
		}
		if !waited && onWait != nil {
			waited = true
			in := p.inUse[pool]
			p.mu.Unlock()
			onWait(in, limit)
			p.mu.Lock()
			continue // state may have moved while logging; re-check
		}
		c.Wait()
	}
	p.inUse[pool]++
	p.mu.Unlock()
	return nil
}

// Release returns one slot and wakes waiters. Releasing an empty pool is
// a no-op rather than a panic: the release path runs in the node's own
// goroutine and must never take the run down over bookkeeping.
func (p *connectionPools) Release(pool string) {
	if pool == "" {
		return
	}
	p.mu.Lock()
	if p.inUse[pool] > 0 {
		p.inUse[pool]--
	}
	if c := p.conds[pool]; c != nil {
		c.Broadcast()
	}
	p.mu.Unlock()
}

// acquireConnectionSlot resolves the node's pool to a budget and takes a
// slot, returning the release func. Three shapes come back:
//
//   - the pool names a connection with max_concurrent > 0: block until a
//     slot frees (logging the wait into the node's run log), release on
//     return;
//   - the budget is 0/absent, or an implicit conn_id doesn't resolve: no
//     limiting -- the resolver already owns warning about bad conn_ids;
//   - an EXPLICIT pool: naming no connection is an error, because a
//     budget that silently doesn't limit is a disabled control, and this
//     codebase has been bitten by exactly that shape before.
func (r *Runner) acquireConnectionSlot(node models.Node, pool string, explicit bool, attempt int) (func(), error) {
	conn, err := r.connResolver.store.GetConnection(pool)
	if err != nil {
		if explicit {
			return nil, fmt.Errorf(
				"node %s (%s) names pool %q, which matches no connection: %w -- a budget that "+
					"cannot be enforced must fail loudly, not silently not limit", node.Name, node.ID, pool, err)
		}
		return func() {}, nil
	}
	if conn.MaxConcurrent <= 0 {
		return func() {}, nil
	}
	if err := r.connResolver.pools.Acquire(r.ctx, pool, conn.MaxConcurrent, func(inUse, limit int) {
		r.log(node.ID, models.LogLevelInfo,
			"waiting for connection pool %q (%d/%d in use)", pool, inUse, limit)
	}); err != nil {
		return nil, fmt.Errorf("wait for connection pool %q (attempt %d): %w", pool, attempt, err)
	}
	return func() { r.connResolver.pools.Release(pool) }, nil
}
