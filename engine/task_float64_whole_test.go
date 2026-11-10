package engine

// A whole float64 value written without ".0" (JSON.stringify(5.0) is "5")
// is decoded as an int64, because task outputs keep integers exact. It is
// still a valid float64 and must pass the task's own output contract
// (#983).

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
)

func TestNodeTaskReturningAWholeFloat64PassesItsOutputContract(t *testing.T) {
	skipIfNoNode(t)
	e := newTaskEngine(t)
	digest := e.nodeBundle(t, "export function run() {\n  return [{ score: 5.0 }, { score: 0.5 }];\n}\n")
	p := &models.Pipeline{
		ID: "p-node-whole-float", Name: "p-node-whole-float", Enabled: true, OrgID: taskOrg,
		Nodes: []models.Node{{ID: "task", Type: models.NodeTypeTask, Name: "Task", Config: map[string]interface{}{
			"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundlev2.Format},
		}, Interface: map[string]interface{}{
			"contract": "brokoli.task-interface/v1",
			"inputs":   map[string]interface{}{},
			"outputs": map[string]interface{}{"result": map[string]interface{}{"value": map[string]interface{}{
				"kind": "dataset",
				"row": map[string]interface{}{"kind": "record", "fields": []interface{}{
					map[string]interface{}{"name": "score", "type": map[string]interface{}{"kind": "float64"}, "required": true},
				}},
			}}},
		}}},
	}
	if err := e.s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := e.eng.RunPipeline(p.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v; a whole float64 written as 5 must satisfy a float64 field", run, err)
	}
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 2 || toF64(ds.Rows[0]["score"]) != 5 || toF64(ds.Rows[1]["score"]) != 0.5 {
		t.Fatalf("rows = %v", ds.Rows)
	}
}
