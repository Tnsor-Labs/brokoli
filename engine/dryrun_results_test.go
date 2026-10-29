package engine

import (
	"fmt"
	"sync"
	"testing"
)

// The nodes of a wave run at once and each records its preview result.
// The map was written with no lock: a data race, and concurrent map writes
// end the process. Run under -race, which CI does.
func TestDryRunResultsAreRecordedConcurrently(t *testing.T) {
	r := &Runner{}
	const nodes = 32
	var wg sync.WaitGroup
	for i := 0; i < nodes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.recordDryRun(&DryRunNodeResult{NodeID: fmt.Sprintf("n%d", i), Status: "success"})
		}(i)
	}
	wg.Wait()
	if len(r.dryRunResults) != nodes {
		t.Errorf("%d results recorded, want %d", len(r.dryRunResults), nodes)
	}
}
