package engine

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A task declaring an artifact output worked locally and failed on the
// instance-worker path with "this server has no artifact blob store to
// hold it" -- blaming configuration for absent plumbing, since
// ExecuteTaskWorkOrderContext hardcoded a nil blob store while
// executeInstanceJobContext held an ArtifactStore at the call site.
//
// The bundle writes an artifact output through the reference harness, so
// this exercises the real read path (staged open, checksum verification,
// blob store Put) rather than a stubbed result.
const artifactTaskSource = `
def run():
    return b"hello artifact bytes"
`

func artifactOutputPipeline(id, digest string) *models.Pipeline {
	p := taskRemoteDispatchPipeline(id, digest)
	p.Nodes[0].Interface = map[string]interface{}{
		"contract": "brokoli.task-interface/v1",
		"inputs":   map[string]interface{}{},
		"outputs": map[string]interface{}{
			"result": map[string]interface{}{
				"value": map[string]interface{}{
					"kind":        "artifact",
					"media_types": []interface{}{"application/octet-stream"},
				},
			},
		},
	}
	return p
}

func artifactWorkOrder(t *testing.T, p *models.Pipeline) *extensions.InstanceWorkOrder {
	t.Helper()
	return &extensions.InstanceWorkOrder{
		NodeType:       string(models.NodeTypeTask),
		OrgID:          taskRemoteOrg,
		Config:         p.Nodes[0].Config,
		NodeInterface:  p.Nodes[0].Interface,
		TimeoutSeconds: 60,
	}
}

func TestTaskWorkOrder_ArtifactOutputUsesTheClaimantsBlobStore(t *testing.T) {
	skipIfNoPython3(t)
	realStore := newExpansionTestStore(t, "task-wo-artifact")
	real := realStore.(*store.SQLiteStore)
	digest := seedRemoteTaskBundle(t, real, artifactTaskSource)
	p := artifactOutputPipeline("task-wo-artifact-pipeline", digest)
	wo := artifactWorkOrder(t, p)

	artifacts := NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	if _, ok := interface{}(artifacts).(BlobStoreProvider); !ok {
		t.Fatal("test premise broken: this ArtifactStore is not a BlobStoreProvider, so it proves nothing about threading one through")
	}

	ds, err := ExecuteTaskWorkOrderWithArtifacts(context.Background(), real, artifacts, "run-1", "task", wo)
	if err != nil {
		t.Fatalf("artifact output failed even with a blob store available: %v", err)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("expected one reference row, got %d", len(ds.Rows))
	}
	// An artifact output is a reference, never inlined bytes.
	uri, _ := ds.Rows[0]["uri"].(string)
	if uri == "" {
		t.Errorf("artifact row has no uri: %#v", ds.Rows[0])
	}
	if sum, _ := ds.Rows[0]["checksum"].(string); !strings.HasPrefix(sum, "sha256:") {
		t.Errorf("artifact row checksum = %v, want a sha256 reference", ds.Rows[0]["checksum"])
	}
}

// The honest refusal must survive: a claimant genuinely without a blob
// store still gets a named error rather than silently inlining bytes.
func TestTaskWorkOrder_ArtifactOutputWithoutABlobStoreStillRefusesByName(t *testing.T) {
	skipIfNoPython3(t)
	realStore := newExpansionTestStore(t, "task-wo-artifact-none")
	real := realStore.(*store.SQLiteStore)
	digest := seedRemoteTaskBundle(t, real, artifactTaskSource)
	p := artifactOutputPipeline("task-wo-artifact-none-pipeline", digest)
	wo := artifactWorkOrder(t, p)

	_, err := ExecuteTaskWorkOrderContext(context.Background(), real, "run-1", "task", wo)
	if err == nil || !strings.Contains(err.Error(), "no artifact blob store") {
		t.Fatalf("err = %v, want a named refusal about the missing blob store", err)
	}
}

func TestWorkOrderBlobStore(t *testing.T) {
	if got := workOrderBlobStore(nil); got != nil {
		t.Errorf("nil ArtifactStore should yield no blob store, got %#v", got)
	}
	artifacts := NewLocalDiskArtifactStore(t.TempDir())
	if got := workOrderBlobStore(artifacts); got == nil {
		t.Error("a BlobStoreProvider's blob store was dropped")
	}
	var notAProvider ArtifactStore = artifactStoreWithoutBlobs{}
	if got := workOrderBlobStore(notAProvider); got != nil {
		t.Errorf("an ArtifactStore that is not a BlobStoreProvider should yield nil, got %#v", got)
	}
}

// An ArtifactStore with no blob store underneath -- a legitimate
// configuration the optional-capability assertion exists for.
type artifactStoreWithoutBlobs struct{}

func (artifactStoreWithoutBlobs) WriteArtifact(runID, nodeID, instanceKey string, ds *common.DataSet) error {
	return nil
}
func (artifactStoreWithoutBlobs) ReadArtifact(runID, nodeID, instanceKey string) (*common.DataSet, error) {
	return nil, nil
}
func (artifactStoreWithoutBlobs) DeleteRunArtifacts(runID string) error { return nil }

// The wiring, not just the function. executeInstanceJobContext holds the
// ArtifactStore and had to start passing it; a mutation reverting that
// call to ExecuteTaskWorkOrderContext passed every other test, so this
// drives the whole worker loop and asserts the artifact was published.
func TestExecuteInstanceJob_TaskArtifactOutputReachesTheBlobStore(t *testing.T) {
	skipIfNoPython3(t)
	realStore := newExpansionTestStore(t, "task-job-artifact")
	real := realStore.(*store.SQLiteStore)
	digest := seedRemoteTaskBundle(t, real, artifactTaskSource)

	pipeline := artifactOutputPipeline("task-job-artifact-pipeline", digest)
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	run := &models.Run{ID: common.NewID(), PipelineID: pipeline.ID, Status: models.RunStatusRunning}
	if err := real.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	if err := real.WithTx(func(tx *sql.Tx) error {
		return real.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
			RunID: run.ID, NodeID: "task", InstanceKey: "", Attempt: 0,
			Status: models.AttemptStatusQueued, IdempotencyKey: run.ID + ":task",
		})
	}); err != nil {
		t.Fatalf("seed execution attempt: %v", err)
	}
	gen, claimed, err := real.ClaimAttempt(run.ID, "task", "", 0, "test-instance", store.DefaultLeaseDuration)
	if err != nil || !claimed {
		t.Fatalf("ClaimAttempt: claimed=%v err=%v", claimed, err)
	}

	artifacts := NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	job := extensions.RunJob{
		ID: common.NewID(), PipelineID: pipeline.ID, RunID: run.ID, OrgID: taskRemoteOrg,
		NodeID: "task", InstanceKey: "", Attempt: 0, FencingGeneration: gen,
		WorkOrder: &extensions.InstanceWorkOrder{
			NodeType: string(models.NodeTypeTask), OrgID: taskRemoteOrg,
			Config:         pipeline.Nodes[0].Config,
			NodeInterface:  pipeline.Nodes[0].Interface,
			TimeoutSeconds: 60,
		},
	}
	if err := ExecuteInstanceJob(real, artifacts, job); err != nil {
		t.Fatalf("ExecuteInstanceJob: %v", err)
	}

	attempt, err := real.GetExecutionAttempt(run.ID, "task", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Status != models.AttemptStatusCompleted {
		t.Fatalf("attempt status = %s (error: %s), want completed -- without the blob store threaded through, this fails with \"no artifact blob store\"", attempt.Status, attempt.Error)
	}
	ds, err := artifacts.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("artifact rows = %v, want one reference row", ds.Rows)
	}
	if uri, _ := ds.Rows[0]["uri"].(string); uri == "" {
		t.Errorf("published row has no uri: %#v", ds.Rows[0])
	}
}
