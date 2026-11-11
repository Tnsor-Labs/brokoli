package engine

import (
	"sync"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// maxPendingLogLines is how many lines may wait to be stored before a
// caller logging another one waits for the writer to catch up, so a run
// logging faster than the database can store holds bounded memory.
const maxPendingLogLines = 10000

// logBatcher stores a run's log lines in the background, with group
// commit: one writer stores whatever has queued since its last write in one
// transaction, in the order the lines arrived.
//
// A caller does not wait for its line to be stored. It waited, and with a
// node mapped over many items each logging as it starts and ends, those
// waits were most of what an item spent blocked: a 500-item fan-out ran 75
// items a second, and 100 with the per-item lines removed (#967 benchmark).
// What the wait bought is that a line was durable when log returned; the
// run's durable record is its event log, which recovery reads, not its
// log lines. flush restores the stronger point where it matters: the
// runner calls it before recording the run's final status, and when
// Execute returns, so whoever sees a run finish sees all of its lines. A
// process that dies loses the lines still queued.
//
// A line that cannot be stored is reported in the server log, and does not
// cost the lines batched with it theirs.
//
// The zero value is ready to use.
type logBatcher struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []*models.LogEntry
	// inflight is how many lines the writer is storing now; they count
	// toward maxPendingLogLines until stored.
	inflight int
	writing  bool
	// onFailed reports a line that could not be stored. Nil means the
	// server log; tests set it.
	onFailed func(entry *models.LogEntry, err error)
}

func (b *logBatcher) init() {
	if b.cond == nil {
		b.cond = sync.NewCond(&b.mu)
	}
}

// append queues entry to be stored through s and returns without waiting
// for it, unless maxPendingLogLines are already waiting.
func (b *logBatcher) append(s store.LogBatchAppender, entry *models.LogEntry) {
	b.mu.Lock()
	b.init()
	for len(b.pending)+b.inflight >= maxPendingLogLines {
		b.cond.Wait()
	}
	b.pending = append(b.pending, entry)
	if !b.writing {
		b.writing = true
		go b.drain(s)
	}
	b.mu.Unlock()
}

// drain stores what has queued, batch after batch, until nothing is left.
func (b *logBatcher) drain(s store.LogBatchAppender) {
	for {
		b.mu.Lock()
		if len(b.pending) == 0 {
			b.writing = false
			b.cond.Broadcast()
			b.mu.Unlock()
			return
		}
		batch := b.pending
		b.pending = nil
		b.inflight = len(batch)
		b.mu.Unlock()
		b.store(s, batch)
		b.mu.Lock()
		b.inflight = 0
		b.cond.Broadcast()
		b.mu.Unlock()
	}
}

func (b *logBatcher) store(s store.LogBatchAppender, batch []*models.LogEntry) {
	if err := s.AppendLogs(batch); err == nil {
		return
	}
	// One bad line must not cost the others theirs: store them one at a
	// time, so only the line that fails is lost.
	for _, e := range batch {
		if err := s.AppendLogs([]*models.LogEntry{e}); err != nil {
			if b.onFailed != nil {
				b.onFailed(e, err)
			} else {
				common.SLog().Warn("append log entry failed",
					common.RunAttr(e.RunID), common.NodeAttr(e.NodeID), "error", err)
			}
		}
	}
}

// flush returns once every line appended before it has been stored or
// reported.
func (b *logBatcher) flush() {
	b.mu.Lock()
	b.init()
	for b.writing || len(b.pending) > 0 {
		b.cond.Wait()
	}
	b.mu.Unlock()
}
