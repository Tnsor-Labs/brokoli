package cmd

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
)

// The bug, directly: a task node dispatches one job whose identity is the
// whole-node attempt, so its InstanceKey is legitimately empty. Rejecting
// it meant every remotely dispatched task node was refused on arrival and
// the run failed with "timed out waiting for a worker" 150 seconds later,
// naming the symptom and hiding the cause.
func TestTaskWorkOrderIsValidWithoutAnInstanceKey(t *testing.T) {
	job := extensions.RunJob{
		ID: "job-1", RunID: "run-1", NodeID: "t_jvm", InstanceKey: "",
		WorkOrder: &extensions.InstanceWorkOrder{NodeType: string(models.NodeTypeTask)},
	}
	if !validInstanceJobIdentity(job) {
		t.Fatal("a task work order with no instance key was rejected; this is the whole-node attempt convention, not a missing field")
	}
}

// The guard still has to do its job for what it was written for. An
// expansion instance always has a derived, non-empty key, and one without
// it cannot be settled against any attempt row.
func TestExpansionWorkOrderStillRequiresAnInstanceKey(t *testing.T) {
	job := extensions.RunJob{
		ID: "job-1", RunID: "run-1", NodeID: "expand", InstanceKey: "",
		WorkOrder: &extensions.InstanceWorkOrder{NodeType: string(models.NodeTypeCode)},
	}
	if validInstanceJobIdentity(job) {
		t.Fatal("an expansion instance with no instance key was accepted; it cannot be settled")
	}
	job.InstanceKey = "0"
	if !validInstanceJobIdentity(job) {
		t.Fatal("a well-formed expansion instance was rejected")
	}
}

// The fields that are required regardless of node type: without any of
// them the worker cannot look up the attempt to settle, so accepting the
// job would strand it.
func TestInstanceJobIdentityRequiresRunAndNode(t *testing.T) {
	for name, job := range map[string]extensions.RunJob{
		"no job id": {ID: "", RunID: "r", NodeID: "n",
			WorkOrder: &extensions.InstanceWorkOrder{NodeType: string(models.NodeTypeTask)}},
		"no run id": {ID: "j", RunID: "", NodeID: "n",
			WorkOrder: &extensions.InstanceWorkOrder{NodeType: string(models.NodeTypeTask)}},
		"no node id": {ID: "j", RunID: "r", NodeID: "",
			WorkOrder: &extensions.InstanceWorkOrder{NodeType: string(models.NodeTypeTask)}},
	} {
		if validInstanceJobIdentity(job) {
			t.Errorf("%s: accepted a job that cannot be settled", name)
		}
	}
}
