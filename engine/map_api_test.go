package engine

// Tests for mapping a source_api node (ADR-050 phase 1, #967): one request
// per item, each item value encoded for where it lands and refused where
// it could change where the request goes.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

func TestBindAPIItemEncodesEachValueForItsPlace(t *testing.T) {
	item := common.DataRow{"id": "a/b?c#d", "region": "eu & us", "n": int64(42), "tenant": "acme"}
	cfg := map[string]interface{}{
		"url":     "https://api.example.com/customers/${item.id}/orders?region=${item.region}&n=${item.n}",
		"params":  map[string]interface{}{"tenant": "${item.tenant}", "fixed": "x"},
		"headers": map[string]interface{}{"X-Tenant": "t-${item.tenant}"},
		"body":    map[string]interface{}{"id": "${item.n}", "note": "for ${item.id}", "list": []interface{}{"${item.region}"}},
		"records": "results",
	}
	got, err := bindAPIItem(cfg, item)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://api.example.com/customers/a%2Fb%3Fc%23d/orders?region=eu+%26+us&n=42"; got["url"] != want {
		t.Fatalf("url = %v, want %s", got["url"], want)
	}
	if p := got["params"].(map[string]interface{}); p["tenant"] != "acme" || p["fixed"] != "x" {
		t.Fatalf("params = %v", p)
	}
	if h := got["headers"].(map[string]interface{}); h["X-Tenant"] != "t-acme" {
		t.Fatalf("headers = %v", h)
	}
	body := got["body"].(map[string]interface{})
	if body["id"] != int64(42) || body["note"] != "for a/b?c#d" || body["list"].([]interface{})[0] != "eu & us" {
		t.Fatalf("body = %#v; a whole-string reference keeps the item's type, a partial one becomes text", body)
	}
	if cfg["url"] != "https://api.example.com/customers/${item.id}/orders?region=${item.region}&n=${item.n}" {
		t.Fatal("bindAPIItem changed the node's own config")
	}
}

func TestBindAPIItemRefusesValuesThatWouldRedirectTheRequest(t *testing.T) {
	cases := []struct {
		item common.DataRow
		cfg  map[string]interface{}
		want string
	}{
		{common.DataRow{"id": ".."}, map[string]interface{}{"url": "https://api.example.com/customers/${item.id}/orders"},
			`made the path segment ".."`},
		{common.DataRow{"t": "a\r\nX-Admin: 1"}, map[string]interface{}{"url": "https://api.example.com/x", "headers": map[string]interface{}{"X-Tenant": "${item.t}"}},
			"line break or NUL"},
		{common.DataRow{"id": nil}, map[string]interface{}{"url": "https://api.example.com/c/${item.id}"},
			`the item's "id" is null`},
		{common.DataRow{}, map[string]interface{}{"url": "https://api.example.com/c/${item.id}"},
			`the item has no column "id"`},
	}
	for _, c := range cases {
		if _, err := bindAPIItem(c.cfg, c.item); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("cfg %v item %v: err = %v, want %q", c.cfg, c.item, err, c.want)
		}
	}
}

func TestMappedAPIErrorsRefuseReferencesWhereTheyCannotBeEncoded(t *testing.T) {
	node := func(cfg map[string]interface{}) models.Node {
		if _, ok := cfg["map"]; !ok {
			cfg["map"] = map[string]interface{}{"over": "customers"}
		}
		return models.Node{ID: "api", Name: "API", Type: models.NodeTypeSourceAPI, Config: cfg}
	}
	cases := []struct {
		cfg  map[string]interface{}
		want string
	}{
		{map[string]interface{}{"url": "https://${item.host}/x"}, "cannot choose the scheme, host or port"},
		{map[string]interface{}{"url": "https://api.example.com:${item.port}/x"}, "cannot choose the scheme, host or port"},
		{map[string]interface{}{"url": "${item.url}"}, "no scheme and host"},
		{map[string]interface{}{"url": "https://api.example.com/x#${item.f}"}, "fragment"},
		{map[string]interface{}{"url": "https://api.example.com/x", "headers": map[string]interface{}{"${item.h}": "v"}}, "cannot name a header"},
		{map[string]interface{}{"url": "https://api.example.com/x", "params": map[string]interface{}{"${item.p}": "v"}}, "cannot name a param"},
		{map[string]interface{}{"url": "https://api.example.com/x", "body": `{"id": "${item.id}"}`}, "'body' is text"},
		{map[string]interface{}{"url": "https://api.example.com/x", "body": map[string]interface{}{"${item.k}": 1}}, "cannot name a field"},
		{map[string]interface{}{"url": "https://api.example.com/x", "pagination": map[string]interface{}{"cursor_param": "${item.c}"}}, "${item...} in 'pagination.cursor_param'"},
		{map[string]interface{}{"url": "https://api.example.com/x", "execution": map[string]interface{}{"checkpoint_every": float64(5)}}, "'execution.checkpoint_every' cannot be used with 'map'"},
		{map[string]interface{}{"url": "https://api.example.com/x", "response": "artifact"}, `"response": "artifact" cannot be used with 'map'`},
		{map[string]interface{}{"url": "https://api.example.com/x", "execution": map[string]interface{}{"requests_per_second": float64(5)}}, "with 4 items at once the API would see up to 4 times that rate"},
	}
	for _, c := range cases {
		errs := strings.Join(mappedAPIErrors(node(c.cfg)), "\n")
		if !strings.Contains(errs, c.want) {
			t.Errorf("cfg %v: errors %q, want %q", c.cfg, errs, c.want)
		}
	}
	ok := node(map[string]interface{}{"url": "https://api.example.com/x", "execution": map[string]interface{}{"requests_per_second": float64(5)},
		"map": map[string]interface{}{"over": "customers", "max_parallel": float64(1)}})
	if errs := mappedAPIErrors(ok); len(errs) != 0 {
		t.Fatalf("requests_per_second with max_parallel 1 refused: %v", errs)
	}
	unmapped := models.Node{ID: "api", Name: "API", Type: models.NodeTypeSourceAPI, Config: map[string]interface{}{"url": "https://api.example.com/c/${item.id}"}}
	if errs := strings.Join(mappedAPIErrors(unmapped), ""); !strings.Contains(errs, "only available in a node with a 'map' block") {
		t.Fatalf("unmapped: %q", errs)
	}
}

// apiRecorder is an API that answers each customer's orders and records
// what it was asked.
type apiRecorder struct {
	mu       sync.Mutex
	requests []string
	at       []time.Time
}

func (a *apiRecorder) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.requests = append(a.requests, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery+" tenant="+r.Header.Get("X-Tenant")+" body="+string(body))
		a.at = append(a.at, time.Now())
		a.mu.Unlock()
		id := strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/orders"), "/customers/")
		if id == "broken" {
			http.Error(w, "no such customer", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": []map[string]interface{}{
			{"customer": id, "order": id + "-1"}, {"customer": id, "order": id + "-2"},
		}})
	}
}

func runMappedAPIPipeline(t *testing.T, name, customers string, cfg map[string]interface{}) (*models.Run, *store.SQLiteStore, []common.DataRow, error) {
	t.Helper()
	real := newExpansionTestStore(t, name).(*store.SQLiteStore)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "customers.csv", customers)
	out := filepath.Join(dir, "out.csv")
	p := &models.Pipeline{
		ID: name, Name: name, Enabled: true,
		Nodes: []models.Node{
			{ID: "customers", Type: models.NodeTypeSourceFile, Name: "Customers", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "orders", Type: models.NodeTypeSourceAPI, Name: "Orders", Config: cfg},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": out, "format": "csv"}},
		},
		Edges:     []models.Edge{{From: "customers", To: "orders"}, {From: "orders", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if ve := ValidatePipeline(p); ve.HasErrors() {
		t.Fatalf("pipeline refused: %v", ve.Error())
	}
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	run, err := eng.RunPipeline(p.ID)
	var rows []common.DataRow
	if ds, readErr := readCSVFile(out); readErr == nil {
		rows = ds.Rows
	}
	return run, real, rows, err
}

func orderColumn(rows []common.DataRow) string {
	var got []string
	for _, r := range rows {
		got = append(got, r["order"].(string))
	}
	sort.Strings(got)
	return strings.Join(got, ",")
}

// Each customer gets its own request, with its values in the path, query,
// header and body, and the node's output is every customer's orders.
func TestMappedSourceAPIRequestsOncePerItem(t *testing.T) {
	api := &apiRecorder{}
	server := httptest.NewServer(api.handler(t))
	defer server.Close()
	run, st, rows, err := runMappedAPIPipeline(t, "map-api", "id,region,tenant\nc1,eu,acme\nc2,us & ca,globex\n", map[string]interface{}{
		"url":     server.URL + "/customers/${item.id}/orders?region=${item.region}",
		"method":  "POST",
		"headers": map[string]interface{}{"X-Tenant": "${item.tenant}"},
		"body":    map[string]interface{}{"customer": "${item.id}"},
		"records": "results",
		"map":     map[string]interface{}{"over": "customers", "key": "id"},
	})
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if got := orderColumn(rows); got != "c1-1,c1-2,c2-1,c2-2" {
		t.Fatalf("orders = %s", got)
	}
	sort.Strings(api.requests)
	want := []string{
		`POST /customers/c1/orders?region=eu tenant=acme body={"customer":"c1"}`,
		`POST /customers/c2/orders?region=us+%26+ca tenant=globex body={"customer":"c2"}`,
	}
	if strings.Join(api.requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(api.requests, "\n"), strings.Join(want, "\n"))
	}
	got := instanceStatuses(t, st, run.ID)
	if got["key:c1"] != models.RunStatusSuccess || got["key:c2"] != models.RunStatusSuccess || len(got) != 2 {
		t.Fatalf("instances = %v", got)
	}
}

// An item value is data: one that tries to leave its path segment stays
// inside it, encoded, and one that would climb the path fails its item.
func TestMappedSourceAPIKeepsHostileValuesInsideTheirSegment(t *testing.T) {
	api := &apiRecorder{}
	server := httptest.NewServer(api.handler(t))
	defer server.Close()
	run, st, rows, err := runMappedAPIPipeline(t, "map-api-hostile", "id\nx/../../admin?all=1\n..\nok\n", map[string]interface{}{
		"url":     server.URL + "/customers/${item.id}/orders",
		"records": "results",
		"map":     map[string]interface{}{"over": "customers", "on_item_failure": "skip"},
	})
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if got := orderColumn(rows); got != "ok-1,ok-2,x/../../admin?all=1-1,x/../../admin?all=1-2" {
		t.Fatalf("orders = %s", got)
	}
	for _, r := range api.requests {
		if strings.Contains(r, "/admin") && !strings.Contains(r, "x%2F..%2F..%2Fadmin%3Fall=1") {
			t.Fatalf("a hostile value left its path segment: %s", r)
		}
	}
	if len(api.requests) != 2 {
		t.Fatalf("requests = %v; the '..' item must not be sent", api.requests)
	}
	if got := instanceStatuses(t, st, run.ID); got["idx:1"] != models.RunStatusFailed {
		t.Fatalf("instances = %v, want the '..' item failed", got)
	}
}

// A failing item is that item's failure, under the node's policy.
func TestMappedSourceAPIFailsOnlyTheFailingItem(t *testing.T) {
	api := &apiRecorder{}
	server := httptest.NewServer(api.handler(t))
	defer server.Close()
	run, st, _, err := runMappedAPIPipeline(t, "map-api-continue", "id\nc1\nbroken\nc3\n", map[string]interface{}{
		"url":         server.URL + "/customers/${item.id}/orders",
		"records":     "results",
		"max_retries": float64(0),
		"map":         map[string]interface{}{"over": "customers", "key": "id", "on_item_failure": "continue"},
	})
	if err == nil || run.Status != models.RunStatusFailed || !strings.Contains(err.Error(), "key:broken") {
		t.Fatalf("run = %+v, err = %v; want a failure naming key:broken", run, err)
	}
	got := instanceStatuses(t, st, run.ID)
	if got["key:c1"] != models.RunStatusSuccess || got["key:c3"] != models.RunStatusSuccess || got["key:broken"] != models.RunStatusFailed {
		t.Fatalf("instances = %v", got)
	}
}

// requests_per_second holds across items: with one item at a time, the
// pause also separates one item's request from the next item's.
func TestMappedSourceAPIRequestsPerSecondHoldsAcrossItems(t *testing.T) {
	api := &apiRecorder{}
	server := httptest.NewServer(api.handler(t))
	defer server.Close()
	run, _, _, err := runMappedAPIPipeline(t, "map-api-rps", "id\nc1\nc2\nc3\nc4\n", map[string]interface{}{
		"url":       server.URL + "/customers/${item.id}/orders",
		"records":   "results",
		"execution": map[string]interface{}{"requests_per_second": float64(10)},
		"map":       map[string]interface{}{"over": "customers", "max_parallel": float64(1)},
	})
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if len(api.at) != 4 {
		t.Fatalf("requests = %d, want 4", len(api.at))
	}
	for i := 1; i < len(api.at); i++ {
		if gap := api.at[i].Sub(api.at[i-1]); gap < 90*time.Millisecond {
			t.Fatalf("gap between requests %d and %d = %v, want at least 100ms (10 per second)", i-1, i, gap)
		}
	}
}

// A mapped item never fetches through remote pagination instances, whose
// keys belong to the whole node: with a job queue configured and an
// offset pagination remote pages could take, the items still fetch here.
func TestMappedSourceAPIItemsDoNotUseRemotePagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") != "0" {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/c/")
		_, _ = w.Write([]byte(`{"results":[{"order":"` + id + `-1"}]}`))
	}))
	defer server.Close()
	real := newExpansionTestStore(t, "map-api-no-remote-pages").(*store.SQLiteStore)
	dir := t.TempDir()
	csvPath := writeCSV(t, dir, "customers.csv", "id\nc1\nc2\n")
	p := &models.Pipeline{
		ID: "map-api-no-remote-pages", Name: "map-api-no-remote-pages", Enabled: true,
		Nodes: []models.Node{
			{ID: "customers", Type: models.NodeTypeSourceFile, Name: "Customers", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "orders", Type: models.NodeTypeSourceAPI, Name: "Orders", Config: map[string]interface{}{
				"url": server.URL + "/c/${item.id}", "records": "results",
				"pagination": map[string]interface{}{"strategy": "offset", "page_size": float64(1)},
				"map":        map[string]interface{}{"over": "customers", "key": "id"},
			}},
		},
		Edges:     []models.Edge{{From: "customers", To: "orders"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.InstanceJobQueue = &fakeInstanceJobQueue{
		attempts: real, artifacts: eng.ArtifactStore,
		respond: func(job extensions.RunJob) ([]string, []common.DataRow, string) {
			t.Errorf("a mapped item was dispatched as remote instance %q", job.InstanceKey)
			return nil, nil, "unexpected"
		},
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	ds, err := eng.ArtifactStore.ReadArtifact(run.ID, "orders", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := orderColumn(ds.Rows); got != "c1-1,c2-1" {
		t.Fatalf("orders = %s", got)
	}
}

// A stored node without a map block that still has an ${item...}
// reference is refused when it runs, instead of requesting the literal
// text.
func TestUnmappedSourceAPIRefusesItemReferencesAtRun(t *testing.T) {
	node := models.Node{ID: "api", Name: "API", Type: models.NodeTypeSourceAPI, Config: map[string]interface{}{"url": "https://api.example.com/c/${item.id}"}}
	_, err := (&Runner{}).runSourceAPI(context.Background(), node)
	if err == nil || !strings.Contains(err.Error(), "only available in a node with a 'map' block") {
		t.Fatalf("err = %v", err)
	}
}
