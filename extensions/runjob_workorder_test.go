package extensions

import (
	"encoding/json"
	"testing"
)

// TestRunJob_WorkOrderRoundTripsThroughJSON proves InstanceWorkOrder
// (ADR-017) survives exactly the boundary it exists for: a RunJob crossing
// a real transport (the enterprise Redis-backed JobQueue implementation
// serializes RunJob to JSON to enqueue it) and being decoded back on the
// other side. Nothing enqueues a populated WorkOrder yet — this only
// proves the wire contract a future dispatch-wiring slice will depend on
// is honest today.
func TestRunJob_WorkOrderRoundTripsThroughJSON(t *testing.T) {
	job := RunJob{
		ID: "job-1", PipelineID: "pipe-1", RunID: "run-1", OrgID: "org-1",
		NodeID: "parse", InstanceKey: "idx:3", Attempt: 1,
		IdempotencyKey: "run-1:parse:idx:3:1", FencingGeneration: 4,
		WorkOrder: &InstanceWorkOrder{
			NodeType:       "code",
			Script:         "output_data = {\"columns\": columns, \"rows\": rows}",
			Config:         map[string]interface{}{"language": "python"},
			ItemColumns:    []string{"path"},
			ItemRow:        map[string]interface{}{"path": "c.csv"},
			RunParams:      map[string]string{"env": "prod"},
			TimeoutSeconds: 30,
		},
	}

	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded RunJob
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.NodeID != "parse" || decoded.InstanceKey != "idx:3" || decoded.Attempt != 1 {
		t.Errorf("identity fields = %+v, want NodeID=parse InstanceKey=idx:3 Attempt=1", decoded)
	}
	if decoded.IdempotencyKey != "run-1:parse:idx:3:1" || decoded.FencingGeneration != 4 {
		t.Errorf("dispatch fields = %+v, want IdempotencyKey=run-1:parse:idx:3:1 FencingGeneration=4", decoded)
	}
	if decoded.WorkOrder == nil {
		t.Fatal("WorkOrder is nil after round-trip, want populated")
	}
	wo := decoded.WorkOrder
	if wo.NodeType != "code" || wo.Script != job.WorkOrder.Script {
		t.Errorf("WorkOrder.NodeType/Script = %+v, want matching the original", wo)
	}
	if wo.Config["language"] != "python" {
		t.Errorf("WorkOrder.Config = %+v, want language=python", wo.Config)
	}
	if len(wo.ItemColumns) != 1 || wo.ItemColumns[0] != "path" {
		t.Errorf("WorkOrder.ItemColumns = %v, want [path]", wo.ItemColumns)
	}
	if wo.ItemRow["path"] != "c.csv" {
		t.Errorf("WorkOrder.ItemRow = %v, want path=c.csv", wo.ItemRow)
	}
	if wo.RunParams["env"] != "prod" {
		t.Errorf("WorkOrder.RunParams = %v, want env=prod", wo.RunParams)
	}
	if wo.TimeoutSeconds != 30 {
		t.Errorf("WorkOrder.TimeoutSeconds = %d, want 30", wo.TimeoutSeconds)
	}
}

// TestRunJob_NilWorkOrderOmittedFromJSON proves today's normal
// whole-pipeline job — the only kind anything actually enqueues — keeps
// producing the exact same JSON shape it always has: no work_order key at
// all, not a null one, so existing consumers (the enterprise JobQueue
// implementation, any dashboard/debug tooling that already parses this
// payload) see no change.
func TestRunJob_NilWorkOrderOmittedFromJSON(t *testing.T) {
	job := RunJob{ID: "job-1", PipelineID: "pipe-1", RunID: "run-1", OrgID: "org-1"}

	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}
	if _, present := raw["work_order"]; present {
		t.Errorf("encoded job has a work_order key, want it omitted entirely for a nil WorkOrder: %s", encoded)
	}
	if _, present := raw["node_id"]; present {
		t.Errorf("encoded job has a node_id key, want it omitted for empty NodeID: %s", encoded)
	}
	if _, present := raw["instance_key"]; present {
		t.Errorf("encoded job has an instance_key key, want it omitted for empty InstanceKey: %s", encoded)
	}
}

// TestRunJob_AttemptAndDeliveryCountAreIndependent guards the exact
// distinction a real deployment caught missing: a JobQueue implementation
// that redelivers a job (visibility timeout, retry) must not confuse "how
// many times has the transport delivered this message" (DeliveryCount)
// with "which node/instance execution attempt does this job settle"
// (Attempt) — the enterprise Redis-backed JobQueue's claim() once
// overwrote Attempt with its own delivery counter, which was harmless for
// whole-pipeline jobs (nothing reads Attempt there) but silently broke
// ADR-017 remote instance dispatch: a redelivered WorkOrder job could no
// longer find its own execution_attempts row, since Attempt no longer
// matched what the dispatcher claimed under. Both fields must round-trip
// independently through JSON.
func TestRunJob_AttemptAndDeliveryCountAreIndependent(t *testing.T) {
	job := RunJob{
		ID: "job-1", RunID: "run-1", NodeID: "parse", InstanceKey: "idx:0",
		Attempt: 1, DeliveryCount: 3,
	}

	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded RunJob
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Attempt != 1 {
		t.Errorf("decoded.Attempt = %d, want 1 (must survive independent of DeliveryCount)", decoded.Attempt)
	}
	if decoded.DeliveryCount != 3 {
		t.Errorf("decoded.DeliveryCount = %d, want 3", decoded.DeliveryCount)
	}
}
