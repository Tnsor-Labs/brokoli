package engine

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// perLineStore hides LogBatchAppender, so the runner writes one line per
// transaction, as it did before batching.
type perLineStore struct{ store.Store }

// Nodes running side by side, each writing log lines through the runner,
// against a real SQLite store.
func BenchmarkRunnerLogConcurrentNodes(b *testing.B) {
	for _, nodes := range []int{1, 8, 32} {
		for _, batched := range []bool{false, true} {
			name := fmt.Sprintf("nodes=%d/per-line", nodes)
			if batched {
				name = fmt.Sprintf("nodes=%d/batched", nodes)
			}
			b.Run(name, func(b *testing.B) {
				st, err := store.NewSQLiteStore(filepath.Join(b.TempDir(), "l.db"))
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { st.Close() })
				now := time.Now().UTC()
				if err := st.CreatePipeline(&models.Pipeline{ID: "p", Name: "p", CreatedAt: now, UpdatedAt: now}); err != nil {
					b.Fatal(err)
				}
				run := &models.Run{ID: "r", PipelineID: "p", Status: models.RunStatusRunning, StartedAt: &now}
				if err := st.CreateRun(run); err != nil {
					b.Fatal(err)
				}
				var s store.Store = st
				if !batched {
					s = perLineStore{st}
				}
				r := &Runner{store: s, run: run}
				const linesPerNode = 20
				for b.Loop() {
					var wg sync.WaitGroup
					for n := 0; n < nodes; n++ {
						wg.Add(1)
						go func(n int) {
							defer wg.Done()
							for i := 0; i < linesPerNode; i++ {
								r.log(fmt.Sprintf("n%d", n), models.LogLevelInfo, "chunk %d written", i)
							}
						}(n)
					}
					wg.Wait()
				}
				b.ReportMetric(float64(b.Elapsed().Microseconds())/float64(b.N*nodes*linesPerNode), "µs/line")
			})
		}
	}
}
