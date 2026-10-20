package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Each source in a preview stops once it has the rows the preview keeps.
// Every test feeds a source that a whole read could not survive (a broken
// line, a value that overflows, a script that never ends, pages without
// end) past the rows a preview needs, so only a source that stopped passes.

func previewOne(t *testing.T, node models.Node) PreviewNode {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "real.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := drainEngineOnCleanup(t, NewEngine(s))
	p := &models.Pipeline{ID: "pv", Name: "pv", Nodes: []models.Node{node}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pv, err := e.PreviewPipeline(ctx, p, 5)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Error != "" {
		t.Fatalf("preview error: %s", pv.Error)
	}
	return pv.Nodes[0]
}

func TestAPreviewReadsTheFirstRowsOfACSV(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,name\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "%d,n%d\n", i, i)
	}
	b.WriteString("\"unterminated,quote\n") // a whole read fails here
	path := filepath.Join(t.TempDir(), "big.csv")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	n := previewOne(t, models.Node{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
		Config: map[string]interface{}{"path": path, "format": "csv"}})
	if n.Status != string(models.RunStatusSuccess) || len(n.Rows) != 5 || len(n.Columns) != 2 {
		t.Fatalf("csv preview = %+v", n)
	}
}

func TestAPreviewStopsADatabaseQuery(t *testing.T) {
	db := filepath.Join(t.TempDir(), "src.db")
	// Rows past the 20th divide... no: overflow. abs() of the smallest
	// integer is an error in SQLite, raised when that row is produced.
	query := `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 100000)
SELECT i, CASE WHEN i > 20 THEN abs(-9223372036854775807 - 1) ELSE i END AS v FROM n`
	n := previewOne(t, models.Node{ID: "src", Type: models.NodeTypeSourceDB, Name: "Q",
		Config: map[string]interface{}{"uri": "sqlite://" + db, "query": query}})
	if n.Status != string(models.RunStatusSuccess) || len(n.Rows) != 5 {
		t.Fatalf("db preview = %+v", n)
	}
}

func TestAPreviewStopsAScriptThatEmits(t *testing.T) {
	for _, pool := range []string{"1", "0"} {
		t.Run("pool="+pool, func(t *testing.T) {
			t.Setenv("BROKOLI_CODE_POOL", pool)
			script := "i = 0\nwhile True:\n    emit({'i': i})\n    i += 1\n"
			start := time.Now()
			n := previewOne(t, models.Node{ID: "gen", Type: models.NodeTypeCode, Name: "Gen",
				Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
				Config:       map[string]interface{}{"language": "python", "script": script, "timeout": 30}})
			if n.Status != string(models.RunStatusSuccess) || len(n.Rows) != 5 {
				t.Fatalf("emit preview = %+v", n)
			}
			if time.Since(start) > 20*time.Second {
				t.Fatalf("the script ran %s; it should stop at the preview's rows", time.Since(start))
			}
		})
	}
}

func TestAPreviewFetchesOnlyThePagesItNeeds(t *testing.T) {
	allowLoopback(t)
	var pages int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pages, 1)
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items := []map[string]interface{}{}
		for i := offset; i < offset+limit; i++ {
			items = append(items, map[string]interface{}{"id": i})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items, "end_flag": false}) // never ends
	}))
	defer srv.Close()
	n := previewOne(t, models.Node{ID: "api", Type: models.NodeTypeSourceAPI, Name: "A",
		Config: map[string]interface{}{"url": srv.URL, "records": "items",
			"pagination": map[string]interface{}{"strategy": "offset", "page_size": 2, "end_flag": "end_flag"}}})
	if n.Status != string(models.RunStatusSuccess) || len(n.Rows) != 5 {
		t.Fatalf("api preview = %+v", n)
	}
	if got := atomic.LoadInt32(&pages); got > 3 {
		t.Fatalf("fetched %d pages for 5 rows of 2 per page", got)
	}
}

// The preview's limits are on a copy: the pipeline's own config is untouched.
func TestPreviewNodeLeavesThePipelineAlone(t *testing.T) {
	pag := map[string]interface{}{"strategy": "offset", "max_records": float64(1000)}
	api := models.Node{Type: models.NodeTypeSourceAPI, Config: map[string]interface{}{"pagination": pag}}
	got := previewNode(api, 10)
	if got.Config["pagination"].(map[string]interface{})["max_records"] != 10 || pag["max_records"] != float64(1000) {
		t.Fatalf("preview api config %v, original %v", got.Config, pag)
	}
	small := previewNode(models.Node{Type: models.NodeTypeSourceAPI, Config: map[string]interface{}{
		"pagination": map[string]interface{}{"max_records": float64(3)}}}, 10)
	if small.Config["pagination"].(map[string]interface{})["max_records"] != float64(3) {
		t.Fatalf("a smaller max_records was raised: %v", small.Config)
	}
	code := models.Node{Type: models.NodeTypeCode, Config: map[string]interface{}{"script": "x"}}
	if previewNode(code, 10).Config[previewRowsKey] != 10 || code.Config[previewRowsKey] != nil {
		t.Fatalf("code config changed in place: %v", code.Config)
	}
}
