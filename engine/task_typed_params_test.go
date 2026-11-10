package engine

// A task node's keyword parameters are the run's resolved, typed
// parameters, with their declared defaults (#978), not the legacy string
// map. A pipeline with no declarations passes the string parameters as it
// always has.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/store"
)

// typedParamsTask reports the Python type and value of each parameter it
// receives.
const typedParamsTask = "def run(threshold, amount, big):\n" +
	"    return f'{type(threshold).__name__}:{threshold}|{type(amount).__name__}:{amount}|{big}'\n"

func typedParamsDeclarations() map[string]interface{} {
	return map[string]interface{}{
		"threshold": map[string]interface{}{"type": map[string]interface{}{"kind": "float64"}, "default": 0.5},
		"amount": map[string]interface{}{"type": map[string]interface{}{"kind": "decimal"},
			"default": map[string]interface{}{"$bptd": "decimal", "value": "12.50"}},
		"big": map[string]interface{}{"type": map[string]interface{}{"kind": "int64"},
			"default": map[string]interface{}{"$bptd": "int64", "value": "9007199254740993"}},
	}
}

func typedParamsPipeline(id, orgID, digest string, declarations map[string]interface{}) *models.Pipeline {
	return &models.Pipeline{
		ID: id, Name: id, Enabled: true, OrgID: orgID, Parameters: declarations,
		Nodes: []models.Node{{ID: "task", Type: models.NodeTypeTask, Name: "Task", Config: map[string]interface{}{
			"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundlev2.Format},
		}}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func taskResult(t *testing.T, eng *Engine, run *models.Run) string {
	t.Helper()
	ds, err := eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("rows = %v", ds.Rows)
	}
	s, _ := ds.Rows[0]["result"].(string)
	return s
}

const typedParamsDefaults = "float:0.5|dict:{'$bptd': 'decimal', 'value': '12.50'}|{'$bptd': 'int64', 'value': '9007199254740993'}"

func TestTaskReceivesTypedParametersWithTheirDefaults(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, typedParamsTask)
	p := typedParamsPipeline("task-typed-params", taskOrg, digest, typedParamsDeclarations())
	if err := e.s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}

	run, err := e.eng.RunPipeline(p.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if got := taskResult(t, e.eng, run); got != typedParamsDefaults {
		t.Fatalf("with defaults the task got %q, want %q", got, typedParamsDefaults)
	}

	// A pipeline that declares parameters passes its tasks the typed
	// values only: a legacy string parameter, of a declared name or not,
	// is not merged in.
	run, err = e.eng.RunPipeline(p.ID, map[string]string{"threshold": "9", "env": "prod"})
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if got := taskResult(t, e.eng, run); got != typedParamsDefaults {
		t.Fatalf("with a string threshold the task got %q, want the typed default %q", got, typedParamsDefaults)
	}

	run, err = e.eng.RunPipelineOpts(p.ID, RunOptions{TypedParams: map[string]interface{}{"threshold": 0.75}})
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if got, want := taskResult(t, e.eng, run), "float:0.75|dict:{'$bptd': 'decimal', 'value': '12.50'}|{'$bptd': 'int64', 'value': '9007199254740993'}"; got != want {
		t.Fatalf("with threshold set the task got %q, want %q", got, want)
	}
}

// A pipeline that declares nothing passes its string run parameters, as
// before.
func TestTaskWithoutDeclarationsStillReceivesStringParameters(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run(env):\n    return f'{type(env).__name__}:{env}'\n")
	p := typedParamsPipeline("task-string-params", taskOrg, digest, nil)
	if err := e.s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := e.eng.RunPipeline(p.ID, map[string]string{"env": "prod"})
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if got := taskResult(t, e.eng, run); got != "str:prod" {
		t.Fatalf("task got %q, want str:prod", got)
	}
}

// Remote dispatch carries the typed parameters in the work order, and the
// worker hands them to the task.
func TestRemoteTaskReceivesTypedParameters(t *testing.T) {
	skipIfNoPython3(t)
	real := newExpansionTestStore(t, "task-typed-params-remote").(*store.SQLiteStore)
	digest := seedRemoteTaskBundle(t, real, typedParamsTask)
	p := typedParamsPipeline("task-typed-params-remote", taskRemoteOrg, digest, typedParamsDeclarations())
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	eng.InstanceJobQueue = &fakeInstanceJobQueue{
		attempts: real, artifacts: eng.ArtifactStore, delay: 10 * time.Millisecond,
		respond: func(job extensions.RunJob) ([]string, []common.DataRow, string) {
			if job.WorkOrder.TypedParams["threshold"] != 0.5 {
				t.Errorf("work order TypedParams = %v, want threshold 0.5", job.WorkOrder.TypedParams)
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
	if got := taskResult(t, eng, run); got != typedParamsDefaults {
		t.Fatalf("remote task got %q, want %q", got, typedParamsDefaults)
	}
}

// A pipeline that declares parameters but resolves none (an optional
// parameter the run did not set) still passes its remote task no string
// parameters: the empty typed map does not survive the work order's JSON,
// so the dispatcher must not send the strings beside it.
func TestRemoteTaskOfADeclaringPipelineGetsNoStringParameters(t *testing.T) {
	skipIfNoPython3(t)
	real := newExpansionTestStore(t, "task-typed-params-none").(*store.SQLiteStore)
	digest := seedRemoteTaskBundle(t, real, "def run(**kwargs):\n    return ','.join(sorted(kwargs)) or 'none'\n")
	p := typedParamsPipeline("task-typed-params-none", taskRemoteOrg, digest, map[string]interface{}{
		"limit": map[string]interface{}{"type": map[string]interface{}{"kind": "int64"}, "required": false},
	})
	if err := real.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(real))
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	eng.InstanceJobQueue = &fakeInstanceJobQueue{
		attempts: real, artifacts: eng.ArtifactStore, delay: 10 * time.Millisecond,
		respond: func(job extensions.RunJob) ([]string, []common.DataRow, string) {
			// A real queue carries the work order as JSON; the round trip
			// is what drops an empty typed map.
			raw, err := json.Marshal(job.WorkOrder)
			if err != nil {
				t.Fatal(err)
			}
			var wo extensions.InstanceWorkOrder
			if err := json.Unmarshal(raw, &wo); err != nil {
				t.Fatal(err)
			}
			ds, err := ExecuteTaskWorkOrderContext(context.Background(), real, job.RunID, job.NodeID, &wo)
			if err != nil {
				return nil, nil, err.Error()
			}
			return ds.Columns, ds.Rows, ""
		},
	}
	run, err := eng.RunPipeline(p.ID, map[string]string{"env": "prod"})
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if got := taskResult(t, eng, run); got != "none" {
		t.Fatalf("task got kwargs %q, want none: string parameters were merged in", got)
	}
}
