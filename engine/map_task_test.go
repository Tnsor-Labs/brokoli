package engine

// Tests for mapping a task node (ADR-050 phase 1, #967): the bundle runs
// once per item with that item as its one-row input, locally and on
// remote workers, each item its own instance.

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/store"
)

// mappedTaskSource doubles each item's n and says how many rows it saw,
// so a test can tell the task got exactly its one item.
const mappedTaskSource = "def run(input):\n    rows = list(input)\n    return [{'id': r['id'], 'doubled': int(r['n']) * 2, 'seen': len(rows)} for r in rows]\n"

func mappedTaskInterface() map[string]interface{} {
	dataset := map[string]interface{}{"value": map[string]interface{}{"kind": "dataset"}}
	return map[string]interface{}{"contract": "brokoli.task-interface/v1",
		"inputs": map[string]interface{}{"input": dataset}, "outputs": map[string]interface{}{"result": dataset}}
}

func mappedTaskPipeline(t *testing.T, id, orgID, csvPath, digest string, block map[string]interface{}) *models.Pipeline {
	t.Helper()
	return &models.Pipeline{
		ID: id, Name: id, Enabled: true, OrgID: orgID,
		Nodes: []models.Node{
			{ID: "items", Type: models.NodeTypeSourceFile, Name: "Items", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "task", Type: models.NodeTypeTask, Name: "Task", Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundlev2.Format},
				"map":         block,
			}, Interface: mappedTaskInterface()},
		},
		Edges:     []models.Edge{{From: "items", To: "task"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func doubledByID(t *testing.T, ds *common.DataSet) string {
	t.Helper()
	var got []string
	for _, r := range ds.Rows {
		if toF64(r["seen"]) != 1 {
			t.Fatalf("an item's task saw %v rows, want exactly its own one: %v", r["seen"], r)
		}
		got = append(got, fmt.Sprintf("%s=%v", r["id"], toF64(r["doubled"])))
	}
	sort.Strings(got)
	return strings.Join(got, ",")
}

func TestMappedTaskRunsTheBundleOncePerItemLocally(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, mappedTaskSource)
	csv := writeCSV(t, t.TempDir(), "items.csv", "id,n\na,1\nb,2\nc,3\n")
	p := mappedTaskPipeline(t, "map-task-local", taskOrg, csv, digest, map[string]interface{}{"over": "items", "key": "id"})
	if ve := ValidatePipeline(p); ve.HasErrors() {
		t.Fatalf("refused: %v", ve.Error())
	}
	if err := e.s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := e.eng.RunPipeline(p.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := doubledByID(t, ds); got != "a=2,b=4,c=6" {
		t.Fatalf("output = %s", got)
	}
	got := instanceStatuses(t, e.s, run.ID)
	if len(got) != 3 || got["key:a"] != models.RunStatusSuccess || got["key:c"] != models.RunStatusSuccess {
		t.Fatalf("instances = %v", got)
	}
}

// One item's failure is that item's, under the node's policy: skip leaves
// it out of the output and records it failed.
func TestMappedTaskSkipsAFailingItem(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, mappedTaskSource)
	csv := writeCSV(t, t.TempDir(), "items.csv", "id,n\na,1\nbad,oops\nc,3\n")
	p := mappedTaskPipeline(t, "map-task-skip", taskOrg, csv, digest, map[string]interface{}{"over": "items", "key": "id", "on_item_failure": "skip"})
	if err := e.s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := e.eng.RunPipeline(p.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := doubledByID(t, ds); got != "a=2,c=6" {
		t.Fatalf("output = %s", got)
	}
	if got := instanceStatuses(t, e.s, run.ID); got["key:bad"] != models.RunStatusFailed {
		t.Fatalf("instances = %v", got)
	}
}

// With remote workers each item is its own job, under its own instance
// key and attempt, run by the real worker-side executor.
func TestMappedTaskDispatchesEachItemRemotely(t *testing.T) {
	skipIfNoPython3(t)
	realStore := newExpansionTestStore(t, "map-task-remote")
	real := realStore.(*store.SQLiteStore)
	digest := seedRemoteTaskBundle(t, real, mappedTaskSource)
	csv := writeCSV(t, t.TempDir(), "items.csv", "id,n\na,1\nb,2\nc,3\n")
	p := mappedTaskPipeline(t, "map-task-remote-pipeline", taskRemoteOrg, csv, digest, map[string]interface{}{"over": "items", "key": "id"})
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	var mu sync.Mutex
	var keys []string
	eng.InstanceJobQueue = &fakeInstanceJobQueue{
		attempts: real, artifacts: eng.ArtifactStore, delay: 10 * time.Millisecond,
		respond: func(job extensions.RunJob) ([]string, []common.DataRow, string) {
			mu.Lock()
			keys = append(keys, job.InstanceKey)
			mu.Unlock()
			if _, leaked := job.WorkOrder.Config["map"]; leaked {
				t.Errorf("the item's work order carries the node's map block")
			}
			ds, err := ExecuteTaskWorkOrderContext(context.Background(), real, job.RunID, job.NodeID, job.WorkOrder)
			if err != nil {
				return nil, nil, err.Error()
			}
			return ds.Columns, ds.Rows, ""
		},
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "key:a,key:b,key:c" {
		t.Fatalf("dispatched instance keys = %v, want one job per item", keys)
	}
	for _, k := range keys {
		a, err := real.GetExecutionAttempt(run.ID, "task", k, 0)
		if err != nil || a.Status != models.AttemptStatusCompleted {
			t.Fatalf("attempt for %s = %+v, err = %v", k, a, err)
		}
	}
	ds, err := eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := doubledByID(t, ds); got != "a=2,b=4,c=6" {
		t.Fatalf("output = %s", got)
	}
}

func TestMappedTaskValidation(t *testing.T) {
	node := func(iface map[string]interface{}) models.Node {
		return models.Node{ID: "task", Name: "Task", Type: models.NodeTypeTask, Interface: iface, Config: map[string]interface{}{
			"task_bundle": map[string]interface{}{"digest": "sha256:" + strings.Repeat("a", 64), "format": taskbundlev2.Format},
			"map":         map[string]interface{}{"over": "items"},
		}}
	}
	if errs := mappedTaskErrors(node(mappedTaskInterface())); len(errs) != 0 {
		t.Fatalf("a dataset-in, dataset-out task refused: %v", errs)
	}
	noInput := map[string]interface{}{"contract": "brokoli.task-interface/v1", "inputs": map[string]interface{}{},
		"outputs": map[string]interface{}{"result": map[string]interface{}{"value": map[string]interface{}{"kind": "dataset"}}}}
	if errs := strings.Join(mappedTaskErrors(node(noInput)), ""); !strings.Contains(errs, "declares no input port") {
		t.Fatalf("no input: %q", errs)
	}
	artifactOut := mappedTaskInterface()
	artifactOut["outputs"] = map[string]interface{}{"result": map[string]interface{}{"value": map[string]interface{}{"kind": "artifact"}}}
	if errs := strings.Join(mappedTaskErrors(node(artifactOut)), ""); !strings.Contains(errs, "can only output datasets") {
		t.Fatalf("artifact output: %q", errs)
	}
}
