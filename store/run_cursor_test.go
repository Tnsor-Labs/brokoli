package store

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// seedRuns creates n runs for a pipeline and returns their IDs, newest
// first — the order a caller paging the history should see them in.
func seedRuns(t *testing.T, s *SQLiteStore, pipelineID string, n int) []string {
	t.Helper()
	// runs.pipeline_id is a foreign key, so the pipeline has to exist.
	if err := s.CreatePipeline(&models.Pipeline{
		ID:        pipelineID,
		Name:      pipelineID,
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreatePipeline %s: %v", pipelineID, err)
	}
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		started := time.Now().UTC().Add(time.Duration(i) * time.Second)
		r := &models.Run{
			ID:         common.NewID(),
			PipelineID: pipelineID,
			Status:     models.RunStatusSuccess,
			StartedAt:  &started,
		}
		if err := s.CreateRun(r); err != nil {
			t.Fatalf("CreateRun %d: %v", i, err)
		}
		ids = append(ids, r.ID)
	}
	// IDs are UUIDv7, so creation order is ascending; newest first is the
	// reverse.
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	return ids
}

// The headline acceptance criterion: a pipeline with a very long history
// can be paged through completely, rather than stopping at the old
// hard-coded 50.
func TestListRunsByPipelineCursor_WalksEntireHistory(t *testing.T) {
	s := newTestStore(t)
	const total = 10000
	want := seedRuns(t, s, "pipe-big", total)

	var got []string
	cursor := ""
	pages := 0
	for {
		runs, hasNext, err := s.ListRunsByPipelineCursor("pipe-big", cursor, 100)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, r := range runs {
			got = append(got, r.ID)
		}
		pages++
		if !hasNext {
			break
		}
		if len(runs) == 0 {
			t.Fatal("hasNext was true but the page was empty — the walk cannot advance")
		}
		cursor = runs[len(runs)-1].ID
		if pages > total {
			t.Fatal("walk did not terminate")
		}
	}

	if len(got) != total {
		t.Fatalf("walked %d runs, want %d", len(got), total)
	}
	if pages != total/100 {
		t.Errorf("took %d pages for %d runs at 100/page, want %d", pages, total, total/100)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("run %d out of order: got %s, want %s", i, got[i], want[i])
		}
	}
}

// hasNext must be exact at the boundary. Off by one here either hides the
// last page or offers a page that turns out to be empty.
func TestListRunsByPipelineCursor_HasNextAtBoundary(t *testing.T) {
	s := newTestStore(t)
	seedRuns(t, s, "pipe-bound", 10)

	cases := []struct {
		limit       int
		wantLen     int
		wantHasNext bool
	}{
		{limit: 9, wantLen: 9, wantHasNext: true},    // one short of the end
		{limit: 10, wantLen: 10, wantHasNext: false}, // exactly the end
		{limit: 11, wantLen: 10, wantHasNext: false}, // past the end
	}
	for _, c := range cases {
		runs, hasNext, err := s.ListRunsByPipelineCursor("pipe-bound", "", c.limit)
		if err != nil {
			t.Fatalf("limit %d: %v", c.limit, err)
		}
		if len(runs) != c.wantLen {
			t.Errorf("limit %d: got %d runs, want %d", c.limit, len(runs), c.wantLen)
		}
		if hasNext != c.wantHasNext {
			t.Errorf("limit %d: hasNext=%v, want %v", c.limit, hasNext, c.wantHasNext)
		}
	}

	// A cursor sitting on the final row reports no next page and returns
	// nothing, rather than looping.
	all, _, err := s.ListRunsByPipelineCursor("pipe-bound", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	runs, hasNext, err := s.ListRunsByPipelineCursor("pipe-bound", all[len(all)-1].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 || hasNext {
		t.Errorf("past the last row: got %d runs hasNext=%v, want 0 and false", len(runs), hasNext)
	}
}

// The cursor must not leak runs belonging to another pipeline.
func TestListRunsByPipelineCursor_ScopedToPipeline(t *testing.T) {
	s := newTestStore(t)
	seedRuns(t, s, "pipe-a", 5)
	seedRuns(t, s, "pipe-b", 5)

	runs, hasNext, err := s.ListRunsByPipelineCursor("pipe-a", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 5 {
		t.Fatalf("got %d runs for pipe-a, want 5", len(runs))
	}
	if hasNext {
		t.Error("hasNext should be false — pipe-b's runs are not pipe-a's next page")
	}
	for _, r := range runs {
		if r.PipelineID != "pipe-a" {
			t.Errorf("leaked a run from %s", r.PipelineID)
		}
	}
}

func TestListRunsByPipelineCursor_EmptyHistory(t *testing.T) {
	s := newTestStore(t)
	runs, hasNext, err := s.ListRunsByPipelineCursor("pipe-none", "", 25)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 0 || hasNext {
		t.Errorf("got %d runs hasNext=%v, want 0 and false", len(runs), hasNext)
	}
}

// The offset path's total must come from the database, not from the length
// of an already-truncated slice — that was the bug that capped it at 50.
func TestListRunsByPipelinePaged_TotalIsNotCapped(t *testing.T) {
	s := newTestStore(t)
	seedRuns(t, s, "pipe-paged", 137)

	runs, total, err := s.ListRunsByPipelinePaged("pipe-paged", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 137 {
		t.Errorf("total = %d, want 137", total)
	}
	if len(runs) != 20 {
		t.Errorf("page size = %d, want 20", len(runs))
	}

	// A page well past the old 50-run ceiling must return real rows.
	deep, total, err := s.ListRunsByPipelinePaged("pipe-paged", 20, 120)
	if err != nil {
		t.Fatal(err)
	}
	if total != 137 {
		t.Errorf("total on deep page = %d, want 137", total)
	}
	if len(deep) != 17 {
		t.Errorf("deep page returned %d runs, want the final 17", len(deep))
	}
}

// Offset and cursor walk the same order, so a caller can switch between
// them without seeing runs jump around.
func TestRunPaging_OffsetAndCursorAgree(t *testing.T) {
	s := newTestStore(t)
	seedRuns(t, s, "pipe-agree", 60)

	var viaCursor []string
	cursor := ""
	for {
		runs, hasNext, err := s.ListRunsByPipelineCursor("pipe-agree", cursor, 7)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs {
			viaCursor = append(viaCursor, r.ID)
		}
		if !hasNext {
			break
		}
		cursor = runs[len(runs)-1].ID
	}

	var viaOffset []string
	for off := 0; ; off += 7 {
		runs, _, err := s.ListRunsByPipelinePaged("pipe-agree", 7, off)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 0 {
			break
		}
		for _, r := range runs {
			viaOffset = append(viaOffset, r.ID)
		}
	}

	if len(viaCursor) != len(viaOffset) {
		t.Fatalf("cursor walked %d runs, offset walked %d", len(viaCursor), len(viaOffset))
	}
	for i := range viaCursor {
		if viaCursor[i] != viaOffset[i] {
			t.Fatalf("orders diverge at %d: cursor %s, offset %s", i, viaCursor[i], viaOffset[i])
		}
	}
	if len(viaCursor) != 60 {
		t.Fatalf("walked %d runs, want 60", len(viaCursor))
	}
}

func TestListRunsByPipelineCursor_NoDuplicatesAcrossPages(t *testing.T) {
	s := newTestStore(t)
	seedRuns(t, s, "pipe-dupe", 250)

	seen := map[string]int{}
	cursor := ""
	for {
		runs, hasNext, err := s.ListRunsByPipelineCursor("pipe-dupe", cursor, 33)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs {
			seen[r.ID]++
			if seen[r.ID] > 1 {
				t.Fatalf("run %s returned on more than one page", r.ID)
			}
		}
		if !hasNext {
			break
		}
		cursor = runs[len(runs)-1].ID
	}
	if len(seen) != 250 {
		t.Fatalf("saw %d distinct runs, want 250", len(seen))
	}
}
