package engine

import (
	"sync"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// logBatcher stores a run's log lines with group commit. A line written
// while no write is in progress is stored at once, alone. Lines that
// arrive while one is in progress wait, and the next write stores them all
// in one transaction. Each caller still returns only once its own line is
// stored (or has failed), so a line is durable when log returns and lines
// keep the order they arrived in, as with one write per line.
//
// The zero value is ready to use.
type logBatcher struct {
	mu      sync.Mutex
	pending []*pendingLog
	writing bool
}

type pendingLog struct {
	entry *models.LogEntry
	err   error
	// done receives once: true when this caller is to write the next
	// batch, false when another caller has written its line.
	done chan bool
}

// append stores entry through s, batched with whatever else is waiting.
func (b *logBatcher) append(s store.LogBatchAppender, entry *models.LogEntry) error {
	p := &pendingLog{entry: entry, done: make(chan bool, 1)}
	b.mu.Lock()
	b.pending = append(b.pending, p)
	lead := !b.writing
	b.writing = true
	b.mu.Unlock()

	if !lead {
		lead = <-p.done
	}
	if lead {
		b.write(s)
	}
	return p.err
}

// write stores every waiting line, its caller's included, then hands the
// next batch to the first caller that arrived meanwhile.
func (b *logBatcher) write(s store.LogBatchAppender) {
	b.mu.Lock()
	batch := b.pending
	b.pending = nil
	b.mu.Unlock()

	entries := make([]*models.LogEntry, len(batch))
	for i, p := range batch {
		entries[i] = p.entry
	}
	if err := s.AppendLogs(entries); err != nil {
		// One bad line must not cost the others theirs: store them one
		// at a time, so only the line that fails reports a failure.
		for _, p := range batch {
			p.err = s.AppendLogs([]*models.LogEntry{p.entry})
		}
	}

	b.mu.Lock()
	var next *pendingLog
	if len(b.pending) > 0 {
		next = b.pending[0]
	} else {
		b.writing = false
	}
	b.mu.Unlock()

	for _, p := range batch {
		p.done <- false
	}
	if next != nil {
		next.done <- true
	}
}
